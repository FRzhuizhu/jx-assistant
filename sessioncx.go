package main

// 会话级组件/插件开关覆盖：每个对话可以有自己的生效集，优先于全局开关；
// 全局开关本身不动（设置页的开关仍归全部对话）。设计动机——语音、Git 面板
// 这类组件不是每个对话都用得上，用户希望按对话开/关而不影响别的对话。
//
// 存储走会话附属数据（meta 表 sessdata:cx-overrides:<会话id>，随会话生命周期）：
// 只记显式覆盖（会话里没有记录的键跟随全局）；把某键拨回与全局一致的值时，
// 覆盖记录即删除——文件不膨胀，"恢复跟随全局"也不需要额外的清理入口。
// 前端（web/cx.js + web/app-cxsession.js）按当前会话的生效集热装配组件，
// 与设置页的全局热开关共用同一条 CX.toggle 注入/卸载路径。

import (
	"encoding/json"
	"sync"
)

// sessionOverridesKey 是会话附属数据里的存储键（Store.SetSessionData 的 key）。
const sessionOverridesKey = "cx-overrides"

// SessionOverrides 是一个会话的显式覆盖集（未记录的键＝跟随全局）。
type SessionOverrides struct {
	Components map[string]bool `json:"components,omitempty"` // 组件id → 生效值
	Plugins    map[string]bool `json:"plugins,omitempty"`    // "层/插件id" → 生效值
}

// sessionCXRegistry 是全部会话覆盖的内存快照（写时同步落库）。
// 启动时从 Store 载入；会话删除经 forget 摘除。并发安全。
type sessionCXRegistry struct {
	mu  sync.RWMutex
	per map[string]*SessionOverrides
}

// loadSessionCX 启动时载入全部会话的覆盖（遍历会话表读附属数据）。
func loadSessionCX(store *Store) *sessionCXRegistry {
	r := &sessionCXRegistry{per: map[string]*SessionOverrides{}}
	if store == nil {
		return r
	}
	sessions, err := store.List()
	if err != nil {
		return r
	}
	for _, s := range sessions {
		if o := parseSessionOverrides(store.GetSessionData(s.ID, sessionOverridesKey)); o != nil {
			r.per[s.ID] = o
		}
	}
	return r
}

func parseSessionOverrides(raw string) *SessionOverrides {
	if raw == "" {
		return nil
	}
	var o SessionOverrides
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		return nil
	}
	if len(o.Components) == 0 && len(o.Plugins) == 0 {
		return nil
	}
	return &o
}

// of 返回某会话的覆盖集（没有＝nil；返回值只读，改动走 set）。
func (r *sessionCXRegistry) of(sid string) *SessionOverrides {
	if r == nil || sid == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.per[sid]
}

// set 在锁内改一份副本并落库；改完两个映射都空了就整体摘除该会话。
func (r *sessionCXRegistry) set(store *Store, sid string, mutate func(o *SessionOverrides)) {
	if r == nil || sid == "" {
		return
	}
	r.mu.Lock()
	cur := r.per[sid]
	if cur == nil {
		cur = &SessionOverrides{}
	} else { // 副本：读侧拿到的旧引用不受影响
		cc, cp := map[string]bool{}, map[string]bool{}
		for k, v := range cur.Components {
			cc[k] = v
		}
		for k, v := range cur.Plugins {
			cp[k] = v
		}
		cur = &SessionOverrides{Components: cc, Plugins: cp}
	}
	mutate(cur)
	empty := len(cur.Components) == 0 && len(cur.Plugins) == 0
	if empty {
		delete(r.per, sid)
	} else {
		r.per[sid] = cur
	}
	r.mu.Unlock()
	if store != nil {
		if empty {
			_ = store.SetSessionData(sid, sessionOverridesKey, "")
		} else if b, err := json.Marshal(cur); err == nil {
			_ = store.SetSessionData(sid, sessionOverridesKey, string(b))
		}
	}
}

// custom 报告某会话是否有任何覆盖记录（前端加号菜单的「已定制」标记）。
func (r *sessionCXRegistry) custom(sid string) bool {
	o := r.of(sid)
	return o != nil && (len(o.Components) > 0 || len(o.Plugins) > 0)
}

// forget 会话删除时摘掉它的覆盖记录（库里的附属数据行由 Store 一并清）。
func (r *sessionCXRegistry) forget(sid string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.per, sid)
	r.mu.Unlock()
}

// anyComponentEnabled 报告是否「还有任何会话」显式启用某组件
// （worker 进程与语音端点的存活判定用它：全局关了但还有会话要它，就留）。
func (r *sessionCXRegistry) anyComponentEnabled(id string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, o := range r.per {
		if o.Components[id] {
			return true
		}
	}
	return false
}

