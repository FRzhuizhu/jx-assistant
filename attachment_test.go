package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("生成测试 PNG 失败: %v", err)
	}
	return buf.Bytes()
}

// attachmentTestApp 建一个数据目录与工作区分离的 App，供附件相关用例使用。
func attachmentTestApp(t *testing.T) *App {
	t.Helper()
	dataDir := t.TempDir()
	ws := t.TempDir()
	store, err := NewStore(dataDir)
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return &App{Store: store, Cfg: &Config{DataDir: dataDir, Workspace: ws}}
}

func TestSniffImageMediaType(t *testing.T) {
	pngBytes := makeTestPNG(t, 4, 4)
	if got := sniffImageMediaType(pngBytes); got != "image/png" {
		t.Fatalf("PNG 嗅探错误: %s", got)
	}
	if got := sniffImageMediaType([]byte("GIF89a....")); got != "image/gif" {
		t.Fatalf("GIF 嗅探错误: %s", got)
	}
	if got := sniffImageMediaType([]byte("hello world, not an image")); got != "" {
		t.Fatalf("非图片应返回空串: %s", got)
	}
}

func TestStoreAttachmentDedupAndEnvelope(t *testing.T) {
	a := attachmentTestApp(t)
	raw := makeTestPNG(t, 32, 16)
	att, err := a.storeAttachment(raw, "image/png")
	if err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	if att.MediaType != "image/png" || att.Width != 32 || att.Height != 16 || att.Bytes != len(raw) {
		t.Fatalf("附件描述不符: %+v", att)
	}
	full := filepath.Join(a.attachmentsDir(), filepath.FromSlash(att.File))
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("附件文件未落盘: %v", err)
	}
	// 同内容再次入库应复用同一文件（内容寻址去重）
	att2, err := a.storeAttachment(raw, "image/png")
	if err != nil {
		t.Fatalf("二次入库失败: %v", err)
	}
	if att2.File != att.File {
		t.Fatalf("同内容应去重: %s vs %s", att.File, att2.File)
	}
	// 信封可被正则提取回附件路径
	m := imageEnvelopeRe.FindStringSubmatch(imageEnvelope(att, `D:\x\测试.png`))
	if m == nil || m[1] != att.File {
		t.Fatalf("信封解析失败: %v", m)
	}
	// 防穿越
	if _, _, err := a.loadAttachment("../../config.json"); err == nil {
		t.Fatal("穿越路径应被拒绝")
	}
}

func TestReadImageTool(t *testing.T) {
	a := attachmentTestApp(t)
	ws := a.Cfg.Workspace
	if err := os.WriteFile(filepath.Join(ws, "截图.png"), makeTestPNG(t, 64, 48), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "文本.txt"), []byte("plain"), 0644); err != nil {
		t.Fatal(err)
	}
	tc := &toolContext{app: a, sess: &SessionMeta{ID: "s1", Mode: "task", Workspace: ws}}

	out, err := runReadImage(tc, mustJSON(t, map[string]string{"path": "截图.png"}))
	if err != nil {
		t.Fatalf("read_image 失败: %v", err)
	}
	if !strings.Contains(out, `<image attachment="`) || !strings.Contains(out, "64x48") {
		t.Fatalf("输出缺信封或尺寸: %s", out)
	}
	// 附件确实落盘
	m := imageEnvelopeRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatal("未解析到信封")
	}
	if _, _, err := a.loadAttachment(m[1]); err != nil {
		t.Fatalf("附件读回失败: %v", err)
	}
	// 非图片路径应报错
	if _, err := runReadImage(tc, mustJSON(t, map[string]string{"path": "文本.txt"})); err == nil {
		t.Fatal("非图片应报错")
	}
	// 不存在的文件应报错
	if _, err := runReadImage(tc, mustJSON(t, map[string]string{"path": "没有.png"})); err == nil {
		t.Fatal("不存在的文件应报错")
	}
}

func TestInlineImagesForRequest(t *testing.T) {
	a := attachmentTestApp(t)
	raw := makeTestPNG(t, 8, 8)
	att, err := a.storeAttachment(raw, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	env := imageEnvelope(att, "ws/截图.png")

	vision := LLMTarget{SupportsImages: true}
	textOnly := LLMTarget{SupportsImages: false}

	history := []Message{
		TextMessage("user", "看图"),
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: ToolCallFunc{Name: "read_image", Arguments: "{}"}}}},
		{Role: "tool", Content: env, ToolCallID: "c1"},
	}

	// 纯文本模型：原样透传，不注入任何消息
	got := inlineImagesForRequest(a, history, textOnly)
	if len(got) != len(history) {
		t.Fatalf("文本模型不应注入消息: %d -> %d", len(history), len(got))
	}

	// 视觉模型：在工具结果之后追加一条 user 消息，携带 base64 图片块
	got = inlineImagesForRequest(a, history, vision)
	if len(got) != len(history)+1 {
		t.Fatalf("视觉模型应追加 1 条图片消息，得到 %d", len(got))
	}
	vm, ok := got[3].Content.([]ContentPart)
	if !ok || got[3].Role != "user" {
		t.Fatalf("追加的消息应为 user 多模态消息: %+v", got[3])
	}
	hasImage := false
	for _, p := range vm {
		if p.Type == "image_url" && p.ImageURL != nil && strings.HasPrefix(p.ImageURL.URL, "data:image/png;base64,") {
			hasImage = true
		}
	}
	if !hasImage {
		t.Fatal("未找到 base64 图片块")
	}

	// 张数上限：第 9 张起不再内联
	many := []Message{}
	for i := 0; i < 10; i++ {
		many = append(many, Message{Role: "tool", Content: env, ToolCallID: "c"})
	}
	got = inlineImagesForRequest(a, many, vision)
	count := 0
	for _, m := range got {
		if parts, ok := m.Content.([]ContentPart); ok {
			for _, p := range parts {
				if p.Type == "image_url" {
					count++
				}
			}
		}
	}
	if count != attachmentMaxPerRequest {
		t.Fatalf("应受 %d 张上限约束，实际内联 %d", attachmentMaxPerRequest, count)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
