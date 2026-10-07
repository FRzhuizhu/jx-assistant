package main

// mods JS 运行时（goja）的语法冒烟：验证 Claude Code mods 的典型代码形态
// （ESM 转 CJS 后的 async/await、模板串、展开、解构、Go 宿主函数互调、
// Promise 驱动）在 goja 上可跑。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

func newModVM() *goja.Runtime {
	vm := goja.New()
	// 宿主同步函数：返回普通值（await 非 Promise 值由 microtask 推进）
	vm.Set("__hostCount", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(41)
	})
	return vm
}

// settlePromise 驱动 microtask 队列直到 Promise settle 或超时。
func settlePromise(t *testing.T, vm *goja.Runtime, v goja.Value, timeout time.Duration) goja.Value {
	t.Helper()
	if p, ok := v.Export().(*goja.Promise); ok {
		deadline := time.Now().Add(timeout)
		for p.State() == goja.PromiseStatePending && time.Now().Before(deadline) {
			// 一次空跑触发 goja 的 microtask flush（Run* 调用结束时自动执行）
			_, _ = vm.RunString("0")
			time.Sleep(2 * time.Millisecond)
		}
		if p.State() == goja.PromiseStateRejected {
			t.Fatalf("Promise rejected: %v", p.Result())
		}
		if p.State() == goja.PromiseStatePending {
			t.Fatal("Promise 未在超时内 settle")
		}
		return p.Result()
	}
	return v
}

func TestGojaModSyntaxSmoke(t *testing.T) {
	vm := newModVM()
	// Claude Code 官方 first-mod 示例的语法形态（ESM→CJS 转换后的 CJS 形态）：
	// async handler、模板串、await Go 函数、展开/解构、let 计数
	src := `
let calls = 0
function register(on) {
  on('tool.call', async ($, e, next) => {
    calls += 1
    const n = await __hostCount()
    return next({ ...e, props: { ...e.props, suffix: ' · tool calls: ' + calls + ' of ' + n + '…' } })
  })
  on('command.run', { command: 'tally' }, async () => {
    return { text: ` + "`" + `Claude has made ${calls} tool calls` + "`" + ` }
  })
}
module.exports = { register }
`
	wrapped := "(function(module, exports){\n" + src + "\n})"
	mod, err := vm.RunString(wrapped)
	if err != nil {
		t.Fatalf("包装函数解析失败: %v", err)
	}
	moduleObj := vm.NewObject()
	exportsObj := vm.NewObject()
	_ = moduleObj.Set("exports", exportsObj)
	fn, ok := goja.AssertFunction(mod)
	if !ok {
		t.Fatal("包装不是函数")
	}
	if _, err := fn(goja.Undefined(), moduleObj, exportsObj); err != nil {
		t.Fatalf("模块执行失败: %v", err)
	}
	// 模块里 module.exports = { register } 整体替换了 exports：从 module 上重取
	regVal := moduleObj.Get("exports")
	regFn, ok := goja.AssertFunction(regVal.ToObject(vm).Get("register"))
	if !ok {
		t.Fatal("module.exports.register 不是函数")
	}
	// on 收集钩子后调 tool.call（async）验证整链
	type hookDef struct {
		matcher goja.Value
		fn      goja.Callable
	}
	hooks := map[string][]hookDef{}
	onNative := func(call goja.FunctionCall) goja.Value {
		args := call.Arguments
		event := args[0].String()
		var matcher goja.Value
		handler := args[len(args)-1]
		if len(args) >= 3 {
			matcher = args[1]
		}
		if h, ok := goja.AssertFunction(handler); ok {
			hooks[event] = append(hooks[event], hookDef{matcher: matcher, fn: h})
		}
		return goja.Undefined()
	}
	onFn := vm.ToValue(onNative)
	if _, err := regFn(goja.Undefined(), onFn); err != nil {
		t.Fatalf("register 调用失败: %v", err)
	}
	if len(hooks["tool.call"]) != 1 || len(hooks["command.run"]) != 1 {
		t.Fatalf("钩子注册 = %#v", hooks)
	}
	// 调 tool.call 钩子：next 捕获改写后的事件
	var suffix string
	nextFn := vm.ToValue(func(call goja.FunctionCall) goja.Value {
		e := call.Argument(0).ToObject(vm)
		p := e.Get("props")
		if p != nil && !goja.IsUndefined(p) {
			suffix = p.ToObject(vm).Get("suffix").String()
		}
		return vm.ToValue("passed")
	})
	res, cerr := hooks["tool.call"][0].fn(goja.Undefined(), vm.ToValue("$"), vm.ToValue(map[string]any{"tool": "Bash"}), nextFn)
	if cerr != nil {
		t.Fatalf("tool.call handler 调用失败: %v", cerr)
	}
	if settled := settlePromise(t, vm, res, 3*time.Second); settled.String() != "passed" {
		t.Fatalf("tool.call 结果 = %v", settled)
	}
	if suffix != " · tool calls: 1 of 41…" {
		t.Fatalf("suffix = %q", suffix)
	}
	// command.run 短路返回（不调 next）
	res2, cerr2 := hooks["command.run"][0].fn(goja.Undefined(), vm.ToValue("$"), vm.ToValue(map[string]any{"command": "tally"}), vm.ToValue(func(call goja.FunctionCall) goja.Value { return goja.Undefined() }))
	if cerr2 != nil {
		t.Fatalf("command handler 调用失败: %v", cerr2)
	}
	settled2 := settlePromise(t, vm, res2, 3*time.Second)
	obj := settled2.ToObject(vm)
	if obj.Get("text").String() != "Claude has made 1 tool calls" {
		t.Fatalf("command.run 结果 = %v", settled2)
	}
}

