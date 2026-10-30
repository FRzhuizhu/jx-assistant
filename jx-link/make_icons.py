# 由主仓库 web/logo.png 生成 Android 自适应图标前景（各密度 PNG）。
# 与主壳 android/make_icons.py 同源（logo 同一张 → 图标与现有应用一致）。
# 用法：python make_icons.py [内容宽度占 108dp 画布比例，默认 0.56]
import os
import sys

from PIL import Image

here = os.path.dirname(os.path.abspath(__file__))
# 图源：品牌素材的「app logo 图标」（2026-10-30 起用不带公司名的纯图标版），
# 与 web/logo.png 同尺寸同版式，只是去掉了名字。单独检出本目录（主仓库之外）
# 时品牌素材不存在，回落主仓库的 web/logo.png。
candidates = [
    os.path.join(os.path.dirname(here), "品牌素材", "app logo 图标.png"),
    os.path.join(os.path.dirname(here), "web", "logo.png"),
    os.path.join(os.path.dirname(here), "软著版数字助理", "web", "logo.png"),
]
src = next((q for q in candidates if os.path.exists(q)), candidates[0])
print("logo 源：", os.path.abspath(src))
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
