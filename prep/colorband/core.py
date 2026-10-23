"""第 1 步：预处理、Lab 聚类、邻接拓扑排序。

分层设色的关键结构：每一层只与高一级、低一级的层相邻。把颜色聚成若干类，统计类与类之间的
边界长度，求一条边界权重最大的链，就得到从低到高的顺序；方向由"哪一端贴着海"决定。
连续渐变色带同样适用(聚类相当于把色带切成若干段)。
"""
import numpy as np
from PIL import Image
from scipy import ndimage
from scipy.cluster.vq import kmeans2

WORK_PX = 1_200_000   # 聚类用的工作图最多像素数
L_WEIGHT = 0.35       # 亮度权重：晕渲主要改变亮度，降低它的权重
CHROMA_K = 300.0      # 色度(r/(r+g+b) 等)放大系数：色度不随晕渲的明暗缩放而变
MERGE_DIST = 6.0      # 聚类中心距离小于此值则合并(加权 Lab 单位)
MIN_AREA = 0.002      # 面积占比低于此值的类并入最近的类
EXACT_COLORS = 64     # 主要颜色不超过这么多时按"精确色"处理(调色板 PNG、无抗锯齿的矢量导出图)


# ---------------- 颜色 ----------------

def srgb_to_lab(rgb):
    c = np.asarray(rgb, float) / 255.0
    c = np.where(c > 0.04045, ((c + 0.055) / 1.055) ** 2.4, c / 12.92)
    M = np.array([[0.4124564, 0.3575761, 0.1804375],
                  [0.2126729, 0.7151522, 0.0721750],
                  [0.0193339, 0.1191920, 0.9503041]])
    xyz = c @ M.T / np.array([0.95047, 1.0, 1.08883])
    f = np.where(xyz > 0.008856, np.cbrt(xyz), 7.787 * xyz + 16 / 116)
    L = 116 * f[..., 1] - 16
    a = 500 * (f[..., 0] - f[..., 1])
    b = 200 * (f[..., 1] - f[..., 2])
    return np.stack([L, a, b], -1)


def feature(rgb):
    """聚类特征：[加权亮度, 色度 r, 色度 g]。晕渲近似把 RGB 整体乘一个系数，色度不变。"""
    rgb = np.asarray(rgb, float)
    L = srgb_to_lab(rgb)[..., 0]
    s = rgb.sum(-1) + 6.0
    return np.stack([L * L_WEIGHT, CHROMA_K * rgb[..., 0] / s, CHROMA_K * rgb[..., 1] / s], -1)


def hex_of(rgb):
    r, g, b = (int(round(min(max(v, 0), 255))) for v in rgb)
    return f"#{r:02x}{g:02x}{b:02x}"


def rgb_of(hexs):
    h = hexs.lstrip("#")
    return np.array([int(h[i:i + 2], 16) for i in (0, 2, 4)], float)


def is_water_rgb(rgb):
    """与预处理(terrain._parse_hypsometric)相同的蓝色水体规则，保证识别结果与反推一致。"""
    r, g, b = rgb
    return b > r + 35 and b > g + 8


# ---------------- 读图 ----------------

class Work:
    """工作图：缩小到 WORK_PX 以内，记录与原图的比例。"""

    def __init__(self, path, max_px=WORK_PX):
        im = Image.open(path)
        if im.mode not in ("RGB", "P", "L"):
            im = im.convert("RGBA")
            bg = Image.new("RGB", im.size, (255, 255, 255))
            bg.paste(im, mask=im.split()[3])
            im = bg
        im = im.convert("RGB")
        self.full = im
        self.W0, self.H0 = im.size
        f = max(1.0, np.sqrt(self.W0 * self.H0 / max_px))
        self.W, self.H = max(1, round(self.W0 / f)), max(1, round(self.H0 / f))
        self.sx, self.sy = self.W0 / self.W, self.H0 / self.H

        # 精确色：前 EXACT_COLORS 种颜色覆盖 97% 以上像素
        small = np.asarray(im.resize((min(self.W0, 600), min(self.H0, 600)), Image.NEAREST)).reshape(-1, 3)
        cols, cnt = np.unique(small, axis=0, return_counts=True)
        top = np.argsort(-cnt)[:EXACT_COLORS]
        self.exact = bool(cnt[top].sum() >= 0.97 * cnt.sum())
        if self.exact:
            self.rgb = np.asarray(im.resize((self.W, self.H), Image.NEAREST)).astype(float)
            self.palette = cols[top].astype(float)
        else:
            a = np.asarray(im.resize((self.W, self.H), Image.BOX))
            # 中值滤波去掉 JPG 压缩噪声与零星杂点
            a = np.stack([ndimage.median_filter(a[..., k], size=3) for k in range(3)], -1)
            self.rgb = a.astype(float)
            self.palette = None

    def to_work(self, box):
        """原图像素框 [x0,y0,x1,y1] → 工作图像素框。"""
        x0, y0, x1, y1 = box
        return [int(x0 / self.sx), int(y0 / self.sy), int(np.ceil(x1 / self.sx)), int(np.ceil(y1 / self.sy))]


