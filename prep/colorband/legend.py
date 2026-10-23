"""第 3 步：图例色块定位(CV)，并与 AI 读出的数字配对。

图例 = 一列(或一行)大小相近、排列整齐的纯色矩形。先找"均匀且近似矩形"的连通块，再按对齐关系
分组，取成员最多的一组。数字由大模型读(给出每个数字在图上的位置)，这里按位置换算到色块上。
"""
import numpy as np
from PIL import Image
from scipy import ndimage

from .core import hex_of, srgb_to_lab

LEGEND_PX = 3_000_000


def find_swatches(img: Image.Image, map_box=None):
    """返回 {"axis": "v"|"h", "swatches": [{box, color}], "box": [...]}，坐标为原图像素；找不到返回 None。"""
    W0, H0 = img.size
    f = max(1.0, np.sqrt(W0 * H0 / LEGEND_PX))
    W, H = max(1, round(W0 / f)), max(1, round(H0 / f))
    a = np.asarray(img.convert("RGB").resize((W, H), Image.BOX)).astype(np.float32)

    # 均匀像素：5×5 内各通道标准差小
    std = np.zeros((H, W), np.float32)
    for k in range(3):
        m = ndimage.uniform_filter(a[..., k], 5)
        s = ndimage.uniform_filter(a[..., k] ** 2, 5) - m ** 2
        std = np.maximum(std, np.sqrt(np.maximum(s, 0)))
    uni = std < 7.0
    # 与右/下邻居颜色差异大处视为边界，切开相邻的不同色块
    d = np.zeros((H, W), bool)
    dx = np.abs(np.diff(a, axis=1)).max(-1) > 14
    dy = np.abs(np.diff(a, axis=0)).max(-1) > 14
    d[:, :-1] |= dx
    d[:, 1:] |= dx
    d[:-1] |= dy
    d[1:] |= dy
    lab, n = ndimage.label(uni & ~d)
    if n == 0:
        return None
    objs = ndimage.find_objects(lab)
    area = ndimage.sum(np.ones_like(lab), lab, np.arange(1, n + 1))
    cands = []
    for i, sl in enumerate(objs):
        if sl is None:
            continue
        h, w = sl[0].stop - sl[0].start, sl[1].stop - sl[1].start
        ar = area[i]
        # 被边界检测剥掉了一圈，按外扩 1 像素算填充率
        fill = ar / max((h + 2) * (w + 2), 1)
        if ar < 20 or h < 3 or w < 3 or fill < 0.55:
            continue
        if w > 0.25 * W or h > 0.25 * H or ar > 0.01 * W * H:
            continue
        m = lab[sl] == i + 1
        col = np.median(a[sl][m], 0)
        cands.append({"x0": sl[1].start - 1, "y0": sl[0].start - 1, "x1": sl[1].stop + 1, "y1": sl[0].stop + 1,
                      "w": w + 2, "h": h + 2, "rgb": col})
    if len(cands) < 3:
        return None

    best = None
    for axis in ("v", "h"):
        # v：左边对齐、宽度相近，按 y 排成一列；h 对称
        p0, plen, q0, qlen = ("x0", "w", "y0", "h") if axis == "v" else ("y0", "h", "x0", "w")
        cs = sorted(cands, key=lambda c: c[p0])
        used = set()
        for i, c in enumerate(cs):
            if i in used:
                continue
            grp = [j for j, o in enumerate(cs) if abs(o[p0] - c[p0]) <= max(3, 0.15 * c[plen])
                   and abs(o[plen] - c[plen]) <= 0.25 * c[plen] + 2]
            used.update(grp)
            g = sorted((cs[j] for j in grp), key=lambda o: o[q0])
            # 按间距切成若干段，每段内相邻色块的间隙不超过 1.5 个色块
            runs, cur = [], [g[0]]
            for o in g[1:]:
                prev = cur[-1]
                gap = o[q0] - (prev[q0] + prev[qlen])
                similar = 0.4 < o[qlen] / max(prev[qlen], 1) < 2.5
                if gap <= 1.5 * max(prev[qlen], o[qlen]) + 3 and similar and gap > -3:
                    cur.append(o)
                else:
                    runs.append(cur)
                    cur = [o]
            runs.append(cur)
            for r in runs:
                r = _dedupe(r)
                if len(r) < 3:
                    continue
                lab_c = srgb_to_lab(np.array([o["rgb"] for o in r]))
                # 相邻色块必须颜色不同；整组颜色跨度要够大(排除地图里一排同色小方块)
                dif = np.sqrt(((lab_c[1:] - lab_c[:-1]) ** 2).sum(-1))
                if (dif < 4).mean() > 0.2 or np.ptp(lab_c, 0).max() < 25:
                    continue
                score = len(r) + 0.001 * sum(o["w"] * o["h"] for o in r) / (W * H) * 1000
                if best is None or score > best[0]:
                    best = (score, axis, r)
    if best is None:
        return None
    _, axis, r = best
    sw = []
    for o in r:
        box = [int(o["x0"] * W0 / W), int(o["y0"] * H0 / H), int(np.ceil(o["x1"] * W0 / W)), int(np.ceil(o["y1"] * H0 / H))]
        sw.append({"box": box, "color": hex_of(_interior_color(img, box))})
    xs = [b for s in sw for b in (s["box"][0], s["box"][2])]
    ys = [b for s in sw for b in (s["box"][1], s["box"][3])]
    box = [min(xs), min(ys), max(xs), max(ys)]
    if map_box and _inside_ratio(box, map_box) > 0.9 and _area(box) > 0.05 * _area(map_box):
        return None  # 大块区域落在地图主体正中，多半不是图例
    return {"axis": axis, "swatches": sw, "box": box}


