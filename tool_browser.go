package main

// 浏览器操作工具：形态无关地驱动 browserTarget（browser_ops.go）——桌面形态
// 的内嵌目标是右栏原生 WebView2 面板（真浏览器，用户可直接旁观共操），网页
// 形态是后台无头实例，headed 弹独立窗口。点击/输入前先 BroadcastPointer 亮出
// AI 位置（原生面板在目标页面内注入标记元素）。
// 所有工具要求会话开启「操作浏览器」能力（perm.go requireCapability 能力门控，
// 权限芯片里的 browser 复选框），fail closed；未开启时模型可在被拦后同轮带
// request_capability=true + justification 重试申请开启（弹审批卡）。每个工具的
// 描述都带这句权限提醒、schema 都带这对升级参数（capRequestProps）——工具
// 定义是模型的第一手说明，权限前置到那里，别只靠运行时报错指路。
// browser_screenshot 的截图入库为附件：用户直接看，视觉模型可内联。

import (
	"encoding/json"
	"fmt"
	"strings"
)

// activeHost 取工具作用目标（最近一次 browser_open 选定的实例，另一形态关闭时
// 自动回退到仍在运行的实例）；浏览器从未打开时给出与旧版一致的提示。
func activeHost(t *toolContext) (browserTarget, error) {
	h := t.app.activeBrowser()
	if h == nil {
		return nil, fmt.Errorf("浏览器尚未打开：先调用 browser_open")
	}
	return h, nil
}

// capNoteBrowser 是 browser_* 工具描述统一携带的权限提醒句。
const capNoteBrowser = "需会话开启「操作浏览器」能力：未开启时本工具会被拦下，可在被拦后同轮带 request_capability=true 与一句 justification 重试向用户申请开启（弹审批卡）；不要改用命令行拉浏览器（沙箱内必败）。"

func browserOpenTool() Tool {
	return Tool{
		Spec: toolSpec("browser_open",
			"打开或导航到指定网页（各实例各自保留地址与登录态，重开会回到之前的页面）。mode 选打开方式："+
				"headless（默认）在内嵌浏览器打开——桌面版显示在右栏「浏览器」面板（原生 Edge 网页，用户可旁观并随时直接操作），网页版在后台运行；"+
				"headed 弹一个独立的浏览器窗口，用户在真实窗口里查看和操作，适合演示给用户、需要用户配合填写的场景。"+
				"之后的其他 browser_* 工具作用于最近一次 browser_open 选定的实例。只接受 http/https 地址。返回页面标题与最终地址。"+capNoteBrowser,
			objSchema(withCapRequest(map[string]any{
				"url":  strProp("完整网址，http 或 https"),
				"mode": strProp("headless（默认，内嵌形态）或 headed（独立窗口）"),
			}), "url")),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器", args); err != nil {
				return "", err
			}
			var p struct {
				URL  string `json:"url"`
				Mode string `json:"mode"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			h := t.app.browserFor(p.Mode)
			url, title, err := h.Navigate(p.URL)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("已打开：%s（标题：%s；%s）", url, title, h.Where()), nil
		},
	}
}

func browserContentTool() Tool {
	return Tool{
		Spec: toolSpec("browser_content",
			"读取当前网页的地址、标题与可见正文文本（截断到 2 万字符）。读不到正文时可用 browser_evaluate 自行提取。"+capNoteBrowser,
			objSchema(withCapRequest(map[string]any{})),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器", args); err != nil {
				return "", err
			}
			h, err := activeHost(t)
			if err != nil {
				return "", err
			}
			url, title, body, err := h.Content()
			if err != nil {
				return "", err
			}
			if len([]rune(body)) > 20000 {
				body = string([]rune(body)[:20000]) + "\n…（正文过长已截断，可用 browser_evaluate 分段提取）"
			}
			return fmt.Sprintf("地址：%s\n标题：%s\n\n%s", url, title, body), nil
		},
	}
}

func browserClickTool() Tool {
	return Tool{
		Spec: toolSpec("browser_click",
			"点击当前网页里匹配 CSS 选择器的元素（第一个匹配项）。点击后稍等页面反应。"+capNoteBrowser,
			objSchema(withCapRequest(map[string]any{"selector": strProp("CSS 选择器")}), "selector"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器", args); err != nil {
				return "", err
			}
			var p struct {
				Selector string `json:"selector"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			h, err := activeHost(t)
			if err != nil {
				return "", err
			}
			h.BroadcastPointer(p.Selector) // 先亮出 AI 位置，人看得见要点的元素
			if err := h.Click(p.Selector); err != nil {
				return "", err
			}
			return "已点击 " + p.Selector, nil
		},
	}
}

