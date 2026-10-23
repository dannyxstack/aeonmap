"""色带自动识别入口(由 Go 调用)：

    python prep/colorband/cli.py --image map.jpg --params '{"bounds": {...}, "stops": [...], ...}'

params(均可选)：
  bounds       {"west","east","south","north"}，等距经纬度范围；有参考 DEM 时用于校准
  controlPoints [[px,py,lon,lat],...] 非等距图的控制点(替代 bounds，仅内部测试用)
  mapBox / legendBox  [x0,y0,x1,y1] 原图像素，通常来自 AI 识图
  stops        AI 读出的图例刻度 [{"x","y","elevation_m"}]，x/y 为 0-1000 归一化坐标
  refdem       参考 DEM 路径(默认 MAPSIM_REFDEM 或 data/refdem/etopo.npz)；false 表示不用
  refine       是否用海岸线微调范围(默认 true)
  preview      是否返回分类预览图(默认 true)
标准输出一行 JSON 结果。
"""
import argparse
import base64
import io
import json
import sys
import time
from pathlib import Path

import numpy as np
from PIL import Image

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from colorband import core, legend as lg, refdem  # noqa: E402


def _pad(b, W, H, frac=0.04):
    px = int(max(W, H) * frac)
    return [max(b[0] - px, 0), max(b[1] - px, 0), min(b[2] + px, W), min(b[3] + px, H)]


def _box_bounds(b, box, W0, H0, to_full):
    """经纬度范围在"地图主体框"与"整张图"之间换算(等距经纬度，线性外推)。"""
    bw, bh = box[2] - box[0], box[3] - box[1]
    if to_full:
        sx, sy = (b["east"] - b["west"]) / bw, (b["north"] - b["south"]) / bh
        return {"west": b["west"] - box[0] * sx, "east": b["west"] + (W0 - box[0]) * sx,
                "north": b["north"] + box[1] * sy, "south": b["north"] - (H0 - box[1]) * sy}
    sx, sy = (b["east"] - b["west"]) / W0, (b["north"] - b["south"]) / H0
    return {"west": b["west"] + box[0] * sx, "east": b["west"] + box[2] * sx,
            "north": b["north"] - box[1] * sy, "south": b["north"] - box[3] * sy}