def region_mask(work, map_box=None, exclude=()):
    """有效区域：地图主体框内、排除图例等区域、去掉四周纯色边框。"""
    H, W = work.H, work.W
    m = np.zeros((H, W), bool)
    if map_box:
        x0, y0, x1, y1 = work.to_work(map_box)
        m[max(y0, 0):min(y1, H), max(x0, 0):min(x1, W)] = True
    else:
        m[:] = True
    for b in exclude:
        x0, y0, x1, y1 = work.to_work(b)
        m[max(y0, 0):min(y1, H), max(x0, 0):min(x1, W)] = False
    # 纯色边框：从四边向内，整行/整列几乎同色就裁掉
    rgb = work.rgb
    def flat(line):
        return line.std(0).max() < 3.0
    top, bot, lef, rig = 0, H, 0, W
    while top < H // 4 and flat(rgb[top]):
        top += 1
    while bot > H * 3 // 4 and flat(rgb[bot - 1]):
        bot -= 1
    while lef < W // 4 and flat(rgb[:, lef]):
        lef += 1
    while rig > W * 3 // 4 and flat(rgb[:, rig - 1]):
        rig -= 1
    m[:top] = m[bot:] = False
    m[:, :lef] = m[:, rig:] = False
    return m


# ---------------- 聚类 ----------------

def _assign(feat, centers, chunk=400_000):
    out = np.empty(len(feat), np.int32)
    for i in range(0, len(feat), chunk):
        f = feat[i:i + chunk]
        d = ((f[:, None, :] - centers[None]) ** 2).sum(-1)
        out[i:i + chunk] = d.argmin(1)
    return out


def _merge_close(centers, counts, thr):
    centers, counts = list(centers), list(counts)
    while len(centers) > 1:
        C = np.array(centers)
        d = np.sqrt(((C[:, None] - C[None]) ** 2).sum(-1))
        np.fill_diagonal(d, np.inf)
        i, j = np.unravel_index(d.argmin(), d.shape)
        if d[i, j] >= thr:
            break
        w = counts[i] + counts[j]
        centers[i] = (centers[i] * counts[i] + centers[j] * counts[j]) / max(w, 1)
        counts[i] = w
        del centers[j], counts[j]
    return np.array(centers), np.array(counts)


def cluster(work, valid, k0=28, seed=0):
    """返回 (标签图[-1=无效], 各类特征中心)。"""
    idx = np.flatnonzero(valid.ravel())
    feat_all = feature(work.rgb).reshape(-1, 3)
    feat = feat_all[idx]
    if work.exact:
        centers = feature(work.palette)
        lab = _assign(feat, centers)
        cnt = np.bincount(lab, minlength=len(centers))
        # 只合并几乎相同的颜色(RGB 差一两个数)；亮度降过权，浅灰与白色在特征空间里也很近
        centers, _ = _merge_close(centers[cnt > 0], cnt[cnt > 0], 0.6)
    else:
        rng = np.random.default_rng(seed)
        samp = feat[rng.choice(len(feat), min(len(feat), 200_000), replace=False)]
        centers, lab = kmeans2(samp, min(k0, len(samp)), minit="++", iter=20, seed=seed)
        cnt = np.bincount(lab, minlength=len(centers))
        centers, cnt = centers[cnt > 0], cnt[cnt > 0]
        centers, _ = _merge_close(centers, cnt, MERGE_DIST)
    # 全图分配 → 去掉过小的类 → 两轮 Lloyd 精修
    for _ in range(3):
        lab = _assign(feat, centers)
        cnt = np.bincount(lab, minlength=len(centers))
        keep = cnt >= MIN_AREA * len(feat)
        if not keep.all():
            centers = centers[keep]
            continue
        if work.exact:
            break
        centers = np.array([feat[lab == c].mean(0) for c in range(len(centers))])
    lab = _assign(feat, centers)
    labels = np.full(work.H * work.W, -1, np.int32)
    labels[idx] = lab
    return labels.reshape(work.H, work.W), centers


