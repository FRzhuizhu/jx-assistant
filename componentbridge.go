package main

// componentbridge.go —— 声明式组件桥：组件自包含后端的通用宿主。
//
// 组件在 manifest 顶层声明 "workers"（见 components.go 的 ComponentWorkerDef）：
// 主程序按声明懒拉起子进程（{python}=共享便携运行时、{dir}=组件目录），并把端点
// 统一挂到 /api/c/<组件id>/<path>：
//   - type=call：HTTP body（一行 JSON）原样写 worker stdin、读一行应答原样返回
//     ——哑管，协议归组件（json-lines 一行一答）；
//   - type=ws：WebSocket 双向哑转发（每帧一行），断开发 {"cmd":"stream_end"} 收尾。
// 后端逻辑零主程序代码：组件（含预编译产物/脚本/模型）可市场直装、免编译、
// 免主程序更新；组件关闭即杀 worker（重开懒启复活）。生命周期兜底：进程退出由
// 顶层 kill-on-close Job Object 清场（与其他子进程同口径）。
//
// 首个示范：语音组件（组件/voice，worker=asr_worker.py，端点 asr/transcribe 与
// asr/stream）。存量套皮组件（todo/git 面板等）不经此桥，维持主程序内置。

import (
	"bufio"
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

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// pipeLineLogger 把子进程的逐行输出转成日志（worker stderr 用）。
type pipeLineLogger struct{ prefix string }

func (p pipeLineLogger) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(b), "\r\n"), "\n") {
		if line != "" {
			log.Printf("%s%s", p.prefix, line)
		}
	}
	return len(b), nil
}

type bridgeWorker struct {
	key    string // 组件id/worker名
	def    ComponentWorkerDef
	dir    string // 组件绝对目录
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Scanner
	mu     sync.Mutex // 单飞行：json-lines 一行一答；ws 会话期间独占
	dead   bool
	logger pipeLineLogger
}

type bridgeManager struct {
	app *App
	mu  sync.Mutex
	ws  map[string]*bridgeWorker // key -> 懒启的进程
}

func newBridgeManager(app *App) *bridgeManager {
	return &bridgeManager{app: app, ws: map[string]*bridgeWorker{}}
}

// resolveCommand 解析 argv 模板：{python}=共享便携运行时、{dir}=组件绝对目录。
func (m *bridgeManager) resolveCommand(def ComponentWorkerDef, dir string) ([]string, error) {
	py, err := m.pythonExe()
	if err != nil {
		return nil, err
	}
	argv := make([]string, 0, len(def.Command))
	for i, a := range def.Command {
		switch a {
		case "{python}":
			argv = append(argv, py)
		case "{dir}":
			argv = append(argv, dir)
		default:
			// 相对路径参数锚到组件目录（如 asr_worker.py）
			if i > 0 && !filepath.IsAbs(a) && strings.Contains(a, ".") && !strings.HasPrefix(a, "-") {
				if _, err := os.Stat(filepath.Join(dir, a)); err == nil {
					a = filepath.Join(dir, a)
				}
			}
			argv = append(argv, a)
		}
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("worker %s 的 command 为空", def.Name)
	}
	return argv, nil
}

// pythonExe 共享便携运行时（技能/_runtime——运行时共享原则，组件不自带）。
// 新机自愈：运行时还没装配过（全新克隆、没用过任何技能）时，借技能装配机制
// 现场装一份并顺带装上 sherpa-onnx 轮子——远端拉下来直接点听写即可用，
// 首次会有装配等待（走引擎日志）。运行时已在但缺 sherpa 的场景由 worker
// 自身的 pip 自愈兜底（asr_worker.ensure_sherpa）。
func (m *bridgeManager) pythonExe() (string, error) {
	if py, err := m.sharedPythonPath(); err == nil {
		return py, nil
	}
	if _, perr := m.app.ensureSkillRuntime([]string{"sherpa_onnx"}, []string{"sherpa-onnx"}); perr == nil {
		if py, err := m.sharedPythonPath(); err == nil {
			return py, nil
		}
	}
	py, _ := m.sharedPythonPath()
	return "", fmt.Errorf("便携运行时未装配（%s）：先在设置里启用任一技能触发装配，或手动放置", py)
}

func (m *bridgeManager) sharedPythonPath() (string, error) {
	dir, err := m.app.SkillsDir()
	if err != nil {
		return "", err
	}
	py := filepath.Join(dir, "_runtime", "python", "python.exe")
	if _, err := os.Stat(py); err != nil {
		return py, err
	}
	return py, nil
}

// get 懒拉起（或复用）组件 worker。调用方不得持有 w.mu。
func (m *bridgeManager) get(compID string, def ComponentWorkerDef) (*bridgeWorker, error) {
	key := compID + "/" + def.Name
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.ws[key]; w != nil && w.cmd.ProcessState == nil {
		return w, nil
	}
	// 组件目录锚定：注册表目录是相对 cwd 的（开发=仓库根，安装版锚 exe 旁）
	dir := filepath.Join(m.app.Components.Dir(), compID)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	argv, err := m.resolveCommand(def, dir)
	if err != nil {
		return nil, err
	}
	logger := pipeLineLogger{prefix: "[c/" + key + "] "}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stderr = logger
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
		return nil, fmt.Errorf("启动 worker 失败: %w", err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20) // 应答行可能含整段转写文本，宽裕些
	w := &bridgeWorker{key: key, def: def, dir: dir, cmd: cmd, stdin: stdin, out: sc, logger: logger}
	go func() {
		_ = cmd.Wait() // 进程退出；下次 get() 重建
		_ = stdout.Close()
	}()
	m.ws[key] = w
	log.Printf("[c/%s] worker 已拉起（%s）", key, def.Name)
	return w, nil
}

