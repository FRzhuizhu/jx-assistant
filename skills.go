package main

// 技能（Skill）体系：文件类能力以「技能」形态组织——每个技能一个独立目录
// （SKILL.md 标准说明 + manifest.json 依赖清单 + scripts/ 脚本），统一放在
// 软件目录的 技能/ 下。浏览器/电脑操作与权限强相关，保持为组件不在此列。
//
// 运行环境自包含：技能一律使用随包分发的便携运行时（技能/_runtime/python），
// 不探测、不依赖用户机器上的系统 Python——面向非编程用户，发行包必须
// 自带全部运行时与依赖，安装即离线全功能。开发目录缺 _runtime 时首次
// 使用联网装配一次（下载 embeddable Python + 镜像装 wheel），此后离线
// 可用；打包脚本会校验运行时依赖齐全，缺一不出包。
// 技能母本内嵌在可执行文件里（_runtime 因下划线前缀被 go:embed 天然排除），
// 软件目录缺技能目录时首次启动自动释放。

import (
	"archive/zip"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed 技能
var skillsFS embed.FS

// SkillManifest 是技能目录里 manifest.json 的结构：声明运行依赖。
type SkillManifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Entry       string `json:"entry"`
	Python      struct {
		MinVersion string   `json:"minVersion"`
		Modules    []string `json:"modules"`
		Wheels     []string `json:"wheels"`
	} `json:"python"`
	Variants map[string]struct {
		Modules []string `json:"modules"`
		Wheels  []string `json:"wheels"`
	} `json:"variants"`
}

var (
	provisionMu   sync.Mutex
	provisionedPy *pythonRuntime
)

// SkillsDir 解析技能目录：配置指定 > 可执行文件旁 > 数据目录；
// 目录不存在时从内嵌母本释放（覆盖缺目录的新装场景）。
func (a *App) SkillsDir() (string, error) {
	a.skillsMu.Lock()
	defer a.skillsMu.Unlock()
	if a.skillsReady && a.skillsDirOurs != "" {
		return a.skillsDirOurs, nil
	}
	dir := a.Cfg.SkillsDir
	if dir == "" {
		dir = filepath.Join("技能")
		if exe, err := os.Executable(); err == nil {
			candidate := filepath.Join(filepath.Dir(exe), "技能")
			if dirWritable(candidate) || dirExists(candidate) {
				dir = candidate
			} else {
				dir = filepath.Join(a.Cfg.DataDir, "技能")
			}
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if !dirExists(abs) {
		if err := materializeSkills(abs); err != nil {
			return "", fmt.Errorf("释放内嵌技能失败: %w", err)
		}
	}
	a.skillsDirOurs = abs
	a.skillsReady = true
	return abs, nil
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func dirWritable(p string) bool {
	if dirExists(p) {
		tmp := filepath.Join(p, ".wtest")
		if err := os.WriteFile(tmp, []byte("1"), 0644); err != nil {
			return false
		}
		_ = os.Remove(tmp)
		return true
	}
	parent := filepath.Dir(p)
	if !dirExists(parent) {
		return false
	}
	return dirWritable(parent)
}

// materializeSkills 把内嵌的技能母本写到目标目录。
// materializeSkills 把内嵌技能母本释放到磁盘（exe 旁没有 技能/ 目录时的
// 兜底，如裸 exe 形态）。fs.WalkDir 全量递归——手写的两层循环只认
// 「技能/<名>/scripts/」布局，技能/user/<名>/ 这类分组子目录会把目录当
// 文件读而整体失败（实测：释放 技能/user/com-probe 时报「is a directory」）。
func materializeSkills(dest string) error {
	return fs.WalkDir(skillsFS, "技能", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		return writeEmbeddedFile(filepath.Join(dest, filepath.FromSlash(strings.TrimPrefix(p, "技能/"))), p)
	})
}

func writeEmbeddedFile(dest, embeddedPath string) error {
	src, err := skillsFS.Open(embeddedPath)
	if err != nil {
		return err
	}
	defer src.Close()
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0644)
}

// readSkillManifest 读取技能的 manifest.json。
func (a *App) readSkillManifest(skillID string) (*SkillManifest, string, error) {
	dir, err := a.SkillsDir()
	if err != nil {
		return nil, "", err
	}
	skillDir := filepath.Join(dir, skillID)
	data, err := os.ReadFile(filepath.Join(skillDir, "manifest.json"))
	if err != nil {
		return nil, "", fmt.Errorf("技能 %s 缺少 manifest.json: %w", skillID, err)
	}
	m := &SkillManifest{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, "", err
	}
	return m, skillDir, nil
}

// readSkillGuide 读取技能的 SKILL.md 正文（给模型看的标准说明）。
func (a *App) readSkillGuide(skillID string) (string, error) {
	dir, err := a.SkillsDir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, skillID, "SKILL.md"))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// skillRuntimeNotice：技能运行时的装配进度只写引擎日志，不再弹进对话。