def majority_filter(labels, K):
    """3×3 众数滤波：去掉晕渲造成的零星杂点(无效像素保持无效)。"""
    best = np.full(labels.shape, -1.0)
    out = labels.copy()
    for c in range(K):
        s = ndimage.uniform_filter((labels == c).astype(np.float32), 3)
        better = s > best
        out[better] = c
        best[better] = s[better]
    out[labels < 0] = -1
    return out


def adjacency(labels, K):
    W = np.zeros((K, K))
    for a, b in ((labels[:, :-1], labels[:, 1:]), (labels[:-1, :], labels[1:, :])):
        m = (a != b) & (a >= 0) & (b >= 0)
        W += np.bincount(a[m] * K + b[m], minlength=K * K).reshape(K, K)
    return W + W.T


def describe(work, labels, centers):
    """每类：代表色(中位数)、面积、"核心"比例(腐蚀后剩余，线条/文字/过渡色很低)。"""
    K = len(centers)
    rgb = work.rgb.reshape(-1, 3)
    flat = labels.ravel()
    total = (flat >= 0).sum()
    out = []
    for c in range(K):
        m = labels == c
        area = int(m.sum())
        core = int(ndimage.binary_erosion(m).sum())
        out.append({"rgb": np.median(rgb[flat == c], 0) if area else np.zeros(3),
                    "area": area / max(total, 1), "core": core / max(area, 1)})
    return out


def is_discrete(info):
    """平涂的分层图：按面积加权，类的核心比例中位数高。连续色带 + 晕渲的图每一类都很"碎"。"""
    areas = np.array([i["area"] for i in info])
    cores = np.array([i["core"] for i in info])
    o = np.argsort(cores)
    return bool(cores[o][np.searchsorted(np.cumsum(areas[o]), areas.sum() / 2)] > 0.7)


def drop_noise(labels, centers, info, strict=False, exact=False):
    """去掉线条、文字、两层之间的过渡色：核心比例很低的类。被去掉的像素按最近的有效类回填。

    strict：平涂分层图(is_discrete)在合并晕渲拆开的类、众数滤波之后再调用，此时真实的层已经完整，
    可以对过渡色用更严的标准；连续色带的每一段都"落在相邻两段之间"，不能这样做。
    """
    K = len(centers)
    # 平涂的分层图里，JPG 色度抽样会在两层之间生成一条几像素宽的过渡色带：颜色落在两层连线上、核心比例中等
    between_core = 0.6 if strict else 0.3
    drop = []
    for c in range(K):
        core = info[c]["core"]
        between = False
        for i in range(K):
            for j in range(i + 1, K):
                if c in (i, j):
                    continue
                a, b, p = centers[i], centers[j], centers[c]
                t = np.clip((p - a) @ (b - a) / max((b - a) @ (b - a), 1e-9), 0, 1)
                if t > 0.15 and t < 0.85 and np.linalg.norm(a + t * (b - a) - p) < 4.0:
                    between = True
        # 精确色图没有抗锯齿和压缩，不存在过渡色；落在两色之间的就是真实的一层，只剔除线条、文字
        if core < 0.12 or (not exact and core < between_core and between):
            drop.append(c)
    if not drop or len(drop) == K:
        return labels, centers, info, []
    keep = [c for c in range(K) if c not in drop]
    remap = np.full(K, -2, np.int32)
    remap[keep] = np.arange(len(keep))
    new = np.where(labels >= 0, remap[np.maximum(labels, 0)], -1)
    hole = new == -2
    if hole.any():
        _, (iy, ix) = ndimage.distance_transform_edt(new < 0, return_indices=True)
        filled = new[iy, ix]
        new = np.where(hole, filled, new)
    dropped = [hex_of(info[c]["rgb"]) for c in drop]
    return new, centers[keep], [info[c] for c in keep], dropped


