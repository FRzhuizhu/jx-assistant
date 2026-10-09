package main

// 电脑操作组件：截屏、鼠标点击、键盘输入、滚动、窗口枚举与激活。
// 工具定义跨平台，实现按平台分文件（tool_computer_windows.go 真实现，
// tool_computer_other.go 空），与 pickfolder 的组织方式一致。
// 全部工具要求会话开启「操作电脑」能力（perm.go requireCapability 能力门控），
// fail closed；未开启时模型可在被拦后同轮带 request_capability=true +
// justification 重试申请开启（弹审批卡）。每个工具的描述都带这句权限提醒、
// schema 都带这对升级参数（capRequestProps）——权限前置到工具定义。
// 点击坐标一律以 computer_screenshot 返回的图片像素坐标为准（1:1 映射屏幕）。

import (
	"encoding/json"
	"fmt"
	"strings"
)

// capNoteComputer 是 computer_* 工具描述统一携带的权限提醒句。
const capNoteComputer = "需会话开启「操作电脑」能力：未开启时本工具会被拦下，可在被拦后同轮带 request_capability=true 与一句 justification 重试向用户申请开启（弹审批卡）；命令行替代不了键鼠与截屏。"

func computerScreenshotTool() Tool {
	return Tool{
		Spec: toolSpec("computer_screenshot",
			"截取整个屏幕，以图片信封返回。后续 computer_click 的坐标就以这张图的像素坐标为准（屏幕 1:1）。"+capNoteComputer,
			objSchema(withCapRequest(map[string]any{})),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capComputer, "操作电脑", args); err != nil {
				return "", err
			}
			raw, w, h, err := computerCaptureScreen()
			if err != nil {
				return "", err
			}
			att, err := t.app.storeAttachment(raw, sniffImageMediaType(raw))
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s\n屏幕截图完成：%dx%d 像素。computer_click 的 (x, y) 请直接用这张图上的像素坐标。",
				imageEnvelope(att, "screen.png"), w, h), nil
		},
	}
}

