package main

// run_python × 内置技能的交叉真机验证（验证计划②③④⑤）：
//   ② office 新路：office_skill 拿指南 → run_python 生成含中文/东亚字体的
//      docx → 结构校验命令（单条）走 run_command；
//   ③ office 老路对照：手动落盘脚本（模拟 write）→ run_command 执行——
//      证明文案改动没有把老流程改坏；
//   ④ pdf / ocr 抽查：技能工具自身链路正常（确认 ensureSkillRuntime 的
//      缓存条件修正无副作用）；
//   ⑤ 顺序交叉：先 run_python（仅标准库）→ office_skill 装依赖 →
//      run_python import docx 仍可用（先用的裸运行时不挡后续依赖补装）。
// 运行时/依赖未装配的环境自动跳过（联网装配不属于单测职责）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func officeCtx(a *App, sess *SessionMeta) *toolContext {
	return &toolContext{app: a, sess: sess, emit: func(Event) {}, ctx: context.Background()}
}

// officeGuide 调 office_skill 工具拿分册指南（内部幂等装配依赖）。
func officeGuide(t *testing.T, a *App, sess *SessionMeta, kind string) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"kind": kind})
	out, err := runOfficeSkill(officeCtx(a, sess), raw)
	if err != nil {
		t.Fatalf("office_skill(%s) 失败: %v", kind, err)
	}
	return out
}

func TestOfficeNewPathRunPython(t *testing.T) { // ② + ⑤
	a := newRunPyTestApp(t)
	skipIfNoPyRuntime(t, a)
	sess := &SessionMeta{ID: "testpyoff01abcdef", Workspace: a.Cfg.Workspace}

	// ⑤-1 先用 run_python 跑纯标准库脚本（此刻尚未装配 office 依赖）
	out0, err := runPyCall(t, a, sess, map[string]any{"code": "print('stdlib-ok')", "name": "pre_check"})
	if err != nil || !strings.Contains(out0, "stdlib-ok") {
		t.Fatalf("⑤-1 run_python 标准库脚本失败: %v\n%s", err, out0)
	}

	// ②-1 office_skill 拿 docx 分册指南（内部装配 python-docx）
	guide := officeGuide(t, a, sess, "docx")
	if !strings.Contains(guide, "python-docx") {
		t.Fatalf("②-1 指南缺少 python-docx 要点:\n%.300s", guide)
	}

	// ②-2 run_python 生成含中文与东亚字体设置的 docx
	docxPath := filepath.Join(a.Cfg.Workspace, "验证-中文文档.docx")
	code := strings.Join([]string{
		"from docx import Document",
		"from docx.oxml.ns import qn",
		"d = Document()",
		"r = d.add_paragraph().add_run('坚祥数字助理run_python验证文档')",
		"r.font.name = 'Microsoft YaHei'",
		"r._element.rPr.rFonts.set(qn('w:eastAsia'), '微软雅黑')",
		"d.save(r'" + filepath.ToSlash(docxPath) + "')",
		"print('docx-saved')",
	}, "\n")
	out1, err := runPyCall(t, a, sess, map[string]any{"code": code, "name": "make_docx", "timeout_sec": 180})
	if err != nil || !strings.Contains(out1, "docx-saved") {
		t.Fatalf("②-2 run_python 生成 docx 失败: %v\n%s", err, out1)
	}
	if _, serr := os.Stat(docxPath); serr != nil {
		t.Fatalf("②-2 docx 未生成: %v", serr)
	}

	// ⑤-2 顺序交叉：先 run_python 后装配的依赖，import 依旧可用
	out2, err := runPyCall(t, a, sess, map[string]any{"code": "import docx; print('import-ok')", "name": "import_probe"})
	if err != nil || !strings.Contains(out2, "import-ok") {
		t.Fatalf("⑤-2 顺序交叉失败（run_python 先用不应挡住依赖）: %v\n%s", err, out2)
	}

	// ②-3 结构校验（单条命令，走 run_command —— 校验器的天然形态）
	py, perr := a.ensureSkillRuntime(nil, nil)
	if perr != nil {
		t.Fatalf("取运行时失败: %v", perr)
	}
	manifest, skillDir, merr := a.readSkillManifest("office")
	if merr != nil {
		t.Skipf("office manifest 不可读: %v", merr)
	}
	checker := filepath.Join(skillDir, filepath.FromSlash(manifest.Entry))
	if _, serr := os.Stat(checker); serr != nil {
		t.Skipf("校验器不存在: %v", serr)
	}
	verify := "& \"" + py.Exe + "\" \"" + checker + "\" \"" + docxPath + "\" --contains 坚祥数字助理"
	raw, _ := json.Marshal(map[string]any{"command": verify, "timeout_sec": 180})
	out3, err := runCommand(officeCtx(a, sess), raw)
	if err != nil {
		t.Fatalf("②-3 结构校验失败: %v\n%s", err, out3)
	}
}

