package main

// 市场发布·元信息与内容体检的单测（market_publish.go）：
//   · frontmatter 五键解析（name/displayName/description/version/tags）；
//   · SKILL.md「文件对上了但内容是空的 / 读不出」的拦截（0 字节、只有空白、
//     只有 frontmatter、压缩数据损坏、超 4MB——后两种直测 inspectSkillZip）；
//   · 中文技能名 → 英文 id 的词表兜底（词表没覆盖全就仍然要求手填）；
//   · publish-meta 端点（弹层预填的单一事实来源）。
//
// 假市场与请求装配复用 market_publish_test.go 里的 newFakePublishMarket /
// newPublishTestApp / installTestSkill / publishReq / zipBytesForTest。

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/* ---------- SKILL.md frontmatter：五个键 ---------- */

func TestSkillFrontmatterKeys(t *testing.T) {
	text := "---\n" +
		"name: wechat-chat-ops\n" +
		"displayName: 微信操作\n" +
		"description: 驱动真实微信 PC 端\n" +
		"version: 1.3.1\n" +
		"tags: 办公, 自动化，wechat\n" +
		"---\n\n正文\n"
	fm := skillFrontmatter(text)
	if fm.Name != "wechat-chat-ops" || fm.DisplayName != "微信操作" ||
		fm.Description != "驱动真实微信 PC 端" || fm.Version != "1.3.1" {
		t.Fatalf("frontmatter 解析不对: %#v", fm)
	}
	if strings.Join(fm.Tags, "|") != "办公|自动化|wechat" {
		t.Fatalf("tags 应支持中文逗号分隔，实得 %v", fm.Tags)
	}
	// [] 形式 + 引号
	fm2 := skillFrontmatter("---\nversion: \"2.0.0\"\ntags: [a, \"b\"]\n---\n正文\n")
	if fm2.Version != "2.0.0" || strings.Join(fm2.Tags, "|") != "a|b" {
		t.Fatalf("[] 形式 tags 解析不对: ver=%q tags=%v", fm2.Version, fm2.Tags)
	}
	// 没有 frontmatter / 没闭合：都不该 panic，返回空
	if fm3 := skillFrontmatter("# 标题\n正文\n"); fm3.Name != "" || fm3.Description != "" || fm3.Tags != nil {
		t.Fatalf("无 frontmatter 应返回空: %#v", fm3)
	}
	if fm4 := skillFrontmatter("---\nname: x\n没闭合\n"); fm4.Name != "" {
		t.Fatalf("没闭合的 frontmatter 不该被认下来: %q", fm4.Name)
	}
}

/* ---------- 中文名 → 英文 id（词表兜底） ---------- */

func TestSkillIDFromChinese(t *testing.T) {
	cases := map[string]string{
		"微信操作_技能包_v1.3.1_20261009.zip": "wechat-ops", // 版本/时间戳/噪音词都去掉
		"微信操作_技能包":                     "wechat-ops",
		"设备点检":                         "device-checkup",
		"图表生成工具":                       "charts-gen",
		"企业微信通知":                       "wecom-notify", // 4 字词优先于 2 字词
		"数据导出脚本":                       "data-export-script",
		"PDF工具":                        "pdf", // 中英混写：英文段原样保留
		"阿米巴经营":                        "",    // 词表没覆盖全 → 不猜
		"微信操作x":                        "wechat-ops-x",
	}
	for in, want := range cases {
		if got := skillIDFromChinese(in); got != want {
			t.Errorf("skillIDFromChinese(%q) = %q，期待 %q", in, got, want)
		}
	}
}

/* ---------- 造包小工具（压缩方式可控 / 数据区可打坏） ---------- */

