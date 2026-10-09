package main

// voiceasr.go —— 本地语音识别（ASR）测试通道：语音组件设置页「本地识别测试」
// 的后端。四个候选模型经同一个 Python worker（asr/asr_worker.py，跑在随包
// 分发的便携运行时上）推理，协议为 stdin/stdout 每行一个 JSON：
//
//	{"cmd":"load","model":"paraformer|sensevoice|qwen0.6|qwen1.7","device":"cpu|auto"}
//	{"cmd":"transcribe","wav_b64":"..."} → {"ok":true,"text":...,"infer_ms":...,"rtf":...}
//
// 模型与引擎落位（开发目录即仓库根，见 asr_worker.py 头注）：
//	data/asr-models/{paraformer,sensevoice,qwen-0.6b,qwen-1.7b}/
//	asr/Qwen3-ASR-GGUF/（llama.cpp Vulkan DLL 放其 qwen_asr_gguf/bin/ 下）
// 这是评估脚手架：测完定标后，同一 worker/端点可直接产品化为听写通道
// （客户端本地或服务器端 jxaiserver 同构）。

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type asrModelDef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Engine string `json:"engine"` // sherpa-onnx | qwen-gguf
	Note   string `json:"note"`
	Dir    string // data/asr-models 下的目录名
	Globs  []string // 就绪判定：每个 glob 至少命中一个文件
}

var asrModels = []asrModelDef{
	{ID: "paraformer", Name: "Paraformer-large int8", Engine: "sherpa-onnx",
		Note: "约 230MB；CPU 十几倍实时；标点经 CT-Transformer 后处理补齐", Dir: "paraformer",
		Globs: []string{"model.int8.onnx", "tokens.txt"}},
	{ID: "sensevoice", Name: "SenseVoice-Small int8", Engine: "sherpa-onnx",
		Note: "约 160MB；CPU 十几倍实时；标点经 CT-Transformer 后处理补齐", Dir: "sensevoice",
		Globs: []string{"model.int8.onnx", "tokens.txt"}},
	{ID: "xasr", Name: "X-ASR int8（小米·中英双语·原生标点）", Engine: "sherpa-onnx",
		Note: "约 170MB；CPU 约 60 倍实时（实测 RTF 0.017）；Zipformer2 transducer，离线/流式一体；中英混说友好", Dir: "xasr",
		Globs: []string{"encoder*.int8.onnx", "decoder*.onnx", "joiner*.int8.onnx", "tokens.txt"}},
	{ID: "qwen0.6", Name: "Qwen3-ASR-0.6B int4/q4_k", Engine: "qwen-gguf",
		Note: "约 560MB；LLM 架构；自带标点；22 种中文方言；核显自动加速", Dir: "qwen-0.6b",
		Globs: []string{"*encoder*frontend*.onnx", "*llm*.gguf"}},
	{ID: "qwen1.7", Name: "Qwen3-ASR-1.7B int4/q4_k", Engine: "qwen-gguf",
		Note: "约 1.4GB；精度最高的 LLM 档；自带标点；核显自动加速", Dir: "qwen-1.7b",
		Globs: []string{"*encoder*frontend*.onnx", "*llm*.gguf"}},
}

func asrModelByID(id string) *asrModelDef {
	for i := range asrModels {
		if asrModels[i].ID == id {
			return &asrModels[i]
		}
	}
	return nil
}

// ---------- worker 进程管理 ----------

// pipeLineLogger 把子进程的逐行输出转成日志（stderr 用）。
type pipeLineLogger struct{ prefix string }

func (p pipeLineLogger) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(b), "\r\n"), "\n") {
		if line != "" {
			log.Printf("%s%s", p.prefix, line)
		}
	}
	return len(b), nil
}

type asrManager struct {
	app *App
	mu  sync.Mutex
	w   *asrWorker
}

type asrWorker struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Scanner
	loaded string // 已加载模型（换模型即重载）
	device string
}

func (m *asrManager) modelsRoot() (string, error) {
	root, err := m.workerRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "data", "asr-models"), nil
}

// workerRoot asr_worker.py 与模型目录的仓库根：桌面/安装版锚在 exe 旁，
// 与技能目录同源（开发目录即仓库根）。
func (m *asrManager) workerRoot() (string, error) {
	dir, err := m.app.SkillsDir()
	if err != nil {
		return "", err
	}
	root := filepath.Dir(dir) // 技能目录的上级 = 仓库/安装根
	if _, err := os.Stat(filepath.Join(root, "asr", "asr_worker.py")); err != nil {
		return "", fmt.Errorf("未找到 asr/asr_worker.py（应在 %s）", root)
	}
	return root, nil
}

func (m *asrManager) pythonExe(root string) (string, error) {
	py := filepath.Join(root, "技能", "_runtime", "python", "python.exe")
	if _, err := os.Stat(py); err != nil {
		return "", fmt.Errorf("便携运行时未装配（%s）：先在设置里启用任一技能触发装配，或手动放置", py)
	}
	return py, nil
}

