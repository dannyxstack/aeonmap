"""地形解析：从分层设色地形图反推海拔、水体，并重投影到规则经纬网格。"""
import numpy as np
from PIL import Image
from scipy import ndimage

from .georef import make_georef


def _hex(c):
    c = c.lstrip("#")
    return [int(c[i:i + 2], 16) for i in (0, 2, 4)]


def _ramp_lut(legend):
    """把 RGB 立方体量化成 32^3 的查找表，每格投影到色带折线上求海拔。"""
    cols = np.array([_hex(c) for c, _ in legend], float)
    elev = np.array([e for _, e in legend], float)
    q = (np.arange(32) * 8 + 4).astype(float)
    R, G, B = np.meshgrid(q, q, q, indexing="ij")
    P = np.stack([R, G, B], -1).reshape(-1, 3)
    best_d = np.full(len(P), np.inf)
    best_e = np.zeros(len(P))
    for i in range(len(cols) - 1):
        a, b = cols[i], cols[i + 1]
        ab = b - a
        t = np.clip(((P - a) @ ab) / (ab @ ab), 0, 1)
        proj = a + t[:, None] * ab
        # 亮度受晕渲影响较大，降低亮度方向的权重，更看重色相
        diff = P - proj
        lum = diff.mean(1, keepdims=True)
        d = ((diff - lum) ** 2).sum(1) + 0.3 * (lum[:, 0] ** 2) * 3
        better = d < best_d
        best_d[better] = d[better]
        best_e[better] = elev[i] + t[better] * (elev[i + 1] - elev[i])
    return best_e.reshape(32, 32, 32)


def _water_lut(legend, water_colors, max_dist=40.0):
    """32^3 查找表：颜色离某个水体颜色最近(比任何色带颜色都近)且距离不超过 max_dist 则为水。"""
    land = np.array([_hex(c) for c, _ in legend], float)
    wat = np.array([_hex(c) for c in water_colors], float)
    q = (np.arange(32) * 8 + 4).astype(float)
    P = np.stack(np.meshgrid(q, q, q, indexing="ij"), -1).reshape(-1, 3)
    dl = np.sqrt(((P[:, None] - land[None]) ** 2).sum(-1)).min(1)
    dw = np.sqrt(((P[:, None] - wat[None]) ** 2).sum(-1)).min(1)
    return ((dw < dl) & (dw <= max_dist)).reshape(32, 32, 32)


def load_raster(path):
    """读取任意栅格：PNG/JPG(8/16 位，灰度或彩色)、TIFF/GeoTIFF(含浮点)。返回 numpy 数组。"""
    path = str(path)
    if path.lower().endswith((".tif", ".tiff")):
        try:
            import tifffile
            return np.asarray(tifffile.imread(path))
        except Exception:
            pass
    img = Image.open(path)
    if img.mode in ("I;16", "I;16B", "I;16L", "I", "F"):
        return np.asarray(img)
    if img.mode in ("L", "LA", "P"):
        return np.asarray(img.convert("L"))
    return np.asarray(img.convert("RGB"))


