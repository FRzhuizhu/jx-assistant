package main

// 技能工具层：pdf / ocr / office_skill 三个模型工具。
// 技能的定义（SKILL.md / manifest.json / scripts）都在 技能/<技能名>/ 目录，
// 本文件只做三件事：解析技能清单 → 保证便携运行时就绪（随包分发，缺时
// 自动装配）→ 执行技能脚本并把结果摘要回给模型。
// 技能自身的用法说明存放在各 SKILL.md：office_skill 工具按分册返回；
// pdf 工具的技能入口是它自己，SKILL.md 经 appendSkillGuide 随会话内
// 首次调用附带——纪律类提示跟着技能「加载」进上下文，不常驻工具描述。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	docsExecTimeout = 5 * time.Minute
	digestMaxRunes  = 8000
	imageAutoPages  = 5 // image 模式缺省最多渲染的页数，其余用 --pages 续读
	embedAutoAttach = 3 // embed 模式最多入库的内嵌图张数
)

// docsJSONDigest 截断脚本输出的 JSON，超限给续读提示。
func docsJSONDigest(report string, hint string) string {
	report = strings.TrimSpace(report)
	if len([]rune(report)) <= digestMaxRunes {
		return report
	}
	cut := string([]rune(report)[:digestMaxRunes])
	return cut + "\n…（结果过长已截断）" + hint
}

// appendSkillGuide 把技能 SKILL.md 正文附带在工具结果末尾，每会话只随
// 首次调用出现一次（key 记在 App.skillGuideOnce）。用法与流程纪律跟着
// 技能「加载」进入上下文，而不是常驻工具描述——office_skill 的分册指南
// 就是这一模式；pdf 工具的技能入口是工具本身，由这里补上。SKILL.md 读
// 不出来时静默跳过，不阻断工具结果。
func (t *toolContext) appendSkillGuide(out, skillID string) string {
	if t.sess == nil {
		return out
	}
	if _, dup := t.app.skillGuideOnce.LoadOrStore(t.sess.ID+"\x00"+skillID, struct{}{}); dup {
		return out
	}
	guide, err := t.app.readSkillGuide(skillID)
	if err != nil {
		return out
	}
	return out + "\n\n# " + skillID + " 技能指南（SKILL.md，随本会话首次调用附带一次）\n" + stripFrontmatter(guide)
}

// runSkillEntry 运行某技能 manifest 里声明的入口脚本：
// 先保证便携运行时就绪（随包分发），再执行脚本，返回合并输出。
func runSkillEntry(t *toolContext, skillID string, args []string, timeout time.Duration) (string, error) {
	manifest, skillDir, err := t.app.readSkillManifest(skillID)
	if err != nil {
		return "", err
	}
	py, err := t.app.ensureSkillRuntime(manifest.Python.Modules, manifest.Python.Wheels)
	if err != nil {
		return "", err
	}
	entry := filepath.Join(skillDir, filepath.FromSlash(manifest.Entry))
	out, err := py.run(append([]string{entry}, args...), timeout)
	if err != nil {
		// 运行时仍缺模块：依赖清单未覆盖或运行时被损坏——给用户可操作的
		// 修复指引，而不是让非编程用户去手工装依赖（安卓指回快照补齐）
		if mod, ok := pyMissingFromStderr(out); ok {
			if isAndroidRuntime {
				return "", fmt.Errorf("内置运行时缺少模块 %s（自动补装未覆盖，可能 PyPI 无安卓轮子）；请把该技能名与模块名反馈给开发者加进运行时快照", mod)
			}
			return "", fmt.Errorf("技能运行时缺少模块 %s。请重新安装软件修复运行时；若重新安装后仍出现，请把该技能名与模块名反馈给开发者补全依赖清单", mod)
		}
		return "", err
	}
	return out, nil
}

// ---------- pdf ----------

func pdfTool() Tool {
	return Tool{
		Spec: toolSpec("pdf",
			"读取 PDF 文件。mode=diagnose 判断文字层是否可信（永远先跑它）；text 抽取文字；table 抽表格；image 把页面渲染成 PNG（自动以图片信封返回，配合 read_image 通路查看，扫描件/乱码件的正确通路）；embed 抽取内嵌图片。技能运行时随软件内置，无需用户安装环境。",
			objSchema(map[string]any{
				"mode":     map[string]any{"type": "string", "enum": []string{"diagnose", "text", "table", "image", "embed"}, "description": "读取模式"},
				"path":     strProp("PDF 文件路径（相对或绝对）"),
				"pages":    strProp(`页码表达式，如 "1,3-5"；缺省全部（image 模式缺省只渲染前 5 页）`),
				"max_rows": intProp("table 模式每张表最多返回行数，默认 50"),
				"scale":    map[string]any{"type": "number", "description": "image 模式渲染倍率，默认 2.0；OCR 用途建议 3"},
				"outdir":   strProp("image/embed 模式的输出目录（缺省落系统临时目录，仅入库为附件）"),
			}, "mode", "path"),
		),
		Run: runPdfTool,
	}
}

