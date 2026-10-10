package main

// 设计软件 MCP 桥目录与一键安装器（mcpservers.go 的配套）。
// 目录内置制造业常用设计软件的社区 MCP 桥，安装器统一动作：
//   1. 从 GitHub codeload 拉 tar.gz 源码包（国内网络下 github.com 主站
//      的 git 协议常被断，codeload/raw 一般可达；已下载过则复用本地副本）
//   2. 在 <dataDir>/mcp-servers/<id>/ 建独立 venv 并 pip 装依赖（PyPI 可达）
//   3. 注册为独立 MCP 服务器（stdio 命令指向 venv 里的解释器/入口）
// 安装只解决「桥就位」；对接的设计软件（SolidWorks/中望/Blender/…）
// 是否安装、是否打开，由使用时的工具调用自行报告。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// mcpCatalogItem 是目录里的一条桥。
type mcpCatalogItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Vendor      string `json:"vendor"`      // 对接的设计软件
	Description string `json:"description"`
	Requirement string `json:"requirement"` // 前置条件（不含本安装器负责的部分）
	PythonMin   string `json:"pythonMin,omitempty"`

	install func(r *MCPServersRegistry) (serverName string, notes []string, err error)
}

// mcpCatalogView 是 API 返回的目录条目（含安装状态）。
type mcpCatalogView struct {
	mcpCatalogItem
	Installed        bool   `json:"installed"`
	InstalledServer  string `json:"installedServer,omitempty"`
}