// ---------- App 侧助手（nil 安全：注册表没初始化时一切回落全局） ----------

// cxoComponentEnabled 某会话里某组件是否生效：显式覆盖优先，否则全局开关。
func (a *App) cxoComponentEnabled(sid, id string) bool {
	if a.Components == nil {
		return false
	}
	if o := a.SessionCX.of(sid); o != nil {
		if v, ok := o.Components[id]; ok {
			return v
		}
	}
	return a.Components.IsEnabled(id)
}

// cxoPluginEnabled 某会话里某插件是否生效（键＝层id/插件id）。
func (a *App) cxoPluginEnabled(sid, layer, id string) bool {
	if o := a.SessionCX.of(sid); o != nil {
		if v, ok := o.Plugins[pluginOverrideKey(layer, id)]; ok {
			return v
		}
	}
	return a.pluginGlobalEnabled(layer, id)
}

// pluginOverrideKey 插件覆盖键：层前缀防跨层重名。
func pluginOverrideKey(layer, id string) string { return layer + "/" + id }

// pluginGlobalEnabled 插件的全局开关值（不认会话；找不到按未启用）。
func (a *App) pluginGlobalEnabled(layer, id string) bool {
	_, ok := a.pluginGlobalLookup(layer, id)
	return ok
}

// pluginGlobalKnown 插件是否存在于某层（会话覆盖写入前的存在性校验）。
func (a *App) pluginGlobalKnown(layer, id string) bool {
	_, ok := a.pluginGlobalLookup(layer, id)
	return ok
}

// pluginGlobalLookup 按 Layers.SetEnabled 同款规则定位插件：layer 为空时在
// 全部层里按 id 匹配（跨层重名取首个——写入侧的一致性靠层 id 前缀键保证）。
func (a *App) pluginGlobalLookup(layer, id string) (LayerPlugin, bool) {
	if a.Layers == nil {
		return LayerPlugin{}, false
	}
	for _, l := range a.Layers.Layers() {
		if layer != "" && l.Info().ID != layer {
			continue
		}
		for _, p := range l.Plugins() {
			if p.ID == id {
				return p, true
			}
		}
	}
	return LayerPlugin{}, false
}

// cxoSetComponent 写一条组件覆盖（与全局一致时删除记录——跟随全局是缺省态）。
func (a *App) cxoSetComponent(sid, id string, on bool) {
	g := a.Components != nil && a.Components.IsEnabled(id)
	a.SessionCX.set(a.Store, sid, func(o *SessionOverrides) {
		if on == g {
			delete(o.Components, id)
		} else {
			if o.Components == nil {
				o.Components = map[string]bool{}
			}
			o.Components[id] = on
		}
	})
}

// cxoSetPlugin 写一条插件覆盖（同款归一）。
func (a *App) cxoSetPlugin(sid, layer, id string, on bool) {
	g := a.pluginGlobalEnabled(layer, id)
	a.SessionCX.set(a.Store, sid, func(o *SessionOverrides) {
		if on == g {
			delete(o.Plugins, pluginOverrideKey(layer, id))
		} else {
			if o.Plugins == nil {
				o.Plugins = map[string]bool{}
			}
			o.Plugins[pluginOverrideKey(layer, id)] = on
		}
	})
}

// cxoComponentLive 组件「活着」的判定：全局开着，或还有任何会话显式要它。
// 组件桥端点与语音端点用它——这些请求不带会话（组件脚本只知道自己的端点），
// 按最宽松口径放行：会话关掉的对话里组件 UI 已被热卸载，根本不会发请求来。
func (a *App) cxoComponentLive(id string) bool {
	if a.Components != nil && a.Components.IsEnabled(id) {
		return true
	}
	return a.SessionCX.anyComponentEnabled(id)
}

// cxoComponentList 全局清单叠加某会话覆盖后的生效视图（设置浮窗/前端装配用）。
func (a *App) cxoComponentList(sid string) []ComponentStatus {
	list := a.Components.List()
	o := a.SessionCX.of(sid)
	for i := range list {
		g := list[i].Enabled
		list[i].GlobalEnabled = g
		if o != nil {
			if v, ok := o.Components[list[i].ID]; ok {
				list[i].Enabled = v
				list[i].Override = true
			}
		}
	}
	return list
}

// cxoPluginList 插件清单的会话生效视图（同 cxoComponentList）。
func (a *App) cxoPluginList(sid string) []LayerPlugin {
	list := a.Layers.AllPlugins()
	o := a.SessionCX.of(sid)
	for i := range list {
		g := list[i].Enabled
		list[i].GlobalEnabled = g
		if o != nil {
			if v, ok := o.Plugins[pluginOverrideKey(list[i].Layer, list[i].ID)]; ok {
				list[i].Enabled = v
				list[i].Override = true
			}
		}
	}
	return list
}