def _parse_hypsometric(cfg, ecfg):
    """分层设色地形图：按图例色带反推海拔。水体：蓝色；elevation.water 指定的颜色(如白色的海)；透明像素。"""
    im = Image.open(cfg.image)
    transparent = None
    if im.mode in ("RGBA", "LA", "PA") or (im.mode == "P" and "transparency" in im.info):
        transparent = np.asarray(im.convert("RGBA"))[..., 3] < 128
    img = np.asarray(im.convert("RGB")).astype(np.int32)
    r, g, b = img[..., 0], img[..., 1], img[..., 2]

    water = (b > r + 35) & (b > g + 8)
    if ecfg.get("water"):
        water |= _water_lut(ecfg["legend"], ecfg["water"])[r // 8, g // 8, b // 8]
    if transparent is not None:
        water |= transparent
    # 灰色修补块：灰、平坦、且紧贴水体 -> 视为水(当前欧亚图在黑海、地中海里有几块)
    gray = (np.ptp(img, axis=2) < 22)
    lum = img.mean(2)
    local_std = np.sqrt(np.maximum(ndimage.uniform_filter(lum ** 2, 7) - ndimage.uniform_filter(lum, 7) ** 2, 0))
    flat_gray = gray & (local_std < 4.0)
    flat_gray = ndimage.binary_opening(flat_gray, iterations=2)
    lab, n = ndimage.label(flat_gray)
    if n:
        ring = ndimage.binary_dilation(flat_gray, iterations=4) & ~flat_gray
        touch = ndimage.sum(water & ring, lab, np.arange(1, n + 1))
        ringsz = ndimage.sum(ring, lab, np.arange(1, n + 1))
        ok = np.zeros(n + 1, bool)
        ok[1:] = touch / np.maximum(ringsz, 1) > 0.15
        water |= ok[lab]
    water = ndimage.binary_closing(water, iterations=1) | water

    lut = _ramp_lut(ecfg["legend"])
    elev = lut[r // 8, g // 8, b // 8]
    elev = np.where(water, 0.0, elev)
    # 海水越亮越浅：浅色大陆架(~200m) -> 深蓝大洋
    depth = np.clip((215 - lum) / 110, 0, 1)
    depth = np.where(water, depth, 0.0)
    return elev, water, depth, None


def _parse_grayscale(cfg, ecfg):
    """灰度/数值高程：encoding=meters 时像素值即米；否则按 [min,max] 线性映射。"""
    a = load_raster(cfg.image).astype(np.float64)
    if a.ndim == 3:
        a = a[..., :3].mean(2)
    nodata = ecfg.get("nodata")
    if nodata is not None:
        a = np.where(a == nodata, np.nan, a)
    if ecfg.get("encoding") == "meters":
        elev = a
    else:
        vmax = ecfg.get("vmax") or (65535.0 if np.nanmax(a) > 255 else 255.0)
        lo, hi = ecfg.get("min", -500.0), ecfg.get("max", 6000.0)
        elev = lo + a / vmax * (hi - lo)
    elev = np.where(np.isnan(elev), -100.0, elev)
    sea = ecfg.get("sea_level", 0.0)
    water = elev <= sea
    depth = np.where(water, np.clip((sea - elev) / 4000.0, 0.05, 1), 0.0)  # 有海底地形时可识别大陆架
    elev = np.where(water, 0.0, elev - sea)
    return elev, water, depth, None


def _parse_sketch(cfg, ecfg, georef_res_km):
    """手绘草图(规则版)：按颜色分为 海/河/山/高地/陆地，再合成高程。

    约定(可在 map.json 的 elevation.palette 中覆盖)：
      蓝色=海或湖，青色=河流，棕/红/黑色笔画=山脉，黄/橙色=高地丘陵，其余(白纸、绿色等)=平原陆地。
    """
    rgb = np.asarray(Image.open(cfg.image).convert("RGB")).astype(np.float64) / 255.0
    H, W, _ = rgb.shape
    classes = {"sea": 0, "land": 1, "mountain": 2, "highland": 3, "river": 4}
    palette = ecfg.get("palette")
    if palette:
        cols = np.array([_hex(c) for c, _ in palette], float) / 255.0
        names = [n for _, n in palette]
        d = ((rgb[:, :, None, :] - cols[None, None]) ** 2).sum(-1)
        cls = np.vectorize(lambda k: classes.get(names[k], 1))(d.argmin(-1))
    else:
        mx, mn = rgb.max(2), rgb.min(2)
        val, sat = mx, (mx - mn) / np.maximum(mx, 1e-6)
        r, g, b = rgb[..., 0], rgb[..., 1], rgb[..., 2]
        hue = np.degrees(np.arctan2(np.sqrt(3) * (g - b), 2 * r - g - b)) % 360
        colored = sat > 0.28
        cls = np.ones((H, W), int)
        cls[colored & (hue >= 200) & (hue < 265) & (val > 0.2)] = 0
        cls[colored & (hue >= 165) & (hue < 200)] = 4
        cls[(colored & ((hue < 38) | (hue >= 320))) | (val < 0.22)] = 2
        cls[colored & (hue >= 38) & (hue < 70)] = 3
    cls = ndimage.median_filter(cls, size=3)
    sea = cls == 0
    sea = ndimage.binary_opening(sea, iterations=1) | ndimage.binary_closing(sea, iterations=1) & sea
    land = ~sea
    px_km = georef_res_km
    # 离海越远越高(内陆平原缓慢抬升)
    d_coast = ndimage.distance_transform_edt(land) * px_km
    elev = 30 + 350 * (1 - np.exp(-d_coast / 600.0))
    # 高地丘陵
    hi = ndimage.gaussian_filter((cls == 3).astype(float), max(25 / px_km, 1))
    elev += 900 * np.clip(hi * 1.6, 0, 1)
    # 山脉：笔画周围按距离衰减形成山脊
    mt = cls == 2
    if mt.any():
        d_mt = ndimage.distance_transform_edt(~mt) * px_km
        ridge = np.exp(-(d_mt / 70.0) ** 1.3)
        elev += 2800 * ridge
    else:
        ridge = np.zeros_like(elev)
    # 分形噪声，避免大片完全平整
    rng = np.random.default_rng(int(ecfg.get("seed", 7)))
    noise = np.zeros_like(elev)
    for k, sigma_km in enumerate([400, 150, 50, 15]):
        n = ndimage.gaussian_filter(rng.standard_normal(elev.shape), max(sigma_km / px_km, 0.5))
        noise += n / (n.std() + 1e-9) * (120 / (k + 1))
    elev += noise * (0.6 + 1.5 * ridge)
    elev = np.where(land, np.maximum(elev, 5), 0.0)
    river = cls == 4
    depth = np.where(sea, 0.5, 0.0)
    return elev, sea, depth, river


def parse_image(cfg, georef_res_km=None):
    """逐像素解析：返回 (海拔m, 水体掩膜, 水深指数0浅-1深, 河流掩膜或None)。"""
    ecfg = cfg.raw["elevation"]
    t = ecfg["type"]
    if t == "hypsometric":
        return _parse_hypsometric(cfg, ecfg)
    if t == "grayscale":
        return _parse_grayscale(cfg, ecfg)
    if t == "sketch":
        return _parse_sketch(cfg, ecfg, georef_res_km or 5.0)
    raise ValueError(f"未知的 elevation.type: {t}")


def _point_in_poly(lon, lat, poly):
    poly = np.asarray(poly)
    inside = np.zeros(lon.shape, bool)
    x, y = poly[:, 0], poly[:, 1]
    j = len(poly) - 1
    for i in range(len(poly)):
        cond = ((y[i] > lat) != (y[j] > lat)) & (
            lon < (x[j] - x[i]) * (lat - y[i]) / (y[j] - y[i] + 1e-12) + x[i])
        inside ^= cond
        j = i
    return inside


class Grid:
    """规则经纬网格。所有后续层都在此网格上计算。"""

    def __init__(self, lon0, lon1, lat0, lat1, res=0.25):
        self.res = res
        self.west, self.north = lon0, lat1
        self.lon = np.arange(lon0 + res / 2, lon1, res)
        self.lat = np.arange(lat1 - res / 2, lat0, -res)  # 北在上
        self.LON, self.LAT = np.meshgrid(self.lon, self.lat)
        self.shape = self.LON.shape
        # 单元面积 km²
        self.dy_km = 111.2 * res
        self.dx_km = 111.2 * res * np.cos(np.radians(self.LAT))
        self.area_km2 = self.dx_km * self.dy_km


def image_size(cfg):
    if str(cfg.image).lower().endswith((".tif", ".tiff")):
        a = load_raster(cfg.image)
        return a.shape[1], a.shape[0]
    with Image.open(cfg.image) as im:
        return im.size


def build_terrain(grid: Grid, cfg):
    W0, H0 = image_size(cfg)
    px_km = (cfg.east - cfg.west) * 111.2 * np.cos(np.radians((cfg.north + cfg.south) / 2)) / W0
    elev_px, water_px, depth_px, river_px = parse_image(cfg, px_km)
    H, W = elev_px.shape
    georef = make_georef(cfg, W, H)

    x, y = georef.geo_to_px(grid.LON.ravel(), grid.LAT.ravel())
    x = x.reshape(grid.shape)
    y = y.reshape(grid.shape)
    valid = (x >= 0) & (x <= W - 1) & (y >= 0) & (y <= H - 1)

    # 先按"每格像素数"平滑再采样，相当于面积平均(欧亚图约 3 像素/格)
    px_per_cell = grid.res * 111.2 * np.cos(np.radians((cfg.north + cfg.south) / 2)) / max(px_km, 1e-6)
    sig = max(px_per_cell / 2.5, 0.5)
    wf = ndimage.gaussian_filter(water_px.astype(float), sig)
    landw = 1 - water_px
    ef = ndimage.gaussian_filter(elev_px * landw, sig) / np.maximum(ndimage.gaussian_filter(landw.astype(float), sig), 1e-3)
    df = ndimage.gaussian_filter(depth_px, sig)
    coords = np.stack([np.clip(y, 0, H - 1), np.clip(x, 0, W - 1)])
    water_frac = ndimage.map_coordinates(wf, coords, order=1)
    elev = ndimage.map_coordinates(ef, coords, order=1)
    depth = ndimage.map_coordinates(df, coords, order=1)

    ocean = water_frac > 0.5
    lake = np.zeros(grid.shape, bool)
    for poly in cfg.lakes.values():
        lake |= _point_in_poly(grid.LON, grid.LAT, poly)
    lake &= ~ocean
    # 不与图边相连、面积小的封闭水体视为湖泊(用户上传的高程图/草图没有湖泊清单)
    lab, n = ndimage.label(ocean)
    if n > 1:
        border = np.unique(np.concatenate([lab[0], lab[-1], lab[:, 0], lab[:, -1]]))
        sizes = ndimage.sum(np.ones_like(lab), lab, np.arange(n + 1))
        small = sizes < 0.02 * ocean.size
        inner = np.ones(n + 1, bool)
        inner[border] = False
        inner[0] = False
        is_lake = inner & small
        lake |= is_lake[lab]
        ocean &= ~is_lake[lab]
    river = np.zeros(grid.shape, bool)
    if river_px is not None and river_px.any():
        rf = ndimage.maximum_filter(river_px.astype(float), size=max(int(px_per_cell), 1))
        river = ndimage.map_coordinates(rf, coords, order=0) > 0.5

    land = ~ocean & ~lake & valid
    elev = np.where(land, np.maximum(elev, -30), 0.0)
    return {
        "valid": valid,
        "land": land,
        "ocean": ocean | ~valid,   # 图外按海洋处理
        "lake": lake,
        "elev": elev,
        "depth": np.where(ocean, depth, 0.0),
        "river_mask": river & land,
        "px": x,
        "py": y,
    }