// mcpCatalog 内置目录。顺序即设置页展示顺序。
var mcpCatalog = []*mcpCatalogItem{
	{
		ID:          "solidworks",
		Name:        "SolidWorks 桥",
		Vendor:      "SolidWorks 2020+（COM）",
		Description: "打开/保存文档、特征与实体清单、质量属性、导出 STEP/IGES/STL、无 GUI 读文件信息（yimu0824/solidworks-mcp）。",
		Requirement: "本机装有 SolidWorks（建模工具调用经 COM 驱动；未装软件时服务器可启动但建模调用会失败）",
		install: func(r *MCPServersRegistry) (string, []string, error) {
			dst := filepath.Join(r.Root(), "solidworks")
			if err := mcpFetchRepo(dst, "yimu0824", "solidworks-mcp"); err != nil {
				return "", nil, err
			}
			// 上游代码按 mcp 1.x 低层 API 写（mcp 2.x 改了 Server 装饰器），
			// requirements 没钉上界，这里显式钉 mcp<2
			py, err := mcpPipInVenv(dst, "install", "mcp>=1.0.0,<2", "pywin32>=300")
			if err != nil {
				return "", nil, err
			}
			name := "solidworks"
			err = r.Upsert(mcpServerEntry{
				Name: name, Enabled: true, Source: "catalog:solidworks",
				Description: "SolidWorks 三维建模（COM 自动化桥）",
				Command:     py, Args: []string{filepath.Join(dst, "server.py")},
				Env:         map[string]string{"PYTHONIOENCODING": "utf-8"},
			})
			if err != nil {
				return "", nil, err
			}
			return name, []string{"服务器命令：" + py + " " + filepath.Join(dst, "server.py")}, nil
		},
	},
	{
		ID:          "multicad",
		Name:        "中望/AutoCAD 桥（multiCAD）",
		Vendor:      "ZWCAD 2020+、AutoCAD 2018+、GstarCAD、BricsCAD（COM）",
		Description: "7 个统一 MCP 工具覆盖 56 条 CAD 命令：绘图、图层、标注、对象变换等，多 CAD 可同时驱动（AnCode666/multiCAD-mcp）。",
		Requirement: "本机装有中望 ZWCAD 或 AutoCAD 等任一受支持 CAD，使用前先打开它",
		install: func(r *MCPServersRegistry) (string, []string, error) {
			dst := filepath.Join(r.Root(), "multicad")
			if err := mcpFetchRepo(dst, "AnCode666", "multiCAD-mcp"); err != nil {
				return "", nil, err
			}
			// 依赖清单取自 pyproject（flat 布局，pip install . 会因多顶层模块
			// 自动发现失败，直接装依赖、源码树运行）；mcp/fastmcp 钉 1.x/3.x
			// ——上游 import mcp.server.fastmcp，mcp 2.x 已改名移除
			if _, err := mcpPipInVenv(dst, "install",
				"fastmcp>=3.1.0,<4", "mcp>=1.24.0,<2", "pywin32>=300", "openpyxl>=3.1.0",
				"pillow>=10.0.0", "fastapi>=0.100.0", "uvicorn>=0.20.0",
				"pydantic>=2.0.0", "pydantic-settings>=2.0.0", "typing-extensions>=4.5.0"); err != nil {
				return "", nil, err
			}
			py := mcpVenvPython(dst)
			name := "multicad"
			err := r.Upsert(mcpServerEntry{
				Name: name, Enabled: true, Source: "catalog:multicad",
				Description: "中望 ZWCAD / AutoCAD 等 DWG 系 CAD（COM 自动化桥）",
				Command:     py, Args: []string{filepath.Join(dst, "src", "server.py")},
				Env:         map[string]string{"PYTHONIOENCODING": "utf-8"},
			})
			if err != nil {
				return "", nil, err
			}
			return name, []string{"服务器命令：" + py + " " + filepath.Join(dst, "src", "server.py")}, nil
		},
	},
	{
		ID:          "blender",
		Name:        "Blender 桥",
		Vendor:      "Blender 3.0+",
		Description: "读场景、在 Blender 内执行 Python（bpy）建模、截取视口；服务器从 PyPI 装（ahujasid/mcp-for-blender）。",
		Requirement: "本机装有 Blender，并安装随桥下载的 addon.py 插件后在侧栏点 Connect（端口 9876）",
		install: func(r *MCPServersRegistry) (string, []string, error) {
			dst := filepath.Join(r.Root(), "blender")
			// 源码树主要为 addon.py（Blender 侧插件）；服务器本体走 PyPI
			if err := mcpFetchRepo(dst, "ahujasid", "mcp-for-blender"); err != nil {
				return "", nil, err
			}
			if _, err := mcpPipInVenv(dst, "install", "mcp-for-blender"); err != nil {
				return "", nil, err
			}
			exe := filepath.Join(dst, ".venv", "Scripts", "mcp-for-blender.exe")
			if runtime.GOOS != "windows" {
				exe = filepath.Join(dst, ".venv", "bin", "mcp-for-blender")
			}
			if _, err := os.Stat(exe); err != nil {
				return "", nil, fmt.Errorf("pip 装好了但找不到入口 %s: %w", exe, err)
			}
			name := "blender"
			err := r.Upsert(mcpServerEntry{
				Name: name, Enabled: true, Source: "catalog:blender",
				Description: "Blender 三维创作（场景+bpy 执行桥）",
				Command:     exe,
			})
			if err != nil {
				return "", nil, err
			}
			return name, []string{
				"Blender 端还需装插件：Blender → 编辑 → 偏好设置 → 插件 → 安装，选择 " + filepath.Join(dst, "addon.py") + "，启用后在 3D 视口侧栏(N面板)→Blender MCP 里点 Connect",
			}, nil
		},
	},
	{
		ID:          "freecad",
		Name:        "FreeCAD 桥",
		Vendor:      "FreeCAD 1.0+",
		Description: "参数化建模、执行 Python 脚本、FEM 分析；FreeCAD 侧装 MCP 工作台（neka-nat/freecad-mcp）。",
		Requirement: "FreeCAD 桥需要 Python 3.12+；FreeCAD 侧需把 addon/FreeCADMCP 复制进 Mod 目录并在 MCP Addon 工作台点 Start RPC Server",
		PythonMin:   "3.12",
		install: func(r *MCPServersRegistry) (string, []string, error) {
			ok, have, err := mcpPythonAtLeast("3.12")
			if err != nil {
				return "", nil, fmt.Errorf("检测本机 Python 版本失败: %w", err)
			}
			if !ok {
				return "", nil, fmt.Errorf("freecad-mcp 需要 Python 3.12+，本机是 %s；请装好 3.12 后重试一键安装", have)
			}
			dst := filepath.Join(r.Root(), "freecad")
			if err := mcpFetchRepo(dst, "neka-nat", "freecad-mcp"); err != nil {
				return "", nil, err
			}
			if _, err := mcpPipInVenv(dst, "install", "freecad-mcp"); err != nil {
				return "", nil, err
			}
			exe := filepath.Join(dst, ".venv", "Scripts", "freecad-mcp.exe")
			if runtime.GOOS != "windows" {
				exe = filepath.Join(dst, ".venv", "bin", "freecad-mcp")
			}
			if _, err := os.Stat(exe); err != nil {
				return "", nil, fmt.Errorf("pip 装好了但找不到入口 %s: %w", exe, err)
			}
			name := "freecad"
			err = r.Upsert(mcpServerEntry{
				Name: name, Enabled: true, Source: "catalog:freecad",
				Description: "FreeCAD 参数化建模（RPC 桥）",
				Command:     exe,
			})
			if err != nil {
				return "", nil, err
			}
			return name, []string{
				"FreeCAD 端还需装工作台：把 " + filepath.Join(dst, "addon", "FreeCADMCP") + " 复制到 FreeCAD 的 Mod 目录（一般在 %APPDATA%\\FreeCAD\\Mod），重启 FreeCAD 后选 MCP Addon 工作台点 Start RPC Server",
			}, nil
		},
	},
	{
		ID:          "autocad",
		Name:        "AutoCAD 轻量桥",
		Vendor:      "AutoCAD（COM / pyautocad）",
		Description: "画线/多段线/圆/矩形、图层管理等 2D 绘图工具（vigneshpbmenon/autocad-mcp-server）；要中望/多 CAD 支持用上面的 multiCAD 桥。",
		Requirement: "本机装有 AutoCAD 并打开（经 pyautocad/COM 驱动）",
		install: func(r *MCPServersRegistry) (string, []string, error) {
			dst := filepath.Join(r.Root(), "autocad")
			if err := mcpFetchRepo(dst, "vigneshpbmenon", "autocad-mcp-server"); err != nil {
				return "", nil, err
			}
			// 上游 requirements 写的 pyautocad>=2.0.0 不存在（PyPI 最高
			// 0.2.0），代码 import mcp.server.fastmcp（mcp 1.x），这里按实际
			// 可用版本直装
			py, err := mcpPipInVenv(dst, "install", "mcp>=1.0.0,<2", "pyautocad", "pywin32>=300", "comtypes")
			if err != nil {
				return "", nil, err
			}
			// 兼容垫片：上游用老版 FastMCP(title=…)，mcp 1.x 的签名是 name=
			if err := mcpPatchFile(filepath.Join(dst, "server.py"),
				"FastMCP(title=", "FastMCP(name="); err != nil {
				return "", nil, err
			}
			name := "autocad"
			err = r.Upsert(mcpServerEntry{
				Name: name, Enabled: true, Source: "catalog:autocad",
				Description: "AutoCAD 2D 绘图（pyautocad COM 桥）",
				Command:     py, Args: []string{filepath.Join(dst, "server.py")},
				Env:         map[string]string{"PYTHONIOENCODING": "utf-8"},
			})
			if err != nil {
				return "", nil, err
			}
			return name, []string{"服务器命令：" + py + " " + filepath.Join(dst, "server.py")}, nil
		},
	},
}

