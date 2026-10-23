"""第 2 步：用公开 DEM(ETOPO)校准色带，并用海岸线微调经纬度范围。

参考数据为 npz：z(int16，米，行从北到南)、west、north、res(度)。用 fetch_refdem.py 生成。
两层颜色的交界线就是一条等高线：交界处参考高程的中位数即分级阈值，不受晕渲与配准小误差影响。
"""
from pathlib import Path

import numpy as np


class RefDEM:
    def __init__(self, path):
        d = np.load(path)
        self.z = d["z"]
        self.west, self.north, self.res = float(d["west"]), float(d["north"]), float(d["res"])
        self.ny, self.nx = self.z.shape
        self.global_lon = abs(self.nx * self.res - 360) < self.res * 1.5

    def sample(self, lon, lat):
        """双线性取样；全球数据按经度回绕。"""
        x = (np.asarray(lon, float) - self.west) / self.res - 0.5
        y = (self.north - np.asarray(lat, float)) / self.res - 0.5
        x0, y0 = np.floor(x).astype(np.int64), np.floor(y).astype(np.int64)
        fx, fy = x - x0, y - y0
        y0c, y1c = np.clip(y0, 0, self.ny - 1), np.clip(y0 + 1, 0, self.ny - 1)
        if self.global_lon:
            x0c, x1c = x0 % self.nx, (x0 + 1) % self.nx
        else:
            x0c, x1c = np.clip(x0, 0, self.nx - 1), np.clip(x0 + 1, 0, self.nx - 1)
        z = self.z
        v = (z[y0c, x0c] * (1 - fx) * (1 - fy) + z[y0c, x1c] * fx * (1 - fy)
             + z[y1c, x0c] * (1 - fx) * fy + z[y1c, x1c] * fx * fy)
        return v.astype(np.float32)


def default_path():
    import os
    p = os.environ.get("MAPSIM_REFDEM")
    if p:
        return Path(p)
    root = Path(__file__).resolve().parents[2]
    for c in (root / "data" / "refdem" / "etopo.npz", root / "cache" / "refdem" / "etopo.npz"):
        if c.exists():
            return c
    return None


# ---------------- 坐标 ----------------

def equirect_lonlat(bounds, W, H, stride=1):
    xs = (np.arange(0, W, stride) + 0.5) / W
    ys = (np.arange(0, H, stride) + 0.5) / H
    lon = bounds["west"] + xs * (bounds["east"] - bounds["west"])
    lat = bounds["north"] - ys * (bounds["north"] - bounds["south"])
    return np.meshgrid(lon, lat)


# ---------------- 海岸线配准 ----------------

def _iou(a, b, m):
    inter = (a & b & m).sum()
    union = ((a | b) & m).sum()
    return inter / max(union, 1)


def register(ref, land, valid, bounds, search=0.08):
    """在给定范围附近搜索(平移 ±search、缩放 ±search)，使地图海陆与参考海陆最吻合。

    返回 (最佳范围, IoU_初始, IoU_最佳)。只适用于等距经纬度投影。
    """
    H, W = land.shape
    st = max(1, int(np.sqrt(H * W / 60_000)))
    lm, vm = land[::st, ::st], valid[::st, ::st]
    h, w = lm.shape

    def score(b):
        lon, lat = equirect_lonlat(b, W, H, st)
        lon, lat = lon[:h, :w], lat[:h, :w]
        return _iou(lm, ref.sample(lon, lat) > 0, vm)

    b0 = dict(bounds)
    s0 = score(b0)
    spanx, spany = b0["east"] - b0["west"], b0["north"] - b0["south"]

    def moved(b, dx=0, dy=0, kx=0, ky=0):
        cx, cy = (b["west"] + b["east"]) / 2 + dx * spanx, (b["south"] + b["north"]) / 2 + dy * spany
        hw, hh = (b["east"] - b["west"]) / 2 * (1 + kx), (b["north"] - b["south"]) / 2 * (1 + ky)
        return {"west": cx - hw, "east": cx + hw, "south": cy - hh, "north": cy + hh}

    # 粗搜平移，再在最好的起点做模式搜索(平移 + 缩放)
    best, bs = b0, s0
    g = np.linspace(-search, search, 5)
    for dx in g:
        for dy in g:
            b = moved(b0, dx, dy)
            s = score(b)
            if s > bs:
                best, bs = b, s
    step = search / 4
    while step > 0.002:
        improved = False
        for mv in ((step, 0, 0, 0), (-step, 0, 0, 0), (0, step, 0, 0), (0, -step, 0, 0),
                   (0, 0, step, 0), (0, 0, -step, 0), (0, 0, 0, step), (0, 0, 0, -step),
                   (0, 0, step, step), (0, 0, -step, -step)):
            b = moved(best, *mv)
            if abs(b["east"] - b["west"] - spanx) > search * 1.5 * spanx:
                continue
            s = score(b)
            if s > bs + 1e-4:
                best, bs, improved = b, s, True
        if not improved:
            step /= 2
    return best, float(s0), float(bs)


