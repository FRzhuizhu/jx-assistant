package main

// call_api：通用直通工具——「前端给用户什么接口，AI 就拥有相同的接口」。
// 接口目录不是手写清单，而是路由注册的副产品：server.go 的 registerRoutes 用
// a.apiRoute(...) 注册的每条路由同时带一份元数据（说明 / 参数要点 / 最低权限
// 档 / 危险标记）。元数据缺席（Perm 为空）的路由照常对前端开放，但**不进 AI
// 目录**——默认关闭、显式声明才开放，新增路由漏配元数据不会意外暴露给模型。
//
// 执行是**进程内直调**：构造 http.Request（RemoteAddr=回环语义）打到缓存的
// mux 上，与前端走同一个 handler、同一套校验与行为，不开网络、不占端口
// （同 componentbridge.go 组件桥的直调模式）。
//
// 三道闸：
//  1. 目录闸：接口必须在注册表里带元数据（未开放=调不着，报错并指路）；
//  2. 权限闸：元数据 Perm 是该接口要求的最低会话权限档，会话档位低于即拒绝
//     （不静默钳制，与派发工具同一口径）；目录本身也按会话档位过滤——环境
//     快照的「可调接口」区块只列当前档位调得着的；
//  3. 危险闸：Danger 非空（删除/清空/切换分支/立即执行类）的接口调用前走与
//     文件越权相同的审批卡（App.requestApproval），用户批准才执行；
//     always=本会话后续免审（与文件授权同一语义）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

// apiMeta 是一条路由对 AI 开放所需的元数据（server.go registerRoutes 里随
// apiRoute 一起登记，路由与目录同源同址，不存在第二份清单）。
type apiMeta struct {
	Method  string
	Path    string // 含 {参数} 占位符，目录按注册原文展示
	Summary string // 一句话说明（目录展示）
	Params  string // 参数要点（目录展示）
	Perm    string // 最低权限档：permReadOnly / permWorkspace / permFull；空=不对 AI 开放
	Danger  string // 非空=危险说明，调用前走审批卡
}

// apiRoute 注册一条 HTTP 路由并（当 meta.Perm 非空时）把它收进 AI 可调目录。
// 幂等：同一 pattern 重复注册只保留第一份（Routes() 与 apiMuxOf() 各自触发
// 一轮注册时不会翻倍）。
func (a *App) apiRoute(mux *http.ServeMux, pattern string, h http.HandlerFunc, meta apiMeta) {
	mux.HandleFunc(pattern, h)
	if meta.Perm == "" {
		return
	}
	method, path, ok := strings.Cut(pattern, " ")
	if !ok { // 无方法前缀的老式注册不会走到这：开放目录全部带方法前缀
		return
	}
	meta.Method, meta.Path = method, path
	a.apiMetaMu.Lock()
	defer a.apiMetaMu.Unlock()
	if a.apiMetaByPattern == nil {
		a.apiMetaByPattern = map[string]apiMeta{}
	}
	if _, dup := a.apiMetaByPattern[pattern]; dup {
		return
	}
	a.apiMetaByPattern[pattern] = meta
	a.apiMetas = append(a.apiMetas, meta)
}

// apiMuxOf 返回（并缓存）一份完整注册的 mux，供 call_api 进程内直调。
// 与前端看到的是同一套路由注册（registerRoutes），行为完全一致。
// 注册在锁外进行（apiRoute 各自拿锁登记元数据），缓存写入用双检——并发的
// 首次调用可能各建一份 mux，元数据登记幂等、两份 mux 内容相同，取先到者。
func (a *App) apiMuxOf() *http.ServeMux {
	a.apiMetaMu.Lock()
	cached := a.apiMux
	a.apiMetaMu.Unlock()
	if cached != nil {
		return cached
	}
	mux := http.NewServeMux()
	a.registerRoutes(mux)
	a.apiMetaMu.Lock()
	if a.apiMux == nil {
		a.apiMux = mux
	}
	a.apiMetaMu.Unlock()
	return a.apiMux
}

