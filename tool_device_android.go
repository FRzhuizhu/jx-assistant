//go:build android

package main

// 手机操控工具（device_* / adb_*，能力「操作手机」门控）：
//   - device_*：经设备桥由 Kotlin 无障碍服务执行——截屏（takeScreenshot）、
//     界面树、点按/滑动手势、焦点输入、返回/主屏/最近任务、打开应用。
//   - adb_*：引擎内置的 ADB 主机客户端直连 adbd（本机无线调试或局域网设备），
//     shell uid 权限（screencap/input/pm 等，比无障碍更强）。
// 与桌面 computer_* 同构：截图走附件信封，点按坐标以截图像素为准。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func deviceTools() []Tool {
	return []Tool{
		deviceScreenshotTool(),
		deviceUiTool(),
		deviceTapTool(),
		deviceSwipeTool(),
		deviceTypeTool(),
		deviceKeyTool(),
		deviceAppTool(),
		adbConnectTool(),
		adbShellTool(),
		adbScreencapTool(),
	}
}

// deviceBridgeHint 桥/服务不可用时的可操作指引（模型转述给用户）。
func deviceBridgeHint() string {
	return "无障碍服务未就绪：请让用户开启 设置→无障碍→已下载的应用→数字助理（开关打开），开启后重试"
}

// requireDevice 设备工具的统一门控：能力开关 + 桥与服务状态（打开应用除外，
// 走引擎自身的 am 命令，不需要无障碍）。args 透传给 requireCapability：
// 能力未开启时模型可在被拦后带 request_capability + justification 重试申请
// 开启（弹审批卡）。
func requireDevice(t *toolContext, needBridge bool, args json.RawMessage) error {
	if err := requireCapability(t, capDevice, "操作手机", args); err != nil {
		return err
	}
	if needBridge {
		if bridgeAlive, svcAlive := deviceBridgeState(); !bridgeAlive || !svcAlive {
			return fmt.Errorf("%s", deviceBridgeHint())
		}
	}
	return nil
}

// capNoteDevice 是 device_* / adb_* 工具描述统一携带的权限提醒句。
const capNoteDevice = "需「操作手机」能力：未开启时本工具会被拦下，可在被拦后同轮带 request_capability=true 与一句 justification 重试向用户申请开启（弹审批卡）。"

func deviceScreenshotTool() Tool {
	return Tool{
		Spec: toolSpec("device_screenshot",
			"截取手机当前屏幕，以图片信封返回。device_tap/device_swipe 的坐标就以这张图的像素坐标为准（屏幕 1:1）。"+capNoteDevice+"无障碍服务需已开启。",
			objSchema(withCapRequest(map[string]any{})),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireDevice(t, true, args); err != nil {
				return "", err
			}
			res, err := deviceSubmit("screenshot", nil, 25*time.Second)
			if err != nil {
				return "", err
			}
			if !res.OK {
				return "", fmt.Errorf("截屏失败：%s", res.Err)
			}
			raw, err := base64.StdEncoding.DecodeString(res.Data)
			if err != nil || len(raw) < 8 {
				return "", fmt.Errorf("截屏数据无效")
			}
			att, err := t.app.storeAttachment(raw, sniffImageMediaType(raw))
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s\n手机截屏完成：%dx%d 像素。device_tap 的 (x, y) 请直接用这张图上的像素坐标。",
				imageEnvelope(att, "device-screen.png"), res.W, res.H), nil
		},
	}
}

func deviceUiTool() Tool {
	return Tool{
		Spec: toolSpec("device_ui",
			"读取手机当前界面的可交互元素树（无障碍节点：文本/描述/类名、是否可点、像素 bounds）。用于在点按前确认元素位置——先 device_screenshot 看全貌，再用本工具精确定位。"+capNoteDevice,
			objSchema(withCapRequest(map[string]any{})),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireDevice(t, true, args); err != nil {
				return "", err
			}
			res, err := deviceSubmit("ui_dump", nil, 20*time.Second)
			if err != nil {
				return "", err
			}
			if !res.OK {
				return "", fmt.Errorf("读取界面失败：%s", res.Err)
			}
			out := res.Data
			if len([]rune(out)) > digestMaxRunes {
				out = string([]rune(out)[:digestMaxRunes]) + "\n…（界面树过大已截断，可先 device_screenshot 看全貌）"
			}
			return "当前界面可交互元素树（[可点] 标记 clickable，bounds 为像素矩形，坐标可直接用于 device_tap）：\n" + out, nil
		},
	}
}

