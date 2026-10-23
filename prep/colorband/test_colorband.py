"""色带自动识别回归测试：python prep/colorband/test_colorband.py

合成一张"真实感"的分层设色图(晕渲、JPG 压缩、经纬网、文字笔画、河流、右侧图例)与对应的参考 DEM，
分别检验：拓扑排序(无图例无参考)、图例 + AI 刻度、参考 DEM 校准 + 海岸线配准；
另用内置欧亚图(连续色带 + 重晕渲)检验排序与已知色带的一致性。
"""
import json
import sys
import tempfile
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw
from scipy import ndimage

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from colorband import cli, core  # noqa: E402

BREAKS = [0, 200, 500, 1000, 1500, 2000, 3000, 4000]
LAND = ["#1f7a3a", "#6aa84f", "#c9d97a", "#f1d27a", "#e0a050", "#b86a2a", "#8c4a2a", "#f0f0f0"]
SEA = ["#a8d0f0", "#5a8fd0"]  # 0~-200，<-200
TRUE_MID = [100, 350, 750, 1250, 1750, 2500, 3500, 4500]
BOUNDS = {"west": 20.0, "east": 60.0, "south": 10.0, "north": 40.0}
REF = {"west": 10.0, "north": 50.0, "res": 0.05, "nx": 1200, "ny": 1000}


def synth_dem():
    """参考网格上的合成地形(米)。"""
    rng = np.random.default_rng(7)
    ny, nx = REF["ny"], REF["nx"]
    lat = REF["north"] - (np.arange(ny) + 0.5) * REF["res"]
    lon = REF["west"] + (np.arange(nx) + 0.5) * REF["res"]
    LON, LAT = np.meshgrid(lon, lat)
    r = np.hypot((LON - 40) / 15, (LAT - 25) / 10)
    noise = sum(ndimage.gaussian_filter(rng.standard_normal((ny, nx)), s) * s for s in (40, 15, 5))
    noise /= noise.std()
    z = 2200 * (1 - r ** 1.6) + 600 * noise - 400
    z += 3500 * np.exp(-((LON - 46) / 1.8) ** 2 - ((LAT - 27) / 6) ** 2)  # 一条南北向山脉
    return z.astype(np.float32)


def render(z, bounds, path, legend=True, hillshade=True, lines=True, quality=80, sea=None):
    W, H = 1400, 1000
    xs = (np.arange(W) + 0.5) / W * (bounds["east"] - bounds["west"]) + bounds["west"]
    ys = bounds["north"] - (np.arange(H) + 0.5) / H * (bounds["north"] - bounds["south"])
    X, Y = np.meshgrid((xs - REF["west"]) / REF["res"] - 0.5, (REF["north"] - ys) / REF["res"] - 0.5)
    e = ndimage.map_coordinates(z, [Y, X], order=1)
    cols = np.array([core.rgb_of(c) for c in LAND])
    idx = np.clip(np.searchsorted(BREAKS, e, side="right") - 1, 0, len(LAND) - 1)
    img = cols[idx]
    img[e < 0] = core.rgb_of(SEA[0])
    img[e < -200] = core.rgb_of(SEA[1])
    if sea:  # 非蓝色的海(如白色)
        img[e < 0] = core.rgb_of(sea)
    if hillshade:
        gy, gx = np.gradient(ndimage.gaussian_filter(e, 1.5))
        shade = np.clip(1 - (gx - gy) / 260, 0.72, 1.12)
        shade[e < 0] = 1
        img = img * shade[..., None]
    img = np.clip(img, 0, 255).astype(np.uint8)
    pad = 260 if legend else 0
    canvas = Image.new("RGB", (W + pad, H), (255, 255, 255))
    canvas.paste(Image.fromarray(img), (0, 0))
    dr = ImageDraw.Draw(canvas)
    if lines:
        for lon in range(20, 61, 5):
            x = (lon - bounds["west"]) / (bounds["east"] - bounds["west"]) * W
            dr.line([(x, 0), (x, H)], fill=(70, 70, 70), width=1)
        for lat in range(10, 41, 5):
            y = (bounds["north"] - lat) / (bounds["north"] - bounds["south"]) * H
            dr.line([(0, y), (W, y)], fill=(70, 70, 70), width=1)
        rng = np.random.default_rng(1)
        for _ in range(40):  # 地名
            x, y = rng.integers(50, W - 120), rng.integers(30, H - 30)
            dr.text((x, y), "Cityname", fill=(10, 10, 10))
        for _ in range(6):  # 河流
            x, y = rng.integers(300, 1100), rng.integers(200, 800)
            pts = [(x, y)]
            for _ in range(30):
                x, y = x + rng.integers(-12, 13), y + rng.integers(4, 14)
                pts.append((x, y))
            dr.line(pts, fill=(60, 120, 220), width=2)
    stops = []
    if legend:
        x0, y0, sw, sh = W + 40, 120, 46, 30
        items = list(zip(LAND, BREAKS))[::-1] + [(SEA[0], -200), (SEA[1], None)]
        for i, (c, lo) in enumerate(items):
            y = y0 + i * (sh + 6)
            dr.rectangle([x0, y, x0 + sw, y + sh], fill=c, outline=(40, 40, 40))
            if lo is not None:
                # 刻度标在色块下沿(层的下界)，模拟常见的"区间"图例
                dr.text((x0 + sw + 10, y + sh - 2), f"{lo} m", fill=(0, 0, 0))
                stops.append({"x": (x0 + sw + 30) / (W + pad) * 1000, "y": (y + sh + 3) / H * 1000, "elevation_m": lo})
    canvas.save(path, quality=quality)
    return stops