func TestGojaAsyncHandlerChain(t *testing.T) {
	vm := newModVM()
	// 直白验证：async handler + next 链 + await Go 函数 + 模板串/展开
	var gotSuffix string
	vm.Set("__next", func(call goja.FunctionCall) goja.Value {
		e := call.Argument(0).ToObject(vm)
		props := e.Get("props")
		if props != nil && !goja.IsUndefined(props) {
			gotSuffix = props.ToObject(vm).Get("suffix").String()
		}
		return vm.ToValue("done")
	})
	handler, err := vm.RunString(`
(async ($, e, next) => {
  const n = await __hostCount()
  return next({ ...e, props: { ...e.props, suffix: ' · tool calls: ' + (n + 1) + '…' } })
})
`)
	if err != nil {
		t.Fatal(err)
	}
	callable, ok := goja.AssertFunction(handler)
	if !ok {
		t.Fatal("handler 不是函数")
	}
	res, cerr := callable(goja.Undefined(), vm.ToValue("$"), vm.ToValue(map[string]any{"tool": "Bash"}), vm.Get("__next"))
	if cerr != nil {
		t.Fatalf("handler 调用失败: %v", cerr)
	}
	settled := settlePromise(t, vm, res, 3*time.Second)
	if settled.String() != "done" {
		t.Fatalf("handler 结果 = %v", settled)
	}
	if gotSuffix != " · tool calls: 42…" {
		t.Fatalf("suffix = %q", gotSuffix)
	}
}

