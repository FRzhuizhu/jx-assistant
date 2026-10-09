# -*- coding: utf-8 -*-
"""asr_worker.py —— 本地语音识别统一 worker（语音模型评估/听写后端）。

协议：每行一个 JSON 请求（stdin），每行一个 JSON 应答（stdout）。
  {"cmd":"load","model":"paraformer|sensevoice|qwen0.6|qwen1.7","device":"cpu|auto"}
      → {"ok":true,"event":"loaded","model":...,"load_ms":...}
  {"cmd":"transcribe","wav_b64":"<16k mono s16le WAV 的 base64>"}
      → {"ok":true,"text":"...","audio_ms":...,"infer_ms":...,"rtf":...}
  出错 → {"ok":false,"err":"..."}

stdout 纪律：协议应答走原 fd1 的独立句柄；启动时把 sys.stdout 与 fd1 全部
改指向 stderr（os.dup2），保证 onnxruntime/llama 子进程的 C 层 printf 与
引擎日志全部落在 stderr，不污染 JSON 流。Go 侧（voiceasr.go）把 stderr
当日志收。

模型与依赖落位（开发目录即仓库根）：
  data/asr-models/paraformer/    sherpa-onnx-paraformer-zh-int8-2025-10-07 解包
  data/asr-models/sensevoice/    sherpa-onnx-sense-voice-…-int8 解包
  data/asr-models/qwen-0.6b/     Qwen3-ASR-0.6B-gguf.zip 解包（int4 编码器 + q4_k 解码器）
  data/asr-models/qwen-1.7b/     Qwen3-ASR-1.7B-gguf.zip 解包
  asr/Qwen3-ASR-GGUF/            推理引擎仓库（qwen_asr_gguf 包），llama.cpp DLL 放
                                  其 qwen_asr_gguf/bin/ 下（Vulkan 版自动核显加速）
依赖装在便携运行时（技能/_runtime/python）：sherpa-onnx、pydub、scipy、gguf、srt。
"""
import base64
import glob
import io
import json
import os
import sys
import time
import wave
from pathlib import Path