def check_order(res, label):
    json.loads(cli.dumps(res))  # 结果必须能序列化(Go 端读的是 JSON)
    strict = json.dumps(res, ensure_ascii=False, default=lambda o: (_ for _ in ()).throw(TypeError(type(o))))
    assert strict
    land = [L for L in res["layers"] if not L["water"]]
    truth = np.array([core.srgb_to_lab(core.rgb_of(c)[None])[0] for c in LAND])
    ti = []
    for L in land:
        f = core.srgb_to_lab(core.rgb_of(L["color"])[None])[0]
        ti.append(int(np.sqrt(((truth - f) ** 2).sum(-1)).argmin()))
    ok = all(b >= a for a, b in zip(ti, ti[1:]))
    print(f"[{label}] 陆地 {len(land)} 层，对应真值序号 {ti}，链一致性 {res['chainConsistency']}，方向依据 {res['direction']}")
    assert ok, f"{label}: 顺序错误 {ti}"
    assert set(ti) == set(range(len(LAND))), f"{label}: 漏层 {sorted(set(ti))}"
    return land, ti


def main():
    tmp = Path(tempfile.mkdtemp())
    z = synth_dem()
    np.savez(tmp / "ref.npz", z=z.astype(np.int16), west=REF["west"], north=REF["north"], res=REF["res"])

    # A：只有图(带图例但不读数字、无参考 DEM)→ 拓扑排序 + 图例色块定位
    img = tmp / "a.jpg"
    stops = render(z, BOUNDS, img)
    res = cli.run(str(img), {"refdem": False, "preview": False})
    land, ti = check_order(res, "A 拓扑")
    assert res["legendDetected"] and len(res["legendDetected"]["swatches"]) == 10, res["legendDetected"]
    assert res["mode"] == "topology"
    print("    图例色块", len(res["legendDetected"]["swatches"]), "个，排列一致性", res["legendDetected"]["orderAgreement"])

    # B：加上 AI 读出的刻度 → 数值来自图例
    res = cli.run(str(img), {"refdem": False, "preview": False, "stops": stops})
    land, ti = check_order(res, "B 图例")
    for L, t in zip(land, ti):
        if t < len(LAND) - 1:  # 最高一档("4000 以上")无上界，不比较
            assert abs(L["elevation"] - TRUE_MID[t]) <= 0.08 * TRUE_MID[t] + 10, (L, TRUE_MID[t])
    print("    数值", [L["elevation"] for L in land], "来源", sorted({L["source"] for L in land}))

    # C：无图例 + 参考 DEM + 有偏差的经纬度范围 → 海岸线配准 + 交界线阈值
    img = tmp / "c.jpg"
    render(z, BOUNDS, img, legend=False)
    off = {"west": 21.0, "east": 61.6, "south": 10.6, "north": 40.4}
    res = cli.run(str(img), {"refdem": str(tmp / "ref.npz"), "bounds": off, "preview": False})
    land, ti = check_order(res, "C 参考DEM")
    rb = res.get("bounds")
    print("    配准", res["registration"], "数值", [L["elevation"] for L in land])
    assert rb and all(abs(rb[k] - BOUNDS[k]) < 0.5 for k in BOUNDS), rb
    for L, t in zip(land, ti):
        if t < len(LAND) - 1:
            assert abs(L["elevation"] - TRUE_MID[t]) <= 0.2 * TRUE_MID[t] + 40, (L, TRUE_MID[t])

    # F：三步合用——带图例的整图 + AI 给的地图主体框、刻度、有偏差的范围 + 参考 DEM
    img = tmp / "a.jpg"
    res = cli.run(str(img), {"refdem": str(tmp / "ref.npz"), "bounds": off, "mapBox": [0, 0, 1400, 1000],
                             "stops": stops, "preview": False})
    land, ti = check_order(res, "F 合用")
    rb = res.get("bounds")
    print("    配准", res["registration"], "模式", res["mode"], "警告", res["warnings"])
    assert res["mode"] == "legend" and rb and all(abs(rb[k] - BOUNDS[k]) < 0.5 for k in BOUNDS), rb

    # D：精确色 PNG(无晕渲、无抗锯齿)
    img = tmp / "d.png"
    render(z, BOUNDS, img, legend=False, hillshade=False, lines=False)
    res = cli.run(str(img), {"refdem": False, "preview": False})
    assert res["exact"], "应识别为精确色"
    check_order(res, "D 精确色")

    # G：平涂分层图 + 浏览器默认 JPG(色度抽样在相邻两层之间生成过渡色带)
    img = tmp / "g.jpg"
    Wg, Hg = 900, 600
    yy, xx = np.mgrid[0:Hg, 0:Wg].astype(float)
    e = 1 - np.hypot((xx - 450) / 330, (yy - 300) / 220) + 0.08 * np.sin(xx / 37) * np.cos(yy / 29) + 0.05 * np.sin(xx / 11 + yy / 17)
    k = np.where(e < -0.15, -2, np.where(e < 0, -1, np.minimum(7, np.floor(np.maximum(e, 0) / 0.11)))).astype(int)
    cols = np.array([core.rgb_of(c) for c in LAND + SEA[::-1]])  # 下标 -1 = 浅海，-2 = 深海
    sh = np.where(k >= 0, 1 + 0.12 * np.sin(xx / 9) * np.cos(yy / 7), 1)
    Image.fromarray(np.clip(cols[k] * sh[..., None], 0, 255).astype(np.uint8)).save(img, quality=85)
    res = cli.run(str(img), {"refdem": False, "preview": False})
    check_order(res, "G 过渡色")

    # H：白色的海 + 精确色 PNG + 图例(蓝色在下端)：背景色识别为水、方向按图例
    img = tmp / "h.png"
    render(z, BOUNDS, img, hillshade=False, lines=False, sea="#ffffff")
    res = cli.run(str(img), {"refdem": False, "preview": False})
    check_order(res, "H 白色海")
    assert res["water"] == ["#ffffff"], res["water"]
    print("    水体", res["water"], "方向依据", res["direction"])

    # E：内置欧亚图(连续色带 + 重晕渲)，与 map.json 的已知色带比较排序
    eu = ROOT.parent / "maps" / "eurasia"
    if (eu / "map.jpg").exists():
        sys.path.insert(0, str(ROOT))
        from mapprep.terrain import _ramp_lut
        known = json.loads((eu / "map.json").read_text(encoding="utf-8"))["elevation"]["legend"]
        lut = _ramp_lut(known)
        res = cli.run(str(eu / "map.jpg"), {"refdem": False, "preview": False})
        land = [L for L in res["layers"] if not L["water"]]
        true_e = [lut[tuple((core.rgb_of(L["color"]) // 8).astype(int))] for L in land]
        from colorband.refdem import _spearman
        rho = _spearman(np.arange(len(true_e)), true_e)
        print(f"[E 欧亚] 陆地 {len(land)} 层，按已知色带换算的海拔 {np.round(true_e).astype(int).tolist()}，"
              f"秩相关 {rho:.3f}，链一致性 {res['chainConsistency']}，用时 {res['elapsedSec']}s")
        assert rho > 0.85, rho
    print("全部通过")


if __name__ == "__main__":
    main()
