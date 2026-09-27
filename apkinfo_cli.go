package main

// apkinfo 子命令：读取 APK 的包名、版本、SDK 档位、权限、ABI 与签名
// 证书指纹。动机：安卓命令行环境没有 aapt2/apksigner/keytool（那是几百
// MB 的 Java 工具链），而智能体在手机上验包（安装前确认包名与签名是谁）
// 是刚需——用纯 Go 库在引擎侧补齐，桌面与安卓同一用法。
//
// 用法：jx-assistant apkinfo <file.apk> [--json]
// 退出码：0 成功；1 失败；2 用法错误（对齐 sandbox 子命令约定）。

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shogo82148/androidbinary/apk"
	"github.com/smallstep/pkcs7"
)

// apkInfo 是 apkinfo 的输出载荷（人读格式由它逐行渲染）。
type apkInfo struct {
	Path        string   `json:"path"`
	SizeBytes   int64    `json:"sizeBytes"`
	Package     string   `json:"package"`
	AppName     string   `json:"appName,omitempty"`
	VersionName string   `json:"versionName,omitempty"`
	VersionCode int32    `json:"versionCode,omitempty"`
	MinSDK      int32    `json:"minSdk,omitempty"`
	TargetSDK   int32    `json:"targetSdk,omitempty"`
	ABI         []string `json:"abi,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	CertSHA256  string   `json:"certSha256,omitempty"`
	CertSubject string   `json:"certSubject,omitempty"`
}

func runApkInfoCLI(args []string) int {
	jsonOut := false
	var path string
	for _, a := range args {
		switch {
		case a == "--json":
			jsonOut = true
		case a == "-h" || a == "--help":
			fmt.Fprintln(os.Stderr, "用法: jx-assistant apkinfo <file.apk> [--json]")
			return 2
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "未知选项: %s\n用法: jx-assistant apkinfo <file.apk> [--json]\n", a)
			return 2
		default:
			if path != "" {
				fmt.Fprintln(os.Stderr, "只能指定一个 APK 文件")
				return 2
			}
			path = a
		}
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "用法: jx-assistant apkinfo <file.apk> [--json]")
		return 2
	}
	info, err := readApkInfo(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apkinfo: %v\n", err)
		return 1
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(info); err != nil {
			fmt.Fprintf(os.Stderr, "apkinfo: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Printf("文件: %s（%d 字节）\n", info.Path, info.SizeBytes)
	fmt.Printf("包名: %s\n", info.Package)
	if info.AppName != "" {
		fmt.Printf("应用名: %s\n", info.AppName)
	}
	fmt.Printf("版本: %s (%d)\n", info.VersionName, info.VersionCode)
	fmt.Printf("SDK: min=%d target=%d\n", info.MinSDK, info.TargetSDK)
	if len(info.ABI) > 0 {
		fmt.Printf("ABI: %s\n", strings.Join(info.ABI, ", "))
	}
	if len(info.Permissions) > 0 {
		fmt.Printf("权限（%d 项）:\n", len(info.Permissions))
		for _, p := range info.Permissions {
			fmt.Printf("  - %s\n", p)
		}
	}
	if info.CertSHA256 != "" {
		fmt.Printf("签名证书 SHA-256: %s\n", info.CertSHA256)
		fmt.Printf("签名证书主体: %s\n", info.CertSubject)
	}
	return 0
}

// readApkInfo 解析 APK：manifest 走 androidbinary 库（二进制 XML 直读），
// ABI 从 lib/<abi>/ 目录名收集，签名证书取 META-INF/*.RSA|DSA|EC 里
// PKCS#7 的第一张证书。
func readApkInfo(path string) (*apkInfo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	a, err := apk.OpenFile(abs)
	if err != nil {
		return nil, fmt.Errorf("解析失败: %w", err)
	}
	defer a.Close()
	m := a.Manifest()

	info := &apkInfo{
		Path:        abs,
		SizeBytes:   st.Size(),
		Package:     m.Package.MustString(),
		VersionName: m.VersionName.MustString(),
		VersionCode: m.VersionCode.MustInt32(),
		MinSDK:      m.SDK.Min.MustInt32(),
		TargetSDK:   m.SDK.Target.MustInt32(),
	}
	if name, err := a.Label(nil); err == nil {
		info.AppName = name
	}
	for _, p := range m.UsesPermissions {
		if n := p.Name.MustString(); n != "" {
			info.Permissions = append(info.Permissions, n)
		}
	}
	sort.Strings(info.Permissions)

	// ABI 与签名证书都要按 zip 条目扫一遍
	abiSet := map[string]bool{}
	certData, certErr := scanZipForCertAndABI(abs, abiSet)
	for abi := range abiSet {
		info.ABI = append(info.ABI, abi)
	}
	sort.Strings(info.ABI)
	if certErr == nil && len(certData) > 0 {
		if p7, perr := pkcs7.Parse(certData); perr == nil && len(p7.Certificates) > 0 {
			cert := p7.Certificates[0]
			sum := sha256.Sum256(cert.Raw)
			info.CertSHA256 = hex.EncodeToString(sum[:])
			info.CertSubject = cert.Subject.String()
		}
	}
	return info, nil
}

// scanZipForCertAndABI 扫 zip：lib/<abi>/ 目录名收进 abiSet，返回第一个
// META-INF 签名块（*.RSA/*.DSA/*.EC）的原始 PKCS#7 数据。
func scanZipForCertAndABI(path string, abiSet map[string]bool) ([]byte, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var cert []byte
	for _, f := range zr.File {
		name := f.Name
		if strings.HasPrefix(name, "lib/") {
			rest := strings.TrimPrefix(name, "lib/")
			if i := strings.Index(rest, "/"); i > 0 {
				abiSet[rest[:i]] = true
			}
			continue
		}
		if cert == nil && strings.HasPrefix(name, "META-INF/") {
			ext := strings.ToLower(filepath.Ext(name))
			if ext == ".rsa" || ext == ".dsa" || ext == ".ec" {
				rc, oerr := f.Open()
				if oerr != nil {
					continue
				}
				buf, rerr := io.ReadAll(rc)
				rc.Close()
				if rerr == nil && len(buf) > 0 {
					cert = buf
				}
			}
		}
	}
	return cert, nil
}