// 这些通知原先经 tool_wait 显示成聊天里的「技能运行时就绪（后续离线可用）」
// 一类提示——任何一次技能调用都会看到（首次装配时还会连报多条），判定为
// 噪声，已按用户要求下线；装配失败仍由各步的 error 返回如实报错。
func skillRuntimeNotice(msg string) {
	log.Printf("[技能运行时] %s", msg)
}

// ensureSkillRuntime 保证「技能的 Python 运行时 + 全部依赖模块」可用：
// 桌面用随包分发的 技能/_runtime/python（自包含原则，不碰系统 Python），
// 已装配直接复用；缺失时联网装配（结果进程级缓存）。
// 安卓用内置 Linux 运行时（Termux 快照）的 python3——缺依赖经 pip 现场补装，
// 每次调用都核对本次技能的依赖（探测结果有缓存，常态零开销）。
func (a *App) ensureSkillRuntime(modules, wheels []string) (*pythonRuntime, error) {
	provisionMu.Lock()
	defer provisionMu.Unlock()
	if provisionedPy != nil && !isAndroidRuntime && pyModulesReady(provisionedPy, modules) {
		return provisionedPy, nil
	}
	emit := skillRuntimeNotice
	if isAndroidRuntime {
		py, err := ensureTermuxSkillRuntime(modules, wheels, emit)
		if err != nil {
			return nil, err
		}
		provisionedPy = py
		return py, nil
	}
	dir, err := a.SkillsDir()
	if err != nil {
		return nil, err
	}
	rtDir := filepath.Join(dir, "_runtime", "python")
	py, err := provisionPythonRuntime(rtDir, modules, wheels, emit)
	if err != nil {
		return nil, err
	}
	provisionedPy = py
	return py, nil
}

// pyModulesReady 缓存的运行时是否已具备本次请求的全部依赖模块。带依赖的
// 请求（office/pdf 等）不能被「裸运行时」缓存短路：run_python 会先取基础
// 运行时（modules 为空），若就此命中缓存，后续带依赖的请求补装永远走不到
// （顺序敏感的隐患）。hasModule 有结果缓存，全命中时零开销；缺模块则不命中
// 缓存、落到 provisionPythonRuntime 幂等补装（已装过的 wheel 会跳过）。
func pyModulesReady(py *pythonRuntime, modules []string) bool {
	for _, m := range modules {
		if !py.hasModule(m) {
			return false
		}
	}
	return true
}

// ensureTermuxSkillRuntime 安卓：技能解释器 = 内置 Linux 运行时的 python3
// （office/pdf 依赖已烘焙进快照，见 android/runtime/make-runtime.ps1）。
// 快照缺的纯 Python 依赖现场走 pip 补装（USTC 镜像已烘焙）；带 C 扩展且
// PyPI 无安卓轮子的依赖装不上就如实报错（该能力需要加进快照才能支持）。
func ensureTermuxSkillRuntime(modules, wheels []string, emit func(string)) (*pythonRuntime, error) {
	prefix := termuxPrefixOrEmpty()
	if prefix == "" {
		return nil, fmt.Errorf("内置 Linux 运行时未就绪（首启装配中或装配失败，详见引擎日志），技能暂不可用；稍后重试")
	}
	py := &pythonRuntime{
		Exe:     filepath.Join(prefix, "bin", "python3"),
		Version: "内置运行时",
		Env:     termuxEnv(prefix),
	}
	missing := []string{}
	for _, m := range modules {
		if !py.hasModule(m) {
			missing = append(missing, m)
		}
	}
	if len(missing) == 0 {
		return py, nil
	}
	emit(fmt.Sprintf("内置运行时缺依赖（%s），正在经 pip 补装（镜像源，需联网）…", strings.Join(wheels, " ")))
	ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
	defer cancel()
	args := append([]string{"install", "--quiet", "-i", pipMirror}, wheels...)
	out, err := runToolChildEnv(ctx, filepath.Join(prefix, "bin", "pip"), args, "", 0, py.Env)
	if err != nil {
		return nil, fmt.Errorf("补装技能依赖失败: %v\n%s", err, tailRunes(out, 800))
	}
	py.clearModuleCache()
	for _, m := range modules {
		if !py.hasModule(m) {
			return nil, fmt.Errorf("依赖 %s 补装后仍不可用（PyPI 可能没有安卓轮子），请把该技能名与模块名反馈给开发者加进运行时快照", m)
		}
	}
	emit("内置运行时依赖已补齐")
	return py, nil
}

