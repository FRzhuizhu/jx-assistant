# -*- coding: utf-8 -*-
"""双击 .md 文件时调用：读取内容 -> 烤进临时网页 -> 用默认浏览器打开。
以 pythonw.exe 运行，全程无黑框。
用法: pythonw md-open.pyw "D:\path\to\file.md"
"""
import sys, os, base64, tempfile, html

HERE = os.path.dirname(os.path.abspath(__file__))
TEMPLATE = os.path.join(HERE, "viewer-template.html")


def main():
    if len(sys.argv) < 2:
        return
    path = sys.argv[1].strip('"').strip()
    if not path or not os.path.isfile(path):
        return

    # 读取 md 内容（utf-8 优先，异常字符容错）
    with open(path, "r", encoding="utf-8", errors="replace", newline="") as f:
        md = f.read()

    # 读取模板
    if not os.path.isfile(TEMPLATE):
        return
    with open(TEMPLATE, "r", encoding="utf-8") as f:
        tpl = f.read()

    name = os.path.basename(path)
    b64 = base64.b64encode(md.encode("utf-8")).decode("ascii")
    meta = name + " · " + str(len(md)) + " 字符"

    out_html = (
        tpl
        .replace("__TITLE__", html.escape(name))
        .replace("__META__", html.escape(meta))
        .replace("__B64__", b64)
    )

    # 写到临时目录（不污染原文件所在位置）
    outdir = os.path.join(tempfile.gettempdir(), "mdviewer")
    try:
        os.makedirs(outdir, exist_ok=True)
    except OSError:
        outdir = tempfile.gettempdir()
    out = os.path.join(outdir, name + ".html")
    with open(out, "w", encoding="utf-8") as f:
        f.write(out_html)

    # 用默认浏览器打开
    os.startfile(out)


if __name__ == "__main__":
    main()
