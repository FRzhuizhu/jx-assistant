# -*- coding: utf-8 -*-
"""从桌面的 markdown-preview.html 提取 CSS 与 marked 渲染库，生成自包含查看器模板。
如果日后你改进了桌面网页的样式，重跑本脚本即可同步。
用法: python build_template.py
"""
import re, os

SRC = r"C:\Users\54358\Desktop\markdown-preview.html"
DST = os.path.join(os.path.dirname(os.path.abspath(__file__)), "viewer-template.html")

with open(SRC, encoding="utf-8") as f:
    src = f.read()

style_m = re.search(r"<style>([\s\S]*?)</style>", src)
marked_m = re.search(r"<script>(/\*\*[\s\S]*?)</script>", src)
if not style_m or not marked_m:
    raise SystemExit("无法从源网页提取样式或 marked 库")

style = style_m.group(1).strip()
marked = marked_m.group(1).strip()
if "</script" in marked:
    raise SystemExit("marked 库中包含 </script，无法内联")

template = """<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>__TITLE__</title>
<style>
{style}
</style>
</head>
<body>
<header>
  <h1>Markdown 离线预览器</h1>
  <span class="meta" id="meta"></span>
</header>
<main><div class="md" id="content"><p class="empty">正在加载…</p></div></main>
<script type="text/plain" id="__md__">__B64__</script>
<script>
{marked}
</script>
<script>
(function(){{
  try {{
    var b = document.getElementById('__md__').textContent;
    var bin = atob(b);
    var bytes = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    var md = new TextDecoder('utf-8').decode(bytes);
    document.getElementById('content').innerHTML = marked.parse(md);
    document.getElementById('meta').textContent = '__META__';
  }} catch(e) {{
    document.getElementById('content').innerHTML = '<p class="empty">解析失败：' + e.message + '</p>';
  }}
}})();
</script>
</body>
</html>
""".format(style=style, marked=marked)

with open(DST, "w", encoding="utf-8") as f:
    f.write(template)

print("OK ->", DST)