def merge_shaded(labels, centers, path, thr=6.5):
    """链上相邻、色度几乎相同(只差明暗)的两类是同一层被晕渲拆开的，合并。返回 (标签图, 中心, 是否合并过)。"""
    groups, cur = [], [path[0]] if path else []
    for a, b in zip(path, path[1:]):
        if np.linalg.norm(centers[a][1:] - centers[b][1:]) < thr:
            cur.append(b)
        else:
            groups.append(cur)
            cur = [b]
    if cur:
        groups.append(cur)
    if all(len(g) == 1 for g in groups):
        return labels, centers, False
    K = len(centers)
    remap = np.arange(K)
    for g in groups:
        remap[g] = g[0]
    used = sorted(set(remap))
    dense = np.full(K, -1)
    dense[used] = np.arange(len(used))
    remap = dense[remap]
    new = np.where(labels >= 0, remap[np.maximum(labels, 0)], -1)
    cnt = np.bincount(labels[labels >= 0], minlength=K).astype(float)
    nc = np.zeros((len(used), centers.shape[1]))
    w = np.zeros(len(used))
    for c in range(K):
        nc[remap[c]] += centers[c] * cnt[c]
        w[remap[c]] += cnt[c]
    return new, nc / np.maximum(w, 1)[:, None], True


def background_water(labels, valid, info, min_border=0.4, min_area=0.1):
    """非蓝色的海：占据地图外缘大部分、面积较大、颜色很浅(白、浅灰、浅青)的类视为水体(海与同色的湖)。"""
    ys, xs = np.nonzero(valid)
    if not len(ys):
        return []
    y0, y1, x0, x1 = ys.min(), ys.max(), xs.min(), xs.max()
    border = np.concatenate([labels[y0, x0:x1 + 1], labels[y1, x0:x1 + 1], labels[y0:y1 + 1, x0], labels[y0:y1 + 1, x1]])
    border = border[border >= 0]
    if not len(border):
        return []
    share = np.bincount(border, minlength=len(info)) / len(border)
    out = []
    for c, i in enumerate(info):
        L, a, b = srgb_to_lab(np.asarray(i["rgb"], float)[None])[0]
        light = L > 88 and np.hypot(a, b) < 15
        if share[c] >= min_border and i["area"] >= min_area and (light or is_water_rgb(i["rgb"])):
            out.append(c)
    return out


# ---------------- 排序 ----------------

def _path_weight(A, p):
    return sum(A[p[i], p[i + 1]] for i in range(len(p) - 1))


def _dp_path(A):
    n = len(A)
    full = 1 << n
    dp = np.full((full, n), -np.inf)
    par = np.full((full, n), -1, np.int32)
    for j in range(n):
        dp[1 << j, j] = 0
    for mask in range(1, full):
        row = dp[mask]
        ends = np.flatnonzero(np.isfinite(row))
        if not len(ends):
            continue
        for k in range(n):
            if mask >> k & 1:
                continue
            cand = row[ends] + A[ends, k]
            b = cand.argmax()
            nm = mask | 1 << k
            if cand[b] > dp[nm, k]:
                dp[nm, k] = cand[b]
                par[nm, k] = ends[b]
    mask = full - 1
    j = int(dp[mask].argmax())
    path = []
    while j >= 0:
        path.append(j)
        pj = par[mask, j]
        mask ^= 1 << j
        j = pj
    return path[::-1]


def _heuristic_path(A):
    n = len(A)
    best, bw = None, -np.inf
    for s in range(n):
        p = [s]
        left = set(range(n)) - {s}
        while left:
            l = list(left)
            a = A[p[0], l]
            b = A[p[-1], l]
            if a.max() > b.max():
                k = l[int(a.argmax())]
                p.insert(0, k)
            else:
                k = l[int(b.argmax())]
                p.append(k)
            left.remove(k)
        improved = True
        while improved:
            improved = False
            w0 = _path_weight(A, p)
            for i in range(n - 1):
                for j in range(i + 1, n):
                    q = p[:i] + p[i:j + 1][::-1] + p[j + 1:]
                    w = _path_weight(A, q)
                    if w > w0 + 1e-12:
                        p, w0, improved = q, w, True
            for i in range(n):  # 单点移动
                for j in range(n):
                    if i == j:
                        continue
                    q = p[:i] + p[i + 1:]
                    q.insert(j, p[i])
                    w = _path_weight(A, q)
                    if w > w0 + 1e-12:
                        p, w0, improved = q, w, True
                        break
        w = _path_weight(A, p)
        if w > bw:
            best, bw = p, w
    return best


