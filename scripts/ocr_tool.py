#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""坚祥数字助理 OCR 脚本：把图片里的文字识别成可复制字符。

引擎为 RapidOCR（PP-OCRv4，Apache-2.0），检测/方向分类/识别三合一，
中英文共用模型，模型随包分发、完全离线。只接受图片输入；PDF 请先由
pdf 脚本的 image 模式渲染成 PNG（scale 建议 3，小字识别更稳）。

结果以 JSON 打到标准输出，非 ASCII 一律 \\uXXXX 转义。

用法：
  python ocr_tool.py text  <image> [--min-score 0.5]
  python ocr_tool.py lines <image> [--min-score 0.5] [--outlined <path.png>]
"""

import argparse
import json
import sys
from pathlib import Path

ACCEPTED = {".png", ".jpg", ".jpeg", ".webp", ".bmp", ".tif", ".tiff"}


def recognize(path, min_score):
    """跑一次引擎，返回过滤后的行列表，每项 [四点框, 文本, 置信度]。"""
    from rapidocr_onnxruntime import RapidOCR

    engine = RapidOCR()
    result, _ = engine(str(path))
    if not result:
        return []
    return [row for row in result if float(row[2]) >= min_score]


def as_box(points):
    return [[round(float(x), 2), round(float(y), 2)] for x, y in points]


def by_reading_order(block):
    """先按行（纵坐标每 10px 一档）再按横坐标排序，稳住阅读顺序。"""
    top, left = block["box"][0][1], block["box"][0][0]
    return (round(top / 10), left)


def do_text(path, min_score):
    rows = recognize(path, min_score)
    blocks = [
        {"text": str(row[1]), "score": round(float(row[2]), 4), "box": as_box(row[0])}
        for row in rows
    ]
    ordered = sorted(blocks, key=by_reading_order)
    scores = [b["score"] for b in blocks]
    return {
        "mode": "text",
        "image": str(path),
        "blocks": len(blocks),
        "text": "\n".join(b["text"] for b in ordered),
        "mean_score": round(sum(scores) / len(scores), 4) if scores else None,
        "min_score": min(scores) if scores else None,
        "detail": blocks,
    }


def do_lines(path, min_score, outlined):
    rows = recognize(path, min_score)
    lines = [
        {
            "index": i + 1,
            "text": str(row[1]),
            "score": round(float(row[2]), 4),
            "box": as_box(row[0]),
        }
        for i, row in enumerate(rows)
    ]
    report = {"mode": "lines", "image": str(path), "lines": len(lines), "detail": lines}
    if outlined is not None and rows:
        # 把识别框描红画回原图，供模型对照"哪段文字对应哪块区域"。
        import cv2

        canvas = cv2.imread(str(path))
        if canvas is not None:
            for row in rows:
                pts = [(int(x), int(y)) for x, y in row[0]]
                for i in range(len(pts)):
                    cv2.line(canvas, pts[i], pts[(i + 1) % len(pts)], (0, 0, 255), 2)
            outlined.parent.mkdir(parents=True, exist_ok=True)
            cv2.imwrite(str(outlined), canvas)
            report["outlined"] = str(outlined)
    return report


def main():
    parser = argparse.ArgumentParser(description="坚祥数字助理 OCR 脚本")
    parser.add_argument("mode", choices=["text", "lines"])
    parser.add_argument("input", type=Path)
    parser.add_argument("--min-score", type=float, default=0.5, help="置信度下限，默认 0.5")
    parser.add_argument("--outlined", type=Path, help="lines 模式：把识别框画回图保存到该路径")
    parser.add_argument("--out", type=Path, help="JSON 报告同时写入该文件")
    args = parser.parse_args()

    if not args.input.is_file():
        parser.error("输入不是文件: %s" % args.input)
    if args.input.suffix.lower() not in ACCEPTED:
        parser.error(
            "输入必须是图片（%s）；PDF 请先用 pdf 脚本 image 模式渲染" % ", ".join(sorted(ACCEPTED))
        )

    if args.mode == "text":
        report = do_text(args.input, args.min_score)
    else:
        report = do_lines(args.input, args.min_score, args.outlined)

    payload = json.dumps(report, ensure_ascii=True, indent=2) + "\n"
    if args.out is not None:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(payload, encoding="utf-8")
    sys.stdout.write(payload)
    return 0


if __name__ == "__main__":
    sys.exit(main())