// mcpCatalogByID 按 id 取目录条目。
func mcpCatalogByID(id string) *mcpCatalogItem {
	for _, it := range mcpCatalog {
		if it.ID == id {
			return it
		}
	}
	return nil
}

// CatalogView 目录清单（含安装状态：同名服务器已注册即视为已装）。
func (r *MCPServersRegistry) CatalogView() []mcpCatalogView {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []mcpCatalogView{}
	for _, it := range mcpCatalog {
		v := mcpCatalogView{mcpCatalogItem: *it}
		for _, s := range r.file.Servers {
			if s.Source == "catalog:"+it.ID {
				v.Installed = true
				v.InstalledServer = s.Name
			}
		}
		out = append(out, v)
	}
	return out
}

// InstallCatalog 一键安装目录条目：拉代码 → venv+pip → 注册服务器。
// 串行化（同一时间只装一个）；返回注册的服务器名与给用户的提示。
func (r *MCPServersRegistry) InstallCatalog(id string) (string, []string, error) {
	it := mcpCatalogByID(id)
	if it == nil {
		return "", nil, fmt.Errorf("目录里没有 %s", id)
	}
	r.installMu.Lock()
	defer r.installMu.Unlock()
	return it.install(r)
}

// ---------- 安装器基础设施 ----------

