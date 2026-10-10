package main

// 市场发布·元信息与内容体检的单测（market_publish.go）：
//   · 五个字段的来源（frontmatter 的 name/description/version/tags → 补全顺序）；
//   · SKILL.md「文件对上了但内容是空的 / 读不出」的拦截（0 字节、只有空白、
//     只有 frontmatter、压缩数据损坏、超 4MB）；
//   · 中文技能名 → 英文 id 的词表兜底（词表没覆盖全就仍然要求手填）。
//
// 假市场与请求装配复用 market_publish_test.go 里的 newFakePublishMarket /
// newPublishTestApp / publishReq / zipBytesForTest。

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

/* ---------- SKILL.md frontmatter：四个键 ---------- */

func TestSkillFrontmatterKeys(t *testing.T) {
	text := "---\n" +
		"name: wechat-chat-ops\n" +
		"description: 驱动真实微信 PC 端\n" +
		"version: 1.3.1\n" +
		"tags: 办公, 自动化，wechat\n" +
		"---\n\n正文\n"
	name, desc, ver, tags := skillFrontmatter(text)
	if name != "wechat-chat-ops" || desc != "驱动真实微信 PC 端" || ver != "1.3.1" {
		t.Fatalf("frontmatter 解析不对: name=%q desc=%q ver=%q", name, desc, ver)
	}
	if strings.Join(tags, "|") != "办公|自动化|wechat" {
		t.Fatalf("tags 应支持中文逗号分隔，实得 %v", tags)
	}
	// [] 形式 + 引号
	_, _, ver2, tags2 := skillFrontmatter("---\nversion: \"2.0.0\"\ntags: [a, \"b\"]\n---\n正文\n")
	if ver2 != "2.0.0" || strings.Join(tags2, "|") != "a|b" {
		t.Fatalf("[] 形式 tags 解析不对: ver=%q tags=%v", ver2, tags2)
	}
	// 没有 frontmatter / 没闭合：都不该 panic，返回空
	if n, d, v, tg := skillFrontmatter("# 标题\n正文\n"); n != "" || d != "" || v != "" || tg != nil {
		t.Fatalf("无 frontmatter 应返回空: %q %q %q %v", n, d, v, tg)
	}
	if n, _, _, _ := skillFrontmatter("---\nname: x\n没闭合\n"); n != "" {
		t.Fatalf("没闭合的 frontmatter 不该被认下来: %q", n)
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

/* ---------- 内容体检：空的 / 读不出的 SKILL.md 一律拦下 ---------- */

func TestPublishRejectsEmptyOrUnreadableSkillMD(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")

	cases := []struct {
		name string
		pack []byte
	}{
		{"0 字节（deflate）", zipOfMethod(t, []string{"SKILL.md"}, [][]byte{nil}, zip.Deflate)},
		{"0 字节（store）", zipOfMethod(t, []string{"SKILL.md"}, [][]byte{nil}, zip.Store)},
		{"只有空白", zipOfMethod(t, []string{"SKILL.md"}, [][]byte{[]byte("  \n\n\t\n")}, zip.Deflate)},
		{"空 frontmatter", zipOfMethod(t, []string{"SKILL.md"}, [][]byte{[]byte("---\n---\n")}, zip.Deflate)},
		{"只有 frontmatter 没有正文", zipOfMethod(t, []string{"SKILL.md"},
			[][]byte{[]byte("---\nname: x\ndescription: d\n---\n")}, zip.Deflate)},
		{"压缩数据损坏", corruptFirstEntry(t, zipOfMethod(t, []string{"SKILL.md"}, [][]byte{[]byte(okSkillMD)}, zip.Deflate))},
		{"SKILL.md 超过 4MB", zipOfMethod(t, []string{"SKILL.md"}, [][]byte{bytes.Repeat([]byte("A"), 5<<20)}, zip.Deflate)},
	}
	for _, c := range cases {
		code, out := publishReq(t, a, map[string]any{"id": "probe-x", "zip": true},
			[]pubFile{{name: "微信操作_技能包_v1.3.1_20261009.zip", data: c.pack}})
		if code != 400 {
			t.Fatalf("[%s] 应 400，实得 %d：%v", c.name, code, out)
		}
		if msg := strings.TrimSpace(out["error"].(string)); !strings.Contains(msg, "SKILL.md") {
			t.Fatalf("[%s] 报错要点名 SKILL.md，实得 %q", c.name, msg)
		}
		t.Logf("[%s] → 400：%v", c.name, out["error"])
	}
	if fm.hits != 0 {
		t.Fatalf("内容体检没过就不该打扰市场服务器，实得 hits=%d", fm.hits)
	}

	// 文件夹投喂同理（前端剥好顶层目录后直接投文件）
	code, out := publishReq(t, a, map[string]any{"id": "probe-x", "version": "1.0.0"},
		[]pubFile{
			{name: "SKILL.md", data: nil},
			{name: "scripts/run.py", data: []byte("print(1)\n")},
		})
	if code != 400 || !strings.Contains(out["error"].(string), "空文件") {
		t.Fatalf("文件夹投喂空 SKILL.md 应 400，实得 %d：%v", code, out)
	}
	if fm.hits != 0 {
		t.Fatalf("文件夹投喂也不该打扰市场服务器，实得 hits=%d", fm.hits)
	}
}

// 正常包不被新校验误伤：SKILL.md 有 frontmatter 也有正文。
func TestPublishKeepsGoodSkillMDPassing(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	pack := zipBytesForTest(t, map[string]string{
		"SKILL.md":       "---\nname: demo\n---\n用这个技能做演示。\n",
		"scripts/run.py": "print(1)\n",
		"assets/图标.png":  "not-a-real-png",
		"docs/使用说明.md":   "# 说明\n",
	})
	if code, out := publishReq(t, a, map[string]any{"id": "demo", "zip": true},
		[]pubFile{{name: "demo-1.0.0.zip", data: pack}}); code != 200 {
		t.Fatalf("正常包应放行，实得 %d：%v", code, out)
	}
}

/* ---------- 中文名：id 用词表兜底，不再必须手填 ---------- */

func TestPublishChineseNameDerivesEnglishID(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	pack := zipBytesForTest(t, map[string]string{
		"微信操作/SKILL.md": "---\nname: 微信操作\ndescription: 驱动真实微信 PC 端\n---\n\n正文\n",
	})
	// meta 不给 id（模拟「全部靠包推导」的老行为）：中文名 + 中文 frontmatter
	code, out := publishReq(t, a, map[string]any{"zip": true},
		[]pubFile{{name: "微信操作_技能包_v1.3.1_20261009.zip", data: pack}})
	if code != 200 {
		t.Fatalf("期待 200，收到 %d：%v", code, out)
	}
	if out["id"] != "wechat-ops" {
		t.Fatalf("中文名应推导出 wechat-ops，实得 %v", out["id"])
	}
	if fm.fields["version"] != "1.3.1" {
		t.Fatalf("版本号仍应从包名解析: %#v", fm.fields)
	}

	// 词表覆盖不到的中文名：仍然 400 要求手填（不猜）
	pack2 := zipBytesForTest(t, map[string]string{
		"阿米巴经营/SKILL.md": "---\nname: 阿米巴经营\n---\n正文\n",
	})
	code, out = publishReq(t, a, map[string]any{"zip": true},
		[]pubFile{{name: "阿米巴经营_v1.0.0.zip", data: pack2}})
	if code != 400 || !strings.Contains(out["error"].(string), "推不出来") {
		t.Fatalf("词表没覆盖的中文名应 400 要求手填，实得 %d：%v", code, out)
	}
}

/* ---------- tags / version 也要能从 SKILL.md 补全（与前端预填同口径） ---------- */

func TestPublishTagsAndVersionFromSkillMD(t *testing.T) {
	fm := newFakePublishMarket(t)
	a := newPublishTestApp(t, fm.srv.URL+"/market/manifest.json")
	pack := zipBytesForTest(t, map[string]string{
		"SKILL.md": "---\nname: demo-skill\ndescription: 演示\ndescription2: x\nversion: 9.9.9\ntags: 办公, 自动化\n---\n演示正文\n",
	})
	// meta 只给 id：版本/标签/描述全靠 SKILL.md
	code, out := publishReq(t, a, map[string]any{"id": "demo-skill", "zip": true},
		[]pubFile{{name: "技能包.zip", data: pack}})
	if code != 200 {
		t.Fatalf("期待 200，收到 %d：%v", code, out)
	}
	if fm.fields["version"] != "9.9.9" {
		t.Fatalf("版本应从 SKILL.md 的 version 补全（包名没有版本号），实得 %q", fm.fields["version"])
	}
	if fm.fields["tags"] != "办公,自动化" {
		t.Fatalf("标签应从 SKILL.md 的 tags 补全，实得 %q", fm.fields["tags"])
	}
	// 前端给了 meta 就以前端为准（meta 优先）
	fm2 := newFakePublishMarket(t)
	b := newPublishTestApp(t, fm2.srv.URL+"/market/manifest.json")
	if code, out = publishReq(t, b, map[string]any{"id": "demo-skill", "version": "1.0.0",
		"tags": []string{"手工"}, "zip": true}, []pubFile{{name: "技能包.zip", data: pack}}); code != 200 {
		t.Fatalf("期待 200，收到 %d：%v", code, out)
	}
	if fm2.fields["version"] != "1.0.0" || fm2.fields["tags"] != "手工" {
		t.Fatalf("显式 meta 应优先于 SKILL.md，实得 %#v", fm2.fields)
	}
}
