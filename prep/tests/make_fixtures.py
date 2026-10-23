"""生成测试用地图：南半球合成大陆(16 位高程 PNG)与手绘风格草图。"""
import json
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw
from scipy import ndimage

HERE = Path(__file__).resolve().parent / "fixtures"


def sh_continent():
    d = HERE / "sh_dem"
    d.mkdir(parents=True, exist_ok=True)
    W, H = 1200, 700
    rng = np.random.default_rng(3)
    y, x = np.mgrid[0:H, 0:W]
    cx, cy = W * 0.5, H * 0.5
    r = np.hypot((x - cx) / (W * 0.38), (y - cy) / (H * 0.40))
    noise = sum(ndimage.gaussian_filter(rng.standard_normal((H, W)), s) * (s ** 0.9) for s in (60, 25, 8))
    noise /= noise.std()
    elev = 1600 * (1 - r ** 2) + 500 * noise - 300
    # 东海岸山脉(类似大分水岭)
    elev += 2200 * np.exp(-((x - W * 0.78) / 25.0) ** 2) * (np.abs(y - cy) < H * 0.33)
    lo, hi = -3000.0, 5000.0
    v = np.clip((elev - lo) / (hi - lo) * 65535, 0, 65535).astype(np.uint16)
    Image.fromarray(v).save(d / "dem.png")
    (d / "map.json").write_text(json.dumps({
        "name": "南方合成大陆", "image": "dem.png",
        "bounds": {"west": 110, "east": 160, "south": -45, "north": -10},
        "projection": {"type": "equirectangular"},
        "elevation": {"type": "grayscale", "min": lo, "max": hi, "sea_level": 0}}, ensure_ascii=False, indent=1))


def sketch():
    d = HERE / "sketch"
    d.mkdir(parents=True, exist_ok=True)
    W, H = 1000, 750
    im = Image.new("RGB", (W, H), (40, 90, 200))  # 海
    dr = ImageDraw.Draw(im)
    dr.polygon([(150, 120), (520, 60), (860, 160), (900, 420), (760, 650), (420, 690), (180, 560), (90, 330)],
               fill=(250, 248, 240))  # 陆地(白纸)
    dr.ellipse((560, 380, 640, 440), fill=(40, 90, 200))  # 内陆湖
    dr.line([(300, 180), (420, 260), (520, 250), (640, 200)], fill=(120, 70, 30), width=14)  # 山脉
    dr.line([(700, 300), (760, 420), (720, 560)], fill=(120, 70, 30), width=12)
    dr.ellipse((250, 380, 420, 520), fill=(235, 200, 60))  # 高地
    dr.line([(520, 270), (500, 400), (430, 560), (420, 685)], fill=(0, 220, 230), width=5)  # 河流
    im.save(d / "sketch.png")
    (d / "map.json").write_text(json.dumps({
        "name": "测试草图", "image": "sketch.png",
        "bounds": {"west": -5, "east": 35, "south": 30, "north": 60},
        "projection": {"type": "equirectangular"},
        "elevation": {"type": "sketch", "seed": 1}}, ensure_ascii=False, indent=1))


if __name__ == "__main__":
    sh_continent()
    sketch()
    print("fixtures ->", HERE)
