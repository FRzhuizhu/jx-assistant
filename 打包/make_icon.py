# 由 web/logo.png 生成多尺寸 Windows 图标（坚祥数字助理GO.ico）。
# 用技能运行时自带的 Pillow 执行：
#   技能/_runtime/python/python.exe 打包/make_icon.py
import os

src = os.path.join(os.path.dirname(__file__), "..", "web", "logo.png")
dst = os.path.join(os.path.dirname(__file__), "坚祥数字助理GO.ico")

from PIL import Image

img = Image.open(src).convert("RGBA")
# 补成正方形画布（原图 512x495 近方），透明填充
size = max(img.size)
canvas = Image.new("RGBA", (size, size), (0, 0, 0, 0))
canvas.paste(img, ((size - img.width) // 2, (size - img.height) // 2))
base = canvas.resize((256, 256), Image.LANCZOS)

sizes = [16, 24, 32, 48, 64, 128, 256]
# bitmap_format="bmp"：PNG 压缩条目部分工具链（go-winres）解析不了，
# 全部落成 32 位 BGRA DIB，兼容面最广。
base.save(dst, format="ICO", sizes=[(s, s) for s in sizes], bitmap_format="bmp")
print("written:", dst, os.path.getsize(dst), "bytes")