func browserTypeTool() Tool {
	return Tool{
		Spec: toolSpec("browser_type",
			"向当前网页里匹配 CSS 选择器的输入框输入文本（支持中文，整段注入）。clear=true 时先清空已有内容（默认清空）。"+capNoteBrowser,
			objSchema(withCapRequest(map[string]any{
				"selector": strProp("输入框的 CSS 选择器，如 input[name='q']"),
				"text":     strProp("要输入的文本"),
				"clear":    boolProp("是否先清空，默认 true"),
			}), "selector", "text"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器", args); err != nil {
				return "", err
			}
			var p struct {
				Selector string `json:"selector"`
				Text     string `json:"text"`
				Clear    *bool  `json:"clear"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			clear := p.Clear == nil || *p.Clear
			h, err := activeHost(t)
			if err != nil {
				return "", err
			}
			h.BroadcastPointer(p.Selector)
			if err := h.TypeInto(p.Selector, p.Text, clear); err != nil {
				return "", err
			}
			return fmt.Sprintf("已向 %s 输入 %d 个字符", p.Selector, len([]rune(p.Text))), nil
		},
	}
}

func browserScrollTool() Tool {
	return Tool{
		Spec: toolSpec("browser_scroll",
			"滚动当前网页。direction=down/up，amount 为滚动幅度（1≈100 像素，默认 5）。"+capNoteBrowser,
			objSchema(withCapRequest(map[string]any{
				"direction": strProp("down 或 up，默认 down"),
				"amount":    intProp("滚动幅度，1≈100 像素，默认 5"),
			})),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器", args); err != nil {
				return "", err
			}
			var p struct {
				Direction string `json:"direction"`
				Amount    int    `json:"amount"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if p.Amount <= 0 {
				p.Amount = 5
			}
			h, err := activeHost(t)
			if err != nil {
				return "", err
			}
			if err := h.Scroll(p.Direction, p.Amount); err != nil {
				return "", err
			}
			dir := "向下"
			if strings.EqualFold(strings.TrimSpace(p.Direction), "up") {
				dir = "向上"
			}
			return fmt.Sprintf("已%s滚动 %d 格", dir, p.Amount), nil
		},
	}
}

func browserViewportTool() Tool {
	return Tool{
		Spec: toolSpec("browser_viewport",
			"设置内嵌浏览器的视口（CSS 像素，宽 320–3840、高 320–2160），用于响应式测试（如手机宽度 393×852）。reset=true 恢复默认 1280×900。"+
				"桌面右栏面板=网页实际显示区域：宽度会请求面板调整（高度跟随右栏，无法独立设定）；网页版后台实例则完整生效。"+capNoteBrowser,
			objSchema(withCapRequest(map[string]any{
				"width":  intProp("视口宽（CSS px）"),
				"height": intProp("视口高（CSS px）"),
				"reset":  boolProp("恢复默认视口"),
			})),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器", args); err != nil {
				return "", err
			}
			var p struct {
				Width  int  `json:"width"`
				Height int  `json:"height"`
				Reset  bool `json:"reset"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if p.Reset {
				p.Width, p.Height = 0, 0
			}
			h, err := activeHost(t)
			if err != nil {
				return "", err
			}
			if err := h.SetViewport(p.Width, p.Height); err != nil {
				return "", err
			}
			st := h.State()
			return fmt.Sprintf("视口已设为 %dx%d", st.VW, st.VH), nil
		},
	}
}

func browserEvaluateTool() Tool {
	return Tool{
		Spec: toolSpec("browser_evaluate",
			"在当前网页执行 JavaScript 并返回结果（JSON 序列化）。用于提取浏览器组件覆盖不到的数据，如 document.querySelector(...).href、JSON.stringify(window.__data)。"+capNoteBrowser,
			objSchema(withCapRequest(map[string]any{"js": strProp("要执行的 JavaScript 表达式或语句")}), "js"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器", args); err != nil {
				return "", err
			}
			var p struct {
				JS string `json:"js"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			h, err := activeHost(t)
			if err != nil {
				return "", err
			}
			res, err := h.Evaluate(p.JS)
			if err != nil {
				return "", err
			}
			out, err := json.Marshal(res)
			if err != nil {
				return fmt.Sprintf("%v", res), nil
			}
			s := string(out)
			if len(s) > 20000 {
				s = s[:20000] + "…"
			}
			return s, nil
		},
	}
}

func browserScreenshotTool() Tool {
	return Tool{
		Spec: toolSpec("browser_screenshot",
			"截取当前网页可见区域，以图片信封返回：用户可在界面查看，支持视觉的模型能直接看到截图。"+capNoteBrowser,
			objSchema(withCapRequest(map[string]any{})),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器", args); err != nil {
				return "", err
			}
			h, err := activeHost(t)
			if err != nil {
				return "", err
			}
			buf, err := h.Screenshot()
			if err != nil {
				return "", err
			}
			att, err := t.app.storeAttachment(buf, sniffImageMediaType(buf))
			if err != nil {
				return "", err
			}
			st := h.State()
			return fmt.Sprintf("%s\n截图完成：%s（标题：%s，%dx%d）",
				imageEnvelope(att, "browser-screenshot.png"), st.URL, st.Title, att.Width, att.Height), nil
		},
	}
}

func browserCloseTool() Tool {
	return Tool{
		Spec: toolSpec("browser_close",
			"关闭当前浏览器实例（工具正在作用的那个）。若另一形态的实例仍在运行，后续工具自动作用于它；都没了，下次 browser_open 重新启动。"+capNoteBrowser,
			objSchema(withCapRequest(map[string]any{}))),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capBrowser, "操作浏览器", args); err != nil {
				return "", err
			}
			h, err := activeHost(t)
			if err != nil {
				return "浏览器未在运行", nil
			}
			h.Close()
			if repl := t.app.activeBrowser(); repl != nil {
				return "已关闭当前浏览器实例；另一实例仍在运行，后续工具作用于它", nil
			}
			return "浏览器已关闭", nil
		},
	}
}

func browserTools() []Tool {
	return []Tool{
		browserOpenTool(), browserContentTool(), browserClickTool(),
		browserTypeTool(), browserScrollTool(), browserViewportTool(),
		browserEvaluateTool(), browserScreenshotTool(), browserCloseTool(),
	}
}
