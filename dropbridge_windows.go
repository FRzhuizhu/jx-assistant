package main

// 桌面壳拖拽「原件路径」桥：WebView2（Chromium）不给页面文件绝对路径——实测
// （本机 WebView2 运行时 155.0.4283.45，见 gui-test 同款手法）页面侧拖入文件
// 后 `file.path` 不存在、`text/uri-list` 与 `text/plain` 皆空、
// `webkitGetAsEntry().fullPath` 只有文件名。所以从桌面拖进输入框的文件过去
// 只能走 multipart 上传：内容进了工作区，原件路径没人登记，交付工具自然送不
// 回原处（「＋→文件上传」那条路能用原生选择框拿到真实路径，就是同一个限制的
// 另一面）。
//
// 官方给宿主留了一条专用通道：页面把 File 对象经
// chrome.webview.postMessageWithAdditionalObjects(msg, files) 发过来，宿主在
// WebMessageReceived 的 AdditionalObjects 里拿到 CoreWebView2File.Path（真实
// 本机路径）。SDK 对该接口的原文：「You can use this object to obtain the path
// of a File dropped on WebView2」。本文件把这条通道接上：拖拽观感、悬停高亮、
// 页内 HTML5 拖拽（排队坞排序等）统统不动，只是路径这一件事由宿主取回来再喂
// 给页面（JX.onNativeDrop），页面据此把附件从「multipart」升级成「路径型」，
// 发送时经既有的 JSON {path} 通路登记原件路径。
//
// 接口 IID 与 vtable 槽位取自 WebView2 SDK（NuGet Microsoft.Web.WebView2 的
// WebView2.idl，字段顺序 = COM ABI，不可改动）：
//
//	ICoreWebView2WebMessageReceivedEventArgs  {0f99a40c-e962-4207-9e92-e3d542eff849}
//	    slot3=get_Source 4=get_WebMessageAsJson 5=TryGetWebMessageAsString
//	ICoreWebView2WebMessageReceivedEventArgs2 {06fc7ab7-c90c-4297-9389-33ca01cf6d5e}
//	    slot6=get_AdditionalObjects（[out,retval] → 调用方 Release）
//	ICoreWebView2ObjectCollectionView         {0f36fd87-4f69-4415-98da-888f89fb9a33}
//	    slot3=get_Count 4=GetValueAtIndex（[out,retval] → 调用方 Release）
//	ICoreWebView2File                         {f2c19559-6bc1-4583-a757-90021be9afec}
//	    slot3=get_Path（LPWSTR → 调用方 CoTaskMemFree）
//
// 绑定库（jchv/go-webview2）只包到 v1 接口，这里按工程既有手法自镜像 vtbl +
// QueryInterface（同 browser_panel_windows.go 的 wvRawVtbl / evtHandler）。