// apiCatalogText 渲染「可调接口」目录（环境快照区块）：按会话权限档过滤——
// 只列当前档位调得着的接口；危险接口标 ⚠（调用前会弹审批卡）。顺序即注册
// 顺序（server.go registerRoutes 的书写序），稳定可读。首次调用会先确保路由
// 注册完成（懒初始化：测试等没有走过 Routes() 的进程也能拿到完整目录）。
func (a *App) apiCatalogText(perm string) string {
	a.apiMuxOf()
	a.apiMetaMu.Lock()
	metas := append([]apiMeta(nil), a.apiMetas...)
	a.apiMetaMu.Unlock()
	rank := sandboxPolicyRank(perm)
	if rank < 0 || len(metas) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range metas {
		if sandboxPolicyRank(m.Perm) > rank {
			continue
		}
		mark := "  "
		if m.Danger != "" {
			mark = "⚠ "
		}
		line := m.Method + " " + m.Path + " — " + m.Summary
		if m.Params != "" {
			line += "（" + m.Params + "）"
		}
		b.WriteString(mark + line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// apiMetaFor 用 mux 的同款匹配规则（含 {参数} 通配与方法优先级）查一条请求
// 对应的元数据：mux.Handler 返回命中的注册 pattern，再查表。匹配不到（404/
// 405/未开放）一律返回零值。
func (a *App) apiMetaFor(r *http.Request) (apiMeta, bool) {
	_, pattern := a.apiMuxOf().Handler(r)
	a.apiMetaMu.Lock()
	meta, ok := a.apiMetaByPattern[pattern]
	a.apiMetaMu.Unlock()
	return meta, ok
}

// callAPIResultMax 是 call_api 返回给模型的正文上限（字符）：接口响应可能很
// 大（会话列表、diff、清单），超限截断并说明——需要更多时用带查询参数的
// 窄化请求或专用工具（读会话历史有 read_session_messages 等）。
const callAPIResultMax = 20000

func callAPITool() Tool {
	return Tool{
		Spec: toolSpec("call_api", "调用本产品的一个 HTTP 接口——与前端界面调用的完全相同（进程内直调同一处理器，不走网络）。可用接口见运行环境快照的「可调接口」区块（已按本会话权限档过滤）；标 ⚠ 的接口（删除/清空类）调用前会弹审批卡，用户批准才执行。path 写完整路径（可带查询串，路径参数直接填实际值，如 /api/sessions/abc123/chat）；GET 之外的请求把 JSON 参数放 params。响应原样返回（状态码+正文，正文超长会截断）。适合做没有专用工具的界面级操作（改会话设置、排队管理、回答提问、待办维护、Git 面板只读查询等）。",
			objSchema(map[string]any{
				"path":   strProp("接口路径，以 / 开头，可带查询串；路径参数填实际值，如 /api/sessions/{id}/chat 的 id 换成目标会话 id"),
				"method": strProp("HTTP 方法：GET（默认）/ POST / PATCH / DELETE，与环境快照目录里标的方法一致"),
				"params": strProp("请求体 JSON 对象（GET 忽略；POST/PATCH/DELETE 按各接口的参数说明填）"),
			}, "path")),
		Run: runCallAPI,
	}
}

// runCallAPI 是 call_api 的执行体：三道闸（目录/权限/危险审批）→ 进程内直调
// mux（与前端同一 handler）→ 状态码与正文回给模型。
func runCallAPI(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Path   string          `json:"path"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path := strings.TrimSpace(p.Path)
	if path == "" || !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("path 不能为空且必须以 / 开头（如 /api/sessions）")
	}
	method := strings.ToUpper(strings.TrimSpace(p.Method))
	if method == "" {
		method = http.MethodGet
	}
	// 直调的请求地址是「回环假地址」：mux 匹配只看 path；RemoteAddr=回环与真实
	// 前端请求同语义（部分接口仅回环可达，如剪贴板读取）
	req, err := http.NewRequestWithContext(t.ctx, method, "http://127.0.0.1"+path, nil)
	if err != nil {
		return "", fmt.Errorf("请求构造失败：%v", err)
	}
	req.RemoteAddr = "127.0.0.1:0"

	// 闸一：目录（mux 同款匹配含方法校验——方法不对会匹配到 405 的空 pattern）
	meta, ok := t.app.apiMetaFor(req)
	if !ok {
		return "", fmt.Errorf("接口 %s %s 不在可调目录里（未对 AI 开放、方法不对或路径不存在）。可用接口以运行环境快照的「可调接口」区块为准；没有对应接口的操作请用专用工具完成", method, path)
	}
	// 闸二：权限（会话档位低于接口要求即拒绝，不静默钳制）
	if sandboxPolicyRank(permModeOf(t.sess)) < sandboxPolicyRank(meta.Perm) {
		return "", fmt.Errorf("接口 %s %s 需要权限档「%s」，本会话当前是「%s」（%s）——不能越档调用。如确有需要，请让用户在权限面板调宽本会话权限，或用 request_permission 申请",
			method, meta.Path, permModeLabel(meta.Perm), permModeLabel(permModeOf(t.sess)), permModeOf(t.sess))
	}
	// 闸三：危险接口走审批卡（与文件越权同一张卡；always=本会话后续免审）
	if meta.Danger != "" {
		approved, _, timedOut := t.app.requestApproval(t.ctx, t.sess, "call_api",
			fmt.Sprintf("%s %s", method, path), meta.Danger)
		if !approved {
			if timedOut {
				return "", fmt.Errorf("接口 %s %s 是危险操作（%s），审批等待超时未获用户批准，已取消。需要时请用户直接在界面上操作", method, path, meta.Danger)
			}
			return "", fmt.Errorf("接口 %s %s 是危险操作（%s），用户未批准，已取消", method, path, meta.Danger)
		}
	}
	// 带上请求体（GET/HEAD 不带）
	if len(p.Params) > 0 && method != http.MethodGet && method != http.MethodHead {
		req.Body = io.NopCloser(bytes.NewReader(p.Params))
		req.ContentLength = int64(len(p.Params))
		req.Header.Set("Content-Type", "application/json")
	}
	// 进程内直调：同一 mux、同一 handler，行为与前端完全一致
	rec := httptest.NewRecorder()
	t.app.apiMuxOf().ServeHTTP(rec, req)
	body := rec.Body.String()
	note := ""
	if truncated, clipped := clipBody(body, callAPIResultMax); clipped {
		body, note = truncated, fmt.Sprintf("\n\n（响应共超出上限，此处显示前 %d 字；需要剩余部分请用更窄的查询参数或专用工具）", callAPIResultMax)
	}
	return fmt.Sprintf("HTTP %d %s\n%s%s", rec.Code, http.StatusText(rec.Code), body, note), nil
}

// clipBody 把响应正文截到 limit 个字符（rune 计数，中文安全），返回截断后的
// 正文与是否发生了截断。
func clipBody(s string, limit int) (string, bool) {
	runes := []rune(s)
	if len(runes) <= limit {
		return s, false
	}
	return string(runes[:limit]), true
}
