package main

// 内置运行时（Termux 快照）装配：解压、路径重写与就绪管理。
//
// 快照由 android/runtime/make-runtime.ps1 在设备上制作（选包/流程与 sha256 见
// 同目录 MANIFEST.json）。制作期已把 Termux 编译期前缀文本重写为制作机前缀
// ——装配时必须做**二次重写**：把制作机前缀与旧前缀统一替换成本机运行时的
// 实际 prefix（脚本 shebang 与符号链接目标都要处理）。
//
// 快照形态：tar.gz，条目路径相对 $PREFIX（顶层 bin/ lib/ etc/ ...），包含
// 符号链接条目；本文件用 Go 标准库解析，零外部命令依赖（Android 的 toybox
// 没有 xz，gzip 因此是唯一稳妥的压缩格式选择）。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// termuxBuildPrefix 是快照的制作机前缀（PoC 包名），装配时需重写。
	termuxBuildPrefix = "/data/user/0/com.jianxiang.poc/files/t"
	// termuxOldPrefix 是 Termux 编译期前缀（双保险，理论上已被制作期替换掉）。
	termuxOldPrefix = "/data/data/com.termux/files/usr"
)

// termuxTextScanDirs 是二次重写扫描文本的目录（脚本与配置所在；符号链接全树扫）。
var termuxTextScanDirs = []string{"bin", "libexec", "etc"}

