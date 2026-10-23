"""水文：潜在蒸散、径流、河网汇流。"""
import heapq

import numpy as np
from scipy import ndimage

from .climate import MONTHS

D8 = [(-1, -1), (-1, 0), (-1, 1), (0, -1), (0, 1), (1, -1), (1, 0), (1, 1)]


def pet_thornthwaite(Tm, lat):
    """Thornthwaite 月潜在蒸散 (mm)。"""
    T = np.maximum(Tm, 0)
    I = ((T / 5) ** 1.514).sum(0)
    I = np.maximum(I, 1e-3)
    a = 6.75e-7 * I ** 3 - 7.71e-5 * I ** 2 + 1.792e-2 * I + 0.49239
    # 日长修正
    doy = 15 + 30.4 * MONTHS
    decl = 0.409 * np.sin(2 * np.pi * doy / 365 - 1.39)
    phi = np.radians(lat)
    x = np.clip(-np.tan(phi)[None] * np.tan(decl)[:, None, None], -1, 1)
    daylen = 24 / np.pi * np.arccos(x)
    pet = 16 * (daylen / 12) * (30.4 / 30) * (10 * T / I[None]) ** a[None]
    # 炎热月份 Thornthwaite 低估，用 Willmott 高温段公式
    hot = Tm >= 26.5
    pet_hot = (-415.85 + 32.24 * Tm - 0.43 * Tm ** 2) * (daylen / 12) * (30.4 / 30)
    return np.where(hot, pet_hot, pet)


def budyko_aet(P, PET, w=2.6):
    """Fu 方程：年实际蒸散。"""
    P = np.maximum(P, 1e-3)
    r = PET / P
    return P * (1 + r - (1 + r ** w) ** (1 / w))


def priority_flood(elev, outlet):
    """Barnes 优先洪泛填洼，返回填平后的高程(加微小坡度保证可排水)。"""
    ny, nx = elev.shape
    filled = elev.astype(float).copy()
    done = outlet.copy()
    heap = []
    # 所有与出口相邻的陆地格入队
    border = ndimage.binary_dilation(outlet, structure=np.ones((3, 3))) & ~outlet
    edge = np.zeros_like(outlet)
    edge[0, :] = edge[-1, :] = edge[:, 0] = edge[:, -1] = True
    seeds = (border | edge) & ~outlet
    for i, j in zip(*np.nonzero(seeds)):
        heapq.heappush(heap, (filled[i, j], i, j))
        done[i, j] = True
    eps = 1e-3
    while heap:
        z, i, j = heapq.heappop(heap)
        for di, dj in D8:
            a, b = i + di, j + dj
            if 0 <= a < ny and 0 <= b < nx and not done[a, b]:
                done[a, b] = True
                if filled[a, b] <= z:
                    filled[a, b] = z + eps
                heapq.heappush(heap, (filled[a, b], a, b))
    return filled


def flow_directions(filled, land, grid):
    """D8 最陡下降方向，返回下游格的平铺索引(-1 表示入海/入湖)。"""
    ny, nx = filled.shape
    best = np.zeros(filled.shape)
    down = np.full(filled.shape, -1, dtype=np.int64)
    idx = np.arange(ny * nx).reshape(ny, nx)
    pad = np.pad(filled, 1, mode="edge")
    pidx = np.pad(idx, 1, mode="constant", constant_values=-1)
    pland = np.pad(land, 1, mode="constant", constant_values=False)
    for di, dj in D8:
        nb = pad[1 + di:1 + di + ny, 1 + dj:1 + dj + nx]
        dist = np.hypot(di * grid.dy_km, dj * grid.dx_km)
        # 流向水体时把水体高程视为 -1，保证入海
        nb_land = pland[1 + di:1 + di + ny, 1 + dj:1 + dj + nx]
        nbz = np.where(nb_land, nb, -1.0)
        slope = (filled - nbz) / dist
        better = slope > best
        best = np.where(better, slope, best)
        down = np.where(better, pidx[1 + di:1 + di + ny, 1 + dj:1 + dj + nx], down)
    down = np.where(land, down, -1)
    # 指向水体的视为出口
    flat_down = down.ravel()
    land_flat = land.ravel()
    to_water = (flat_down >= 0) & ~land_flat[np.clip(flat_down, 0, None)]
    flat_down[to_water] = -1
    return flat_down.reshape(filled.shape)


def route(runoff_mm, filled, down, land, grid, loss, inflow=()):
    """沿河网累加流量(m³/s)。loss: 每格的河道蒸发/渗漏损失率(干旱区高)。"""
    ny, nx = filled.shape
    local = runoff_mm * grid.area_km2 * 1e3 / 3.156e7  # m³/s
    local = np.where(land, local, 0).ravel()
    for src in inflow:
        i = int((grid.north - src["lat"]) / grid.res)
        j = int((src["lon"] - grid.west) / grid.res)
        if 0 <= i < ny and 0 <= j < nx:
            local[i * nx + j] += src["q"]
    acc = local.copy()
    order = np.argsort(-filled.ravel(), kind="stable")
    order = order[land.ravel()[order]]
    dflat = down.ravel()
    keep = 1 - loss.ravel()
    for k in order:
        d = dflat[k]
        if d >= 0:
            acc[d] += acc[k] * keep[k]
    return acc.reshape(ny, nx)