# ---------------- 校准 ----------------

def _isotonic(y, w):
    """加权保序回归(递增)。"""
    blocks = [[float(v), float(max(wi, 1e-6)), 1] for v, wi in zip(y, w)]
    i = 0
    while i < len(blocks) - 1:
        if blocks[i][0] > blocks[i + 1][0]:
            a, b = blocks[i], blocks[i + 1]
            ww = a[1] + b[1]
            blocks[i] = [(a[0] * a[1] + b[0] * b[1]) / ww, ww, a[2] + b[2]]
            del blocks[i + 1]
            i = max(i - 1, 0)
        else:
            i += 1
    out = []
    for v, _, n in blocks:
        out += [v] * n
    return np.array(out)


def calibrate(z, labels, path, water):
    """z：与 labels 同形状的参考高程。path：陆地层从低到高的顺序。

    返回 dict：每层参考高程分位(q10/q50/q90)、相邻层交界的阈值(原始/保序后)、海岸线处高程(配准误差的指示)。
    """
    K = int(labels.max()) + 1
    flat_l, flat_z = labels.ravel(), z.ravel()
    q = {}
    for c in range(K):
        v = flat_z[flat_l == c]
        if len(v):
            q[c] = [float(x) for x in np.percentile(v, [10, 50, 90])]
    # 相邻像素对：交界处两侧参考高程的均值
    pairs = {}
    for a, b, za, zb in ((labels[:, :-1], labels[:, 1:], z[:, :-1], z[:, 1:]),
                         (labels[:-1, :], labels[1:, :], z[:-1, :], z[1:, :])):
        m = (a != b) & (a >= 0) & (b >= 0)
        key = np.minimum(a[m], b[m]) * K + np.maximum(a[m], b[m])
        zz = (za[m] + zb[m]) / 2
        o = np.argsort(key, kind="stable")
        key, zz = key[o], zz[o]
        u, st = np.unique(key, return_index=True)
        for k, s, e in zip(u, st, list(st[1:]) + [len(key)]):
            pairs.setdefault(int(k), []).append(zz[s:e])
    def boundary(i, j):
        v = pairs.get(min(i, j) * K + max(i, j))
        if not v:
            return None, 0
        v = np.concatenate(v)
        return float(np.median(v)), len(v)

    raw, cnt = [], []
    for k in range(len(path) - 1):
        t, n = boundary(path[k], path[k + 1])
        if t is None or n < 20:  # 交界太短：用两层中位数的中点
            a, b = q.get(path[k], [0, 0, 0])[1], q.get(path[k + 1], [0, 0, 0])[1]
            t, n = (a + b) / 2, 1
        raw.append(t)
        cnt.append(n)
    iso = list(_isotonic(raw, cnt)) if raw else []
    coast = []
    for wc in water:
        for c in path[:3]:
            t, n = boundary(wc, c)
            if t is not None:
                coast += [t] * n
    med = [q.get(c, [0, 0, 0])[1] for c in path]
    rho = _spearman(np.arange(len(med)), med) if len(med) >= 3 else 1.0
    return {"q": q, "raw": raw, "count": cnt, "thresholds": iso, "coast": float(np.median(coast)) if coast else None,
            "spearman": rho}


def _spearman(a, b):
    ra = np.argsort(np.argsort(a))
    rb = np.argsort(np.argsort(b))
    if np.std(ra) == 0 or np.std(rb) == 0:
        return 0.0
    return float(np.corrcoef(ra, rb)[0, 1])
