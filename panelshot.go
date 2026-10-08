package main

// panel_screenshot 工具：对桌面版右侧栏截图（浏览器页/文件查看页/任何视图
// 通用）。背景（用户口径）：用户在右栏打开了什么不再注入对话——模型不该
// 顺带知道；当用户主动说「我在右侧栏打开了 XX」时，模型用本工具看画面。
// 截的是屏幕像素（GDI BitBlt），因此右栏里无论是什么内容都支持——原生
// WebView2 浏览器面板、iframe 文件查看页、以后新增的视图一概通用。

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// panelViewReport 前端注入 JS 回传的右栏矩形（CSS px，相对主界面客户区）。
type panelViewReport struct {
	X, Y, W, H float64
	Dpr        float64
	OK         bool // 右栏展开且有有效尺寸
}

// handleShellPanelView 收前端探针回传（只服务本机主界面注入的 JS；跨平台，
// 非 Windows 下通道不会被填充，工具侧自然报「仅桌面版支持」）。
func (a *App) handleShellPanelView(w http.ResponseWriter, r *http.Request) {
	var rep panelViewReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, "参数解析失败: "+err.Error())
		return
	}
	if a.panelViewCh != nil {
		select {
		case a.panelViewCh <- rep:
		default: // 只留最新一份，旧等待者早已超时放行
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func panelScreenshotTool() Tool {
	return Tool{
		Spec: toolSpec("panel_screenshot",
			"截取桌面版右侧栏的当前画面，以图片信封返回（用户可查看，支持视觉的模型能直接看到）。"+
				"适用于：用户说他在右侧栏打开了某个文件或网页、让你看看内容/检查效果/确认显示是否正常时。"+
				"右栏无论是「浏览器」页面还是「文件」查看页都能截到；右栏没开时会明确报错。仅桌面版支持。",
			objSchema(map[string]any{})),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			buf, err := t.app.captureRightPanel()
			if err != nil {
				return "", err
			}
			att, err := t.app.storeAttachment(buf, sniffImageMediaType(buf))
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s\n右侧栏截图完成（%dx%d）", imageEnvelope(att, "panel-screenshot.png"), att.Width, att.Height), nil
		},
	}
}