// callRaw 发一行原始请求、读一行原始应答（哑管：内容归组件协议）。
func (w *bridgeWorker) callRaw(line string) (string, error) {
	if _, err := w.stdin.Write([]byte(line + "\n")); err != nil {
		return "", fmt.Errorf("worker 管道已断: %w", err)
	}
	if !w.out.Scan() {
		if err := w.out.Err(); err != nil {
			return "", fmt.Errorf("读 worker 应答失败: %v", err)
		}
		return "", fmt.Errorf("worker 无应答（已退出）")
	}
	return w.out.Text(), nil
}

// kill 立即终止进程（幂等）。
func (w *bridgeWorker) kill() {
	if p := w.cmd.Process; p != nil {
		_ = p.Kill()
	}
}

// KillComponent 关闭组件/卸载时杀它声明的全部 worker。
func (m *bridgeManager) KillComponent(compID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, w := range m.ws {
		if strings.HasPrefix(key, compID+"/") {
			w.kill()
			delete(m.ws, key)
			log.Printf("[c/%s] 组件关闭，worker 已终止", key)
		}
	}
}

// handleComponentBridge GET/POST /api/c/{id}/{rest...}：按启用中组件的 manifest
// 端点声明分发（未命中/未启用 404——与组件资产 404 口径一致）。固定路由在启动
// 时注册一次，之后全靠 manifest 数据驱动，无需动态改路由。
func (a *App) handleComponentBridge(w http.ResponseWriter, r *http.Request) {
	compID := r.PathValue("id")
	path := strings.Trim(r.PathValue("rest"), "/")
	if compID == "" || path == "" {
		http.NotFound(w, r)
		return
	}
	m, ok := a.Components.Manifest(compID)
	if !ok || !a.Components.IsEnabled(compID) {
		http.NotFound(w, r)
		return
	}
	for _, def := range m.Workers {
		for _, ep := range def.Endpoints {
			if !strings.EqualFold(ep.Method, r.Method) || ep.Path != path {
				continue
			}
			switch ep.Type {
			case "call":
				a.bridgeCall(w, r, compID, def)
			case "ws":
				a.bridgeWS(w, r, compID, def)
			default:
				http.NotFound(w, r)
			}
			return
		}
	}
	http.NotFound(w, r)
}

// bridgeCall HTTP 一行一答哑管：body（JSON）原样进 worker stdin，应答行原样返回。
func (a *App) bridgeCall(w http.ResponseWriter, r *http.Request, compID string, def ComponentWorkerDef) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil || len(body) == 0 {
		writeErr(w, http.StatusBadRequest, "请求体读取失败（需一行 JSON，上限 32MB）")
		return
	}
	wk, err := a.Bridge.get(compID, def)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	wk.mu.Lock()
	defer wk.mu.Unlock()
	type res struct {
		line string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := wk.callRaw(string(body))
		ch <- res{line, err}
	}()
	timeout := time.After(180 * time.Second)
	select {
	case r2 := <-ch:
		if r2.err != nil {
			wk.kill()
			writeErr(w, http.StatusInternalServerError, r2.err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(r2.line))
	case <-timeout:
		wk.kill()
		writeErr(w, http.StatusGatewayTimeout, "worker 应答超时（180 秒），已重启通道")
	}
}

// bridgeWS WebSocket 双向哑转发（照搬语音流式验证过的模式）：每帧一行进 worker、
// 每行应答一帧回浏览器；断开时发 stream_end 收尾。会话期间独占该 worker。
func (a *App) bridgeWS(w http.ResponseWriter, r *http.Request, compID string, def ComponentWorkerDef) {
	conn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		return
	}
	defer conn.Close()
	wk, err := a.Bridge.get(compID, def)
	if err != nil {
		_ = wsutil.WriteServerText(conn, []byte(`{"ok":false,"err":"`+jsonEscape(err.Error())+`"}`))
		return
	}
	wk.mu.Lock()
	defer wk.mu.Unlock()
	for {
		msg, err := wsutil.ReadClientText(conn)
		if err != nil {
			break
		}
		line, err := wk.callRaw(string(msg))
		if err != nil {
			_ = wsutil.WriteServerText(conn, []byte(`{"ok":false,"err":"`+jsonEscape(err.Error())+`"}`))
			wk.kill()
			return
		}
		if err := wsutil.WriteServerText(conn, []byte(line)); err != nil {
			break
		}
	}
	// 断开即收尾：通知 worker 结束当前流（组件协议自理；下次 stream_start 会重置）
	_, _ = wk.callRaw(`{"cmd":"stream_end"}`)
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return strings.Trim(string(b), `"`)
}
