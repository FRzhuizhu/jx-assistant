# 由 web/logo.png 生成 Android 自适应图标前景（各密度 PNG）与圆形遮罩预览。
# 用法（任意带 Pillow 的 Python，如技能运行时或系统 Python）：
#   python android/make_icons.py [内容宽度占 108dp 画布比例，默认 0.56]
# 输出：android/app/src/main/res/drawable-*/ic_launcher_foreground.png
#       预览图落到 data/launcher_preview_<比例>.png（scratch 目录，可删）
import os
import sys

from PIL import Image, ImageDraw

here = os.path.dirname(os.path.abspath(__file__))
src = os.path.join(here, "..", "web", "logo.png")
res_dir = os.path.join(here, "app", "src", "main", "res")
ratio = float(sys.argv[1]) if len(sys.argv) > 1 else 0.56

logo = Image.open(src).convert("RGBA")

# 自适应图标画布基准 108dp，各密度对应像素尺寸
DENSITIES = {"mdpi": 1.0, "hdpi": 1.5, "xhdpi": 2.0, "xxhdpi": 3.0, "xxxhdpi": 4.0}


def compose(canvas_px: int) -> Image.Image:
    """把 logo 按比例居中放进正方形透明画布。"""
    content_w = round(canvas_px * ratio)
    content_h = round(logo.height * content_w / logo.width)
    scaled = logo.resize((content_w, content_h), Image.LANCZOS)
    canvas = Image.new("RGBA", (canvas_px, canvas_px), (0, 0, 0, 0))
    canvas.paste(scaled, ((canvas_px - content_w) // 2, (canvas_px - content_h) // 2), scaled)
    return canvas


for name, scale in DENSITIES.items():
    px = round(108 * scale)
    out_dir = os.path.join(res_dir, f"drawable-{name}")
    os.makedirs(out_dir, exist_ok=True)
    out = os.path.join(out_dir, "ic_launcher_foreground.png")
    compose(px).save(out, optimize=True)
    print("written:", out, px, "px")

# 预览：白底 + 圆形遮罩（72dp 视区 = 圆形桌面下可见范围），灰底示意裁切区
size = 432
prev = Image.new("RGBA", (size, size), (255, 255, 255, 255))
content = compose(size)
prev.paste(content, (0, 0), content)
mask = Image.new("L", (size, size), 0)
draw = ImageDraw.Draw(mask)
r = round(size * 72 / 108 / 2)
draw.ellipse((size // 2 - r, size // 2 - r, size // 2 + r, size // 2 + r), fill=255)
dim = Image.new("RGBA", (size, size), (205, 209, 215, 255))
prev_crop = Image.composite(prev, dim, mask)
out_prev = os.path.join(here, "..", "data", f"launcher_preview_{ratio:.2f}.png")
prev_crop.convert("RGB").save(out_prev)
print("preview:", os.path.abspath(out_prev))
