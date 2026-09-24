package main

// Python 运行环境发现：PDF / OCR / Office 组件的脚本需要系统 Python。
// 我们不随包分发 Python 运行时（DSH 的 office-runtime 是约 500MB 的
// 便携发行版，属于后续可选的发行形态），第一版按顺序探测系统解释器，
// 并对依赖模块做带缓存的可用性检查，缺失时给模型可直接转述的安装指引。

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// pythonRuntime 是一个可用的解释器：可执行文件与启动前缀参数
// （Windows 的 py 启动器需要先传 -3）。
type pythonRuntime struct {
	Exe     string
	Prefix  []string
	Version string
}

var (
	pyMu          sync.Mutex
	pyCached      *pythonRuntime
	pyCachedFailed bool
	pyModuleCache = map[string]bool{}
)

// findPython 探测系统 Python：依次尝试 python / py -3 / python3，
// 第一个能执行 --version 的胜出。结果进程级缓存。
func findPython() (*pythonRuntime, error) {
	pyMu.Lock()
	defer pyMu.Unlock()
	if pyCached != nil {
		return pyCached, nil
	}
	if pyCachedFailed {
		return nil, fmt.Errorf("未找到可用的 Python（此前探测失败）。请安装 Python 3.9 及以上版本，安装时勾选「加入 PATH」后重启助手")
	}
	candidates := []pythonRuntime{
		{Exe: "python"},
		{Exe: "py", Prefix: []string{"-3"}},
		{Exe: "python3"},
	}
	for i, cand := range candidates {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		out, err := exec.CommandContext(ctx, cand.Exe, append(cand.Prefix, "--version")...).CombinedOutput()
		cancel()
		if err != nil {
			_ = i
			continue
		}
		cand.Version = strings.TrimSpace(string(out))
		pyCached = &cand
		return pyCached, nil
	}
	pyCachedFailed = true
	return nil, fmt.Errorf("未找到可用的 Python。请安装 Python 3.9 及以上版本（安装时勾选「加入 PATH」），然后重试")
}

// hasModule 检查该解释器是否能 import 指定模块（结果缓存）。
func (p *pythonRuntime) hasModule(module string) bool {
	pyMu.Lock()
	defer pyMu.Unlock()
	key := p.Exe + ":" + module
	if ok, seen := pyModuleCache[key]; seen {
		return ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err := exec.CommandContext(ctx, p.Exe, append(p.Prefix, []string{"-c", "import " + module}...)...).Run()
	cancel()
	ok := err == nil
	pyModuleCache[key] = ok
	return ok
}

// run 执行一条脚本命令，返回标准输出；超时或非零退出返回错误（含 stderr 尾部）。
func (p *pythonRuntime) run(args []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.Exe, append(append([]string{}, p.Prefix...), args...)...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("脚本执行超时（%v）", timeout)
	}
	if err != nil {
		tail := string(out)
		if len(tail) > 1500 {
			tail = tail[:1500] + "…"
		}
		return "", fmt.Errorf("脚本执行失败: %v\n%s", err, tail)
	}
	return string(out), nil
}

// pyInstallHint 生成缺失模块的安装指引；module 为空表示引导装解释器本身。
func pyInstallHint(modules ...string) string {
	if len(modules) == 0 {
		return "请安装 Python 3.9+（勾选加入 PATH）后重试"
	}
	return "请在终端执行安装后重试: python -m pip install " + strings.Join(modules, " ")
}

// pyMissingFromStderr 从脚本报错里识别 ModuleNotFoundError，转成安装指引。
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
