package main

// 演示记录器的语音转写接线：复用语音组件（组件/voice）的本地 X-ASR worker
// （asr_worker.py，sherpa-onnx 离线模型），经组件桥直调——与前端听写走的
// POST /api/c/voice/asr/transcribe 同一通道同一协议（一行 JSON：
// cmd=transcribe + wav_b64 → {ok,text}）。recorderASR 钩子在 NewApp 注册；
// 组件停用时调用返回错误＝该段字幕丢弃，录制本体不受影响。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// transcribeWavBytes 把一段 16kHz/单声道/16bit WAV 交给语音组件的常驻识别
// worker。bridgeCall 同款纪律：调用期间持有 worker 锁（worker 串行），超时杀
// 通道重启（下一调用自动再拉起）。
func (a *App) transcribeWavBytes(wav []byte) (string, error) {
	m, ok := a.Components.Manifest(voiceComponentID)
	if !ok || !a.Components.IsEnabled(voiceComponentID) {
		return "", fmt.Errorf("语音组件未启用（讲解字幕不可用）")
	}
	var def *ComponentWorkerDef
	for i := range m.Workers {
		if m.Workers[i].Name == "asr" {
			def = &m.Workers[i]
			break
		}
	}
	if def == nil {
		return "", fmt.Errorf("语音组件未声明 asr worker")
	}
	wk, err := a.Bridge.get(voiceComponentID, *def)
	if err != nil {
		return "", err
	}
	req, _ := json.Marshal(map[string]any{"cmd": "transcribe", "wav_b64": base64.StdEncoding.EncodeToString(wav)})
	wk.mu.Lock()
	defer wk.mu.Unlock()
	type callRes struct {
		line string
		err  error
	}
	ch := make(chan callRes, 1)
	go func() {
		line, err := wk.callRaw(string(req))
		ch <- callRes{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			wk.kill()
			return "", r.err
		}
		var out struct {
			OK    bool   `json:"ok"`
			Text  string `json:"text"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(r.line), &out); err != nil {
			return "", fmt.Errorf("识别应答不是 JSON: %w", err)
		}
		if !out.OK {
			return "", fmt.Errorf("识别失败: %s", out.Error)
		}
		return out.Text, nil
	case <-time.After(180 * time.Second):
		wk.kill()
		return "", fmt.Errorf("识别超时（180 秒），通道已重启")
	}
}

// voiceComponentOn 环境公告用：本地识别线是否可用（组件启用即视为可用——
// 模型随组件分发，manifest 声明了 asr worker）。
func (a *App) voiceComponentOn() bool {
	if a.Components == nil {
		return false
	}
	if m, ok := a.Components.Manifest(voiceComponentID); ok {
		for i := range m.Workers {
			if m.Workers[i].Name == "asr" {
				return a.Components.IsEnabled(voiceComponentID)
			}
		}
	}
	return false
}