const (
	pythonEmbedURL   = "https://www.python.org/ftp/python/3.12.8/python-3.12.8-embed-amd64.zip"
	getPipURL        = "https://bootstrap.pypa.io/get-pip.py"
	pipMirror        = "https://pypi.tuna.tsinghua.edu.cn/simple"
	provisionTimeout = 20 * time.Minute
)

// provisionPythonRuntime 装配便携 Python：下载 embeddable 包 → 启用
// site-packages → 装 pip → 从镜像装技能 wheel。幂等：已存在直接复用。
func provisionPythonRuntime(rtDir string, modules, wheels []string, emit func(string)) (*pythonRuntime, error) {
	pyExe := filepath.Join(rtDir, "python.exe")
	if _, err := os.Stat(pyExe); err != nil {
		emit("首次使用本技能：正在下载便携 Python（约 11MB）…")
		if err := os.MkdirAll(rtDir, 0755); err != nil {
			return nil, err
		}
		zipPath := filepath.Join(rtDir, "python-embed.zip")
		if err := downloadFile(pythonEmbedURL, zipPath); err != nil {
			return nil, fmt.Errorf("下载便携 Python 失败（需联网一次）: %w", err)
		}
		if err := unzip(zipPath, rtDir); err != nil {
			return nil, fmt.Errorf("解压便携 Python 失败: %w", err)
		}
		_ = os.Remove(zipPath)
		// embeddable 包默认禁用 site-packages：改 ._pth 启用，否则装好的库不可见
		if err := enableSitePackages(rtDir); err != nil {
			return nil, err
		}
		emit("便携 Python 就绪，正在安装 pip…")
		getPip := filepath.Join(rtDir, "get-pip.py")
		if err := downloadFile(getPipURL, getPip); err != nil {
			return nil, fmt.Errorf("下载 get-pip 失败: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
		defer cancel()
		out, err := runCommandCapture(ctx, pyExe, []string{getPip, "--no-warn-script-location"}, rtDir)
		if err != nil {
			return nil, fmt.Errorf("安装 pip 失败: %v\n%s", err, tailRunes(out, 800))
		}
		_ = os.Remove(getPip)
	}

	py := &pythonRuntime{Exe: pyExe, Version: "便携运行时"}
	// 已装过 wheel 就不重复装（幂等）
	missing := []string{}
	for _, m := range modules {
		if !py.hasModule(m) {
			missing = append(missing, m)
		}
	}
	if len(missing) > 0 {
		emit(fmt.Sprintf("正在安装技能依赖（%s，走清华镜像）…", strings.Join(wheels, " ")))
		ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
		defer cancel()
		args := []string{"-m", "pip", "install", "--quiet", "-i", pipMirror}
		args = append(args, wheels...)
		out, err := runCommandCapture(ctx, pyExe, args, rtDir)
		if err != nil {
			return nil, fmt.Errorf("安装技能依赖失败: %v\n%s", err, tailRunes(out, 800))
		}
		// 清除安装前的缓存负结果，否则永远"装了也没用"（缓存毒化）
		py.clearModuleCache()
		for _, m := range modules {
			if !py.hasModule(m) {
				return nil, fmt.Errorf("依赖 %s 安装后仍不可用，请查看日志", m)
			}
		}
	}
	emit("技能运行时就绪（后续离线可用）")
	return py, nil
}

// runCommandCapture 在指定目录运行命令并捕获合并输出（供运行时装配用）。
func runCommandCapture(ctx context.Context, exe string, args []string, dir string) (string, error) {
	// 统一入口（child.go）：压窗 + 捕获 + 解码
	return runToolChild(ctx, exe, args, dir, 0)
}

func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// enableSitePackages 打开 embeddable 包默认关闭的 site-packages 支持。
func enableSitePackages(rtDir string) error {
	entries, err := os.ReadDir(rtDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "._pth") {
			p := filepath.Join(rtDir, e.Name())
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			fixed := strings.Replace(string(data), "#import site", "import site\nLib/site-packages", 1)
			return os.WriteFile(p, []byte(fixed), 0644)
		}
	}
	return fmt.Errorf("找不到 ._pth 配置文件")
}

// downloadFile 流式下载文件到目标路径。
func downloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s 返回 %d", url, resp.StatusCode)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

// unzip 解压 zip 到目标目录（防条目路径穿越）。
func unzip(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	cleanDest := filepath.Clean(dest)
	for _, f := range r.File {
		target := filepath.Join(dest, filepath.FromSlash(f.Name))
		if target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
			return fmt.Errorf("zip 内含非法路径: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			src.Close()
			return err
		}
		_, err = io.Copy(dst, src)
		src.Close()
		dst.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
