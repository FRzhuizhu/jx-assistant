package main

// 技能 Python 运行时的统一执行器：跑脚本、查依赖模块、缓存管理。
// 运行时本身随包分发（技能/_runtime/python，装配见 skills.go），
// 不探测、不依赖用户机器上的系统 Python——发行包自包含一切依赖。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// pythonRuntime 是一个可用的解释器：可执行文件与启动前缀参数
// （预留 Windows py 启动器这类前缀形态，便携运行时为空）。
type pythonRuntime struct {
	Exe     string
	Prefix  []string
	Version string
	Env     []string // 非空 = 执行时替换继承环境（安卓内置运行时需 Termux 前缀环境）
}

var (
	pyMu          sync.Mutex
	pyModuleCache = map[string]bool{}
)

// hasModule 检查该解释器是否能 import 指定模块（结果缓存）。
func (p *pythonRuntime) hasModule(module string) bool {
	pyMu.Lock()
	defer pyMu.Unlock()
	key := p.Exe + ":" + module
	if ok, seen := pyModuleCache[key]; seen {
		return ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, err := runToolChildEnv(ctx, p.Exe, append(p.Prefix, "-c", "import "+module), "", 0, p.Env)
	cancel()
	ok := err == nil
	pyModuleCache[key] = ok
	return ok
}

// clearModuleCache 清掉该解释器的模块检查缓存：装完依赖后必须调用，
// 否则会读到安装前的缓存负结果（缓存毒化）。
func (p *pythonRuntime) clearModuleCache() {
	pyMu.Lock()
	defer pyMu.Unlock()
	prefix := p.Exe + ":"
	for k := range pyModuleCache {
		if strings.HasPrefix(k, prefix) {
			delete(pyModuleCache, k)
		}
	}
}

// run 执行一条脚本命令，返回标准输出；超时或非零退出返回错误（含 stderr 尾部）。
func (p *pythonRuntime) run(args []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// 统一入口（child.go）：压窗 + 超时 + 捕获 + 解码
	out, err := runToolChildEnv(ctx, p.Exe, append(append([]string{}, p.Prefix...), args...), "", timeout, p.Env)
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("脚本执行超时（%v）", timeout)
	}
	if err != nil {
		tail := out
		if len(tail) > 1500 {
			tail = tail[:1500] + "…"
		}
		return "", fmt.Errorf("脚本执行失败: %v\n%s", err, tail)
	}
	return out, nil
}

// pyMissingFromStderr 从脚本报错里识别 ModuleNotFoundError：便携运行时
// 已随包分发仍缺模块，说明依赖清单未覆盖或运行时损坏——转成修复指引。
func pyMissingFromStderr(stderr string) (string, bool) {
	const marker = "No module named '"
	i := strings.Index(stderr, marker)
	if i < 0 {
		return "", false
	}
	rest := stderr[i+len(marker):]
	if j := strings.Index(rest, "'"); j >= 0 {
		return rest[:j], true
	}
	return "", false
}