def main():
    root = Path(__file__).resolve().parent.parent  # 仓库根（asr/ 的上级）
    models_root = root / "data" / "asr-models"

    # ---- stdout 纪律：fd1 留给协议，其余全部转 stderr ----
    proto = os.fdopen(os.dup(1), "w", encoding="utf-8")
    os.dup2(2, 1)
    sys.stdout = sys.stderr

    def reply(obj):
        proto.write(json.dumps(obj, ensure_ascii=False) + "\n")
        proto.flush()

    state = {"model": None, "engine": None}

    def close_engine():
        eng = state.get("engine")
        if eng is None:
            return
        try:
            if hasattr(eng, "shutdown"):
                eng.shutdown()
        except Exception:
            pass
        state["engine"] = None
        state["model"] = None

    # ---------- 加载 ----------
    def load(model, device):
        t0 = time.time()
        close_engine()
        if model == "paraformer":
            import sherpa_onnx
            d = models_root / "paraformer"
            asr_model = sorted(glob.glob(str(d / "**" / "model.int8.onnx"), recursive=True))
            tokens = sorted(glob.glob(str(d / "**" / "tokens.txt"), recursive=True))
            if not asr_model or not tokens:
                raise RuntimeError(f"paraformer 模型文件不完整：{d}")
            engine = sherpa_onnx.OfflineRecognizer.from_paraformer(
                paraformer=asr_model[0], tokens=tokens[0], num_threads=4)
        elif model == "sensevoice":
            import sherpa_onnx
            d = models_root / "sensevoice"
            asr_model = sorted(glob.glob(str(d / "**" / "model.int8.onnx"), recursive=True))
            tokens = sorted(glob.glob(str(d / "**" / "tokens.txt"), recursive=True))
            if not asr_model or not tokens:
                raise RuntimeError(f"sensevoice 模型文件不完整：{d}")
            engine = sherpa_onnx.OfflineRecognizer.from_sense_voice(
                model=asr_model[0], tokens=tokens[0], use_itn=True, num_threads=4)
        elif model == "xasr":
            # 小米 X-ASR：Zipformer2 transducer，中英双语、自带标点与大小写，
            # 离线/流式一体（这里用离线档），int8 约 170MB
            import sherpa_onnx
            d = models_root / "xasr"

            def one(pattern):
                hits = sorted(glob.glob(str(d / "**" / pattern), recursive=True))
                return hits[0] if hits else None

            enc, dec, joi, tok = (one("encoder*.int8.onnx"), one("decoder*.onnx"),
                                  one("joiner*.int8.onnx"), one("tokens.txt"))
            if not (enc and dec and joi and tok):
                raise RuntimeError(f"xasr 模型文件不完整：{d}")
            engine = sherpa_onnx.OfflineRecognizer.from_transducer(
                encoder=enc, decoder=dec, joiner=joi, tokens=tok, num_threads=4)
        elif model in ("qwen0.6", "qwen1.7"):
            gguf_repo = root / "asr" / "Qwen3-ASR-GGUF"
            sys.path.insert(0, str(gguf_repo))
            from qwen_asr_gguf.inference import QwenASREngine, ASREngineConfig
            d = models_root / ("qwen-0.6b" if model == "qwen0.6" else "qwen-1.7b")

            def pick(pattern):
                hits = sorted(glob.glob(str(d / "**" / pattern), recursive=True))
                return Path(hits[0]).name if hits else None

            cfg = ASREngineConfig(
                model_dir=str(d),
                encoder_frontend_fn=pick("*encoder*frontend*.onnx") or "qwen3_asr_encoder_frontend.fp16.onnx",
                encoder_backend_fn=pick("*encoder*backend*.onnx") or "qwen3_asr_encoder_backend.fp16.onnx",
                llm_fn=pick("*llm*.gguf") or "qwen3_asr_llm.q5_k.gguf",
                # 设备语义：cpu=编码器CPU+解码器CPU；auto=编码器CPU（够快，且
                # 共享运行时里的 onnxruntime 是 CPU 版——装 DML 版会覆盖 OCR
                # 技能的依赖，不动它）+ 解码器 GPU（llama.cpp Vulkan，核显通吃，
                # 加速的大头本来就在解码器）
                onnx_provider="CPU",
                llm_use_gpu=(device != "cpu"),
                enable_aligner=False,  # 听写不需要字级时间戳，省一半算力
                verbose=False,
            )
            engine = QwenASREngine(config=cfg)
        else:
            raise RuntimeError(f"未知模型: {model}")
        state["engine"] = engine
        state["model"] = model
        reply({"ok": True, "event": "loaded", "model": model, "load_ms": int((time.time() - t0) * 1000)})

    # ---------- 标点后处理（paraformer/sensevoice 无标点输出，用 CT-Transformer 补） ----------
    punct_state = {"engine": None, "tried": False}

    def apply_punct(text):
        if not text:
            return text
        if not punct_state["tried"]:
            punct_state["tried"] = True
            try:
                import sherpa_onnx
                d = models_root / "punct"
                if os.path.exists(d / "model.onnx"):
                    mc = sherpa_onnx.OfflinePunctuationModelConfig(
                        ct_transformer=str(d / "model.onnx"), num_threads=1)
                    punct_state["engine"] = sherpa_onnx.OfflinePunctuation(
                        sherpa_onnx.OfflinePunctuationConfig(model=mc))
            except Exception as e:  # noqa: BLE001 —— 标点失败不挡转写，返回原文
                print(f"[punct] 标点模型加载失败（不启用后处理）: {e}", file=sys.stderr)
        eng = punct_state["engine"]
        if eng is None:
            return text
        try:
            return eng.add_punctuation(text).strip()
        except Exception as e:  # noqa: BLE001
            print(f"[punct] 加标点失败（返回原文）: {e}", file=sys.stderr)
            return text

    # ---------- 转录 ----------
    def transcribe(wav_b64):
        engine = state.get("engine")
        if engine is None:
            raise RuntimeError("模型尚未加载")
        raw = base64.b64decode(wav_b64)
        with wave.open(io.BytesIO(raw), "rb") as w:
            sr = w.getframerate()
            n = w.getnframes()
            ch = w.getnchannels()
            sw = w.getsampwidth()
            pcm = w.readframes(n)
        audio_ms = int(n / sr * 1000)
        import numpy as np
        samples = np.frombuffer(pcm, dtype=np.int16)
        if ch > 1:
            samples = samples.reshape(-1, ch).mean(axis=1).astype(np.int16)
        t0 = time.time()

        if state["model"] in ("paraformer", "sensevoice", "xasr"):
            stream = engine.create_stream()
            stream.accept_waveform(sr, samples.astype(np.float32) / 32768.0)
            engine.decode_stream(stream)
            text = (stream.result.text or "").strip()
            # X-ASR 原生带标点；paraformer/sensevoice 用 CT-Transformer 后处理补
            if state["model"] in ("paraformer", "sensevoice"):
                text = apply_punct(text)
        else:
            tmp = models_root / f"_tmp_{os.getpid()}.wav"
            with wave.open(str(tmp), "wb") as w:
                w.setnchannels(1)
                w.setsampwidth(2)
                w.setframerate(sr)
                w.writeframes(samples.astype(np.int16).tobytes())
            try:
                # duration=None 必须显式传：引擎默认 duration=0.0 会被
                # load_audio_numpy 折成 frames=0，读出空音频、0 分段、空文本
                res = engine.transcribe(audio_file=str(tmp), language="Chinese", duration=None)
                text = (res.text or "").strip()
            finally:
                try:
                    tmp.unlink()
                except OSError:
                    pass
        infer_ms = int((time.time() - t0) * 1000)
        reply({"ok": True, "text": text, "audio_ms": audio_ms,
               "infer_ms": infer_ms, "rtf": round(infer_ms / max(audio_ms, 1), 3),
               "model": state["model"], "sampwidth": sw})

    # ---------- 主循环 ----------
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
            if req.get("cmd") == "load":
                load(req.get("model", ""), req.get("device", "cpu"))
            elif req.get("cmd") == "transcribe":
                transcribe(req.get("wav_b64", ""))
            elif req.get("cmd") == "shutdown":
                close_engine()
                reply({"ok": True})
                return
            else:
                reply({"ok": False, "err": f"未知命令: {req.get('cmd')}"})
        except Exception as e:  # noqa: BLE001 —— worker 必须活着回错误，不能崩
            import traceback
            traceback.print_exc()
            reply({"ok": False, "err": f"{type(e).__name__}: {e}"})
        proto.flush()


if __name__ == "__main__":
    main()