func zipOfMethod(t *testing.T, names []string, data [][]byte, method uint16) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i, n := range names {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: n, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if data[i] != nil {
			if _, err := w.Write(data[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// corruptFirstEntry 把第一个条目的数据区打脏（结构仍是合法 zip，解压会失败）。
func corruptFirstEntry(t *testing.T, data []byte) []byte {
	t.Helper()
	out := append([]byte(nil), data...)
	off := bytes.Index(out, []byte("PK\x03\x04"))
	if off < 0 {
		t.Fatal("找不到本地头")
	}
	nlen := int(binary.LittleEndian.Uint16(out[off+26:]))
	elen := int(binary.LittleEndian.Uint16(out[off+28:]))
	start := off + 30 + nlen + elen
	if start+4 > len(out) {
		t.Fatal("数据区太短")
	}
	for i := start; i < start+4; i++ {
		out[i] ^= 0xFF
	}
	return out
}

/* ---------- 内容体检：空的 SKILL.md 在安装口与发布口都拦下 ---------- */

func TestPublishRejectsEmptyOrWhitespaceSkillMD(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	cases := []struct {
		name string
		md   string
	}{
		{"0 字节", ""},
		{"只有空白", "  \n\n\t\n"},
		{"空 frontmatter", "---\n---\n"},
		{"只有 frontmatter 没有正文", "---\nname: x\ndescription: d\n---\n"},
	}
	for _, c := range cases {
		installTestSkill(t, a, "probe-"+c.name, map[string]string{"SKILL.md": c.md, "scripts/run.py": "print(1)\n"})
		code, out := publishReq(t, a, map[string]any{"skillId": "probe-" + c.name})
		if code != 400 {
			t.Fatalf("[%s] 应 400，实得 %d：%v", c.name, code, out)
		}
		if msg := strings.TrimSpace(out["error"].(string)); !strings.Contains(msg, "SKILL.md") {
			t.Fatalf("[%s] 报错要点名 SKILL.md，实得 %q", c.name, msg)
		}
	}
	if fm.hits != 0 {
		t.Fatalf("内容体检没过就不该打扰市场服务器，实得 hits=%d", fm.hits)
	}
}

// 压缩数据损坏 / 超 4MB 只可能出现在「别人发的包」里（本机安装目录打成的新 zip
// 不会坏）——直测 inspectSkillZip 这条防御路径。
func TestInspectSkillZipRejectsCorruptAndHuge(t *testing.T) {
	if _, err := inspectSkillZip(corruptFirstEntry(t,
		zipOfMethod(t, []string{"SKILL.md"}, [][]byte{[]byte(okSkillMD)}, zip.Deflate))); err == nil ||
		!strings.Contains(err.Error(), "读不出来") {
		t.Fatalf("压缩数据损坏应拒收，实得 %v", err)
	}
	if _, err := inspectSkillZip(zipOfMethod(t, []string{"SKILL.md"},
		[][]byte{bytes.Repeat([]byte("A"), 5<<20)}, zip.Deflate)); err == nil {
		t.Fatal("超 4MB 的 SKILL.md 应拒收")
	}
}

// 正常包不被内容体检误伤：SKILL.md 有 frontmatter 也有正文。
func TestPublishKeepsGoodSkillMDPassing(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	installTestSkill(t, a, "demo", map[string]string{
		"SKILL.md":       "---\nname: demo\n---\n用这个技能做演示。\n",
		"scripts/run.py": "print(1)\n",
		"assets/图标.png":  "not-a-real-png",
		"docs/使用说明.md":   "# 说明\n",
	})
	if code, out := publishReq(t, a, map[string]any{"skillId": "demo"}); code != 200 {
		t.Fatalf("正常包应放行，实得 %d：%v", code, out)
	}
}

/* ---------- 中文名：id 用词表兜底，不再必须手填 ---------- */

func TestPublishChineseNameDerivesEnglishID(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	installTestSkill(t, a, "微信操作", map[string]string{
		"SKILL.md": "---\nname: 微信操作\ndescription: 驱动真实微信 PC 端\n---\n\n正文\n",
	})
	// 不给 id（全部靠推导）：中文名走词表兜底
	code, out := publishReq(t, a, map[string]any{"skillId": "微信操作"})
	if code != 200 {
		t.Fatalf("期待 200，收到 %d：%v", code, out)
	}
	if out["id"] != "wechat-ops" {
		t.Fatalf("中文名应推导出 wechat-ops，实得 %v", out["id"])
	}

	// 词表覆盖不到的中文名：仍然 400 要求手填（不猜）
	installTestSkill(t, a, "阿米巴经营", map[string]string{"SKILL.md": "---\nname: 阿米巴经营\n---\n正文\n"})
	if code, out = publishReq(t, a, map[string]any{"skillId": "阿米巴经营"}); code != 400 ||
		!strings.Contains(out["error"].(string), "推不出来") {
		t.Fatalf("词表没覆盖的中文名应 400 要求手填，实得 %d：%v", code, out)
	}
}

/* ---------- publish-meta 端点：弹层预填的单一事实来源 ---------- */

func TestPublishMetaEndpoint(t *testing.T) {
	a := newPublishTestApp(t, "http://unused/market/manifest.json")
	installTestSkill(t, a, "微信操作", map[string]string{
		"SKILL.md": "---\nname: 微信操作\ndisplayName: 微信操作\ndescription: 驱动微信\nversion: 3.1.0\ntags: 办公\n---\n\n正文\n",
	})
	req := httptest.NewRequest(http.MethodGet, "/api/market/publish-meta?skill=微信操作", nil)
	rec := httptest.NewRecorder()
	a.handleMarketPublishMeta(rec, req)
	if rec.Code != 200 {
		t.Fatalf("期待 200，收到 %d：%s", rec.Code, rec.Body.String())
	}
	out := publishMeta{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "wechat-ops" || !out.IDSuggested {
		t.Fatalf("中文名应经词表建议出 wechat-ops（可改标注），实得 %#v", out)
	}
	if out.Name != "微信操作" || out.Version != "3.1.0" || out.Description != "驱动微信" {
		t.Fatalf("元信息推导不对: %#v", out)
	}
	if strings.Join(out.Tags, "|") != "办公" {
		t.Fatalf("tags 应来自 SKILL.md: %v", out.Tags)
	}
	// 没装的技能：400 指路设置页
	req2 := httptest.NewRequest(http.MethodGet, "/api/market/publish-meta?skill=没有装", nil)
	rec2 := httptest.NewRecorder()
	a.handleMarketPublishMeta(rec2, req2)
	if rec2.Code != 400 || !strings.Contains(rec2.Body.String(), "设置 · 技能") {
		t.Fatalf("未安装技能应 400 指路，实得 %d：%s", rec2.Code, rec2.Body.String())
	}
}
