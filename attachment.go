package main

// 附件库：read_image 等工具产出的图片按内容寻址（SHA-256）落盘到
// data/attachments/，工具结果里只保留一段文本信封（引用附件相对路径）。
// 前端经 GET /attachments/<路径> 直接查看原图；发给模型时按当前模型的
// 视觉能力处理——支持图片的模型在请求组装时按预算内联为 base64 内容块，
// 纯文本模型则保留信封文本（模型仍知道有一张图、存放在哪）。
// 结构化输出与预算压缩的思路借鉴 ZCode 开源版（Apache-2.0）的 read-image
// 实现：最大边长 / 字节预算 / token≈base64 字符数×0.125 三重约束。

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	// 注册 PNG/GIF 解码器（宽高解析用）；JPEG 同时用于超预算转码
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// attachmentMaxInputBytes 原始图片文件的字节上限。
	attachmentMaxInputBytes = 10 << 20
	// attachmentMaxInlineBytes 单张图内联给模型的原始字节预算（base64 后约 40 万字符），
	// 超限自动转 JPEG 重压；webp 无标准库解码器，保留原样。
	attachmentMaxInlineBytes = 300 << 10
	// attachmentJPEGQuality 超预算转 JPEG 的质量档。
	attachmentJPEGQuality = 85
	// attachmentMaxPerRequest 单次模型请求最多内联的图片张数，
	// 超出的只保留信封占位（DSH image-offload 同思路：卸载最老的）。
	attachmentMaxPerRequest = 8
)

// Attachment 是一张已入库图片的描述。
type Attachment struct {
	File      string // 相对 data/attachments/ 的存放路径（含扩展名）
	MediaType string
	Width     int
	Height    int
	Bytes     int
	SHA256    string
}

// imageEnvelopeRe 从工具结果的文本里提取图片信封；捕获组是附件相对路径。
var imageEnvelopeRe = regexp.MustCompile(`<image attachment="([^"]+)"[^>]*/>`)

func (a *App) attachmentsDir() string {
	return filepath.Join(a.Cfg.DataDir, "attachments")
}

