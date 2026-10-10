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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type asrModelDef struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Engine string   `json:"engine"` // sherpa-onnx | qwen-gguf
	Note   string   `json:"note"`
	Dir    string   // data/asr-models 下的目录名
	Globs  []string // 就绪判定：每个 glob 至少命中一个文件
}

var asrModels = []asrModelDef{
	{ID: "xasr", Name: "X-ASR int8（小米·中英双语·原生标点）", Engine: "sherpa-onnx",
		Note: "约 170MB；CPU 约 60 倍实时；Zipformer2 transducer，离线/流式一体；中英混说友好", Dir: "xasr",
		Globs: []string{"encoder*.int8.onnx", "decoder*.onnx", "joiner*.int8.onnx", "tokens.txt"}},
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

	streamMu sync.Mutex // 流式会话互斥（单人单麦，同时只一路）
	sw       *asrWorker // 流式专用 worker（与离线转写互不阻塞）
}

type asrWorker struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Scanner
	loaded string // 已加载模型（换模型即重载）
	device string
}

// voiceDir 语音组件目录（组件自包含：worker 脚本与 models/ 都归组件本体）。
// 相对路径 "组件/voice" 与技能目录同规约：开发＝仓库根，桌面/安装版 cwd 已锚 exe 旁。
func (m *asrManager) voiceDir() (string, error) {
	dir := filepath.Join("组件", "voice")
	if _, err := os.Stat(filepath.Join(dir, "asr_worker.py")); err != nil {
		return "", fmt.Errorf("未找到语音组件 worker（%s）：组件文件不完整", filepath.Join(dir, "asr_worker.py"))
	}
	return dir, nil
}

// pythonExe 共享便携运行时（技能/_runtime——运行时共享原则，语音不自带）。
func (m *asrManager) pythonExe() (string, error) {
	dir, err := m.app.SkillsDir()
	if err != nil {
		return "", err
	}
	py := filepath.Join(dir, "_runtime", "python", "python.exe")
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
	dir, err := m.voiceDir()
	if err != nil {
		return nil, err
	}
	py, err := m.pythonExe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(py, filepath.Join(dir, "asr_worker.py"))
	cmd.Dir = dir
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

// callRaw 发一行原始请求、读一行原始应答（流式桥用：透传不做 JSON 解释）。
func (w *asrWorker) callRaw(line string) (string, error) {
	if _, err := w.stdin.Write([]byte(line + "\n")); err != nil {
		return "", fmt.Errorf("worker 管道已断: %w", err)
	}
	if !w.out.Scan() {
		if err := w.out.Err(); err != nil {
			return "", fmt.Errorf("读 worker 应答失败: %v", err)
		}
		return "", fmt.Errorf("worker 无应答（已退出）")
	}
	return string(w.out.Bytes()), nil
}

// getStream 惰性拉起流式专用 worker（与离线 worker 分离：流式会话长时间占用
// stdin/stdout，不能堵住逐句转写）。调用方须持有 streamMu。
func (m *asrManager) getStream() (*asrWorker, error) {
	if m.sw != nil && m.sw.cmd.ProcessState == nil {
		return m.sw, nil
	}
	dir, err := m.voiceDir()
	if err != nil {
		return nil, err
	}
	py, err := m.pythonExe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(py, filepath.Join(dir, "asr_worker.py"))
	cmd.Dir = dir
	cmd.Stderr = pipeLineLogger{prefix: "[asr-stream-worker] "}
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
		return nil, fmt.Errorf("启动流式 worker 失败: %w", err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	w := &asrWorker{cmd: cmd, stdin: stdin, out: sc}
	go func() {
		_ = cmd.Wait()
		_ = stdout.Close()
	}()
	m.sw = w
	return w, nil
}

func (m *asrManager) killStreamLocked() {
	if m.sw != nil {
		if p := m.sw.cmd.Process; p != nil {
			_ = p.Kill()
		}
		m.sw = nil
	}
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

// ---------- 流式听写（WebSocket ↔ worker 的一行一答哑桥） ----------

// handleASRStream GET /api/voice/asr/stream：浏览器经 WebSocket 逐块送 16k
// int16 PCM，本端把每条消息原样转发给流式 worker（asr/asr_worker.py 的
// stream_* 命令），worker 每请求恰好回一行 JSON（partial=实时灰字 /
// final=端点后离线精修的黑字 / done），原样写回浏览器。同时只允许一路
// 流式会话（听写本来就是单人单麦）。
func (a *App) handleASRStream(w http.ResponseWriter, r *http.Request) {
	conn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		return
	}
	defer conn.Close()
	m := a.asr
	m.streamMu.Lock()
	defer m.streamMu.Unlock()
	worker, err := m.getStream()
	if err != nil {
		_ = wsutil.WriteServerText(conn, []byte(`{"ok":false,"err":`+strconv.Quote(err.Error())+`}`))
		return
	}
	for {
		msg, err := wsutil.ReadClientText(conn)
		if err != nil {
			break // 连接关闭/出错：会话结束
		}
		line, err := worker.callRaw(string(msg))
		if err != nil {
			_ = wsutil.WriteServerText(conn, []byte(`{"ok":false,"err":`+strconv.Quote(err.Error())+`}`))
			m.killStreamLocked()
			return
		}
		if err := wsutil.WriteServerText(conn, []byte(line)); err != nil {
			break
		}
	}
	// 断开即收尾：通知 worker 结束当前流（下次 stream_start 会整体重置）
	_, _ = worker.callRaw(`{"cmd":"stream_end"}`)
}
