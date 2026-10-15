// websites.go —— 网页托管平台的客户端半边：publish_webpage 工具。
//
// 把本地一个网页文件（.html/.htm）或一个整站文件夹发布到服务端
// （jxai-server）的网页托管平台，服务端分配 /s/<id>/ 地址对外服务
// （HTTP 与 HTTPS 同口，公网与否取决于路由器转发）。同名（或同 id）重发布
// = 更新站点，已收集的数据文件不受影响。
//
// 与服务端的协议契约（服务端半边见 jxaiserver 仓库 sites.go 头注释，改动需
// 双侧同步）：
//
//	POST <市场地址>/api/sites/publish   multipart：file=（单个 .html 或 .zip）
//	                                  + name 表单字段；身份走 X-Jxai-User 头
//	                                  （与市场发布同一条链路，Go 客户端可带中文名）
//
// 依托既有设施：市场地址（config.marketURL，设置·网络访问）、发布者姓名
// （PublisherName）、marketRequest 身份头、zipSkillDir 目录打包。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
)

// publishWebpageTool 注册 publish_webpage（任务/创建技能模式）。
func publishWebpageTool() Tool {
	return Tool{
		Spec: toolSpec("publish_webpage",
			"把本地一个网页发布到服务端的网页托管平台，拿到一个可直接访问的网址（局域网/公网均可访问，取决于服务器部署）。"+
				"入参 path 指向一个 .html/.htm 文件（它就是首页）或一个以 index.html 为首页的文件夹（整站：css/js/图片等子目录一并上传）。"+
				"站点名 name 缺省取文件/文件夹名；同名重发布=更新站点内容，访问者收集的数据文件（表单提交的 CSV 等）会保留。"+
				"需要在设置·网络访问里配置服务端地址（与市场同地址）并填写发布者姓名。"+
				"托管页面可用平台提供的同源数据 API（api/append、api/file 等）存取表单数据，详见平台页 /sites 的说明。"+
				"调用后把返回的网址告诉用户即可。",
			objSchema(map[string]any{
				"path": strProp("待发布的网页文件（.html/.htm）或整站文件夹的路径（须在工作区内，相对或绝对）"),
				"name": strProp("站点名（可选，缺省取文件/文件夹名；同名即同一站点，重发布为更新）"),
			}, "path")),
		Run: runPublishWebpage,
	}
}

func runPublishWebpage(t *toolContext, args json.RawMessage) (string, error) {
	var p struct{ Path, Name string }
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Path) == "" {
		return "", fmt.Errorf("path 不能为空")
	}
	if t.isRemote() {
		return "", fmt.Errorf("网页发布仅支持本地会话：远程工作区的文件不在本机，无法打包上传")
	}
	abs, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	if !t.inWorkspace(abs) {
		return "", fmt.Errorf("待发布网页必须位于工作区内：%s", filepath.ToSlash(abs))
	}
	isDir, _, err := t.envStat(abs)
	if err != nil {
		return "", fmt.Errorf("路径不存在：%s", filepath.ToSlash(abs))
	}
	ext := strings.ToLower(filepath.Ext(abs))
	if !isDir && ext != ".html" && ext != ".htm" {
		return "", fmt.Errorf("单个文件只支持 .html/.htm；整站请给文件夹路径（内含 index.html）：%s", filepath.ToSlash(abs))
	}
	if _, err := t.authorize(permAction{Op: "read", Tool: "publish_webpage", Path: abs}); err != nil {
		return "", err
	}

	base, err := marketOrigin(t.app.Cfg.MarketURL)
	if err != nil {
		return "", fmt.Errorf("服务端地址未配置或不可用（设置·网络访问里的市场地址即服务端地址）: %w", err)
	}
	if t.app.marketUser() == "" {
		return "", fmt.Errorf("发布者姓名未设置：设置·网络访问里填写发布者姓名后重试")
	}

	// 组 multipart：文件夹整站打包成 zip；单 html 直接传（服务端落成 index.html）
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	name := strings.TrimSpace(p.Name)
	if isDir {
		if name == "" {
			name = filepath.Base(abs)
		}
		zipData, err := zipSkillDir(abs)
		if err != nil {
			return "", fmt.Errorf("打包站点失败: %w", err)
		}
		w, err := mw.CreateFormFile("file", "site.zip")
		if err != nil {
			return "", err
		}
		if _, err := w.Write(zipData); err != nil {
			return "", err
		}
	} else {
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
		}
		data, err := t.envReadFile(abs)
		if err != nil {
			return "", fmt.Errorf("读取网页失败: %w", err)
		}
		w, err := mw.CreateFormFile("file", filepath.Base(abs))
		if err != nil {
			return "", err
		}
		if _, err := w.Write(data); err != nil {
			return "", err
		}
	}
	_ = mw.WriteField("name", name)
	mw.Close()

	req, err := t.app.marketRequest(t.ctx, http.MethodPost, base+"/api/sites/publish", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := marketHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("连接服务端失败（%s）: %w", base, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error string }
		_ = json.Unmarshal(body, &e)
		if e.Error != "" {
			return "", fmt.Errorf("发布失败: %s", e.Error)
		}
		return "", fmt.Errorf("发布失败: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Entry struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Files int    `json:"files"`
			Size  int64  `json:"size"`
		} `json:"entry"`
		Path string `json:"path"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.URL == "" {
		return "", fmt.Errorf("服务端返回不识别的内容: %.200s", string(body))
	}
	kind := "整站文件夹"
	if !isDir {
		kind = "单页"
	}
	return fmt.Sprintf("已发布到网页托管平台：%s（%s，%d 个文件，%s）\n访问地址：%s\n站点路径：%s\n同名重发布即为更新；数据文件（表单收集等）保留在服务端，可在平台页管理：%s/sites",
		out.Entry.Name, kind, out.Entry.Files, fmtSizeClient(out.Entry.Size), out.URL, out.Path, base), nil
}

// fmtSizeClient 字节数的人读形式（工具结果用）。
func fmtSizeClient(n int64) string {
	switch {
	case n >= 1073741824:
		return fmt.Sprintf("%.2f GB", float64(n)/1073741824)
	case n >= 1048576:
		return fmt.Sprintf("%.1f MB", float64(n)/1048576)
	case n >= 1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