def _dedupe(run):
    out = []
    for o in run:
        if out and abs(o["x0"] - out[-1]["x0"]) < 2 and abs(o["y0"] - out[-1]["y0"]) < 2:
            continue
        out.append(o)
    return out


def _interior_color(img, box):
    x0, y0, x1, y1 = box
    dx, dy = max(1, (x1 - x0) // 4), max(1, (y1 - y0) // 4)
    crop = np.asarray(img.crop((x0 + dx, y0 + dy, max(x1 - dx, x0 + dx + 1), max(y1 - dy, y0 + dy + 1))).convert("RGB"))
    return np.median(crop.reshape(-1, 3), 0)


def _area(b):
    return max(b[2] - b[0], 0) * max(b[3] - b[1], 0)


def _inside_ratio(b, outer):
    ix = max(0, min(b[2], outer[2]) - max(b[0], outer[0]))
    iy = max(0, min(b[3], outer[3]) - max(b[1], outer[1]))
    return ix * iy / max(_area(b), 1)


def stops_to_swatch_values(leg, stops, W0, H0):
    """AI 读出的刻度(位置 + 数值，位置为 0-1000 归一化坐标)按位置换算到每个色块中心。

    刻度标在色块中间就是该层的值；标在色块交界处，则色块中心得到的是上下两个刻度的中点。
    返回与 swatches 等长的数值列表(无法换算时为 None)和警告。
    """
    sw = leg["swatches"]
    if len(stops) < 2:
        return [None] * len(sw), []
    v = leg["axis"] == "v"
    centers = np.array([((s["box"][1] + s["box"][3]) / 2) if v else ((s["box"][0] + s["box"][2]) / 2) for s in sw])
    pos = np.array([(s["y"] / 1000 * H0) if v else (s["x"] / 1000 * W0) for s in stops])
    elev = np.array([s["elevation_m"] for s in stops], float)
    # 位置 → 色块序号(可为小数；超出两端线性外推)
    idx = np.arange(len(sw), dtype=float)
    frac = np.interp(pos, centers, idx)
    step = np.diff(centers).mean() if len(centers) > 1 else 1
    frac = np.where(pos < centers[0], (pos - centers[0]) / step, frac)
    frac = np.where(pos > centers[-1], len(sw) - 1 + (pos - centers[-1]) / step, frac)
    o = np.argsort(frac)
    frac, elev = frac[o], elev[o]
    warn = []
    dif = np.diff(elev)
    if not (np.all(dif > 0) or np.all(dif < 0)):
        warn.append("图例刻度按位置排列后数值不单调，读数可能有误")
        return [None] * len(sw), warn
    vals = np.interp(idx, frac, elev)
    # 超出刻度范围的色块不外推(最高/最低一档多为"5000 以上"之类)，用相邻间距估计
    out = []
    for i, x in enumerate(vals):
        if i < frac[0] - 0.6 or i > frac[-1] + 0.6:
            out.append(None)
        else:
            out.append(float(x))
    return out, warn


def sample_stop_colors(img, stops):
    """连续渐变图例：在 AI 给出的刻度位置取色(5×5 中位数)。"""
    W0, H0 = img.size
    a = np.asarray(img.convert("RGB"))
    out = []
    for s in stops:
        x, y = int(s["x"] / 1000 * W0), int(s["y"] / 1000 * H0)
        if not (0 <= x < W0 and 0 <= y < H0):
            continue
        p = a[max(y - 2, 0):y + 3, max(x - 2, 0):x + 3].reshape(-1, 3)
        out.append((np.median(p, 0), float(s["elevation_m"])))
    return out