func runPdfTool(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Mode    string  `json:"mode"`
		Path    string  `json:"path"`
		Pages   string  `json:"pages"`
		MaxRows int     `json:"max_rows"`
		Scale   float64 `json:"scale"`
		Outdir  string  `json:"outdir"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	if _, err := t.authorize(permAction{Op: "read", Tool: "pdf", Path: path}); err != nil {
		return "", err
	}

	scriptArgs := []string{p.Mode, path}
	var outdir string
	needsOutdir := p.Mode == "image" || p.Mode == "embed"
	if needsOutdir {
		if strings.TrimSpace(p.Outdir) != "" {
			outdir, err = t.resolve(p.Outdir)
			if err != nil {
				return "", err
			}
			// 显式指定输出目录：写入工作区产物，走写权限
			if _, err := t.authorize(permAction{Op: "write", Tool: "pdf", Path: outdir}); err != nil {
				return "", err
			}
		} else {
			outdir = filepath.Join(os.TempDir(), "jx-assistant-docs", t.sess.ID, p.Mode)
		}
		scriptArgs = append(scriptArgs, "--outdir", outdir)
	}
	if strings.TrimSpace(p.Pages) != "" {
		scriptArgs = append(scriptArgs, "--pages", p.Pages)
	}
	if p.Mode == "image" {
		if p.Scale > 0 {
			scriptArgs = append(scriptArgs, "--scale", fmt.Sprintf("%g", p.Scale))
		}
		// 缺省限前 N 页，避免大文档一次渲染几十张图
		if strings.TrimSpace(p.Pages) == "" {
			scriptArgs = append(scriptArgs, "--pages", fmt.Sprintf("1-%d", imageAutoPages))
		}
	}
	if p.Mode == "table" && p.MaxRows > 0 {
		scriptArgs = append(scriptArgs, "--max-rows", fmt.Sprintf("%d", p.MaxRows))
	}

	out, err := runSkillEntry(t, "pdf", scriptArgs, docsExecTimeout)
	if err != nil {
		return "", err
	}

	// image / embed：把渲染出的图片入库为附件，模型与用户都能直接看
	if needsOutdir {
		out += t.attachReportImages(outdir, p.Mode)
	}
	hint := "；可用 --pages 缩小范围后重跑"
	if p.Mode == "diagnose" {
		hint = "；按 verdict 选择下一步通路"
	}
	return t.appendSkillGuide(docsJSONDigest(out, hint), "pdf"), nil
}

// attachReportImages 扫描脚本输出目录里的图片，入库为附件并返回信封段。
func (t *toolContext) attachReportImages(dir, mode string) string {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return ""
	}
	capN := embedAutoAttach
	if mode == "image" {
		capN = imageAutoPages
	}
	var b strings.Builder
	count := 0
	for _, e := range entries {
		if e.IsDir() || count >= capN {
			break
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".png") && !strings.HasSuffix(strings.ToLower(name), ".jpg") {
			continue
		}
		full := filepath.Join(dir, name)
		raw, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		att, err := t.app.storeAttachment(raw, sniffImageMediaType(raw))
		if err != nil {
			continue
		}
		b.WriteString("\n" + imageEnvelope(att, full))
		count++
	}
	if count > 0 {
		b.WriteString(fmt.Sprintf("\n（已入库 %d 张图片，可用 read_image 的信封机制查看；更多结果在 %s）", count, dir))
	}
	return b.String()
}

// ---------- ocr ----------

func ocrTool() Tool {
	return Tool{
		Spec: toolSpec("ocr",
			"识别图片中的文字（离线 OCR，中英文）。mode=text 返回按阅读顺序拼接的全文与逐块明细；lines 逐行输出，可用 outlined=true 把识别框描红画回图（以图片信封返回，便于对照）。只接受图片；PDF 请先跑 pdf 工具的 image 模式（scale 建议 3）。技能运行时随软件内置，无需用户安装环境。",
			objSchema(map[string]any{
				"mode":      map[string]any{"type": "string", "enum": []string{"text", "lines"}, "description": "text=全文；lines=逐行"},
				"path":      strProp("图片路径（相对或绝对）"),
				"min_score": map[string]any{"type": "number", "description": "置信度下限，默认 0.5"},
				"outlined":  boolProp("lines 模式：把识别框画回图并入库为附件"),
			}, "mode", "path"),
		),
		Run: runOcrTool,
	}
}

func runOcrTool(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Mode     string  `json:"mode"`
		Path     string  `json:"path"`
		MinScore float64 `json:"min_score"`
		Outlined bool    `json:"outlined"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path, err := t.resolve(p.Path)
	if err != nil {
		return "", err
	}
	if _, err := t.authorize(permAction{Op: "read", Tool: "ocr", Path: path}); err != nil {
		return "", err
	}

	scriptArgs := []string{p.Mode, path}
	if p.MinScore > 0 {
		scriptArgs = append(scriptArgs, "--min-score", fmt.Sprintf("%g", p.MinScore))
	}
	var outlinedPath string
	if p.Mode == "lines" && p.Outlined {
		outlinedPath = filepath.Join(os.TempDir(), "jx-assistant-docs", t.sess.ID, "ocr-outlined.png")
		scriptArgs = append(scriptArgs, "--outlined", outlinedPath)
	}

	out, err := runSkillEntry(t, "ocr", scriptArgs, docsExecTimeout)
	if err != nil {
		return "", err
	}
	if outlinedPath != "" {
		if raw, err := os.ReadFile(outlinedPath); err == nil {
			if att, err := t.app.storeAttachment(raw, sniffImageMediaType(raw)); err == nil {
				out += "\n" + imageEnvelope(att, outlinedPath) + "\n（描红图已入库，可对照识别框与文字）"
			}
		}
	}
	return docsJSONDigest(out, "；可调大 --min-score 过滤低置信度噪声"), nil
}