func TestOfficeOldPathWriteAndRunCommand(t *testing.T) { // ③ 老路对照
	a := newRunPyTestApp(t)
	skipIfNoPyRuntime(t, a)
	sess := &SessionMeta{ID: "testpyoff02abcdef", Workspace: a.Cfg.Workspace}
	officeGuide(t, a, sess, "docx") // 幂等确保依赖就绪

	// 模拟老流程：write 工具把脚本落到 .jxai/tmp 自选子目录 → run_command 执行
	oldScript := filepath.Join(a.Cfg.Workspace, ".jxai", "tmp", "mk", "old_way.py")
	if err := os.MkdirAll(filepath.Dir(oldScript), 0o755); err != nil {
		t.Fatal(err)
	}
	docxPath := filepath.Join(a.Cfg.Workspace, "验证-老路文档.docx")
	code := strings.Join([]string{
		"from docx import Document",
		"d = Document()",
		"d.add_paragraph('老流程对照')",
		"d.save(r'" + filepath.ToSlash(docxPath) + "')",
		"print('old-saved')",
	}, "\n")
	if err := os.WriteFile(oldScript, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	py, _ := a.ensureSkillRuntime(nil, nil)
	cmd := "& \"" + py.Exe + "\" \"" + oldScript + "\""
	raw, _ := json.Marshal(map[string]any{"command": cmd, "timeout_sec": 180})
	out, err := runCommand(officeCtx(a, sess), raw)
	if err != nil || !strings.Contains(out, "old-saved") {
		t.Fatalf("③ 老路（write 落盘 + run_command）失败: %v\n%s", err, out)
	}
	if _, serr := os.Stat(docxPath); serr != nil {
		t.Fatalf("③ 老路 docx 未生成: %v", serr)
	}
}

func TestPdfToolDiagnoseUnaffected(t *testing.T) { // ④ pdf 抽查
	a := newRunPyTestApp(t)
	skipIfNoPyRuntime(t, a)
	sess := &SessionMeta{ID: "testpyoff03abcdef", Workspace: a.Cfg.Workspace}
	// 素材：run_python + pypdf 生成一页空白 PDF（pypdf 随 pdf 技能依赖装配）
	pdfPath := filepath.Join(a.Cfg.Workspace, "smoke.pdf")
	code := strings.Join([]string{
		"from pypdf import PdfWriter",
		"w = PdfWriter()",
		"w.add_blank_page(200, 200)",
		"with open(r'" + filepath.ToSlash(pdfPath) + "', 'wb') as f: w.write(f)",
		"print('pdf-saved')",
	}, "\n")
	out, err := runPyCall(t, a, sess, map[string]any{"code": code, "name": "mk_pdf", "timeout_sec": 120})
	if err != nil {
		if strings.Contains(out, "No module named") {
			t.Skip("pypdf 未装配（pdf 技能未用过），跳过 pdf 抽查")
		}
		t.Fatalf("PDF 素材生成失败: %v\n%s", err, out)
	}
	raw, _ := json.Marshal(map[string]any{"mode": "diagnose", "path": pdfPath})
	out2, err := runPdfTool(officeCtx(a, sess), raw)
	if err != nil {
		t.Fatalf("pdf diagnose 失败（缓存修正的副作用？）: %v\n%s", err, out2)
	}
	if !strings.Contains(out2, "verdict") {
		t.Fatalf("pdf diagnose 输出异常:\n%.300s", out2)
	}
}

func TestOcrToolUnaffected(t *testing.T) { // ④ ocr 抽查
	a := newRunPyTestApp(t)
	skipIfNoPyRuntime(t, a)
	sess := &SessionMeta{ID: "testpyoff04abcdef", Workspace: a.Cfg.Workspace}
	img, _ := filepath.Abs(filepath.Join("LOGO中轴式   标准色稿-10.png")) // go test 的 cwd = 项目根
	if _, serr := os.Stat(img); serr != nil {
		t.Skip("没找到现成图片素材，跳过 ocr 抽查")
	}
	raw, _ := json.Marshal(map[string]any{"mode": "text", "path": img})
	out, err := runOcrTool(officeCtx(a, sess), raw)
	if err != nil {
		t.Fatalf("ocr 失败（缓存修正的副作用？）: %v\n%.300s", err, out)
	}
}