import (
	"encoding/json"
	"log"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// jxDropMsgKind 页面→宿主的消息标识（见 web/app-chat.js 的 requestNativeDropPaths）
const jxDropMsgKind = "jx-drop-paths"

// dropBridgeMsg 页面发来的消息（WebMessageAsJson 解析；Names 只为排障对账）
type dropBridgeMsg struct {
	Kind  string   `json:"kind"`
	Batch int      `json:"batch"`
	Names []string `json:"names"`
}

// dropBridgeReply 交回页面的应答（页面按 batch 找到那一次的附件并升级成路径型）
type dropBridgeReply struct {
	Batch int      `json:"batch"`
	Names []string `json:"names"`
	Paths []string `json:"paths"`
}

// ---------- COM vtbl 镜像（字段顺序 = Microsoft COM ABI） ----------

type dropObjVtbl struct{ queryInterface, addRef, release edge.ComProc }

type dropMsgArgsVtbl struct {
	dropObjVtbl
	getSource, getWebMessageAsJson, tryGetWebMessageAsString edge.ComProc
}

type dropMsgArgs2Vtbl struct {
	dropMsgArgsVtbl
	getAdditionalObjects edge.ComProc
}

type dropCollVtbl struct {
	dropObjVtbl
	getCount, getValueAtIndex edge.ComProc
}

type dropFileVtbl struct {
	dropObjVtbl
	getPath edge.ComProc
}

func dropVtblAt(p unsafe.Pointer) unsafe.Pointer { return *(*unsafe.Pointer)(p) }

// dropWStr 复制并释放调用 get_* 方法取回的出参字符串（CoTaskMem 分配）——
// 与 browser_panel_windows.go 的 readWVString 同款约定（出参用 *uint16 接，
// 不用 uintptr 转 unsafe.Pointer，go vet 干净）。
func dropWStr(p *uint16) string {
	if p == nil {
		return ""
	}
	s := windows.UTF16PtrToString(p)
	windows.CoTaskMemFree(unsafe.Pointer(p))
	return s
}

var (
	// COM 侧只存裸指针，Go 侧必须持引用保活（同 frontendPfHandler 的约定）
	dropBridgeHandler *evtHandler
	dropBridgeToken   int64
	// dropBridgeDeliver 往页面投递 JS（由 runDesktop 注入：经 Dispatch 回 UI
	// 线程再 Eval——ExecuteScript 是 COM 调用，工程约定只在 UI 线程执行）。
	dropBridgeDeliver func(js string)
)

// attachDropBridge 给主 WebView2 挂「附加对象」消息处理器（每轮前端创建后调用）。
func attachDropBridge(wv *edge.ICoreWebView2, deliver func(string)) {
	if wv == nil {
		log.Printf("[拖拽桥] 未取到 ICoreWebView2，原件路径采集不可用（拖拽仍按 multipart 上传）")
		return
	}
	dropBridgeDeliver = deliver
	dropBridgeHandler = newEvtHandler(onDropBridgeMessage)
	_, _, _ = wvVtblOf(wv).addWebMessageReceived.Call(
		uintptr(unsafe.Pointer(wv)), uintptr(unsafe.Pointer(dropBridgeHandler)), uintptr(unsafe.Pointer(&dropBridgeToken)))
	log.Printf("[拖拽桥] 已挂 WebMessageReceived 处理器（token=%d）", dropBridgeToken)
}

// detachDropBridge 反注册（前端收尾时调用，与 attach 配对；旧 handler 不能留给
// 已销毁的环境）。
func detachDropBridge(wv *edge.ICoreWebView2) {
	if wv == nil || dropBridgeHandler == nil {
		return
	}
	_, _, _ = wvVtblOf(wv).removeWebMessageReceived.Call(
		uintptr(unsafe.Pointer(wv)), uintptr(unsafe.Pointer(dropBridgeHandler)), uintptr(unsafe.Pointer(&dropBridgeToken)))
	dropBridgeHandler = nil
	dropBridgeDeliver = nil
}

// onDropBridgeMessage 收页面消息：只认 jx-drop-paths，取出附加 File 对象的真实
// 路径交回页面。回调跑在 WebView2 的原生事件栈上——panic 由 evtHandler 兜住并落
// 日志（从这里冒出去就是整进程无声死亡）。
func onDropBridgeMessage(sender, args unsafe.Pointer) {
	if args == nil {
		return
	}
	v := (*dropMsgArgsVtbl)(dropVtblAt(args))
	var pJSON *uint16
	_, _, _ = v.getWebMessageAsJson.Call(uintptr(args), uintptr(unsafe.Pointer(&pJSON)))
	raw := dropWStr(pJSON)
	var msg dropBridgeMsg
	if err := json.Unmarshal([]byte(raw), &msg); err != nil || msg.Kind != jxDropMsgKind {
		return // 不是给拖拽桥的消息（其它 postMessage 一律不管）
	}
	paths, supported := dropAdditionalFilePaths(args)
	if !supported {
		log.Printf("[拖拽桥] 本机 WebView2 运行时没有 AdditionalObjects（版本过旧），本次拖拽退回 multipart 上传")
		return
	}
	log.Printf("[拖拽桥] 收到 %d 个拖入文件的附加对象，取到路径 %d 条：%v", len(msg.Names), len(paths), paths)
	payload, err := json.Marshal(dropBridgeReply{Batch: msg.Batch, Names: msg.Names, Paths: paths})
	if err != nil {
		return
	}
	if dropBridgeDeliver != nil {
		dropBridgeDeliver("window.JX&&JX.onNativeDrop&&JX.onNativeDrop(" + string(payload) + ")")
	}
}

// dropAdditionalFilePaths 从事件参数里取出附加 File 对象的绝对路径（顺序与页面
// 传给 postMessageWithAdditionalObjects 的数组一致，取不到的位置留空串）。
// supported=false 表示运行时没有 ICoreWebView2WebMessageReceivedEventArgs2。
func dropAdditionalFilePaths(args unsafe.Pointer) (paths []string, supported bool) {
	v := (*dropMsgArgsVtbl)(dropVtblAt(args))
	iidArgs2 := edge.NewGUID("{06fc7ab7-c90c-4297-9389-33ca01cf6d5e}")
	var a2 unsafe.Pointer
	_, _, _ = v.queryInterface.Call(uintptr(args), uintptr(unsafe.Pointer(iidArgs2)), uintptr(unsafe.Pointer(&a2)))
	if a2 == nil {
		return nil, false
	}
	defer func() { _, _, _ = (*dropObjVtbl)(dropVtblAt(a2)).release.Call(uintptr(a2)) }()

	var coll unsafe.Pointer
	_, _, _ = (*dropMsgArgs2Vtbl)(dropVtblAt(a2)).getAdditionalObjects.Call(
		uintptr(a2), uintptr(unsafe.Pointer(&coll)))
	if coll == nil {
		return []string{}, true // 支持但没有附加对象（页面没带 File？）
	}
	defer func() { _, _, _ = (*dropObjVtbl)(dropVtblAt(coll)).release.Call(uintptr(coll)) }()

	var n uint32
	_, _, _ = (*dropCollVtbl)(dropVtblAt(coll)).getCount.Call(uintptr(coll), uintptr(unsafe.Pointer(&n)))
	iidFile := edge.NewGUID("{f2c19559-6bc1-4583-a757-90021be9afec}")
	out := make([]string, 0, n)
	for i := uint32(0); i < n; i++ {
		var obj unsafe.Pointer
		_, _, _ = (*dropCollVtbl)(dropVtblAt(coll)).getValueAtIndex.Call(
			uintptr(coll), uintptr(i), uintptr(unsafe.Pointer(&obj)))
		if obj == nil { // 集合里可以有 nullptr（页面传了 null）
			out = append(out, "")
			continue
		}
		objV := (*dropObjVtbl)(dropVtblAt(obj))
		var f unsafe.Pointer
		_, _, _ = objV.queryInterface.Call(uintptr(obj), uintptr(unsafe.Pointer(iidFile)), uintptr(unsafe.Pointer(&f)))
		_, _, _ = objV.release.Call(uintptr(obj)) // GetValueAtIndex 交出的引用
		if f == nil {                             // 不是 File 对象（当前只有 File，留空让页面走 multipart）
			out = append(out, "")
			continue
		}
		var p *uint16
		_, _, _ = (*dropFileVtbl)(dropVtblAt(f)).getPath.Call(uintptr(f), uintptr(unsafe.Pointer(&p)))
		out = append(out, dropWStr(p))
		_, _, _ = (*dropObjVtbl)(dropVtblAt(f)).release.Call(uintptr(f))
	}
	return out, true
}