func TestESMToCJSConversion(t *testing.T) {
	src := `import { read } from 'claude-code'
import helper from './util.js'
import * as lib from './lib.js'

export let calls = 0
export function register(on) {
  calls++
  return read() + helper() + lib.ok
}
export const tag = 'x'
export default register
`
	want := "const { read } = require('claude-code');\n" +
		"const helper = require('./util.js');\n" +
		"const lib = require('./lib.js');\n" +
		"\n" +
		"let calls = 0\n" +
		"function register(on) {\n" +
		"  calls++\n" +
		"  return read() + helper() + lib.ok\n" +
		"}\n" +
		"const tag = 'x'\n" +
		"__jxDefault = register\n" +
		"\n" +
		"module.exports.calls = calls;\n" +
		"module.exports.register = register;\n" +
		"module.exports.tag = tag;\n" +
		"module.exports.default = __jxDefault;"
	got := esmToCJS(src)
	if got != want {
		t.Fatalf("ESM→CJS 转换不对:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestModsEndToEnd 用官方 first-mod 形态的完整插件目录验收 mods 链路：
// 安装 → 开组件 → 装载（session.start 注册 /tally 命令）→ tool.call 计数与
// ui.invalidate no-op → /tally 斜杠命令执行。register.js 原文取自 Claude Code
// 官方文档的入门示例。
func TestModsEndToEnd(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, content string) {
		p := rel2path(root, rel)
		os.MkdirAll(filepath.Dir(p), 0755)
		os.WriteFile(p, []byte(content), 0644)
	}
	mk(".claude-plugin/plugin.json", `{"name":"first-mod","version":"0.1.0","description":"官方示例 mod"}`)
	mk("hooks/hooks.json", `{"description":"The first-mod hooks module","modules":["./register.js"]}`)
	mk("hooks/register.js", `let calls = 0

export function register(on) {
  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'tally',
      description: 'Show how many tool calls Claude has made',
    })
    return next(e)
  })

  on('tool.call', async ($, e, next) => {
    calls += 1
    $.ui.invalidate('ui.render')
    return next(e)
  })

  on('command.run', { command: 'tally' }, async () => {
    return { text: 'Claude has made ' + calls + ' tool calls since this mod loaded' }
  })
}
`)
	r := InitCCPlugins(nil, t.TempDir())
	key, err := r.ccInstallFromPath(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetComponent(key, "mods", true); err != nil {
		t.Fatal(err)
	}
	r.ModsStartAll()
	if !r.hasMods() {
		t.Fatal("mods 宿主未装载")
	}
	// mod 命令清单：session.start 里注册的 /tally（全名 first-mod:tally）
	cmds := r.ccModCommandList()
	if len(cmds) != 1 || cmds[0]["name"] != "first-mod:tally" {
		t.Fatalf("mod 命令 = %#v", cmds)
	}
	app := &App{CCPlugins: r}
	// tool.call 两次（观察放行 + 计数）
	for i := 0; i < 2; i++ {
		out := app.ccModPreToolUse(context.Background(), nil, &ToolCall{Function: ToolCallFunc{Name: "Bash", Arguments: `{}`}})
		if out != "" {
			t.Fatalf("观察型 tool.call 应放行，got %q", out)
		}
	}
	// /tally 与全名 /first-mod:tally 都能执行
	for _, name := range []string{"/tally", "/first-mod:tally"} {
		msg := app.ccExpandSlashMessage(context.Background(), nil, name)
		if !strings.Contains(msg, "Claude has made 2 tool calls") {
			t.Fatalf("%s 展开结果 = %q", name, msg)
		}
	}
	// tool.call 的 deny 短路：再造一个会拒绝的 mod
	mk2 := t.TempDir()
	mkF := func(rel, content string) {
		p := rel2path(mk2, rel)
		os.MkdirAll(filepath.Dir(p), 0755)
		os.WriteFile(p, []byte(content), 0644)
	}
	mkF(".claude-plugin/plugin.json", `{"name":"guard-mod","version":"0.1.0"}`)
	mkF("hooks/hooks.json", `{"modules":["./register.js"]}`)
	mkF("hooks/register.js", `export function register(on) {
  on('tool.call', { tool: 'Bash' }, async ($, e, next) => {
    if (e.command && e.command.includes('danger')) {
      return { deny: '危险命令' }
    }
    return next(e)
  })
}
`)
	key2, err := r.ccInstallFromPath(mk2, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetComponent(key2, "mods", true); err != nil {
		t.Fatal(err)
	}
	r.ModsStartAll()
	if out := app.ccModPreToolUse(context.Background(), nil, &ToolCall{Function: ToolCallFunc{Name: "Bash", Arguments: `{"command":"danger"}`}}); out != "工具调用被 mod 拒绝：危险命令" {
		t.Fatalf("deny 短路 = %q", out)
	}
	if out := app.ccModPreToolUse(context.Background(), nil, &ToolCall{Function: ToolCallFunc{Name: "Bash", Arguments: `{"command":"safe"}`}}); out != "" {
		t.Fatalf("安全命令应放行 = %q", out)
	}
}

// rel2path 拼测试目录路径。
func rel2path(root, rel string) string {
	return filepath.Join(root, filepath.FromSlash(rel))
}
