package main

// 已登记的 SSH 远程工作区（工作目录选择面板里的远程条目）。
// 「使用」过一次的远程目录自动登记，新对话在面板里一键复挂：
//   - 落盘 data/remote_workspaces.json，密码与会话库同一套 DPAPI 加密
//     （protectSecret / unprotectSecret，见 dpapi.go）；
//   - GET /api/remotes 只回配置不回密码；
//   - 复挂条目（前端不带密码）经 fillRemoteCreds 按 主机+端口+用户 从
//     登记库回填，明文密码不出服务端。
// 登记、回填、清点都只在本机文件上操作，不联网。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const registeredRemotesCap = 12 // 上限：最近使用的排前面，超出裁掉最旧的

func (a *App) registeredRemotesPath() string {
	return filepathAbsOf(filepath.Join(a.Cfg.DataDir, "remote_workspaces.json"))
}

// loadRegisteredRemotes 读登记库（密码透明解密为明文，仅在服务端内存中）。
func (a *App) loadRegisteredRemotes() []*RemoteWorkspace {
	data, err := os.ReadFile(a.registeredRemotesPath())
	if err != nil {
		return nil
	}
	var list []*RemoteWorkspace
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	out := make([]*RemoteWorkspace, 0, len(list))
	for _, r := range list {
		if r == nil || r.Host == "" {
			continue
		}
		r.Password = unprotectSecret(r.Password)
		out = append(out, r)
	}
	return out
}

func (a *App) saveRegisteredRemotes(list []*RemoteWorkspace) error {
	out := make([]*RemoteWorkspace, 0, len(list))
	for _, r := range list {
		c := *r
		c.Password, _ = protectSecret(c.Password)
		out = append(out, &c)
	}
	data, _ := json.MarshalIndent(out, "", " ")
	return writeFileAtomic(a.registeredRemotesPath(), data)
}

// registerRemote 登记一个使用过的远程工作区（按 user@host[:port]:dir 去重，
// 最近使用的排最前）。spec 须已 normalize（含密码明文）。
func (a *App) registerRemote(spec *RemoteWorkspace) {
	list := a.loadRegisteredRemotes()
	kept := make([]*RemoteWorkspace, 0, len(list)+1)
	kept = append(kept, spec)
	for _, r := range list {
		if r.Display() != spec.Display() {
			kept = append(kept, r)
		}
	}
	if len(kept) > registeredRemotesCap {
		kept = kept[:registeredRemotesCap]
	}
	_ = a.saveRegisteredRemotes(kept)
}

// fillRemoteCreds 密码缺席时按 主机+端口+用户 从登记库回填（面板复挂路径：
// 前端拿不到也不该拿到密码）。认证方式不同或登记库里也没有就原样返回。
func (a *App) fillRemoteCreds(spec *RemoteWorkspace) {
	if spec == nil || spec.Auth != "password" || spec.Password != "" {
		return
	}
	for _, r := range a.loadRegisteredRemotes() {
		if r.Auth == "password" && r.Password != "" &&
			r.Host == spec.Host && r.Port == spec.Port && r.User == spec.User {
			spec.Password = r.Password
			return
		}
	}
}

// resolveDispatchRemote 按登记 key（= Display()，如 user@host:dir）取远程
// 工作区的**副本**（登记时已 normalize，密码已透明解密在服务端内存）。
// dispatch_sub / dispatch_task 的 remote_workspace 参数由此解析——派发方
// 只需要照抄环境快照「可派发远程工作区」行里的完整地址，凭据不出服务端。
func (a *App) resolveDispatchRemote(key string) (*RemoteWorkspace, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	for _, r := range a.loadRegisteredRemotes() {
		if r.Display() == key {
			c := *r
			return &c, nil
		}
	}
	return nil, fmt.Errorf("远程工作区 %q 不在已登记清单里（remote_workspace 请照抄环境快照「可派发远程工作区」行里的完整地址；新远程先在对话里挂载使用一次即完成登记）", key)
}

// dispatchRemoteLine 环境快照的「可派发远程工作区」公告行（登记库为空返回
// 空串）。给模型看见可选的二号工作区入口；不依赖当前会话是否挂远程。
func (a *App) dispatchRemoteLine() string {
	var keys []string
	for _, r := range a.loadRegisteredRemotes() {
		keys = append(keys, r.Display())
	}
	if len(keys) == 0 {
		return ""
	}
	return "- 可派发远程工作区（dispatch_sub / dispatch_task 带 remote_workspace=下列完整地址之一，子对话在该远程目录工作：文件走 SFTP、命令经 SSH；与 workspace 参数互斥；缺省沿用当前环境）：" + strings.Join(keys, "、")
}

// handleListRemotes GET /api/remotes：登记清单（不含密码）。key 即 display，
// 供删除接口定位。
func (a *App) handleListRemotes(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, rm := range a.loadRegisteredRemotes() {
		out = append(out, map[string]any{
			"host": rm.Host, "port": rm.Port, "user": rm.User, "auth": rm.Auth,
			"keyPath": rm.KeyPath, "dir": rm.Dir,
			"display": rm.Display(), "key": rm.Display(),
		})
	}
	writeJSON(w, 200, map[string]any{"remotes": out})
}

// handleRegisterRemote POST /api/remotes {remote}：登记/刷新一条。密码缺席时
// 先从登记库回填（面板复挂后的静默续登场景）。
func (a *App) handleRegisterRemote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Remote *RemoteWorkspace `json:"remote"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Remote == nil {
		writeErr(w, 400, "需要 remote 字段")
		return
	}
	a.fillRemoteCreds(body.Remote)
	spec, err := body.Remote.normalize()
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	a.registerRemote(spec)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleDeleteRemote POST /api/remotes/delete {key}：忘记一条登记。
func (a *App) handleDeleteRemote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
		writeErr(w, 400, "需要 key 字段")
		return
	}
	list := a.loadRegisteredRemotes()
	kept := list[:0]
	for _, x := range list {
		if x.Display() != body.Key {
			kept = append(kept, x)
		}
	}
	if err := a.saveRegisteredRemotes(kept); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