// ---------- office_skill ----------

func officeSkillTool() Tool {
	return Tool{
		Spec: toolSpec("office_skill",
			"获取 Word/Excel/PowerPoint 文档技能的操作指南（docx/xlsx/pptx 分册）：库的选择、关键 API 模式、中文字体陷阱与生成后的结构校验命令。拿到指南后，用 run_command 执行 Python 实际创建或修改文档（Python 运行时随软件内置，无需用户安装环境；解释器绝对路径见运行环境快照与调用结果）。",
			objSchema(map[string]any{
				"kind": map[string]any{"type": "string", "enum": []string{"docx", "xlsx", "pptx"}, "description": "文档类型"},
			}, "kind"),
		),
		Run: runOfficeSkill,
	}
}

func runOfficeSkill(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	guide, err := t.app.readSkillGuide("office")
	if err != nil {
		return "", fmt.Errorf("读取 office 技能说明失败: %w", err)
	}
	// 只带对应分册 + 通用纪律，避免把三种文档的指南一起塞进上下文
	section := filterOfficeGuide(guide, p.Kind)

	manifest, skillDir, err := t.app.readSkillManifest("office")
	if err != nil {
		return "", err
	}
	variant, ok := manifest.Variants[p.Kind]
	if !ok {
		return "", fmt.Errorf("office 技能缺少 %s 分册配置", p.Kind)
	}
	py, err := t.app.ensureSkillRuntime(variant.Modules, variant.Wheels)
	if err != nil {
		// 不当 error 返回（指南正文仍有用），但文案必须是明确的指令——
		// 裸错误文本容易被当成「环境没装」而放弃或转投系统 python。
		return section + "\n\n# 运行环境\n内置 Python 运行时装配失败：" + err.Error() +
			"\n装配需联网（首次下载约数分钟）：向用户说明需联网后重试本工具即可；不要改用系统的 python/py（与本软件无关，多半也没有）。", nil
	}
	checker := filepath.Join(skillDir, filepath.FromSlash(manifest.Entry))
	prefix := strings.Join(py.Prefix, " ")
	return fmt.Sprintf("%s\n\n# 运行环境（%s 分册）\n- Python 解释器：\"%s %s\"\n- %s 库：已就绪\n- 结构校验命令：\"%s %s\" \"%s\" \"<文档路径>\"（生成后必须执行）\n",
		section, p.Kind, py.Exe, prefix, p.Kind, prefix, py.Exe, checker), nil
}

// filterOfficeGuide 从 office 技能的 SKILL.md 里抽出通用纪律 + 指定分册。
func filterOfficeGuide(guide, kind string) string {
	kindTitle := map[string]string{
		"docx": "## docx 分册要点",
		"xlsx": "## xlsx 分册要点",
		"pptx": "## pptx 分册要点",
	}[kind]
	sections := strings.Split(guide, "\n## ")
	head := strings.TrimSpace(sections[0])
	for _, s := range sections[1:] {
		if strings.HasPrefix(s, kindTitle[4:]) {
			return head + "\n\n## " + strings.TrimSpace(s)
		}
	}
	return guide
}