// mcpFetchRepo 从 GitHub codeload 拉 tar.gz 源码包解压到 dst（main/master
// 依次尝试）；dst 已有内容则视为已下载直接复用（重装不重复下载）。
func mcpFetchRepo(dst, owner, repo string) error {
	if entries, err := os.ReadDir(dst); err == nil && len(entries) > 0 {
		return nil // 已就位
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	var lastErr error
	for _, branch := range []string{"main", "master"} {
		url := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/refs/heads/%s", owner, repo, branch)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			lastErr = fmt.Errorf("下载 %s：%s HTTP %d", repo, branch, resp.StatusCode)
			continue
		}
		err = mcpExtractTarGz(resp.Body, dst)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("下载 %s/%s 失败（codeload main/master 均未成功）: %w", owner, repo, lastErr)
}

// mcpExtractTarGz 解 tar.gz 流到 dst，剥掉 GitHub 包的第一层目录。
func mcpExtractTarGz(src io.Reader, dst string) error {
	gz, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("gzip 解压失败: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar 读取失败: %w", err)
		}
		parts := strings.SplitN(filepath.ToSlash(hdr.Name), "/", 2)
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(parts[1]))
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			continue // 防路径穿越
		}
		target := filepath.Join(dst, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
}

// mcpPatchFile 把文件里的 old 串替换为 new（不存在 old 时为幂等空操作）。
func mcpPatchFile(path, old, new string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !strings.Contains(string(data), old) {
		return nil
	}
	return os.WriteFile(path, bytes.ReplaceAll(data, []byte(old), []byte(new)), 0644)
}

// mcpVenvPython 返回 dir/.venv 里的解释器绝对路径。
func mcpVenvPython(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, ".venv", "Scripts", "python.exe")
	}
	return filepath.Join(dir, ".venv", "bin", "python")
}

// mcpPipInVenv 确保 dir 下有 venv，然后用 venv 的 pip 执行给定子命令，
// 返回 venv 解释器路径。输出捕获进错误信息（pip 的进度噪声不进日志）。
func mcpPipInVenv(dir string, pipArgs ...string) (string, error) {
	py := mcpVenvPython(dir)
	if _, err := os.Stat(py); err != nil {
		if err := mcpRunCmd(2*time.Minute, "", "python", "-m", "venv", filepath.Join(dir, ".venv")); err != nil {
			return "", fmt.Errorf("创建 venv 失败（需要本机 PATH 里有 python 3.10+）: %w", err)
		}
	}
	args := append([]string{"-m", "pip", "--disable-pip-version-check"}, pipArgs...)
	if err := mcpRunCmd(10*time.Minute, dir, py, args...); err != nil {
		return py, fmt.Errorf("pip %s 失败: %w", strings.Join(pipArgs, " "), err)
	}
	return py, nil
}

// mcpRunCmd 跑一条命令，失败时带上尾部输出（截断）。
func mcpRunCmd(timeout time.Duration, dir, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		tail := out.String()
		if len(tail) > 1500 {
			tail = tail[len(tail)-1500:]
		}
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%s %s 超时；输出尾部:\n%s", name, strings.Join(args, " "), tail)
		}
		return fmt.Errorf("%s %s: %w；输出尾部:\n%s", name, strings.Join(args, " "), err, tail)
	}
	return nil
}

// mcpPythonAtLeast 报告 PATH 里的 python 是否 ≥ 指定主次版本（"3.12"）。
func mcpPythonAtLeast(min string) (ok bool, have string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "python", "-c",
		`import sys; print("%d.%d" % sys.version_info[:2])`).Output()
	if err != nil {
		return false, "", err
	}
	have = strings.TrimSpace(string(out))
	var maj, minM int
	if _, err := fmt.Sscanf(min, "%d.%d", &maj, &minM); err != nil {
		return false, have, fmt.Errorf("版本下限 %q 解析失败: %w", min, err)
	}
	var h1, h2 int
	if _, err := fmt.Sscanf(have, "%d.%d", &h1, &h2); err != nil {
		return false, have, err
	}
	return h1 > maj || (h1 == maj && h2 >= minM), have, nil
}