// sniffImageMediaType 按魔数识别图片类型；空串表示不是受支持的图片。
func sniffImageMediaType(b []byte) string {
	switch {
	case len(b) >= 8 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G':
		return "image/png"
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	case len(b) >= 6 && b[0] == 'G' && b[1] == 'I' && b[2] == 'F':
		return "image/gif"
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

func mediaTypeExt(mt string) string {
	switch mt {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

// imageDims 解出图片宽高；标准库不认识的格式（如 webp）返回 0,0。
func imageDims(b []byte) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

func base64Len(rawLen int) int { return (rawLen + 2) / 3 * 4 }

// toJPEG 把 PNG/GIF 等解码后转成 JPEG（有损压缩换预算）；解不动就报错。
func toJPEG(raw []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: attachmentJPEGQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// storeAttachment 把图片字节入库：超预算自动转 JPEG 重压，内容寻址落盘，
// 同内容天然去重。成功后返回附件描述。
func (a *App) storeAttachment(raw []byte, mediaType string) (*Attachment, error) {
	if mediaType == "" {
		return nil, fmt.Errorf("不是受支持的图片格式（支持 png / jpg / gif / webp）")
	}
	if len(raw) > attachmentMaxInputBytes {
		return nil, fmt.Errorf("图片过大（%d 字节，上限 %d 字节）", len(raw), attachmentMaxInputBytes)
	}
	att := &Attachment{
		MediaType: mediaType,
		Width:     0,
		Height:    0,
		Bytes:     len(raw),
	}
	att.Width, att.Height = imageDims(raw)
	data := raw
	if base64Len(len(data)) > attachmentMaxInlineBytes && mediaType != "image/webp" {
		if conv, err := toJPEG(data); err == nil && base64Len(len(conv)) < base64Len(len(data)) {
			data = conv
			att.MediaType = "image/jpeg"
			att.Bytes = len(data)
			att.Width, att.Height = imageDims(data)
		}
	}
	sum := sha256.Sum256(data)
	att.SHA256 = hex.EncodeToString(sum[:])
	att.File = filepath.ToSlash(filepath.Join(att.SHA256[:2], att.SHA256+mediaTypeExt(att.MediaType)))

	full := filepath.Join(a.attachmentsDir(), filepath.FromSlash(att.File))
	if _, err := os.Stat(full); err == nil {
		return att, nil // 同内容已入库，直接复用
	}
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(full, data, 0644); err != nil {
		return nil, err
	}
	return att, nil
}

// loadAttachment 读回附件字节（带路径防穿越校验），用于内联与 HTTP 端点。
func (a *App) loadAttachment(rel string) ([]byte, string, error) {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil, "", fmt.Errorf("非法附件路径")
	}
	full := filepath.Join(a.attachmentsDir(), filepath.FromSlash(rel))
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, "", err
	}
	mt := sniffImageMediaType(data)
	if mt == "" {
		mt = "application/octet-stream"
	}
	return data, mt, nil
}

// imageEnvelope 生成写进工具结果文本的图片信封；path 为源文件（供模型与用户对照）。
func imageEnvelope(att *Attachment, srcPath string) string {
	src := strings.ReplaceAll(filepath.ToSlash(srcPath), `"`, `'`)
	w, h := att.Width, att.Height
	if w <= 0 {
		w = 0
	}
	if h <= 0 {
		h = 0
	}
	return fmt.Sprintf(`<image attachment="%s" mediaType="%s" width="%d" height="%d" bytes="%d" path="%s" />`,
		att.File, att.MediaType, w, h, att.Bytes, src)
}

// mcpImagesEnvelope 把 MCP 工具结果携带的图片入库为附件并生成注入文本段
// （信封 + 说明行；无图片时返回空串）。MCP 服务器（如 Windows-MCP 的
// Screenshot/Snapshot）以 image content 回传截图，入库后与 read_image 走
// 同一条注入链路：inlineImagesForRequest 在请求组装时把信封内联为视觉块，
// 前端在工具卡片渲染原图。信封段应拼在工具文本结果**之前**——规则压缩只
// 保留工具结果的开头片段，放尾部会被长文本截掉。入库失败降级为一行说明，
// 不吞掉工具本身的结果。
func mcpImagesEnvelope(app *App, images []mcpImage) string {
	if len(images) == 0 {
		return ""
	}
	var b strings.Builder
	ok := 0
	for _, img := range images {
		att, err := app.storeAttachment(img.Data, img.MIMEType)
		if err != nil {
			continue
		}
		b.WriteString(imageEnvelope(att, "MCP 工具返回"))
		b.WriteString("\n")
		ok++
	}
	if ok == 0 {
		return fmt.Sprintf("（工具随结果返回了 %d 张图片，均入库失败：格式不受支持或超出大小上限）\n", len(images))
	}
	fmt.Fprintf(&b, "（以上 %d 张图片来自 MCP 工具返回：支持视觉的模型可直接看到内容，用户可在工具卡片里查看原图）", ok)
	return b.String()
}

// inlineImagesForRequest 是发给模型前的最后一步变换：当前模型支持视觉时，
// 把历史里工具结果携带的图片信封内联为 user 角色的 base64 内容块
// （紧跟该步全部工具结果之后；部分兼容端不接受 tool 角色直接携带图片）。
// 纯文本模型原样返回——信封文本本身就是对图片的描述。超过单次张数上限的
// 图片留在信封里不内联（最老的先被卸载）。
func inlineImagesForRequest(app *App, msgs []Message, target LLMTarget) []Message {
	if app == nil || !target.SupportsImages {
		return msgs
	}
	out := make([]Message, 0, len(msgs)+2)
	var pending []ContentPart
	flush := func() {
		if len(pending) > 0 {
			out = append(out, Message{Role: "user", Content: pending})
			pending = nil
		}
	}
	inlined := 0
	for _, m := range msgs {
		if m.Role != "tool" {
			flush()
			out = append(out, m)
			continue
		}
		out = append(out, m)
		s, ok := m.Content.(string)
		if !ok {
			continue
		}
		for _, match := range imageEnvelopeRe.FindAllStringSubmatch(s, -1) {
			if inlined >= attachmentMaxPerRequest {
				break // 张数上限已满，本条及后续信封保持占位
			}
			data, mt, err := app.loadAttachment(match[1])
			if err != nil {
				continue
			}
			if len(pending) == 0 {
				pending = append(pending, ContentPart{Type: "text", Text: "（上面工具结果里引用的图片，供你查看）"})
			}
			pending = append(pending, ContentPart{
				Type:     "image_url",
				ImageURL: &ImageURL{URL: "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(data)},
			})
			inlined++
		}
	}
	flush()
	return out
}
