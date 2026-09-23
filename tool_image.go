package main

import (
	"encoding/json"
	"fmt"
)

// read_image：读取工作区里的图片文件，入库为附件并返回文本信封。
// 前端在工具卡片里直接渲染原图；支持视觉的模型在请求组装时由
// inlineImagesForRequest 自动内联看到图片内容（见 attachment.go）。
// DSH 的 read_image 与 ZCode 的 Read 工具都是这个定位，本实现为本栈新写。

func readImageTool() Tool {
	return Tool{
		Spec: toolSpec("read_image",
			"查看一张图片（png / jpg / gif / webp）。图片会展示给用户；若当前模型支持视觉，你将直接看到图片内容。path 支持相对工作区的路径。",
			objSchema(map[string]any{
				"path": strProp("图片文件路径（相对或绝对）"),
			}, "path")),
		Run: runReadImage,
	}
}

func runReadImage(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	// 只读动作：经权限引擎统一做受保护路径检查后放行。
	if _, err := t.authorize(permAction{Op: "read", Tool: "read_image", Path: path}); err != nil {
		return "", err
	}
	raw, err := t.envReadFile(path)
	if err != nil {
		return "", err
	}
	mediaType := sniffImageMediaType(raw)
	if mediaType == "" {
		return "", fmt.Errorf("%s 不是受支持的图片格式（支持 png / jpg / gif / webp）", path)
	}
	att, err := t.app.storeAttachment(raw, mediaType)
	if err != nil {
		return "", err
	}
	note := "图片已就绪：用户可在界面上直接查看"
	if t.app.resolveTarget(t.sess).SupportsImages {
		note = "图片已就绪：你将在下一条消息中直接看到它的内容"
	} else {
		note = "图片已就绪：用户可在界面上直接查看（当前模型不支持视觉输入，你只能看到这份元信息；可结合 read_file / grep 读取同目录文本，或请用户描述图片内容）"
	}
	return fmt.Sprintf("%s\n%s\n来源文件：%s\n格式：%s，尺寸 %dx%d，%d 字节\n如需查看图片里的文字，可配合 OCR 能力（若已安装）处理。",
		imageEnvelope(att, path), note, path, att.MediaType, att.Width, att.Height, att.Bytes), nil
}
