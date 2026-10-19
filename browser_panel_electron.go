package main

// Electron 壳右栏面板桥：browserTarget 的第三个实现（另两个：chromedp 宿主
// browser_host.go、桌面原生 WebView2 面板 browser_panel_windows.go）。
//
// Electron 主进程（jx-desktop/electron/panel-bridge.js）持有页面内
// <webview> 元素并在 127.0.0.1 随机端口起带令牌的 HTTP 服务；本文件把
// browserTarget 的 15 个操作逐个翻译成对它的 JSON 调用。环境变量
// JX_ELECTRON_BRIDGE / JX_ELECTRON_BRIDGE_TOKEN 由 Electron 拉起本进程时
// 注入（main.go 发现即挂 App.nativePanel）；网页/旧桌面壳不带变量，行为
// 与从前完全一致。导航到达由桥经 /api/browser/electron-report 回推，
// onReport 广播 browser_nav——与旧壳 onNavCompleted 同一事件面，前端
// 工具栏/多实例跟随逻辑零改动。

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type electronPanel struct {
	app   *App
	base  string // http://127.0.0.1:<port>
	token string
	hc    *http.Client

	mu      sync.Mutex
	url     string // 最近一次导航地址（state 兜底缓存）
	title   string
	started bool
}

// newElectronPanelFromEnv 从环境变量发现 Electron 面板桥；任一变量缺失返
// 回 nil（非 Electron 形态）。
func newElectronPanelFromEnv(app *App) *electronPanel {
	base := strings.TrimRight(os.Getenv("JX_ELECTRON_BRIDGE"), "/")
	tok := os.Getenv("JX_ELECTRON_BRIDGE_TOKEN")
	if base == "" || tok == "" {
		return nil
	}
	return &electronPanel{
		app:   app,
		base:  base,
		token: tok,
		hc:    &http.Client{Timeout: 90 * time.Second},
	}
}

// bridgeResp 面板桥应答的公共字段（各操作按需取用，ok=false 时只填 err）。
type bridgeResp struct {
	OK      bool            `json:"ok"`
	Err     string          `json:"err"`
	Running bool            `json:"running"`
	URL     string          `json:"url"`
	Title   string          `json:"title"`
	Body    string          `json:"body"`
	Data    string          `json:"data"` // screenshot：base64 PNG
	VW      int             `json:"vw"`
	VH      int             `json:"vh"`
	Value   json.RawMessage `json:"value"` // evaluate：JSON 值
}