def run(image, p):
    t0 = time.time()
    warnings = []
    work = core.Work(image)
    W0, H0 = work.W0, work.H0
    stops = [s for s in (p.get("stops") or []) if {"x", "y", "elevation_m"} <= set(s)]

    # ---- 3a. 图例色块(先找，地图分析时排除图例区域) ----
    leg = lg.find_swatches(work.full, p.get("mapBox"))
    exclude = []
    if leg:
        exclude.append(_pad(leg["box"], W0, H0))
    if p.get("legendBox"):
        exclude.append(_pad(p["legendBox"], W0, H0, 0.01))

    # ---- 1. 聚类 + 拓扑排序 ----
    valid = core.region_mask(work, p.get("mapBox"), exclude)
    labels, centers = core.cluster(work, valid)
    info = core.describe(work, labels, centers)
    discrete = work.exact or core.is_discrete(info)
    labels, centers, info, dropped = core.drop_noise(labels, centers, info, exact=work.exact)
    K = len(centers)
    if not work.exact:
        labels = core.majority_filter(labels, K)
        info = core.describe(work, labels, centers)
    for it in range(6):
        Wadj = core.adjacency(labels, K)
        bg_water = [c for c in core.background_water(labels, valid, info) if not core.is_water_rgb(info[c]["rgb"])]
        water = [c for c in range(K) if core.is_water_rgb(info[c]["rgb"])] + bg_water
        land = [c for c in range(K) if c not in water]
        path, chain, per, how = core.order_layers(Wadj, centers, land, water, [i["rgb"] for i in info])
        merged = False
        if not work.exact:  # 精确色没有晕渲，色度相近的也是不同的层
            labels, centers, merged = core.merge_shaded(labels, centers, path)
        if not merged:
            more = []
            if discrete:
                labels, centers, info, more = core.drop_noise(labels, centers, info, strict=True, exact=work.exact)
            if not more:
                break
            dropped += more
            K = len(centers)
            continue
        K = len(centers)
        info = core.describe(work, labels, centers)
    if not land:
        raise SystemExit("图上没有识别出陆地颜色")
    if bg_water:
        warnings.append("把占据地图外缘的浅色(" + "、".join(core.hex_of(info[c]["rgb"]) for c in bg_water)
                        + ")当作海与同色的湖；上传时会作为水体颜色一并提交")
    if chain < 0.6:
        warnings.append(f"颜色之间的邻接关系不是单一链条(一致性 {chain:.0%})，顺序可能有误：地图可能叠加了晕渲、或不是分层设色")

    color = {c: info[c]["rgb"] for c in range(K)}
    src = {}      # 层 → (数值, 来源)
    swatch_of = {}

    # ---- 3b. 图例色块 ↔ 地图颜色，配上 AI 读数 ----
    legend_info = None
    if leg:
        sw_lab = core.feature(np.array([core.rgb_of(s["color"]) for s in leg["swatches"]]))
        d = np.sqrt(((sw_lab[:, None] - centers[None]) ** 2).sum(-1))
        tol = 4 if work.exact else 15  # 精确色图的图例色与地图色应完全一致
        match = [int(d[i].argmin()) if d[i].min() < tol else None for i in range(len(sw_lab))]
        vals, w = lg.stops_to_swatch_values(leg, stops, W0, H0) if stops else ([None] * len(match), [])
        warnings += w
        for i, c in enumerate(match):
            if c is None:
                continue
            swatch_of.setdefault(c, []).append(i)
            if c in land and vals[i] is not None:
                src.setdefault(c, []).append(vals[i])
        for c, ids in swatch_of.items():
            best = min(ids, key=lambda i: d[i, c])
            color[c] = core.rgb_of(leg["swatches"][best]["color"])  # 图例色是不带晕渲的纯色
        # 图例的排列本身就是高低顺序：水色(蓝)所在的一端为低；没有水色时竖排下低上高、横排左低右高
        n_sw = len(leg["swatches"])
        wet = [i for i, s in enumerate(leg["swatches"]) if core.is_water_rgb(core.rgb_of(s["color"]))]
        if wet:
            low_first = np.mean(wet) < (n_sw - 1) / 2
        else:
            low_first = leg["axis"] == "h"
        rank = {}
        for i, c in enumerate(match):
            if c in land:
                rank.setdefault(c, []).append(i if low_first else n_sw - 1 - i)
        rank = {c: float(np.mean(v)) for c, v in rank.items()}
        pos = {c: k for k, c in enumerate(path)}
        seq = [(rank[c], pos[c]) for c in rank]
        agree = None
        if len(seq) >= 2:
            rho = refdem._spearman([a for a, _ in seq], [b for _, b in seq]) if len(seq) >= 3 else \
                (1.0 if (seq[0][0] - seq[1][0]) * (seq[0][1] - seq[1][1]) > 0 else -1.0)
            if len(rank) == len(land) and rho < 0.999:
                path = sorted(land, key=lambda c: rank[c])  # 图例覆盖了全部陆地颜色：直接按图例排
                how = "legend"
            elif rho < 0:
                path = path[::-1]
                how = "legend"
            elif how == "color":
                how = "legend"
            agree = abs(rho)
            if agree < 0.7:
                warnings.append(f"图例色块的排列顺序与地图上的邻接顺序不一致(相关 {agree:.2f})，已按图例方向")
        legend_info = {"box": leg["box"], "axis": leg["axis"], "swatches": [
            {**s, "layer": match[i], "value": vals[i]} for i, s in enumerate(leg["swatches"])],
            "orderAgreement": agree}
        unmatched = sum(1 for c in match if c is None)
        if unmatched:
            warnings.append(f"图例有 {unmatched} 个色块在地图上没有出现(或面积太小)")
    elif stops:
        # 连续渐变图例：刻度位置取色 → 最近的地图颜色
        for rgb, e in lg.sample_stop_colors(work.full, stops):
            f = core.feature(rgb[None])[0]
            dd = np.sqrt(((centers - f) ** 2).sum(-1))
            c = int(dd.argmin())
            if dd[c] < 20 and c in land:
                src.setdefault(c, []).append(e)
    if how == "color":
        warnings.append("图上没有海，高低方向按配色习惯(绿低、灰白高)判断，请核对")
    legend_vals = {c: float(np.mean(v)) for c, v in src.items()}
    if stops and not legend_vals:
        warnings.append("AI 读出了图例刻度，但没能对应到地图上的颜色")

    # ---- 2. 参考 DEM 校准 ----
    dem = None
    bounds = p.get("bounds")
    reg = None
    ref_path = p.get("refdem")
    if ref_path is None:
        ref_path = refdem.default_path()
    if ref_path and (bounds or p.get("controlPoints")):
        ref = refdem.RefDEM(ref_path)
        landmask = valid & np.isin(labels, land)
        if p.get("controlPoints"):
            from mapprep.georef import ControlPoints
            cp = ControlPoints(p["controlPoints"], 3)
            ys, xs = np.mgrid[0:work.H, 0:work.W]
            lon, lat = cp.px_to_geo(((xs + 0.5) * work.sx - 0.5).ravel(), ((ys + 0.5) * work.sy - 0.5).ravel())
            lon, lat = lon.reshape(work.H, work.W), lat.reshape(work.H, work.W)
            iou = refdem._iou(landmask, ref.sample(lon, lat) > 0, valid)
            reg = {"iou": round(float(iou), 3), "refined": False}
        else:
            # 给了地图主体框时，bounds 指的是框内范围；换算到整张图再配准
            box = p.get("mapBox")
            full = _box_bounds(bounds, box, W0, H0, True) if box else bounds
            nb, s0, s1 = full, None, None
            if p.get("refine", True):
                nb, s0, s1 = refdem.register(ref, landmask, valid, full)
                if s1 < s0 + 0.01:
                    nb = full
            lon, lat = refdem.equirect_lonlat(nb, work.W, work.H)
            iou = refdem._iou(landmask, ref.sample(lon, lat) > 0, valid)
            reg = {"iou": round(float(iou), 3), "iouBefore": round(float(s0), 3) if s0 is not None else None,
                   "refined": nb is not full}
            if nb is not full:
                nb = _box_bounds(nb, box, W0, H0, False) if box else nb
                reg["bounds"] = {k: round(float(v), 3) for k, v in nb.items()}
                warnings.append("按海岸线吻合程度微调了经纬度范围，请核对")
        if iou < 0.35:
            warnings.append(f"地图海陆与参考高程数据对不上(吻合度 {iou:.0%})，经纬度范围可能有误，已跳过参考高程校准")
        else:
            if iou < 0.6:
                warnings.append(f"地图海陆与参考高程的吻合度偏低({iou:.0%})，校准结果仅供参考")
            z = ref.sample(lon, lat)
            lab_v = np.where(valid, labels, -1)
            cal = refdem.calibrate(z, lab_v, path, water)
            if cal["spearman"] < -0.5:  # 参考高程说方向反了：以它为准
                path = path[::-1]
                cal = refdem.calibrate(z, lab_v, path, water)
                how = "dem"
            t = [core.snap_nice(x) if x > 0 else float(round(x)) for x in cal["thresholds"]]
            for i in range(1, len(t)):
                t[i] = max(t[i], t[i - 1] + 1)
            top = cal["q"].get(path[-1], [0, 0, 0])[1]
            vals = core.values_from_thresholds(t, top)
            dem = {"values": dict(zip(path, vals)), "thresholds": t, "raw": cal["raw"], "q": cal["q"],
                   "coast": cal["coast"], "spearman": cal["spearman"], "iou": iou}
            if cal["coast"] is not None and abs(cal["coast"]) > 300:
                warnings.append(f"海岸线处参考高程中位数为 {cal['coast']:.0f} m，范围可能有偏移")
            if legend_vals:
                bad = [c for c, v in legend_vals.items() if c in dem["values"]
                       and abs(v - dem["values"][c]) > 0.35 * max(abs(v), 200)]
                if len(bad) > len(legend_vals) / 3:
                    warnings.append("图例读数与参考高程差异较大，请核对图例或经纬度范围")

    # ---- 融合 ----
    n = len(path)
    val = [None] * n
    source = [None] * n
    for k, c in enumerate(path):
        if c in legend_vals:
            val[k], source[k] = legend_vals[c], "legend"
        elif dem:
            val[k], source[k] = float(dem["values"][c]), "dem"
    known = [k for k in range(n) if val[k] is not None]
    if not known:
        t = core.template_thresholds(n)
        for k, v in enumerate(core.values_from_thresholds(t)):
            val[k], source[k] = float(v), "template"
        warnings.append("没有图例读数和参考高程，数值按常见分级模板估计(相对高低可靠，绝对米数仅供参考)")
    elif len(known) < n:
        kv = np.array([val[k] for k in known])
        for k in range(n):
            if val[k] is None:
                if len(known) >= 2:
                    val[k] = float(np.interp(k, known, kv, left=np.nan, right=np.nan))
                    if np.isnan(val[k]):  # 两端外推：沿用相邻间距
                        a, b = (known[0], known[1]) if k < known[0] else (known[-2], known[-1])
                        step = (val[b] - val[a]) / max(b - a, 1)
                        ref_k = known[0] if k < known[0] else known[-1]
                        val[k] = float(val[ref_k] + step * (k - ref_k))
                else:
                    val[k] = float(val[known[0]] + 300 * (k - known[0]))
                source[k] = "interp"
    w = [{"legend": 4, "dem": 2}.get(s, 1) for s in source]
    mono = refdem._isotonic(val, w)
    if np.abs(mono - np.array(val)).max() > 1:
        warnings.append("部分层的数值与高低顺序冲突，已按顺序做了平滑")
    val = [float(round(v)) for v in mono]
    for k in range(1, n):
        if val[k] <= val[k - 1]:
            val[k] = val[k - 1] + 1

    # 置信度：来源 × 该层在链上的一致性
    base = {"legend": 0.9, "dem": 0.5 + 0.4 * min((dem or {}).get("iou", 0), 1), "interp": 0.5, "template": 0.3}
    layers = []
    for k, c in enumerate(path):
        lo = val[0] - (val[1] - val[0]) / 2 if k == 0 and n > 1 else ((val[k - 1] + val[k]) / 2 if k else 0)
        hi = (val[k] + val[k + 1]) / 2 if k + 1 < n else val[k] + ((val[k] - val[k - 1]) / 2 if k else 200)
        conf = base[source[k]] * (0.6 + 0.4 * per.get(c, 0))
        L = {"color": core.hex_of(color[c]), "water": False, "elevation": val[k], "range": [round(max(lo, 0)), round(hi)],
             "source": source[k], "confidence": round(conf, 2), "area": round(info[c]["area"], 4),
             "swatch": c in swatch_of, "_id": c}
        if dem and c in dem["q"]:
            L["dem"] = [round(x) for x in dem["q"][c]]
        layers.append(L)
    # 水体：浅色为浅水
    wl = sorted(water, key=lambda c: -core.srgb_to_lab(info[c]["rgb"][None])[0, 0])
    for k, c in enumerate(wl):
        e = core.WATER_TEMPLATE[min(k, len(core.WATER_TEMPLATE) - 1)]
        s = "template"
        if dem and c in dem["q"] and dem["q"][c][1] < 0:
            e, s = round(dem["q"][c][1]), "dem"
        layers.append({"color": core.hex_of(color[c]), "water": True, "elevation": e, "source": s,
                       "confidence": 0.5, "area": round(info[c]["area"], 4), "swatch": c in swatch_of, "_id": c})

    land_layers = [L for L in layers if not L["water"]]
    tot = sum(L["area"] for L in land_layers) or 1
    confidence = sum(L["confidence"] * L["area"] for L in land_layers) / tot
    mode = "legend" if any(s == "legend" for s in source) else ("dem" if dem else "topology")

    out = {
        "input": "hypsometric",
        "legend": [[L["color"], L["elevation"]] for L in land_layers],
        "waterColors": [L["color"] for L in layers if L["water"]],
        # 预处理只认蓝色为水；非蓝色的水体颜色需要作为 elevation.water 一并提交
        "water": [L["color"] for L in layers if L["water"] and not core.is_water_rgb(core.rgb_of(L["color"]))],
        "layers": layers,
        "mode": mode,
        "confidence": round(confidence, 2),
        "chainConsistency": round(chain, 3),
        "direction": how,
        "exact": bool(work.exact),
        "discrete": discrete,
        "droppedColors": dropped,
        "legendDetected": legend_info,
        "registration": reg,
        "refdem": bool(dem),
        "size": [W0, H0],
        "warnings": warnings,
    }
    if reg and reg.get("bounds"):
        out["bounds"] = reg["bounds"]
    if p.get("preview", True):
        out["preview"] = preview(work, labels, valid, color)
    for L in layers:
        L.pop("_id")
    out["elapsedSec"] = round(time.time() - t0, 2)
    return out