func computerClickTool() Tool {
	return Tool{
		Spec: toolSpec("computer_click",
			"在屏幕的指定像素坐标点击鼠标。坐标来自 computer_screenshot 返回的图片。button 可选 left（默认）/ right / middle；double=true 双击。"+capNoteComputer,
			objSchema(withCapRequest(map[string]any{
				"x":      map[string]any{"type": "integer", "description": "横坐标（屏幕像素）"},
				"y":      map[string]any{"type": "integer", "description": "纵坐标（屏幕像素）"},
				"button": map[string]any{"type": "string", "enum": []string{"left", "right", "middle"}, "description": "鼠标键，默认 left"},
				"double": boolProp("是否双击，默认 false"),
			}), "x", "y"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capComputer, "操作电脑", args); err != nil {
				return "", err
			}
			var p struct {
				X      int    `json:"x"`
				Y      int    `json:"y"`
				Button string `json:"button"`
				Double bool   `json:"double"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			button := p.Button
			if button == "" {
				button = "left"
			}
			if err := computerClickAt(p.X, p.Y, button, p.Double); err != nil {
				return "", err
			}
			return fmt.Sprintf("已在 (%d, %d) %s%s", p.X, p.Y, button, map[bool]string{true: "双击", false: "单击"}[p.Double]), nil
		},
	}
}

func computerTypeTool() Tool {
	return Tool{
		Spec: toolSpec("computer_type",
			"向当前聚焦的窗口输入文本（Unicode 逐字符注入，支持中文）。先用 computer_click 让目标输入框获得焦点。"+capNoteComputer,
			objSchema(withCapRequest(map[string]any{"text": strProp("要输入的文本")}), "text"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capComputer, "操作电脑", args); err != nil {
				return "", err
			}
			var p struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if p.Text == "" {
				return "", fmt.Errorf("text 不能为空")
			}
			if err := computerTypeText(p.Text); err != nil {
				return "", err
			}
			return fmt.Sprintf("已输入 %d 个字符", len([]rune(p.Text))), nil
		},
	}
}

func computerKeyTool() Tool {
	return Tool{
		Spec: toolSpec("computer_key",
			"向当前聚焦的窗口发送按键。key 支持名称（enter/esc/tab/backspace/delete/home/end/pageup/pagedown/up/down/left/right/space/f1-f12）或单个字符；可用 ctrl/alt/shift 修饰（如 ctrl+s）。"+capNoteComputer,
			objSchema(withCapRequest(map[string]any{
				"key":   strProp("按键名称或单字符"),
				"ctrl":  boolProp("按住 Ctrl"),
				"alt":   boolProp("按住 Alt"),
				"shift": boolProp("按住 Shift"),
			}), "key"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capComputer, "操作电脑", args); err != nil {
				return "", err
			}
			var p struct {
				Key   string `json:"key"`
				Ctrl  bool   `json:"ctrl"`
				Alt   bool   `json:"alt"`
				Shift bool   `json:"shift"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if p.Key == "" {
				return "", fmt.Errorf("key 不能为空")
			}
			if err := computerSendKey(p.Key, p.Ctrl, p.Alt, p.Shift); err != nil {
				return "", err
			}
			mod := ""
			if p.Ctrl {
				mod += "Ctrl+"
			}
			if p.Alt {
				mod += "Alt+"
			}
			if p.Shift {
				mod += "Shift+"
			}
			return "已发送按键 " + mod + p.Key, nil
		},
	}
}

func computerScrollTool() Tool {
	return Tool{
		Spec: toolSpec("computer_scroll",
			"在鼠标当前位置滚动滚轮。delta 为正向上滚、为负向下滚，单位是滚轮格数。"+capNoteComputer,
			objSchema(withCapRequest(map[string]any{
				"delta": map[string]any{"type": "integer", "description": "滚动格数，正=上滚，负=下滚"},
			}), "delta"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capComputer, "操作电脑", args); err != nil {
				return "", err
			}
			var p struct {
				Delta int `json:"delta"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if p.Delta == 0 {
				return "", fmt.Errorf("delta 不能为 0")
			}
			if err := computerScroll(p.Delta); err != nil {
				return "", err
			}
			return fmt.Sprintf("已滚动 %d 格", p.Delta), nil
		},
	}
}

func computerWindowsTool() Tool {
	return Tool{
		Spec: toolSpec("computer_windows",
			"列出当前可见的桌面窗口（标题清单），或把标题包含指定文字的窗口激活到前台。用于确认目标程序在运行、切换操作对象。"+capNoteComputer,
			objSchema(withCapRequest(map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"list", "activate"}, "description": "list=列出窗口；activate=激活窗口"},
				"title":  strProp("activate 模式：窗口标题需要包含的文字（子串匹配）"),
			}), "action"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireCapability(t, capComputer, "操作电脑", args); err != nil {
				return "", err
			}
			var p struct {
				Action string `json:"action"`
				Title  string `json:"title"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if p.Action == "activate" {
				if strings.TrimSpace(p.Title) == "" {
					return "", fmt.Errorf("activate 模式需要 title 参数")
				}
				if err := computerActivateWindow(p.Title); err != nil {
					return "", err
				}
				return "已激活包含「" + p.Title + "」的窗口", nil
			}
			titles, err := computerListWindows()
			if err != nil {
				return "", err
			}
			if len(titles) == 0 {
				return "（没有可见的带标题窗口）", nil
			}
			return "可见窗口：\n- " + strings.Join(titles, "\n- "), nil
		},
	}
}

func computerTools() []Tool {
	return []Tool{
		computerScreenshotTool(), computerClickTool(), computerTypeTool(),
		computerKeyTool(), computerScrollTool(), computerWindowsTool(),
	}
}
