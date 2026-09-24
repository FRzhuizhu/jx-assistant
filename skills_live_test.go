package main

// 技能体系的实弹测试：真实下载便携 Python 与依赖 wheel 装配运行时
// （约 25MB 下载，走清华镜像），再用装配出的解释器跑通 pdf 技能。
// -short 模式跳过。

import (
	"strings"
	"fmt"
	"os"
	"testing"
)

func TestLiveProvisionRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("live test")
	}
	a := attachmentTestApp(t) // DataDir 与 Workspace 都在临时目录
	rtDir := a.Cfg.DataDir + "\\_rt-test\\python"

	emit := func(msg string) { t.Log("进度:", msg) }
	py, err := provisionPythonRuntime(rtDir,
		[]string{"pypdfium2"}, []string{"pypdfium2"}, emit)
	if err != nil {
		t.Fatalf("装配运行时失败: %v", err)
	}
	if !py.hasModule("pypdfium2") {
		t.Fatal("装配后 pypdfium2 仍不可用")
	}

	// 用装配出的解释器跑 pdf 技能脚本（diagnose 模式）：先现场生成一份真实 PDF
	dir, err := a.SkillsDir()
	if err != nil {
		t.Fatal(err)
	}
	pdfPath := a.Cfg.DataDir + `\技能实测.pdf`
	if err := os.WriteFile(pdfPath, minimalTestPDF(), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := py.run([]string{
		dir + `\pdf\scripts\pdf_tool.py`, "diagnose", pdfPath,
	}, 2*60*1e9)
	if err != nil {
		t.Fatalf("diagnose 失败: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"verdict": "text-usable"`) {
		t.Fatalf("diagnose 结果异常: %s", out)
	}
	t.Logf("装配运行时跑通 pdf 技能（输出 %d 字节，verdict=text-usable）", len(out))
}

// minimalTestPDF 手写一份最小合法 PDF（含一行英文文本）。
func minimalTestPDF() []byte {
	stream := []byte("BT /F1 24 Tf 72 700 Td (JX skill runtime test 123) Tj ET")
	objs := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>"),
		[]byte(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream)),
		[]byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
	}
	var out []byte
	out = append(out, []byte("%PDF-1.4\n")...)
	offsets := []int{}
	for i, body := range objs {
		offsets = append(offsets, len(out))
		out = append(out, []byte(fmt.Sprintf("%d 0 obj\n", i+1))...)
		out = append(out, body...)
		out = append(out, []byte("\nendobj\n")...)
	}
	xref := len(out)
	out = append(out, []byte(fmt.Sprintf("xref\n0 %d\n", len(objs)+1))...)
	out = append(out, []byte("0000000000 65535 f \n")...)
	for _, off := range offsets {
		out = append(out, []byte(fmt.Sprintf("%010d 00000 n \n", off))...)
	}
	out = append(out, []byte(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref))...)
	return out
}

func TestSkillsMaterialize(t *testing.T) {
	a := attachmentTestApp(t)
	a.Cfg.SkillsDir = a.Cfg.DataDir + "\\技能"
	dir, err := a.SkillsDir()
	if err != nil {
		t.Fatalf("解析技能目录失败: %v", err)
	}
	for _, f := range []string{
		`pdf\SKILL.md`, `pdf\manifest.json`, `pdf\scripts\pdf_tool.py`,
		`ocr\SKILL.md`, `ocr\manifest.json`, `ocr\scripts\ocr_tool.py`,
		`office\SKILL.md`, `office\manifest.json`, `office\scripts\check_office.py`,
	} {
		full := dir + "\\" + f
		if data, err := os.ReadFile(full); err != nil || len(data) == 0 {
			t.Fatalf("母本未释放: %s (%v)", full, err)
		}
	}
	// manifest 解析
	m, _, err := a.readSkillManifest("pdf")
	if err != nil {
		t.Fatalf("解析 manifest 失败: %v", err)
	}
	if m.ID != "pdf" || len(m.Python.Modules) == 0 {
		t.Fatalf("manifest 内容不符: %+v", m)
	}
}