def preview(work, labels, valid, color):
    """分类结果：每类涂成代表色，层与层的交界画深色细线，排除区域为灰色。"""
    K = int(labels.max()) + 1
    pal = np.array([color[c] for c in range(K)], np.uint8)
    img = pal[np.maximum(labels, 0)]
    edge = np.zeros(labels.shape, bool)
    edge[:, 1:] |= labels[:, 1:] != labels[:, :-1]
    edge[1:] |= labels[1:] != labels[:-1]
    img[edge] = (img[edge] * 0.45).astype(np.uint8)
    img[~valid] = (img[~valid] * 0.25 + 150).astype(np.uint8)
    im = Image.fromarray(img)
    if im.width > 900:
        im = im.resize((900, max(1, round(im.height * 900 / im.width))), Image.NEAREST)
    buf = io.BytesIO()
    im.save(buf, "PNG", optimize=True)
    return "data:image/png;base64," + base64.b64encode(buf.getvalue()).decode()


def _np_default(o):
    """numpy 标量/数组 → Python 原生类型(结果里难免混入 np.bool_、np.float64 等)。"""
    if isinstance(o, np.generic):
        return o.item()
    if isinstance(o, np.ndarray):
        return o.tolist()
    raise TypeError(f"Object of type {o.__class__.__name__} is not JSON serializable")


def dumps(res):
    return json.dumps(res, ensure_ascii=False, default=_np_default)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--image", required=True)
    ap.add_argument("--params", default="{}")
    a = ap.parse_args()
    try:
        res = run(a.image, json.loads(a.params))
    except SystemExit as e:
        print(json.dumps({"error": str(e)}, ensure_ascii=False))
        sys.exit(2)
    print(dumps(res))


if __name__ == "__main__":
    main()