// TermuxManifest 对应 android/runtime/MANIFEST.json 的解析子集。
type TermuxManifest struct {
	Version string `json:"version"`
	File    string `json:"file"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

func readTermuxManifest(path string) (*TermuxManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := &TermuxManifest{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("MANIFEST.json 解析失败: %w", err)
	}
	if m.Version == "" {
		return nil, fmt.Errorf("MANIFEST.json 缺少 version")
	}
	return m, nil
}

// termuxEnsure 确保运行时 prefix 就绪并返回 prefix 路径（幂等，按版本水位短路）。
func termuxEnsure(manifestPath, tarGzPath, prefixRoot string, emit func(string)) (string, error) {
	if emit == nil {
		emit = func(string) {}
	}
	m, err := readTermuxManifest(manifestPath)
	if err != nil {
		return "", fmt.Errorf("读取运行时清单失败: %w", err)
	}
	versionFile := filepath.Join(prefixRoot, ".version")
	if b, err := os.ReadFile(versionFile); err == nil &&
		strings.TrimSpace(string(b)) == m.Version {
		if _, serr := os.Stat(filepath.Join(prefixRoot, "bin", "bash")); serr == nil {
			return prefixRoot, nil
		}
	}
	emit(fmt.Sprintf("装配内置运行时（约 %.0f MB，首次需要十几秒）…", float64(m.Size)/1e6))
	staging := prefixRoot + ".staging"
	if err := os.RemoveAll(staging); err != nil {
		return "", err
	}
	if err := os.MkdirAll(staging, 0755); err != nil {
		return "", err
	}
	if err := extractTermuxTar(tarGzPath, staging, m.SHA256); err != nil {
		os.RemoveAll(staging)
		return "", err
	}
	texts, links, err := termuxRelocate(staging, prefixRoot)
	if err != nil {
		os.RemoveAll(staging)
		return "", err
	}
	emit(fmt.Sprintf("已重写 %d 个脚本、%d 个符号链接", texts, links))
	// 写入 jx-pkg 包管理包装（装配产物；内容平台无关、无构建机路径，无需重写）
	if err := os.WriteFile(filepath.Join(staging, "bin", "jx-pkg"), []byte(termuxJxPkgScript), 0755); err != nil {
		os.RemoveAll(staging)
		return "", err
	}
	if err := os.RemoveAll(prefixRoot); err != nil {
		return "", err
	}
	if err := os.Rename(staging, prefixRoot); err != nil {
		return "", fmt.Errorf("落位运行时失败: %w", err)
	}
	if err := os.WriteFile(versionFile, []byte(m.Version+"\n"), 0644); err != nil {
		return "", err
	}
	emit("运行时装配完成")
	return prefixRoot, nil
}

// extractTermuxTar 解压快照到 dest：目录/普通文件/硬链接/符号链接按 tar 条目
// 重建，权限按 header 恢复；同时对压缩流做 sha256 校验（与 MANIFEST 比对）。
func extractTermuxTar(tarGz, dest, wantSHA string) error {
	f, err := os.Open(tarGz)
	if err != nil {
		return fmt.Errorf("打开运行时快照失败: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	gz, err := gzip.NewReader(io.TeeReader(f, h))
	if err != nil {
		return fmt.Errorf("快照不是合法的 gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	symlinkFail := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取快照失败: %w", err)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./"), "/")
		if name == "" {
			continue
		}
		target, err := safeJoin(dest, name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			_ = os.Chmod(target, os.FileMode(hdr.Mode)&0777)
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				symlinkFail++ // Windows 测试环境可能无符号链接权限；Android 上不应发生
			}
		case tar.TypeLink:
			src, err := safeJoin(dest, strings.TrimPrefix(filepath.ToSlash(hdr.Linkname), "./"))
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Link(src, target); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
			_ = os.Chmod(target, os.FileMode(hdr.Mode)&0777)
		}
	}
	if symlinkFail > 0 {
		fmt.Fprintf(os.Stderr, "[runtime] 符号链接创建失败 %d 条（当前环境可能未开启符号链接权限；Android 上应为 0）\n", symlinkFail)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if wantSHA != "" && !strings.EqualFold(sum, wantSHA) {
		return fmt.Errorf("快照完整性校验失败：sha256=%s，期望 %s（请重新构建快照）", sum, wantSHA)
	}
	return nil
}

// safeJoin 把 tar 条目名拼到 root 下并防路径穿越。
func safeJoin(root, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || filepath.IsAbs(clean) {
		return "", fmt.Errorf("快照包含非法路径: %s", name)
	}
	return filepath.Join(root, clean), nil
}

// termuxRelocate 装配期二次重写：把全树符号链接与文本脚本中残留的制作机前缀/
// 旧前缀替换为运行时实际 prefix。返回文本与链接的改写数量。
func termuxRelocate(tree, finalPrefix string) (texts, links int, err error) {
	if err = filepath.WalkDir(tree, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil || d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		tgt, rerr := os.Readlink(path)
		if rerr != nil ||
			(!strings.Contains(tgt, termuxBuildPrefix) && !strings.Contains(tgt, termuxOldPrefix)) {
			return nil
		}
		nt := strings.ReplaceAll(tgt, termuxBuildPrefix, finalPrefix)
		nt = strings.ReplaceAll(nt, termuxOldPrefix, finalPrefix)
		_ = os.Remove(path)
		if lerr := os.Symlink(nt, path); lerr == nil {
			links++
		}
		return nil
	}); err != nil {
		return 0, 0, err
	}
	for _, sub := range termuxTextScanDirs {
		root := filepath.Join(tree, sub)
		if !dirExists(root) {
			continue
		}
		if werr := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil || d.IsDir() {
				return nil
			}
			fi, ierr := d.Info()
			if ierr != nil || fi.Mode()&fs.ModeSymlink != 0 || fi.Size() > 1<<20 {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil ||
				(!bytes.Contains(data, []byte(termuxBuildPrefix)) && !bytes.Contains(data, []byte(termuxOldPrefix))) {
				return nil
			}
			head := data
			if len(head) > 8192 {
				head = head[:8192]
			}
			if bytes.IndexByte(head, 0) >= 0 {
				return nil // 二进制文件跳过
			}
			nd := bytes.ReplaceAll(data, []byte(termuxBuildPrefix), []byte(finalPrefix))
			nd = bytes.ReplaceAll(nd, []byte(termuxOldPrefix), []byte(finalPrefix))
			if werr2 := os.WriteFile(path, nd, fi.Mode().Perm()); werr2 == nil {
				texts++
			}
			return nil
		}); werr != nil {
			return texts, links, werr
		}
	}
	return texts, links, nil
}

// termuxEnv 返回运行内置命令所需的环境变量（在系统环境之上覆盖式追加，
// 重复 KEY 以后者为准：execve 语义）。prefix 不存在时补建 home/tmp。
func termuxEnv(prefix string) []string {
	home := filepath.Join(prefix, "home")
	tmp := filepath.Join(prefix, "tmp")
	_ = os.MkdirAll(home, 0755)
	_ = os.MkdirAll(tmp, 0755)
	return append(os.Environ(),
		"HOME="+home,
		"PREFIX="+prefix,
		"PATH="+prefix+"/bin:"+prefix+"/bin/applets:/system/bin:/system/xbin",
		"TMPDIR="+tmp,
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"SHELL="+prefix+"/bin/bash",
		"LD_LIBRARY_PATH="+prefix+"/lib",
		"SSL_CERT_FILE="+prefix+"/etc/tls/cert.pem",
		"APT_CONFIG="+prefix+"/etc/apt/apt.conf",
		"DPKG_ROOT="+prefix,
	)
}

// ---------- 运行时装配的单例与自检 ----------

var (
	termuxMu       sync.Mutex
	termuxProvider func(emit func(string)) (string, error)
	termuxCached   string
)

// setTermuxProvider 由 main（安卓）注册"确保运行时就绪"的实现；未注册返回空。
func setTermuxProvider(fn func(emit func(string)) (string, error)) {
	termuxMu.Lock()
	defer termuxMu.Unlock()
	termuxProvider = fn
}

// termuxPrefixOrEmpty 返回内置运行时 prefix（首次调用触发装配并进程级缓存）；
// 未注册/装配失败返回 ""，调用方回落系统路径执行。
func termuxPrefixOrEmpty() string {
	termuxMu.Lock()
	defer termuxMu.Unlock()
	if termuxCached != "" {
		return termuxCached
	}
	if termuxProvider == nil {
		return ""
	}
	p, err := termuxProvider(nil)
	if err != nil {
		log.Printf("[runtime] 内置运行时装配失败（回落系统执行）: %v", err)
		return ""
	}
	termuxCached = p
	return p
}

// runRuntimeSelftest 是 `jx runtime-selftest [dataDir]` 子命令：装配内置运行时并
// 跑冒烟命令（安卓故障诊断；不启动服务）。dataDir 缺省 "data"。
func runRuntimeSelftest(args []string) int {
	if !isAndroidRuntime {
		fmt.Fprintln(os.Stderr, "runtime-selftest 仅安卓平台可用")
		return 2
	}
	dataDir := "data"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		dataDir = args[0]
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dataDir 解析失败:", err)
		return 1
	}
	fmt.Println("[selftest] dataDir =", abs)
	prefix, err := termuxEnsure(
		filepath.Join(abs, "runtime-MANIFEST.json"),
		filepath.Join(abs, "runtime.tgz"),
		filepath.Join(abs, "runtime"),
		func(s string) { fmt.Println("[selftest]", s) })
	if err != nil {
		fmt.Fprintln(os.Stderr, "[selftest] 装配失败:", err)
		return 1
	}
	fmt.Println("[selftest] prefix =", prefix)
	out, err := runTermuxBash(context.Background(), prefix, sandboxRun{
		Dir:     filepath.Join(prefix, "home"),
		Command: `echo SELFTEST-OK; python3 -c "import docx,openpyxl,pptx; print('PY-OK')"; apt --version 2>/dev/null | head -1`,
		Timeout: 120 * time.Second,
	})
	fmt.Println("[selftest] 冒烟输出:")
	fmt.Println(out)
	if err != nil || !strings.Contains(out, "SELFTEST-OK") || !strings.Contains(out, "PY-OK") {
		fmt.Fprintln(os.Stderr, "[selftest] 冒烟失败:", err)
		return 1
	}
	fmt.Println("[selftest] 全部通过")
	return 0
}

// termuxJxPkgScript 是随装配写入 prefix/bin 的包管理包装：把"下载→解包→归位→
// 重写"一条龙化，避免模型手动三步（指引见系统提示词与环境快照）。apt 所需
// 环境（APT_CONFIG / SSL_CERT_FILE / DPKG_ROOT）已由引擎 termuxEnv 注入。
const termuxJxPkgScript = `#!/bin/bash
# jx-pkg：内置 Termux 运行时的包管理包装（引擎装配时写入）。
set -e
P="$PREFIX"
O="/data/data/com.termux/files/usr"
rewrite() {
  grep -RIl "$O" "$P/bin" "$P/libexec" "$P/etc" 2>/dev/null | xargs -r sed -i s#"$O"#"$P"#g || true
}
case "${1:-}" in
  update)
    apt update
    ;;
  install)
    shift
    apt install -y -d "$@"
    for f in "$P"/var/cache/apt/archives/*.deb; do dpkg-deb -x "$f" "$P" >/dev/null 2>&1 || true; done
    if [ -d "$P/data/data/com.termux/files/usr" ]; then
      cp -a "$P/data/data/com.termux/files/usr/." "$P/" && rm -rf "$P/data"
    fi
    rm -f "$P"/var/cache/apt/archives/*.deb
    rewrite
    echo "jx-pkg: 已安装（$*）"
    ;;
  pip)
    shift
    pip "$@"
    rewrite
    ;;
  *)
    echo "用法: jx-pkg update | jx-pkg install <包...> | jx-pkg pip <pip 参数...>"
    ;;
esac
`
