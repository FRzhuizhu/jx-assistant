package main

// 文件查看页：会话工作区里的代码/文本类文件在右栏「文件」视图的自包含渲染。
//
// 为什么不走右栏原生浏览器面板（browser_panel_windows.go 的 file://）：
//   1. 原生面板是 overlay 子窗口，矩形靠前端上报同步——切页签/收起面板后再
//      回来偶发不上报，用户看到一片空白（实测复现过：地址栏有内容、舞台空）；
//   2. file:// 纯文本是「原样倾倒」，没有行号与代码样式，观感差；
//   3. 网页端/远程手机端没有原生面板，行为对不齐。
// 本查看页是普通 HTML（等宽字体 + 行号的代码样式），桌面壳、网页、远程三端
// 行为完全一致；图片/PDF 经 fileraw 交给浏览器自带能力展示；二进制给「用系统
// 应用打开」引导。边界与 open-view 同口径：会话工作区内已存在的文件，用户
// 主动点击进入，不经模型权限门控。

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

// fileviewTextExt：按代码样式展示的文本后缀（不在清单里的后缀会按内容探测，
// 文本也照常渲染，这里只列「确定值得列出的」）。
var fileviewTextExt = map[string]bool{
	"txt": true, "md": true, "markdown": true, "log": true, "csv": true, "tsv": true,
	"json": true, "xml": true, "yaml": true, "yml": true, "toml": true, "ini": true,
	"conf": true, "cfg": true, "properties": true, "env": true, "gitignore": true,
	"js": true, "mjs": true, "cjs": true, "ts": true, "jsx": true, "tsx": true,
	"css": true, "scss": true, "less": true, "go": true, "py": true, "rb": true,
	"rs": true, "java": true, "c": true, "h": true, "cpp": true, "hpp": true,
	"cs": true, "php": true, "swift": true, "kt": true, "sh": true, "bat": true,
	"cmd": true, "ps1": true, "sql": true, "lua": true, "r": true, "vue": true,
	"http": true, "diff": true, "patch": true,
}

// fileviewMIME：非文本但浏览器能直接展示的类型（fileraw 的 Content-Type 也用它）。
var fileviewMIME = map[string]string{
	"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "gif": "image/gif",
	"webp": "image/webp", "bmp": "image/bmp", "svg": "image/svg+xml", "ico": "image/x-icon",
	"pdf": "application/pdf",
}

const fileviewMaxText = 2 << 20 // 文本渲染上限 2MB：超出截断提示（防巨型文件拖死 DOM）

func fileviewExt(rel string) string {
	return strings.ToLower(strings.TrimPrefix(path.Ext(strings.ReplaceAll(rel, "\\", "/")), "."))
}

// looksBinary：扩展名不在清单里时按内容判断（前 8KB 含 NUL 或非法 UTF-8 即
// 视为二进制；Excel/Word 等全是这种）。
func looksBinary(data []byte) bool {
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	for _, b := range head {
		if b == 0 {
			return true
		}
	}
	return !utf8.Valid(head)
}

// fileviewArgs 校验并读取会话工作区文件；ok=false 时响应已写好。
func (a *App) fileviewArgs(w http.ResponseWriter, r *http.Request) (m *SessionMeta, rel string, data []byte, ok bool) {
	sid := strings.TrimSpace(r.URL.Query().Get("sid"))
	rel = strings.TrimSpace(r.URL.Query().Get("path"))
	if sid == "" || rel == "" {
		writeErr(w, http.StatusBadRequest, "需要 sid 与 path")
		return nil, "", nil, false
	}
	var abs string
	m, abs, ok = a.workspaceFileForView(sid, rel)
	if !ok {
		writeErr(w, http.StatusNotFound, "文件不存在或不在会话工作区内: "+rel)
		return nil, "", nil, false
	}
	tctx := &toolContext{app: a, sess: m}
	data, err := tctx.envReadFile(abs)
	if err != nil {
		http.NotFound(w, r)
		return nil, "", nil, false
	}
	return m, rel, data, true
}

