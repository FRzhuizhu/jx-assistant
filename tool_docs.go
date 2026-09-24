package main

// 文档组件：pdf / ocr / office_skill 三个模型工具。
// 执行链路：工具 → 探测系统 Python → 运行内嵌落盘的组件脚本 →
// JSON 结果摘要 + 图片信封回给模型。只读语义：diagnose/text/table/ocr
// 不碰用户文件（报告与渲染图都落在系统临时目录并入库为附件）；
// 模型确需把产物留在工作区时，用 outdir 参数（会走写权限）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	docsExecTimeout  = 5 * time.Minute
	digestMaxRunes   = 8000
	imageAutoPages   = 5 // image 模式缺省最多渲染的页数，其余用 --pages 续读
	embedAutoAttach  = 3 // embed 模式最多入库的内嵌图张数
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

// runDocsScript 找解释器、查依赖、跑脚本，返回标准输出。
func runDocsScript(t *toolContext, script string, module string, install []string, args []string, timeout time.Duration) (string, error) {
	py, err := findPython()
	if err != nil {
		return "", err
	}
	if !py.hasModule(module) {
		return "", fmt.Errorf("缺少 Python 模块 %s。%s", module, pyInstallHint(install...))
	}
	dir, err := ensureScripts()
	if err != nil {
		return "", err
	}
	out, err := py.run(append([]string{filepath.Join(dir, script)}, args...), timeout)
	if err != nil {
		if mod, ok := pyMissingFromStderr(out); ok {
			return "", fmt.Errorf("缺少 Python 模块 %s。%s", mod, pyInstallHint(mod))
		}
		return "", err
	}
	return out, nil
}

// ---------- pdf ----------

func pdfTool() Tool {
	return Tool{
		Spec: toolSpec("pdf",
			"读取 PDF 文件。mode=diagnose 判断文字层是否可信（永远先跑它）；text 抽取文字；table 抽表格；image 把页面渲染成 PNG（自动以图片信封返回，配合 read_image 通路查看，扫描件/乱码件的正确通路）；embed 抽取内嵌图片。需要本机 Python 与依赖库（缺失时返回安装指引）。",
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

	type depSpec struct {
		module  string
		install []string
	}
	dep := map[string]depSpec{
		"diagnose": {"pypdfium2", []string{"pypdfium2"}},
		"text":     {"pypdfium2", []string{"pypdfium2"}},
		"image":    {"pypdfium2", []string{"pypdfium2"}},
		"table":    {"pdfplumber", []string{"pdfplumber"}},
		"embed":    {"pypdf", []string{"pypdf"}},
	}[p.Mode]
	out, err := runDocsScript(t, "pdf_tool.py", dep.module, dep.install, scriptArgs, docsExecTimeout)
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
	return docsJSONDigest(out, hint), nil
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
			"识别图片中的文字（离线 OCR，中英文）。mode=text 返回按阅读顺序拼接的全文与逐块明细；lines 逐行输出，可用 outlined=true 把识别框描红画回图（以图片信封返回，便于对照）。只接受图片；PDF 请先跑 pdf 工具的 image 模式（scale 建议 3）。需要本机 Python 与 rapidocr-onnxruntime。",
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
		Mode      string  `json:"mode"`
		Path      string  `json:"path"`
		MinScore  float64 `json:"min_score"`
		Outlined  bool    `json:"outlined"`
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

	out, err := runDocsScript(t, "ocr_tool.py", "rapidocr_onnxruntime", []string{"rapidocr-onnxruntime"}, scriptArgs, docsExecTimeout)
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

type officeGuide struct {
	Kind      string
	Library   string // import 名
	Install   string // pip 包名
	Guidance  string
}

func officeGuides() map[string]officeGuide {
	return map[string]officeGuide{
		"docx": {
			Kind: "docx", Library: "docx", Install: "python-docx",
			Guidance: `## Word（.docx）操作要点
- 库：python-docx（from docx import Document）。
- 新建：Document() → add_heading(title, level) → add_paragraph(text, style="Heading 1") → save(path)。
- 修改既有文件：先遍历段落/表格/节看结构再动手；**直接改 paragraph.text 会丢掉 run 格式**，要改具体的 run。
- 中文必须同时设置 run.font.name 与东亚字体（rPr.rFonts 的 w:eastAsia），只改字体名不保证字形生效。
- 表格：按所在节的页面宽度设列宽；注意合并单元格与嵌套表。
- 生成后用校验脚本核结构（下方命令），通过后再交付。`,
		},
		"xlsx": {
			Kind: "xlsx", Library: "openpyxl", Install: "openpyxl",
			Guidance: `## Excel（.xlsx）操作要点
- 库：openpyxl（读写同一工作簿，保留公式与格式；公式不会被执行计算，读取到的公式单元格 value 是公式串，需要计算结果时用 data_only=True 打开——前提是该文件曾被 Excel 打开保存过）。
- 新建：Workbook() → ws.active / create_sheet → ws["A1"]=值 → save(path)。
- 读取：遍历 ws.iter_rows(values_only=True)；大文件不要整表读进上下文，先看 ws.dimensions 与 max_row。
- 样式：字体/填充/边框/数字格式都在单元格对象上设置；合并单元格 ws.merge_cells。
- 生成后用校验脚本核结构（下方命令），通过后再交付。`,
		},
		"pptx": {
			Kind: "pptx", Library: "pptx", Install: "python-pptx",
			Guidance: `## PowerPoint（.pptx）操作要点
- 库：python-pptx（from pptx import Presentation）。
- 新建：Presentation() → 用 slide_layouts 添加页（0=标题页，1=标题+内容）→ 页内 placeholder 或 add_textbox 填内容 → save(path)。
- 修改既有文件：先遍历 slides / shapes 弄清版式再动手；文本在 shape.text_frame 的 paragraph/run 层级里。
- 图片：slide.shapes.add_picture(path, left, top, width, height)；坐标单位是 EMU。
- 生成后用校验脚本核结构（下方命令），通过后再交付。`,
		},
	}
}

func officeSkillTool() Tool {
	return Tool{
		Spec: toolSpec("office_skill",
			"获取 Word/Excel/PowerPoint 文档的操作指南（docx/xlsx/pptx）：库的选择、关键 API 模式、中文字体陷阱与生成后的结构校验命令。拿到指南后，用 run_command 执行 Python 来实际创建或修改文档。需要本机 Python 与对应库。",
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
	guide, ok := officeGuides()[p.Kind]
	if !ok {
		return "", fmt.Errorf("未知文档类型 %q（可选 docx / xlsx / pptx）", p.Kind)
	}
	py, err := findPython()
	if err != nil {
		return guide.Guidance + "\n\n# 解释器\n" + err.Error(), nil
	}
	dir, err := ensureScripts()
	if err != nil {
		return "", err
	}
	status := "已安装"
	if !py.hasModule(guide.Library) {
		status = "未安装。" + pyInstallHint(guide.Install)
	}
	return fmt.Sprintf("# 运行环境\n- Python：%s（%s）\n- %s 库：%s\n- 结构校验命令：\"%s\" \"%s\" <文档路径>（生成后执行）\n\n%s",
		py.Exe, py.Version, p.Kind, status, strings.Join(py.Prefix, " ")+" "+py.Exe, filepath.Join(dir, "check_office.py"), guide.Guidance), nil
}