func (p *electronPanel) call(op string, args map[string]any) (bridgeResp, error) {
	if args == nil {
		args = map[string]any{}
	}
	args["op"] = op
	body, _ := json.Marshal(args)
	req, err := http.NewRequest("POST", p.base+"/op", bytes.NewReader(body))
	if err != nil {
		return bridgeResp{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-jx-token", p.token)
	resp, err := p.hc.Do(req)
	if err != nil {
		return bridgeResp{}, fmt.Errorf("Electron 面板桥不可达: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	var br bridgeResp
	if err := json.Unmarshal(raw, &br); err != nil {
		return br, fmt.Errorf("面板桥应答解析失败: %w", err)
	}
	if !br.OK {
		return br, fmt.Errorf("%s", br.Err)
	}
	return br, nil
}

// callReady 面板未就绪（壳刚起、<webview> 还没 attach）时短重试放行。
func (p *electronPanel) callReady(op string, args map[string]any) (bridgeResp, error) {
	var lastErr error
	for i := 0; i < 12; i++ {
		br, err := p.call(op, args)
		if err == nil {
			return br, nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "尚未就绪") {
			return br, err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return bridgeResp{}, lastErr
}

func (p *electronPanel) setNav(u, t string) {
	p.mu.Lock()
	p.url, p.title = u, t
	p.mu.Unlock()
}

// ---------- browserTarget 操作面 ----------

// Navigate 打开/跳转 http(s) 与本地文件 file:/// 地址（本地 html 交付预览的
// 主要形态，工具与用户点击同口径），返回落定地址与标题。
func (p *electronPanel) Navigate(raw string) (string, string, error) {
	return p.navigate(raw, true)
}

func (p *electronPanel) navigate(raw string, allowFile bool) (string, string, error) {
	u := strings.TrimSpace(raw)
	ok := strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
	if !ok && allowFile && strings.HasPrefix(u, "file:///") {
		ok = true
	}
	if !ok {
		return "", "", fmt.Errorf("只支持 http/https 或 file:/// 地址，收到: %s", u)
	}
	br, err := p.callReady("navigate", map[string]any{"url": u, "privileged": allowFile})
	if err != nil {
		return "", "", err
	}
	p.mu.Lock()
	p.url, p.title, p.started = br.URL, br.Title, true
	p.mu.Unlock()
	p.app.broadcast(Event{Type: "browser_nav", URL: br.URL, Title: br.Title})
	return br.URL, br.Title, nil
}

func (p *electronPanel) NavAction(action string) error {
	br, err := p.callReady("navAction", map[string]any{"action": action})
	if err != nil {
		return err
	}
	p.setNav(br.URL, br.Title)
	return nil
}

func (p *electronPanel) Content() (string, string, string, error) {
	br, err := p.callReady("content", nil)
	if err != nil {
		return "", "", "", err
	}
	p.setNav(br.URL, br.Title)
	return br.URL, br.Title, br.Body, nil
}

func (p *electronPanel) Click(selector string) error {
	_, err := p.callReady("click", map[string]any{"selector": selector})
	return err
}

func (p *electronPanel) TypeInto(selector, text string, clear bool) error {
	_, err := p.callReady("typeInto", map[string]any{"selector": selector, "text": text, "clear": clear})
	return err
}

func (p *electronPanel) Scroll(direction string, amount int) error {
	_, err := p.callReady("scroll", map[string]any{"direction": direction, "amount": amount})
	return err
}

// SetViewport 与旧壳同语义：视口=面板实际尺寸，宽度经 browser_resize 事件请
// 前端让位（--content-w 调整），高度跟随右栏；桥的 state 回流实际值。
func (p *electronPanel) SetViewport(w, h int) error {
	if w == 0 || h == 0 {
		w, h = browserDefW, browserDefH
	}
	w = clampInt(w, browserMinSide, browserMaxW)
	h = clampInt(h, browserMinSide, browserMaxH)
	p.app.broadcast(Event{Type: "browser_resize", X: float64(w), Y: float64(h)})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if br, err := p.call("state", nil); err == nil && br.VW >= w-4 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func (p *electronPanel) Evaluate(js string) (any, error) {
	br, err := p.callReady("evaluate", map[string]any{"js": js})
	if err != nil {
		return nil, err
	}
	var v any
	if len(br.Value) > 0 {
		_ = json.Unmarshal(br.Value, &v)
	}
	return v, nil
}

func (p *electronPanel) Screenshot() ([]byte, error) {
	br, err := p.callReady("screenshot", nil)
	if err != nil {
		return nil, err
	}
	buf, err := base64.StdEncoding.DecodeString(br.Data)
	if err != nil || len(buf) == 0 {
		return nil, fmt.Errorf("截图数据无效")
	}
	return buf, nil
}

func (p *electronPanel) State() BrowserState {
	if br, err := p.call("state", nil); err == nil {
		p.setNav(br.URL, br.Title)
		return BrowserState{Running: br.Running, URL: br.URL, Title: br.Title, VW: br.VW, VH: br.VH}
	}
	p.mu.Lock()
	st := BrowserState{Running: p.started, URL: p.url, Title: p.title}
	p.mu.Unlock()
	return st
}

func (p *electronPanel) Started() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started
}

func (p *electronPanel) Where() string {
	return "右栏「浏览器」面板（Electron，用户可直接操作）"
}

// Close 关闭面板实例（browser_close）：webview 复位到空白页（元素本身常驻
// DOM，下次打开免重建），下次 Navigate 重新就位。
func (p *electronPanel) Close() {
	_, _ = p.call("close", nil)
	p.mu.Lock()
	p.url, p.title, p.started = "", "", false
	p.mu.Unlock()
	p.app.broadcast(Event{Type: "browser_nav", URL: "", Title: ""})
}

// BroadcastPointer 点击/输入前亮出 AI 位置（桥在页面内注入临时标记）。
func (p *electronPanel) BroadcastPointer(selector string) {
	_, _ = p.call("broadcastPointer", map[string]any{"selector": selector})
}

// onReport 收桥推送的导航到达（did-navigate）：更新缓存并广播 browser_nav。
func (p *electronPanel) onReport(u, t string) {
	p.mu.Lock()
	p.url, p.title = u, t
	if u != "" {
		p.started = true
	}
	p.mu.Unlock()
	p.app.broadcast(Event{Type: "browser_nav", URL: u, Title: t})
}

// handleBrowserElectronReport 面板桥的导航回报端点（仅桥调用；非 Electron
// 形态下静默忽略，返回 ok 以免桥侧重试）。
func (a *App) handleBrowserElectronReport(w http.ResponseWriter, r *http.Request) {
	var p struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "参数解析失败: "+err.Error())
		return
	}
	if ep, ok := a.nativePanel.(*electronPanel); ok {
		ep.onReport(p.URL, p.Title)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
