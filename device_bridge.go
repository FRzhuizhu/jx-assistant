package main

// 设备桥：引擎与 Kotlin 壳（无障碍服务）之间的命令队列。
//
// device_* 工具（截屏/点按/滑动/输入/界面树）依赖无障碍服务，而服务跑在
// 应用进程里——桥的方向是「壳轮询引擎」：DeviceBridge 线程长轮询
// GET /api/device/poll 拿命令，执行完 POST /api/device/result 回传。
// 三个端点只接受回环来源（命令面与结果面都不暴露到局域网/公网中继）。
//
// 轮询握手顺带上报无障碍服务连接状态（?svc=1），引擎据此在环境快照里
// 公告「无障碍已就绪/未开启（带开启指引）」——桥活着但服务没开时，模型
// 能引导用户去 设置→无障碍 开启，而不是对着报错瞎试。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// deviceResult 是壳侧执行结果：Data 对截图是 base64 PNG（W/H 为像素尺寸），
// 对 ui_dump 是文本树，其余命令为空。
type deviceResult struct {
	OK    bool   `json:"ok"`
	Err   string `json:"err,omitempty"`
	Data  string `json:"data,omitempty"`
	W     int    `json:"w,omitempty"`
	H     int    `json:"h,omitempty"`
}

type deviceCmd struct {
	ID   string         `json:"id"`
	Op   string         `json:"op"`
	Args map[string]any `json:"args,omitempty"`
}

var (
	devMu          sync.Mutex
	devQueue       = make(chan *deviceCmd, 32)
	devResults     = map[string]chan deviceResult{}
	devLastPoll    time.Time
	devSvcAlive    bool // 壳上报：无障碍服务已连接
)

// deviceBridgeState 描述桥与服务状态（环境快照/自检用）。
func deviceBridgeState() (bridgeAlive, svcAlive bool) {
	devMu.Lock()
	defer devMu.Unlock()
	return time.Since(devLastPoll) < 6*time.Second, devSvcAlive
}

// deviceSubmit 入队一条设备命令并等待壳侧执行结果。
func deviceSubmit(op string, args map[string]any, timeout time.Duration) (deviceResult, error) {
	c := &deviceCmd{ID: "dc-" + newID(), Op: op, Args: args}
	ch := make(chan deviceResult, 1)
	devMu.Lock()
	devResults[c.ID] = ch
	devMu.Unlock()
	select {
	case devQueue <- c:
	case <-time.After(2 * time.Second):
		devMu.Lock()
		delete(devResults, c.ID)
		devMu.Unlock()
		return deviceResult{}, fmt.Errorf("设备桥拥塞（命令队列已满），稍后重试")
	}
	select {
	case r := <-ch:
		return r, nil
	case <-time.After(timeout):
		devMu.Lock()
		delete(devResults, c.ID)
		devMu.Unlock()
		return deviceResult{}, fmt.Errorf("设备命令超时（%v）——无障碍服务可能未开启或已断开", timeout)
	}
}

// loopbackOnly 设备桥端点的来源闸：只认 127.0.0.1 / [::1]。
func loopbackOnly(w http.ResponseWriter, r *http.Request) bool {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		return true
	}
	http.Error(w, "仅限本机访问", http.StatusForbidden)
	return false
}

// handleDevicePoll 壳侧长轮询：?svc=0/1 上报无障碍服务状态；20 秒无命令回 204。
func (a *App) handleDevicePoll(w http.ResponseWriter, r *http.Request) {
	if !loopbackOnly(w, r) {
		return
	}
	devMu.Lock()
	devLastPoll = time.Now()
	devSvcAlive = r.URL.Query().Get("svc") == "1"
	devMu.Unlock()
	var c *deviceCmd
	select {
	case c = <-devQueue:
	case <-time.After(20 * time.Second):
		w.WriteHeader(http.StatusNoContent)
		return
	case <-r.Context().Done():
		return
	}
	writeJSON(w, 200, c)
}

// handleDeviceResult 壳侧回传执行结果。
func (a *App) handleDeviceResult(w http.ResponseWriter, r *http.Request) {
	if !loopbackOnly(w, r) {
		return
	}
	var p struct {
		ID string       `json:"id"`
		R  deviceResult `json:"result"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.ID == "" {
		http.Error(w, "参数不合法", http.StatusBadRequest)
		return
	}
	devMu.Lock()
	ch := devResults[p.ID]
	delete(devResults, p.ID)
	devMu.Unlock()
	if ch == nil {
		http.Error(w, "未知或已超时的命令 id", http.StatusNotFound)
		return
	}
	ch <- p.R
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleDeviceSelftest 一键自检（curl 诊断用，不经过模型）：截屏→界面树→
// ADB 自连（可关）→ 汇报各环节状态。仅回环。
func (a *App) handleDeviceSelftest(w http.ResponseWriter, r *http.Request) {
	if !loopbackOnly(w, r) {
		return
	}
	withADB := r.URL.Query().Get("adb") != "0"
	out := map[string]any{}

	if r.URL.Query().Get("screenshot") != "0" {
		res, err := deviceSubmit("screenshot", nil, 20*time.Second)
		out["screenshot"] = map[string]any{
			"ok": err == nil && res.OK, "w": res.W, "h": res.H,
			"err": firstNonEmpty(errText(err), res.Err),
		}
	}
	if r.URL.Query().Get("ui") != "0" {
		res, err := deviceSubmit("ui_dump", nil, 20*time.Second)
		out["ui_dump"] = map[string]any{
			"ok": err == nil && res.OK,
			"err": firstNonEmpty(errText(err), res.Err),
			"sample": firstRunes(res.Data, 400),
		}
	}
	if withADB {
		if adbStateText() == "未连接" {
			info, err := adbConnect(filepath.Join(a.Cfg.DataDir, "adbkey"), "127.0.0.1:5555")
			out["adb_connect"] = map[string]any{"ok": err == nil, "info": info, "err": errText(err)}
		} else {
			out["adb_connect"] = map[string]any{"ok": true, "info": adbStateText()}
		}
		if o, err := adbShellText("echo SELFTEST-OK; id", 10*time.Second); err == nil {
			out["adb_shell"] = map[string]any{"ok": strings.Contains(o, "SELFTEST-OK"), "out": firstRunes(o, 300)}
		} else {
			out["adb_shell"] = map[string]any{"ok": false, "err": errText(err)}
		}
		// 二进制通道：screencap PNG 经 shell v2 raw 直传（无 PTY 改写）
		if png, err := adbExecRaw("screencap -p", 20*time.Second); err == nil {
			out["adb_screencap"] = map[string]any{"ok": len(png) > 8 && png[0] == 0x89, "bytes": len(png), "err": ""}
		} else {
			out["adb_screencap"] = map[string]any{"ok": false, "bytes": 0, "err": errText(err)}
		}
	}
	// 桥状态在命令跑完之后读：自检首问时轮询可能尚未续期，先读会误报 false
	bridgeAlive, svcAlive := deviceBridgeState()
	out["bridge"] = bridgeAlive
	out["accessibility"] = svcAlive
	writeJSON(w, 200, out)
}

// firstRunes 取字符串前 n 个字符（自检样例截断用）。
func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// errText nil 归一为空串（自检 JSON 字段用）。
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
