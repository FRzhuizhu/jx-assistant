#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""坚祥数字助理 PDF 读取脚本： diagnose / text / table / image / embed 五种模式。

设计要点（与数字助理的 pdf 工具约定一致）：
  * 第一步永远先跑 diagnose：它用字符分布判断文字层是否可信，
    给出 verdict（text-usable / text-suspect / image-only），模型照 verdict 走。
  * text-suspect 的典型成因是"反复制 PDF"：字体子集缺 ToUnicode 映射，
    抽出来的文本大量落在私用区/控制区。本脚本只做启发式判断，不下断言。
  * 结果以 JSON 打到标准输出，非 ASCII 一律 \\uXXXX 转义，规避控制台编码差异。

依赖：pypdfium2（diagnose/text/image）、pdfplumber（table，可选）、pypdf（embed，可选）。

用法：
  python pdf_tool.py diagnose <input.pdf>
  python pdf_tool.py text     <input.pdf> [--pages 1,3-5]
  python pdf_tool.py table    <input.pdf> [--pages 1,3-5] [--max-rows 50]
  python pdf_tool.py image    <input.pdf> --outdir <dir> [--scale 2] [--pages 1,3-5]
  python pdf_tool.py embed    <input.pdf> --outdir <dir>
"""

import argparse
import json
import sys
from pathlib import Path

# diagnose 的判定阈值：有文字层的页至少要有这么多字符；
# 可疑页占比过半、或单页可疑字符比例超过该值，都视为可疑。
MIN_TEXT_CHARS = 20
SUSPECT_RATIO = 0.05
MEANINGFUL_RATIO = 0.3


def expand_pages(spec, total):
    """把 "1,3-5" 展开成升序去重的 0 基页码；spec 为空返回全部页。"""
    if not spec:
        return list(range(total))
    picked = set()
    for chunk in spec.split(","):
        chunk = chunk.strip()
        if not chunk:
            continue
        head, dash, tail = chunk.partition("-")
        if dash:
            picked.update(range(int(head) - 1, int(tail)))
        else:
            picked.add(int(head) - 1)
    return sorted(p for p in picked if 0 <= p < total)


def classify_text(text):
    """统计一段抽取文本的字符类别，产出可信度指标。

    私用区（U+E000..U+F8FF 等）与控制字符的高占比意味着字形缺 ToUnicode
    映射；replacement 字符说明解码失败。这些是"文字层不可信"的信号。
    """
    stats = {
        "chars": len(text),
        "letters": 0,
        "symbols": 0,
        "private_use": 0,
        "control": 0,
        "replacement": 0,
    }
    for ch in text:
        code = ord(ch)
        if ch == "\ufffd":
            stats["replacement"] += 1
        elif 0xE000 <= code <= 0xF8FF or 0xF0000 <= code <= 0x10FFFD:
            stats["private_use"] += 1
        elif code < 0x20 and ch not in "\t\r\n":
            stats["control"] += 1
        elif ch.isalpha():
            stats["letters"] += 1
        elif not ch.isspace() and not ch.isdigit():
            stats["symbols"] += 1
    total = stats["chars"]
    bad = stats["private_use"] + stats["control"] + stats["replacement"]
    stats["suspect_ratio"] = round(bad / total, 4) if total else 0.0
    meaningful = stats["letters"] + stats["symbols"]
    stats["suspect"] = bool(total) and (
        stats["suspect_ratio"] > SUSPECT_RATIO or meaningful / total < MEANINGFUL_RATIO
    )
    return stats


def open_pages(path):
    """打开 PDF，返回 (pypdfium2 文档, 总页数)。"""
    import pypdfium2 as pdfium

    doc = pdfium.PdfDocument(path)
    return doc, len(doc)


def page_text(doc, index):
    """抽取指定页的全文；失败按空串处理（缺文字层的页会走到这里）。"""
    page = doc[index]
    try:
        return page.get_textpage().get_text_range() or ""
    except Exception:
        return ""
    finally:
        page.close()


def do_diagnose(path):
    doc, total = open_pages(path)
    try:
        pages = []
        for i in range(total):
            text = page_text(doc, i)
            pages.append({"page": i + 1, **classify_text(text), "sample": text[:120]})
    finally:
        doc.close()

    has_text = [p for p in pages if p["chars"] >= MIN_TEXT_CHARS]
    suspect = [p for p in pages if p["suspect"]]
    if not has_text:
        verdict, advice = "image-only", "没有可用文字层（扫描件）。走 image 模式渲染成图片后看图理解。"
    elif len(suspect) >= max(1, len(has_text) // 2):
        verdict, advice = (
            "text-suspect",
            "多数页文字层可疑（反复制 PDF 的典型特征）。走 image 模式看图；"
            "确需可复制文字时再用 OCR。",
        )
    else:
        verdict, advice = "text-usable", "文字层可用。先用 text 模式读文字，需要表格再补 table 模式。"
    return {
        "mode": "diagnose",
        "pages_total": total,
        "pages_with_text": len(has_text),
        "pages_suspect": len(suspect),
        "verdict": verdict,
        "advice": advice,
        "pages": pages,
    }


def do_text(path, pages_spec):
    doc, total = open_pages(path)
    try:
        rows = []
        for i in expand_pages(pages_spec, total):
            text = page_text(doc, i)
            rows.append({"page": i + 1, "chars": len(text), "text": text})
    finally:
        doc.close()
    return {
        "mode": "text",
        "pages_total": total,
        "pages_read": len(rows),
        "chars_total": sum(r["chars"] for r in rows),
        "pages": rows,
    }


def do_table(path, pages_spec, max_rows):
    import pdfplumber

    tables = []
    with pdfplumber.open(path) as doc:
        total = len(doc.pages)
        for i in expand_pages(pages_spec, total):
            for order, grid in enumerate(doc.pages[i].extract_tables()):
                rows = [[("" if cell is None else cell) for cell in row] for row in grid]
                tables.append({
                    "page": i + 1,
                    "table": order + 1,
                    "rows": len(rows),
                    "columns": max((len(r) for r in rows), default=0),
                    "truncated": len(rows) > max_rows,
                    "data": rows[:max_rows],
                })
    return {"mode": "table", "pages_total": total, "tables_found": len(tables), "tables": tables}


def do_image(path, outdir, scale, pages_spec):
    doc, total = open_pages(path)
    try:
        outdir.mkdir(parents=True, exist_ok=True)
        written = []
        for i in expand_pages(pages_spec, total):
            bitmap = doc[i].render(scale=scale)
            try:
                pil = bitmap.to_pil()
                name = "page-%03d.png" % (i + 1)
                target = outdir / name
                pil.save(target)
                written.append({
                    "page": i + 1,
                    "file": str(target),
                    "width": pil.width,
                    "height": pil.height,
                })
            finally:
                bitmap.close()
    finally:
        doc.close()
    return {"mode": "image", "pages_total": total, "pages_written": len(written), "pages": written}


def do_embed(path, outdir):
    from pypdf import PdfReader

    reader = PdfReader(path)
    outdir.mkdir(parents=True, exist_ok=True)
    found = []
    for number, page in enumerate(reader.pages, start=1):
        for order, img in enumerate(page.images):
            suffix = Path(img.name).suffix or ".bin"
            name = "embedded-p%03d-%02d%s" % (number, order, suffix)
            (outdir / name).write_bytes(img.data)
            found.append({
                "page": number,
                "file": str(outdir / name),
                "bytes": len(img.data),
                "source_name": img.name,
            })
    return {"mode": "embed", "pages_total": len(reader.pages), "images_found": len(found), "images": found}


def main():
    parser = argparse.ArgumentParser(description="坚祥数字助理 PDF 读取脚本")
    parser.add_argument("mode", choices=["diagnose", "text", "table", "image", "embed"])
    parser.add_argument("input", type=Path)
    parser.add_argument("--pages", help='页码表达式，如 "1,3-5"；缺省读全部')
    parser.add_argument("--outdir", type=Path, help="image / embed 模式的输出目录")
    parser.add_argument("--scale", type=float, default=2.0, help="image 模式渲染倍率，默认 2.0")
    parser.add_argument("--max-rows", type=int, default=50, help="table 模式每张表最多返回行数")
    parser.add_argument("--out", type=Path, help="JSON 报告同时写入该文件")
    args = parser.parse_args()

    if not args.input.is_file():
        parser.error("输入不是文件: %s" % args.input)
    if args.mode in ("image", "embed") and args.outdir is None:
        parser.error("%s 模式需要 --outdir" % args.mode)

    if args.mode == "diagnose":
        report = do_diagnose(args.input)
    elif args.mode == "text":
        report = do_text(args.input, args.pages)
    elif args.mode == "table":
        report = do_table(args.input, args.pages, args.max_rows)
    elif args.mode == "image":
        report = do_image(args.input, args.outdir, args.scale, args.pages)
    else:
        report = do_embed(args.input, args.outdir)

    payload = json.dumps(report, ensure_ascii=True, indent=2) + "\n"
    if args.out is not None:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(payload, encoding="utf-8")
    sys.stdout.write(payload)
    return 0


if __name__ == "__main__":
    sys.exit(main())
