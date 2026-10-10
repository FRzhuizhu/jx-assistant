# -*- coding: utf-8 -*-
"""asr_worker.py —— 本地语音识别统一 worker（语音模型评估/听写后端）。

协议：每行一个 JSON 请求（stdin），每行一个 JSON 应答（stdout）。
  {"cmd":"load","model":"xasr","device":"cpu"}
      → {"ok":true,"event":"loaded","model":...,"load_ms":...}
  {"cmd":"transcribe","wav_b64":"<16k mono s16le WAV 的 base64>"}
      → {"ok":true,"text":"...","audio_ms":...,"infer_ms":...,"rtf":...}
  流式（一行请求一行应答，Go 侧哑桥转发）：
  {"cmd":"stream_start"} → {"ok":true}
  {"cmd":"stream_chunk","pcm_b64":"<int16 PCM 块>"} 
      → {"ok":true,"event":"partial","text":"灰字实时增量"}
      → 端点触发（用户停顿）时 {"ok":true,"event":"final","text":"离线模型精修后的整句（黑字）"}
  {"cmd":"stream_end"} → {"ok":true,"event":"done"}（尾段若有先回一条 final）
  出错 → {"ok":false,"err":"..."}

stdout 纪律：协议应答走原 fd1 的独立句柄；启动时把 sys.stdout 与 fd1 全部
改指向 stderr（os.dup2），保证 onnxruntime/llama 子进程的 C 层 printf 与
引擎日志全部落在 stderr，不污染 JSON 流。Go 侧（voiceasr.go）把 stderr
当日志收。

模型与依赖落位（开发目录即仓库根）：
  data/asr-models/xasr/          sherpa-onnx-x-asr-…-int8 解包（评估定标：小米 X-ASR）
依赖装在便携运行时（技能/_runtime/python）：sherpa-onnx。
（评估期的 paraformer/sensevoice/qwen 分支已删——结论见 git 历史 054c19e881。）
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
    def load_engine(model, device):
        """装好并返回离线引擎（不回话、不动 state——load 与流式精修共用）。"""
        if model == "xasr":
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
            return sherpa_onnx.OfflineRecognizer.from_transducer(
                encoder=enc, decoder=dec, joiner=joi, tokens=tok, num_threads=4)
        raise RuntimeError(f"未知模型: {model}")

    def load(model, device):
        t0 = time.time()
        close_engine()
        state["engine"] = load_engine(model, device)
        state["model"] = model
        reply({"ok": True, "event": "loaded", "model": model, "load_ms": int((time.time() - t0) * 1000)})

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

        stream = engine.create_stream()
        stream.accept_waveform(sr, samples.astype(np.float32) / 32768.0)
        engine.decode_stream(stream)
        text = (stream.result.text or "").strip()
        infer_ms = int((time.time() - t0) * 1000)
        reply({"ok": True, "text": text, "audio_ms": audio_ms,
               "infer_ms": infer_ms, "rtf": round(infer_ms / max(audio_ms, 1), 3),
               "model": state["model"], "sampwidth": sw})

    # ---------- 流式识别（X-ASR streaming 实时灰字 + 端点检测 + 离线精修黑字） ----------
    # 实时层：OnlineRecognizer（480ms 档）边收边出 partial；端点（用户停顿）触发时
    # 用离线 X-ASR（精度与标点更完整）重转该段，作为最终文字返回——对应前端
    # 「灰色实时字 → 黑色精修字」的两段式体验。协议严格一行请求一行应答。
    st = {"online": None, "stream": None, "seg": [], "started": False}

    def ensure_online():
        if st["online"] is not None:
            return
        import sherpa_onnx
        d = models_root / "xasr-streaming"

        def one(pattern):
            hits = sorted(glob.glob(str(d / "**" / pattern), recursive=True))
            return hits[0] if hits else None

        enc, dec, joi, tok = (one("encoder*.int8.onnx"), one("decoder*.onnx"),
                              one("joiner*.int8.onnx"), one("tokens.txt"))
        if not (enc and dec and joi and tok):
            raise RuntimeError(f"流式模型文件不完整：{d}")
        kwargs = {}
        # 断句时机由前端统一控制（3 秒停顿，手机/电脑同一实现）：服务端关掉
        # 端点检测避免双重切句——worker 只负责收流、出实时 partial（灰字）、
        # 收到 stream_flush/stream_end 时用离线模型精修该段（黑字）。
        # （enable_endpoint_detection 默认即关，这里显式声明意图）
        kwargs = {"enable_endpoint_detection": False}
        try:
            st["online"] = sherpa_onnx.OnlineRecognizer.from_transducer(
                encoder=enc, decoder=dec, joiner=joi, tokens=tok, num_threads=4, **kwargs)
        except TypeError:
            st["online"] = sherpa_onnx.OnlineRecognizer.from_transducer(
                encoder=enc, decoder=dec, joiner=joi, tokens=tok, num_threads=4)
        print("[asr-stream] 端点检测关闭：断句由前端 3 秒停顿驱动（stream_flush）", file=sys.stderr)

    def refine_offline(samples_i16):
        """离线 X-ASR 精修一段（int16 样本）——精度/标点完整版。静默装载：
        不走 load()（那会多发一条 loaded 应答，打乱一行一答的桥接协议）。"""
        import numpy as np
        if state["model"] != "xasr" or state["engine"] is None:
            state["engine"] = load_engine("xasr", "cpu")
            state["model"] = "xasr"
        engine = state["engine"]
        s2 = engine.create_stream()
        s2.accept_waveform(16000, samples_i16.astype(np.float32) / 32768.0)
        engine.decode_stream(s2)
        return (s2.result.text or "").strip()

    def stream_reset():
        st["stream"] = st["online"].create_stream()
        st["seg"] = []

    def feed_chunk(pcm_b64):
        import numpy as np
        raw = base64.b64decode(pcm_b64)
        samples = np.frombuffer(raw, dtype=np.int16)
        st["seg"].append(samples.copy())
        st["stream"].accept_waveform(16000, samples.astype(np.float32) / 32768.0)
        online = st["online"]
        while online.is_ready(st["stream"]):
            online.decode_stream(st["stream"])
        text = (online.get_result(st["stream"]) or "").strip()
        if online.is_endpoint(st["stream"]):
            # 端点：该段定稿——先离线精修（同一条应答里带回），再重开流
            seg_samples = np.concatenate(st["seg"]) if st["seg"] else np.zeros(0, dtype=np.int16)
            refined = refine_offline(seg_samples) if len(seg_samples) >= 1600 else ""
            stream_reset()
            reply({"ok": True, "event": "final", "text": refined or text})
            return
        reply({"ok": True, "event": "partial", "text": text})

    def stream_flush():
        """前端检测到停顿（3 秒）时调用：精修当前段（黑字）并重开流，录音继续。"""
        import numpy as np
        seg_samples = np.concatenate(st["seg"]) if st["seg"] else np.zeros(0, dtype=np.int16)
        text = (st["online"].get_result(st["stream"]) or "").strip()
        refined = refine_offline(seg_samples) if len(seg_samples) >= 1600 else ""
        stream_reset()
        reply({"ok": True, "event": "final", "text": refined or text})

    def stream_finish():
        import numpy as np
        online = st["online"]
        st["stream"].input_finished()
        while online.is_ready(st["stream"]):
            online.decode_stream(st["stream"])
        text = (online.get_result(st["stream"]) or "").strip()
        seg_samples = np.concatenate(st["seg"]) if st["seg"] else np.zeros(0, dtype=np.int16)
        refined = refine_offline(seg_samples) if len(seg_samples) >= 1600 else ""
        stream_reset()
        if refined or text:
            reply({"ok": True, "event": "final", "text": refined or text})
        reply({"ok": True, "event": "done"})

    # ---------- 主循环 ----------
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
            if req.get("cmd") == "load":
                load(req.get("model", ""), req.get("device", "cpu"))
            elif req.get("cmd") == "stream_start":
                ensure_online()
                stream_reset()
                st["started"] = True
                reply({"ok": True})
            elif req.get("cmd") == "stream_chunk":
                feed_chunk(req.get("pcm_b64", ""))
            elif req.get("cmd") == "stream_flush":
                stream_flush()
            elif req.get("cmd") == "stream_end":
                if st["started"]:
                    st["started"] = False
                    stream_finish()
                else:
                    reply({"ok": True, "event": "done"})
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
