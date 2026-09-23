#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""坚祥数字助理 Office 结构校验脚本：检查 OOXML 文档的完整性并统计结构。

纯标准库实现（zipfile + xml.etree），只做只读检查：
  * ZIP 完整性（能否打开、有无损坏条目）
  * 关键部件存在且 XML 良构（word/document.xml 等）
  * 关系文件（.rels）可解析
  * 结构统计：docx 段落/表格（含行列）/节；xlsx 工作表与单元格数；pptx 页与形状数
  * --contains 断言：主文档文本里必须出现指定内容

退出码：0 = 通过；1 = 结构错误或断言未命中。
结果以 JSON 打到标准输出，非 ASCII 一律 \\uXXXX 转义。

用法：
  python check_office.py <document.docx> [--contains 需要出现的文字] ... [--out report.json]
"""

import argparse
import json
import sys
import xml.etree.ElementTree as ET
import zipfile
from pathlib import Path

# 三种文档的主部件与类型判定
KINDS = {
    ".docx": {"kind": "docx", "main": "word/document.xml"},
    ".xlsx": {"kind": "xlsx", "main": "xl/workbook.xml"},
    ".pptx": {"kind": "pptx", "main": "ppt/presentation.xml"},
}
W = "{http://schemas.openxmlformats.org/wordprocessingml/2006/main}"


def all_text(root):
    """拉取部件里全部文本节点（w:t/a:t），供 --contains 断言。"""
    texts = []
    for node in root.iter():
        tag = node.tag.rsplit("}", 1)[-1]
        if tag in ("t",) and node.text:
            texts.append(node.text)
    return "\n".join(texts)


def check_docx(zf):
    stats, errors = {}, []
    root = ET.fromstring(zf.read("word/document.xml"))
    body = root.find(f"{W}body")
    paragraphs = body.findall(f".//{W}p") if body is not None else []
    tables = body.findall(f".//{W}tbl") if body is not None else []
    dims = []
    for tbl in tables:
        rows = tbl.findall(f"{W}tr")
        cols = len(rows[0].findall(f"{W}tc")) if len(rows) else 0
        dims.append({"rows": len(rows), "columns": cols})
    stats["paragraphs"] = len(paragraphs)
    stats["tables"] = dims
    stats["sections"] = len(body.findall(f".//{W}sectPr")) if body is not None else 0
    stats["text_chars"] = len(all_text(root))
    return stats, errors


def check_xlsx(zf):
    stats, errors = {}, []
    root = ET.fromstring(zf.read("xl/workbook.xml"))
    ns = "{http://schemas.openxmlformats.org/spreadsheetml/2006/main}"
    sheets = [{"name": s.get("name")} for s in root.iter(f"{ns}sheet")]
    cells = 0
    for name in zf.namelist():
        if name.startswith("xl/worksheets/sheet") and name.endswith(".xml"):
            sheet = ET.fromstring(zf.read(name))
            cells += sum(1 for _ in sheet.iter(f"{ns}c"))
    stats["sheets"] = sheets
    stats["cell_count"] = cells
    return stats, errors


def check_pptx(zf):
    stats, errors = {}, []
    slides = [n for n in zf.namelist() if n.startswith("ppt/slides/slide") and n.endswith(".xml")]
    shapes = 0
    for name in slides:
        root = ET.fromstring(zf.read(name))
        shapes += sum(1 for _ in root.iter())
    stats["slides"] = len(slides)
    stats["shape_nodes"] = shapes
    return stats, errors


def main():
    parser = argparse.ArgumentParser(description="坚祥数字助理 Office 结构校验脚本")
    parser.add_argument("input", type=Path)
    parser.add_argument("--contains", action="append", default=[], help="主文档文本必须包含的内容，可多次")
    parser.add_argument("--out", type=Path, help="JSON 报告同时写入该文件")
    args = parser.parse_args()

    report = {"file": str(args.input), "ok": False, "errors": [], "contains": []}
    spec = KINDS.get(args.input.suffix.lower())
    if spec is None or not args.input.is_file():
        report["errors"].append("输入不是受支持的 OOXML 文档（.docx/.xlsx/.pptx）")
        payload = json.dumps(report, ensure_ascii=True, indent=2) + "\n"
        sys.stdout.write(payload)
        return 1

    try:
        zf = zipfile.ZipFile(args.input)
    except Exception as exc:
        report["errors"].append("ZIP 打不开（文件损坏或不是 OOXML 包）: %s" % exc)
        payload = json.dumps(report, ensure_ascii=True, indent=2) + "\n"
        sys.stdout.write(payload)
        return 1

    with zf:
        bad = zf.testzip()
        if bad is not None:
            report["errors"].append("ZIP 内有损坏条目: %s" % bad)
        main_part = spec["main"]
        if main_part not in zf.namelist():
            report["errors"].append("缺少主部件: %s" % main_part)
        for rels in [n for n in zf.namelist() if n.endswith(".rels")]:
            try:
                ET.fromstring(zf.read(rels))
            except ET.ParseError as exc:
                report["errors"].append("关系文件 XML 不良构 %s: %s" % (rels, exc))
        if main_part in zf.namelist():
            try:
                root = ET.fromstring(zf.read(main_part))
                checker = {"docx": check_docx, "xlsx": check_xlsx, "pptx": check_pptx}[spec["kind"]]
                stats, errors = checker(zf)
                report["stats"] = stats
                report["errors"].extend(errors)
                haystack = all_text(root)
                for needle in args.contains:
                    report["contains"].append({"text": needle, "found": needle in haystack})
            except ET.ParseError as exc:
                report["errors"].append("主部件 XML 不良构: %s" % exc)

    report["ok"] = not report["errors"] and all(c["found"] for c in report["contains"])
    payload = json.dumps(report, ensure_ascii=True, indent=2) + "\n"
    if args.out is not None:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(payload, encoding="utf-8")
    sys.stdout.write(payload)
    return 0 if report["ok"] else 1


if __name__ == "__main__":
    sys.exit(main())