func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", float64(n)/(1<<20)), "0"), ".") + " MB"
	case n >= 1<<10:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", float64(n)/(1<<10)), "0"), ".") + " KB"
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// handleFileView 渲染自包含查看页（样式脚本全内联，无外部依赖；文本用
// textContent 逐行落 DOM，不经 innerHTML，内容天然转义）。
func (a *App) handleFileView(w http.ResponseWriter, r *http.Request) {
	m, rel, data, ok := a.fileviewArgs(w, r)
	if !ok {
		return
	}
	ext := fileviewExt(rel)
	rawURL := "/api/fileraw?sid=" + url.QueryEscape(m.ID) + "&path=" + url.QueryEscape(rel)
	name := path.Base(strings.ReplaceAll(rel, "\\", "/"))
	local := m.Remote == nil // 远程会话没有本机路径，「系统应用打开/定位」不展示

	kind := "text"
	if mime, known := fileviewMIME[ext]; known {
		kind = "mime:" + mime
	} else if !fileviewTextExt[ext] && looksBinary(data) {
		kind = "binary"
	}

	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>` + html.EscapeString(name) + `</title><style>
:root{--bg:#0f172a;--fg:#dbe4f0;--dim:#64748b;--line:#1e293b}
*{box-sizing:border-box}html,body{height:100%;margin:0}
body{font:13px/1.65 ui-monospace,Consolas,"Cascadia Mono","Courier New",monospace;background:var(--bg);color:var(--fg);display:flex;flex-direction:column}
header{display:flex;align-items:center;gap:10px;padding:8px 12px;background:#111c33;border-bottom:1px solid var(--line);flex:none;flex-wrap:wrap}
.fname{font-weight:700;color:#fff;word-break:break-all}
.meta{color:var(--dim);font-size:12px}
button{font:12px system-ui,sans-serif;background:#1e293b;color:var(--fg);border:1px solid #334155;border-radius:6px;padding:4px 10px;cursor:pointer}
button:hover{background:#334155}
main{flex:1;min-height:0;display:flex;flex-direction:column}
pre.body{flex:1;margin:0;overflow:auto;padding:8px 0;tab-size:4}
.row{display:flex}.row:hover{background:#16213a}
.ln{flex:none;min-width:3.5em;padding:0 10px 0 14px;text-align:right;color:var(--dim);user-select:none}
.tx{white-space:pre;padding-right:16px}
.notice{margin:auto;padding:40px 24px;text-align:center;color:var(--dim);max-width:520px;line-height:2}
img.body{flex:1;min-height:0;width:100%;object-fit:contain;background:#fff}
iframe.body{flex:1;width:100%;border:none;background:#fff}
.trunc{padding:6px 14px;color:#e6c07b;background:#211d0e;border-top:1px solid var(--line);flex:none}
</style></head><body><header><span class="fname">` + html.EscapeString(name) + `</span><span class="meta">`)

	meta := humanSize(len(data))
	textLines := -1
	if kind == "text" {
		text := fileviewCut(data)
		textLines = strings.Count(text, "\n") + 1
		meta = itoa(textLines) + " 行 · " + meta
	}
	b.WriteString(meta + `</span>`)

	if local {
		b.WriteString(`<button onclick="jxSys('open-path')">用系统应用打开</button>` +
			`<button onclick="jxSys('reveal-path')">在文件夹中定位</button>`)
	}
	b.WriteString(`</header><main>`)

	switch {
	case kind == "binary":
		hint := "这是远程工作区里的文件，本机无法用系统程序打开。"
		if local {
			hint = "可以点上方「用系统应用打开」交给默认程序处理。"
		}
		b.WriteString(`<div class="notice">这个文件是二进制格式，不适合按文本预览。<br>` + html.EscapeString(hint) + `</div>`)
	case strings.HasPrefix(kind, "mime:image/"):
		b.WriteString(`<img class="body" src="` + rawURL + `" alt="` + html.EscapeString(name) + `">`)
	case kind == "mime:application/pdf":
		b.WriteString(`<iframe class="body" src="` + rawURL + `" title="` + html.EscapeString(name) + `"></iframe>`)
	default: // 文本：行号 + 等宽（textContent 落 DOM，内容不经 HTML 解析）
		text := fileviewCut(data)
		truncated := len(data) > fileviewMaxText
		b.WriteString(`<pre class="body" id="code"></pre>`)
		if truncated {
			b.WriteString(`<div class="trunc">文件超过 2MB，已截断显示前面部分；完整内容请用系统应用打开</div>`)
		}
		b.WriteString(`<script>var T=` + jsonString(text) + `,ls=T.split("\n"),c=document.getElementById('code'),f=document.createDocumentFragment();` +
			`for(var i=0;i<ls.length;i++){var r=document.createElement('div');r.className='row';` +
			`var n=document.createElement('span');n.className='ln';n.textContent=i+1;` +
			`var t=document.createElement('span');t.className='tx';t.textContent=ls[i];` +
			`r.appendChild(n);r.appendChild(t);f.appendChild(r);}c.appendChild(f);</script>`)
	}

	b.WriteString(`</main>`)
	if local {
		b.WriteString(`<script>function jxSys(ep){fetch('/api/sessions/` + m.ID + `/'+ep,` +
			`{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({path:` + jsonString(rel) + `})})` +
			`.then(function(r){if(!r.ok)return r.text().then(function(t){throw t||('HTTP '+r.status)});})` +
			`.catch(function(e){alert('操作失败：'+e)});}</script>`)
	}
	b.WriteString(`</body></html>`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(b.String()))
}

// fileviewCut 把文本裁到渲染上限（对齐 rune 边界）。
func fileviewCut(data []byte) string {
	if len(data) <= fileviewMaxText {
		return string(data)
	}
	cut := data[:fileviewMaxText]
	for len(cut) > 0 && !utf8.Valid(cut) {
		cut = cut[:len(cut)-1]
	}
	return string(cut)
}

// handleFileRaw 以真实 Content-Type 原样吐文件字节（图片/PDF 内嵌用）。
func (a *App) handleFileRaw(w http.ResponseWriter, r *http.Request) {
	m, rel, data, ok := a.fileviewArgs(w, r)
	if !ok {
		return
	}
	_ = m
	mime := "application/octet-stream"
	if v, known := fileviewMIME[fileviewExt(rel)]; known {
		mime = v
	} else if !looksBinary(data) {
		mime = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(path.Base(rel)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