func deviceTapTool() Tool {
	return Tool{
		Spec: toolSpec("device_tap",
			"在手机屏幕指定像素坐标点按（坐标以 device_screenshot 返回的图为准）。"+capNoteDevice,
			objSchema(withCapRequest(map[string]any{
				"x": map[string]any{"type": "integer", "description": "横坐标（屏幕像素）"},
				"y": map[string]any{"type": "integer", "description": "纵坐标（屏幕像素）"},
			}), "x", "y"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireDevice(t, true, args); err != nil {
				return "", err
			}
			var p struct {
				X int `json:"x"`
				Y int `json:"y"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			res, err := deviceSubmit("tap", map[string]any{"x": p.X, "y": p.Y}, 15*time.Second)
			if err != nil {
				return "", err
			}
			if !res.OK {
				return "", fmt.Errorf("点按失败：%s", res.Err)
			}
			return fmt.Sprintf("已在 (%d, %d) 点按", p.X, p.Y), nil
		},
	}
}

func deviceSwipeTool() Tool {
	return Tool{
		Spec: toolSpec("device_swipe",
			"在手机屏幕上从 (x1,y1) 滑动到 (x2,y2)，duration 毫秒（默认 300，滚动列表建议 200-400）。"+capNoteDevice,
			objSchema(withCapRequest(map[string]any{
				"x1": map[string]any{"type": "integer", "description": "起点横坐标"},
				"y1": map[string]any{"type": "integer", "description": "起点纵坐标"},
				"x2": map[string]any{"type": "integer", "description": "终点横坐标"},
				"y2": map[string]any{"type": "integer", "description": "终点纵坐标"},
				"duration": map[string]any{"type": "integer", "description": "滑动时长毫秒，默认 300"},
			}), "x1", "y1", "x2", "y2"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireDevice(t, true, args); err != nil {
				return "", err
			}
			var p struct {
				X1, Y1, X2, Y2, Duration int
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if p.Duration <= 0 {
				p.Duration = 300
			}
			res, err := deviceSubmit("swipe", map[string]any{"x1": p.X1, "y1": p.Y1, "x2": p.X2, "y2": p.Y2, "duration": p.Duration}, 15*time.Second)
			if err != nil {
				return "", err
			}
			if !res.OK {
				return "", fmt.Errorf("滑动失败：%s", res.Err)
			}
			return fmt.Sprintf("已从 (%d,%d) 滑动到 (%d,%d)", p.X1, p.Y1, p.X2, p.Y2), nil
		},
	}
}

func deviceTypeTool() Tool {
	return Tool{
		Spec: toolSpec("device_type",
			"向手机当前获得输入焦点的输入框输入文本（整框替换）。先点按输入框获得焦点再输入。"+capNoteDevice,
			objSchema(withCapRequest(map[string]any{
				"text": strProp("要输入的文本（整框替换）"),
			}), "text"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireDevice(t, true, args); err != nil {
				return "", err
			}
			var p struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			res, err := deviceSubmit("type", map[string]any{"text": p.Text}, 15*time.Second)
			if err != nil {
				return "", err
			}
			if !res.OK {
				return "", fmt.Errorf("输入失败：%s（多半是没有输入框持有焦点，先 device_tap 点一下目标输入框）", res.Err)
			}
			return "已输入", nil
		},
	}
}

func deviceKeyTool() Tool {
	return Tool{
		Spec: toolSpec("device_key",
			"按手机系统键：back 返回 / home 主屏 / recents 最近任务 / notifications 通知栏。"+capNoteDevice,
			objSchema(withCapRequest(map[string]any{
				"key": map[string]any{"type": "string", "enum": []string{"back", "home", "recents", "notifications"}, "description": "系统键"},
			}), "key"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireDevice(t, true, args); err != nil {
				return "", err
			}
			var p struct {
				Key string `json:"key"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			res, err := deviceSubmit("key", map[string]any{"key": p.Key}, 10*time.Second)
			if err != nil {
				return "", err
			}
			if !res.OK {
				return "", fmt.Errorf("按键失败：%s", res.Err)
			}
			return "已按 " + p.Key, nil
		},
	}
}

func deviceAppTool() Tool {
	return Tool{
		Spec: toolSpec("device_app",
			"在手机上打开指定应用（参数为应用包名，如 com.android.settings 打开设置；不确定包名可先用 adb_shell 的 pm list packages 查）。经系统 am 启动，不需要无障碍服务。"+capNoteDevice,
			objSchema(withCapRequest(map[string]any{
				"package": strProp("应用包名"),
			}), "package"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireDevice(t, false, args); err != nil {
				return "", err
			}
			var p struct {
				Package string `json:"package"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if p.Package == "" || len(p.Package) > 200 {
				return "", fmt.Errorf("包名不合法")
			}
			cmd := fmt.Sprintf("am start -n %s/%s.Main -W 2>/dev/null || monkey -p %s -c android.intent.category.LAUNCHER 1",
				p.Package, p.Package, p.Package)
			out, err := t.app.runChild(t.ctx, sandboxRun{
				Dir: t.workspace(), Command: cmd, Shell: "sh", Timeout: 30 * time.Second,
				Policy: permFull, Workspace: t.workspace(), Label: "device_app", Model: true,
			})
			if err != nil {
				return "", err
			}
			if len(out) > 4000 {
				out = out[:4000]
			}
			return "启动请求已发出：\n" + out, nil
		},
	}
}

func adbConnectTool() Tool {
	return Tool{
		Spec: toolSpec("adb_connect",
			"以 ADB 主机身份连接 adbd（本机无线调试或局域网安卓设备）。本机用法：用户在 开发者选项→无线调试 里查看端口后填 host=127.0.0.1、port=该端口；首次连接会在设备上弹「允许调试」授权框（用户点允许即永久记住）。"+capNoteDevice,
			objSchema(withCapRequest(map[string]any{
				"host": strProp("adbd 主机（本机无线调试填 127.0.0.1）"),
				"port": map[string]any{"type": "integer", "description": "adbd 端口（无线调试页显示的端口）"},
			}), "host", "port"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireDevice(t, false, args); err != nil {
				return "", err
			}
			var p struct {
				Host string `json:"host"`
				Port int    `json:"port"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if p.Host == "" || p.Port <= 0 || p.Port > 65535 {
				return "", fmt.Errorf("host/port 不合法")
			}
			addr := fmt.Sprintf("%s:%d", p.Host, p.Port)
			info, err := adbConnect(filepath.Join(t.app.Cfg.DataDir, "adbkey"), addr)
			if err != nil {
				return "", fmt.Errorf("%w（首次连接需在设备上点「允许调试」后重试一次）", err)
			}
			return "已连接 " + addr + "：" + info, nil
		},
	}
}

func adbShellTool() Tool {
	return Tool{
		Spec: toolSpec("adb_shell",
			"在已连接的 adbd 上执行 shell 命令（shell uid 权限：screencap/input/pm/dumpsys 等全可用）。先用 adb_connect 建立连接。危险命令护栏照常拦截。"+capNoteDevice,
			objSchema(withCapRequest(map[string]any{
				"command":   strProp("要执行的命令"),
				"timeout_s": map[string]any{"type": "integer", "description": "超时秒数，默认 60，上限 600"},
			}), "command"),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireDevice(t, false, args); err != nil {
				return "", err
			}
			var p struct {
				Command   string `json:"command"`
				TimeoutS  int    `json:"timeout_s"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if strings.TrimSpace(p.Command) == "" {
				return "", fmt.Errorf("command 不能为空")
			}
			if why := destructiveCommandHit(p.Command); why != "" && t.app.guardEnabled() {
				return "", fmt.Errorf("危险命令已拦截：%s", why)
			}
			tout := 60 * time.Second
			if p.TimeoutS > 0 {
				tout = time.Duration(min(p.TimeoutS, 600)) * time.Second
			}
			out, err := adbShellText(p.Command, tout)
			if err != nil {
				return "", err
			}
			return out, nil
		},
	}
}

func adbScreencapTool() Tool {
	return Tool{
		Spec: toolSpec("adb_screencap",
			"经 ADB 通道截取设备屏幕（shell uid 的 screencap，比无障碍截屏更底层，解锁界面等系统界面也能截）。以图片信封返回；坐标语义与 device_screenshot 相同。"+capNoteDevice,
			objSchema(withCapRequest(map[string]any{})),
		),
		Run: func(t *toolContext, args json.RawMessage) (string, error) {
			if err := requireDevice(t, false, args); err != nil {
				return "", err
			}
			raw, err := adbExecRaw("screencap -p", 25*time.Second)
			if err != nil {
				return "", err
			}
			if len(raw) < 8 || raw[0] != 0x89 {
				return "", fmt.Errorf("screencap 返回的不是 PNG（连接可能已断开）")
			}
			att, err := t.app.storeAttachment(raw, sniffImageMediaType(raw))
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s\nADB 截屏完成（%d 字节）。点按坐标可直接用图上像素坐标。",
				imageEnvelope(att, "adb-screen.png"), len(raw)), nil
		},
	}
}