def burn_rivers(elev, land, grid, rivers, depth=400.0):
    """河道刻蚀：沿已知大河干流把高程压低并强制单调下降，保证汇流走向正确。"""
    elev = elev.copy()
    channel = np.zeros(elev.shape, bool)
    ny, nx = elev.shape
    for line in rivers.values():
        pts = np.array(line, float)
        seg = []
        for a, b in zip(pts[:-1], pts[1:]):
            n = max(int(np.hypot(*(b - a)) / (grid.res * 0.4)), 1)
            t = np.linspace(0, 1, n, endpoint=False)[:, None]
            seg.append(a + t * (b - a))
        seg.append(pts[-1:])
        # 折线末端可能没画到海岸：沿末段方向再延伸 3°，直到入海
        d = pts[-1] - pts[-2]
        d = d / (np.hypot(*d) + 1e-9)
        seg.append(pts[-1] + np.arange(1, int(3 / (grid.res * 0.4)))[:, None] * d * grid.res * 0.4)
        path = np.vstack(seg)
        ii = ((grid.north - path[:, 1]) / grid.res).astype(int)
        jj = ((path[:, 0] - grid.west) / grid.res).astype(int)
        ok = (ii >= 0) & (ii < ny) & (jj >= 0) & (jj < nx)
        ii, jj = ii[ok], jj[ok]
        # 去重并保持顺序
        key = ii * nx + jj
        _, first = np.unique(key, return_index=True)
        order = np.sort(first)
        ii, jj = ii[order], jj[order]
        # 跳过源头的湖泊(如安加拉河出自贝加尔湖)，之后遇到水体(入海/入湖)即停
        wet = ~land[ii, jj]
        if wet.all():
            continue
        start = np.argmax(~wet)
        ii, jj, wet = ii[start:], jj[start:], wet[start:]
        if wet.any():
            stop = np.argmax(wet)
            ii, jj = ii[:stop], jj[:stop]
        if len(ii) == 0:
            continue
        z = np.minimum.accumulate(elev[ii, jj]) - depth - np.arange(len(ii)) * 0.5
        elev[ii, jj] = np.minimum(elev[ii, jj], z)
        channel[ii, jj] = True
    return elev, channel


def snow_runoff(Tm, Pm, frac=0.7):
    """冬季积雪在春季融化时，冻土阻止下渗，相当一部分直接形成径流。"""
    snow = np.where(Tm < -2, Pm, 0).sum(0)
    return frac * snow


def run(grid, terr, clim, rivers=None, inflow=()):
    land = terr["land"]
    Tm, Pm = clim["Tm"], clim["Pm"]
    PET_m = pet_thornthwaite(Tm, grid.LAT)
    PET = PET_m.sum(0)
    P = Pm.sum(0)
    melt = snow_runoff(Tm, Pm)
    # 寒冷地区(冻土、蒸发季短)Budyko 形状参数取小，径流系数更高
    w = 1.6 + 1.0 * np.clip((clim["Tann"] + 5) / 15, 0, 1)
    AET = budyko_aet(P - melt, PET, w)
    runoff = np.maximum(P - melt - AET, 0) + melt
    AET = P - runoff

    outlet = ~land
    dem, channel = burn_rivers(np.where(land, terr["elev"], 0), land, grid, rivers or {})
    rm = terr.get("river_mask")
    if rm is not None and rm.any():
        # 草图里画出的河道：压低高程，引导汇流
        dem = np.where(rm, dem - 150, dem)
        channel = channel | rm
    filled = priority_flood(dem, outlet)
    down = flow_directions(filled, land, grid)
    aridity = np.clip(1 - P / np.maximum(PET, 1), 0, 1)
    k = grid.res / 0.25  # 损耗率按 0.25° 格标定，按格长换算
    loss = (0.002 + 0.025 * aridity ** 2) * k
    loss = np.where(channel, loss * 0.4, loss)  # 大河干流水量大，相对损耗小
    Q = route(runoff, filled, down, land, grid, loss, inflow)
    return {"PET_m": PET_m, "PET": PET, "AET": AET, "runoff": runoff,
            "discharge": np.where(land, Q, 0), "filled": filled, "down": down, "channel": channel,
            "MI": P / np.maximum(PET, 1)}


def river_geojson(grid, Q, down, land, min_q=None):
    """把流量超过阈值的河道格串成折线(GeoJSON)。阈值默认取陆地流量的高分位，至少 50 m³/s。"""
    ny, nx = Q.shape
    if min_q is None:
        ql = Q[land]
        min_q = max(50.0, float(np.quantile(ql, 0.985))) if ql.size else 50.0
    riv = (Q >= min_q) & land
    flat_down = down.ravel()
    rivf = riv.ravel()
    idx = np.nonzero(rivf)[0]
    indeg = np.zeros(ny * nx, int)
    for k in idx:
        d = flat_down[k]
        if d >= 0 and rivf[d]:
            indeg[d] += 1
    seen = np.zeros(ny * nx, bool)
    lon = grid.LON.ravel()
    lat = grid.LAT.ravel()
    Qf = Q.ravel()
    feats = []

    def walk(k):
        pts = [[round(float(lon[k]), 4), round(float(lat[k]), 4)]]
        qmax = Qf[k]
        seen[k] = True
        while True:
            d = flat_down[k]
            if d < 0:
                # 入海/入湖：延伸到下游水体格
                break
            pts.append([round(float(lon[d]), 4), round(float(lat[d]), 4)])
            if not rivf[d] or seen[d] or indeg[d] > 1:
                break
            seen[d] = True
            qmax = max(qmax, Qf[d])
            k = d
        return pts, float(qmax)

    starts = [k for k in idx if indeg[k] == 0]
    starts += [k for k in idx if indeg[k] > 1]  # 汇流点之后另起一段
    for k in starts:
        if seen[k] and indeg[k] <= 1:
            continue
        pts, qmax = walk(k)
        if len(pts) >= 2:
            feats.append({"type": "Feature", "properties": {"discharge": round(qmax, 1)},
                          "geometry": {"type": "LineString", "coordinates": pts}})
    return {"type": "FeatureCollection", "features": feats, "properties": {"min_discharge": min_q}}