// get 惰性拉起 worker；已死则重启。调用方必须已持有 m.mu（Go 互斥锁不可
// 重入——Transcribe 持锁后调这里，get 内不能再 Lock，曾因此整拍自死锁）。
func (m *asrManager) get() (*asrWorker, error) {
	if m.w != nil && m.w.cmd.ProcessState == nil {
		return m.w, nil
	}
	root, err := m.workerRoot()
	if err != nil {
		return nil, err
	}
	py, err := m.pythonExe(root)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(py, filepath.Join("asr", "asr_worker.py"))
	cmd.Dir = root
	cmd.Stderr = pipeLineLogger{prefix: "[asr-worker] "}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	hideConsoleWindow(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 ASR worker 失败: %w", err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20) // 转录应答含全文，宽裕些
	w := &asrWorker{cmd: cmd, stdin: stdin, out: sc}
	go func() {
		_ = cmd.Wait() // 进程退出；下次 get() 重建
		_ = stdout.Close()
	}()
	m.w = w
	return w, nil
}

// call 发一行请求、读一行应答（单飞行：manager.mu 已由调用方持有……不，
// transcribe 里自行加锁）。
func (w *asrWorker) call(req map[string]any) (map[string]any, error) {
	b, _ := json.Marshal(req)
	if _, err := w.stdin.Write(append(b, '\n')); err != nil {
		return nil, fmt.Errorf("worker 管道已断: %w", err)
	}
	if !w.out.Scan() {
		if err := w.out.Err(); err != nil {
			return nil, fmt.Errorf("读 worker 应答失败: %v", err)
		}
		return nil, fmt.Errorf("worker 无应答（已退出）")
	}
	var resp map[string]any
	if err := json.Unmarshal(w.out.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("worker 应答解析失败: %v", err)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		msg, _ := resp["err"].(string)
		return resp, fmt.Errorf("%s", msg)
	}
	return resp, nil
}

// Transcribe 加载（按需/换模型）并转录一段 16k mono WAV。
func (m *asrManager) Transcribe(model, device string, wav []byte) (map[string]any, error) {
	def := asrModelByID(model)
	if def == nil {
		return nil, fmt.Errorf("未知模型: %s", model)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	w, err := m.get()
	if err != nil {
		return nil, err
	}
	if w.loaded != model || w.device != device {
		resp, err := w.call(map[string]any{"cmd": "load", "model": model, "device": device})
		if err != nil {
			m.killLocked()
			return nil, fmt.Errorf("加载 %s 失败: %w", def.Name, err)
		}
		w.loaded = model
		w.device = device
		if lm, ok := resp["load_ms"].(float64); ok {
			log.Printf("[asr] %s 加载完成 %dms（%s）", model, int64(lm), device)
		}
	}
	resp, err := w.call(map[string]any{"cmd": "transcribe", "wav_b64": base64.StdEncoding.EncodeToString(wav)})
	if err != nil {
		m.killLocked()
		return nil, err
	}
	return resp, nil
}

func (m *asrManager) killLocked() {
	if m.w != nil {
		if p := m.w.cmd.Process; p != nil {
			_ = p.Kill()
		}
		m.w = nil
	}
}

func (m *asrManager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w != nil {
		_, _ = m.w.call(map[string]any{"cmd": "shutdown"})
		m.killLocked()
	}
}

// ---------- HTTP 端点 ----------

// asrReady 就绪判定：文件在模型目录根或一级子目录（解包布局两种都有）。
// 注意 Go 的 filepath.Glob 不支持 ** 递归。
func asrReady(root string, d asrModelDef) bool {
	for _, g := range d.Globs {
		found := false
		for _, base := range []string{d.Dir, filepath.Join(d.Dir, "*")} {
			hits, _ := filepath.Glob(filepath.Join(root, "data", "asr-models", base, g))
			if len(hits) > 0 {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (a *App) handleASRModels(w http.ResponseWriter, r *http.Request) {
	root, err := a.asr.workerRoot()
	type item struct {
		asrModelDef
		Ready bool `json:"ready"`
	}
	out := []item{}
	if err == nil {
		for _, d := range asrModels {
			out = append(out, item{asrModelDef: d, Ready: asrReady(root, d)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

func (a *App) handleASRTranscribe(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Model  string `json:"model"`
		Device string `json:"device"` // cpu|auto
		WavB64 string `json:"wavB64"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "参数解析失败: "+err.Error())
		return
	}
	if p.Device == "" {
		p.Device = "cpu"
	}
	wav, err := base64.StdEncoding.DecodeString(p.WavB64)
	if err != nil || len(wav) < 44 {
		writeErr(w, http.StatusBadRequest, "音频数据无效（需 16k 单声道 WAV 的 base64）")
		return
	}
	if !strings.EqualFold(p.Device, "cpu") {
		p.Device = "auto"
	}
	t0 := time.Now()
	resp, err := a.asr.Transcribe(p.Model, p.Device, wav)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp["total_ms"] = time.Since(t0).Milliseconds()
	writeJSON(w, http.StatusOK, resp)
}