def best_path(A):
    """边权最大的哈密顿路径(开放链)：≤12 个节点精确求解，否则多起点贪心 + 2-opt。"""
    n = len(A)
    if n <= 2:
        return list(range(n))
    return _dp_path(A) if n <= 12 else _heuristic_path(A)


def order_layers(W, centers, land, water, rgbs):
    """陆地各层排成从低到高的链；返回 (顺序, 链一致性, 每层一致性, 方向依据)。"""
    land = list(land)
    if len(land) == 0:
        return [], 0.0, {}, "none"
    P = W.sum(1) + 1e-9
    N = W / np.sqrt(np.outer(P, P))
    sub = N[np.ix_(land, land)]
    cd = np.sqrt(((centers[land][:, None] - centers[land][None]) ** 2).sum(-1))
    A = sub + 0.02 * np.exp(-cd / 15.0) * (sub.max() if sub.max() > 0 else 1)  # 颜色相近只做平局裁决
    np.fill_diagonal(A, 0)
    path = [land[i] for i in best_path(A)]
    tot = np.triu(sub, 1).sum()
    on = sum(N[path[i], path[i + 1]] for i in range(len(path) - 1))
    chain = float(on / tot) if tot > 0 else 1.0
    per = {}
    for k, c in enumerate(path):
        row = N[c, land].sum()
        nb = (N[c, path[k - 1]] if k > 0 else 0) + (N[c, path[k + 1]] if k + 1 < len(path) else 0)
        per[c] = float(nb / row) if row > 0 else 0.0
    # 方向：贴海的一端是低处
    how = "coast"
    if water and len(path) >= 2:
        coast = W[:, list(water)].sum(1) / P
        m = max(1, len(path) // 4)
        lo, hi = coast[path[:m]].mean(), coast[path[-m:]].mean()
        if abs(lo - hi) < 0.02:
            how = "color"
        elif hi > lo:
            path = path[::-1]
    else:
        how = "color"
    if how == "color" and len(path) >= 2:
        # 无海可依：偏绿的一端为低；否则更亮、更灰(雪、岩)的一端为高
        def highness(c):
            L, a, b = srgb_to_lab(np.asarray(rgbs[c], float)[None])[0]
            return -max(-a, 0) * 1.5 + L * 0.3 - np.hypot(a, b) * 0.3
        if highness(path[0]) > highness(path[-1]):
            path = path[::-1]
    return path, chain, per, how


# ---------------- 模板数值 ----------------

_NICE = [50, 100, 200, 300, 500, 700, 1000, 1500, 2000, 2500, 3000, 4000, 5000, 6000, 7000]
_TEMPLATES = {
    2: [500], 3: [200, 1000], 4: [200, 500, 2000], 5: [200, 500, 1000, 2000],
    6: [200, 500, 1000, 2000, 3000], 7: [100, 200, 500, 1000, 2000, 3000],
    8: [100, 200, 500, 1000, 1500, 2000, 3000], 9: [100, 200, 500, 1000, 1500, 2000, 3000, 4000],
    10: [50, 100, 200, 500, 1000, 1500, 2000, 3000, 4000],
}


def template_thresholds(n):
    if n <= 1:
        return []
    if n in _TEMPLATES:
        return list(_TEMPLATES[n])
    out = []
    for x in np.geomspace(50, 6000, n - 1):
        v = snap_nice(float(x))
        out.append(v if not out or v > out[-1] else out[-1] + 50)
    return out


def values_from_thresholds(t, top=None):
    """层值 = 区间中点；最低层 [0, t0]，最高层取 top 或上延半个区间。"""
    if not t:
        return [top if top is not None else 200.0]
    v = [t[0] / 2]
    for i in range(1, len(t)):
        v.append((t[i - 1] + t[i]) / 2)
    last = t[-1] + (t[-1] - (t[-2] if len(t) > 1 else 0)) / 2
    v.append(max(top, t[-1]) if top is not None else last)
    return v


WATER_TEMPLATE = [-100, -500, -1500, -3000, -4500, -6000, -8000]


def snap_nice(x):
    """贴近整数刻度(图例多用整百、整千)时取整；相对误差 12% 以内。"""
    if x <= 0:
        return float(round(x, -1))
    mag = 10 ** np.floor(np.log10(x))
    cands = [m * mag for m in (1, 1.5, 2, 2.5, 3, 4, 5, 6, 7, 7.5, 8, 10)]
    best = min(cands, key=lambda c: abs(c - x))
    return float(best) if abs(best - x) / x < 0.12 else float(round(x, -1))
