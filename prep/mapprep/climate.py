"""气候模型：由纬度、海拔、海陆分布推算逐月气温、降水与柯本气候分类。

这是一个"概念型"气候模型，而不是GCM：
  气温 = 纬度基准 - 海拔递减 + 暖流修正 ± 大陆度决定的季节振幅
  降水 = 季节性盛行风把海洋水汽向内陆输送(半拉格朗日平流)，
         沿途因大尺度辐合/地形抬升/饱和凝结而降水，并有陆面蒸散再循环。

通用规则只依赖纬度和海陆分布，适用于任意等距经纬度地图与南北半球；
季风、暖流等区域环流可由地图的 hints.json 修正。没有提供风场修正时，
启用由大尺度海陆分布推导的"通用季风"。

分辨率无关：物理参数以 0.25° 格(约 28km)标定，其他分辨率按格子大小换算。
"""
import numpy as np
from scipy import ndimage

MONTHS = np.arange(12)  # 0=1月
REF_RES = 0.25          # 参数标定时的格子大小(度)


def soft_box(grid, bbox, edge=None):
    """柔和边界的经纬度框权重 [0,1]。bbox=[西,南,东,北]，edge=[西,南,东,北]过渡宽度(度)。"""
    w, s, e, n = bbox
    ew, es, ee, en = edge or [3, 3, 3, 3]
    lon, lat = grid.LON, grid.LAT
    return (np.clip((lon - w) / max(ew, 1e-6), 0, 1) * np.clip((e - lon) / max(ee, 1e-6), 0, 1)
            * np.clip((lat - s) / max(es, 1e-6), 0, 1) * np.clip((n - lat) / max(en, 1e-6), 0, 1))


# 陆地纬向年均温基准(海平面, °C)，参考北半球陆地观测；南半球按纬度绝对值使用
_LAT_T = ([0, 10, 20, 25, 30, 35, 40, 45, 50, 55, 60, 65, 70, 75, 80, 90],
          [26.5, 27, 26.5, 25, 22.5, 19, 15.5, 12, 9, 6, 3, -0.5, -5, -10, -15, -22])

LAPSE = 4.5  # °C/km，作用于平滑地形：宽阔高原的地面加热使有效递减率小于自由大气(参考站点拟合)


def _k(grid):
    return grid.res / REF_RES


def _sig(grid, s):
    """以 0.25° 格标定的平滑尺度换算到当前分辨率(格)。"""
    return max(s / _k(grid), 0.3)


def _distance_km(mask, grid):
    """到 mask 的近似距离(km)。"""
    if not mask.any():
        return np.full(mask.shape, 1e4)
    d = ndimage.distance_transform_edt(~mask)
    return d * grid.dy_km


def _upwind_distance_km(sea, grid, direction=+1):
    """沿纬圈向西(direction=+1 表示气流自西向东)到最近海面的距离(km)。"""
    ny, nx = sea.shape
    d = np.full(sea.shape, 5000.0)
    run = np.full(ny, 5000.0)
    cols = range(nx) if direction > 0 else range(nx - 1, -1, -1)
    for j in cols:
        run = np.where(sea[:, j], 0.0, run + grid.dx_km[:, j])
        d[:, j] = run
    return d


def summer_peak(lat, sea=None):
    """当地最暖月(0=1月)：北半球 7 月，南半球 1 月；海洋滞后约半个月。"""
    base = np.where(lat >= 0, 6.0, 0.0)
    if sea is not None:
        base = base + np.where(sea, 0.5, 0.0)
    return base


def summer_months(lat):
    """每格的夏半年月份掩膜 [12, ny, nx]：北半球 4-9 月，南半球 10-3 月。"""
    nh = np.zeros(12, bool)
    nh[3:9] = True
    return np.where(lat[None] >= 0, nh[:, None, None], ~nh[:, None, None])


def temperature(grid, terr, hints=None):
    hints = hints or {}
    land = terr["land"]
    sea = terr["ocean"] | terr["lake"]
    big_sea = terr["ocean"]
    lat = grid.LAT
    s2 = np.sin(np.radians(lat)) ** 2

    elev_s = ndimage.gaussian_filter(np.where(land, terr["elev"], 0), _sig(grid, 0.6))

    # 大陆度：上风向(西风带自西)到海洋距离 + 到任意海洋距离
    d_west = _upwind_distance_km(big_sea, grid, +1)
    d_any = _distance_km(big_sea, grid)
    C = 1 - np.exp(-(0.6 * d_west + 0.4 * d_any) / 1300.0)
    for h in hints.get("continentality_min", []):
        C = np.maximum(C, h["value"] * soft_box(grid, h["bbox"], h.get("edge")))
    C = ndimage.gaussian_filter(C, _sig(grid, 2))

    # 暖洋流(区域修正)：对下风向大陆冬季增温显著
    warm = np.zeros(grid.shape, bool)
    for h in hints.get("warm_sea", []):
        warm |= soft_box(grid, h["bbox"], h.get("edge", [1, 1, 1, 1])) > 0.5
    warm_sea = big_sea & warm
    if warm_sea.any():
        atl = np.exp(-_distance_km(warm_sea, grid) / 1500.0) * 11 * s2
    else:
        atl = np.zeros(grid.shape)

    # 高纬大陆腹地冬季冷高压(通用规则)：只作用于大陆度很高的地方
    sib = np.exp(-((np.abs(lat) - 64) / 7) ** 2) * np.clip((C - 0.55) / 0.35, 0, 1)

    T0 = np.interp(np.abs(lat), *_LAT_T)
    Tann = T0 - LAPSE * np.maximum(elev_s, 0) / 1000 + atl * 0.45 - C * 7 * s2 - 4 * sib
    sinl = np.sin(np.radians(np.abs(lat)))
    amp = 2 + 6 * sinl + C * (2 + 16 * s2) - atl * 0.25 + 8 * sib
    amp = np.where(sea, 1 + 5 * sinl, amp)
    amp = np.maximum(amp, 1.0)

    phase = summer_peak(lat, sea)
    Tm = Tann[None] + amp[None] * np.cos(2 * np.pi * (MONTHS[:, None, None] - phase[None]) / 12)
    return {"Tm": Tm, "Tann": Tann, "amp": amp, "continentality": C, "elev_s": elev_s}


def _qsat(T):
    """饱和比湿近似(g/kg)，Clausius-Clapeyron。"""
    return 3.8 * np.exp(17.27 * T / (T + 237.3))


def _local_winter(grid, period):
    """period='DJF' 时北半球是冬季，'JJA' 时南半球是冬季。"""
    nh = grid.LAT >= 0
    return nh if period == "DJF" else ~nh


def _hint_period(h):
    # 区域修正里的 season 按北半球季节书写(winter=DJF, summer=JJA)
    if "period" in h:
        return h["period"]
    return "DJF" if h.get("season") == "winter" else "JJA"


def subtropical_subsidence(grid, terr):
    """夏季副热带高压下沉的权重 [0,1]：纬度约 28-45°(峰值 36°)、西侧离海近的地方最强。

    副高盘踞在大洋东部与大陆西部，夏季晴空少雨，造就地中海式气候(地中海、加州、智利中部、南非西南、澳洲西南)；
    大陆东侧受季风影响，不在此列。用"沿纬圈向西到大洋的距离"区分大陆西部与东部。
    """
    d_west = _upwind_distance_km(terr["ocean"], grid, +1)
    lat = np.abs(grid.LAT)
    S = np.exp(-((lat - 37) / 9) ** 2) * np.exp(-d_west / 2500.0)
    # 大陆东侧(大尺度上陆地在西边)是季风区：西边离海近只是因为边缘海(如日本海)，不算
    L = ndimage.gaussian_filter(terr["land"].astype(float), max(8.0 / grid.res, 1.0), mode="constant", cval=0.0)
    gx = np.gradient(L, axis=1) / grid.res  # 每度向东的陆地占比变化
    S = S * (1 - np.clip(-gx / 0.02, 0, 1))
    return ndimage.gaussian_filter(S, _sig(grid, 3))


def generic_monsoon(grid, terr):
    """由大尺度海陆分布推导季风：夏季风由海吹向大陆，冬季反向。返回 (单位方向 ex, ey(北向), 强度)。"""
    land = terr["land"].astype(float)
    L = ndimage.gaussian_filter(land, max(8.0 / grid.res, 1.0), mode="constant", cval=0.0)
    gy, gx = np.gradient(L)
    gy = -gy  # 行号向南递增，转换为北向分量
    mag = np.hypot(gx, gy) + 1e-9
    ex, ey = gx / mag, gy / mag
    # 只在中低纬(<48°)且存在明显的海陆差异处有季风；按每度的变化量归一
    per_deg = mag / grid.res
    strength = np.clip(per_deg / 0.05, 0, 1) * np.clip((48 - np.abs(grid.LAT)) / 15, 0, 1)
    strength *= np.clip(L * 3, 0, 1)  # 深海区不需要
    # 海在西侧(ex>0)时，夏季是副高东缘的离岸/沿岸风，没有向陆季风(大陆西岸没有季风)
    strength *= 1 - np.clip(ex, 0, 1)
    return ex, ey, strength


def _winds(grid, period, hints, monsoon=None):
    """季节盛行风(u 东向, v 北向, 单位: 格/步)。通用纬向风带 + 季风 + 区域修正。"""
    lat = np.abs(grid.LAT)
    north = np.sign(np.where(grid.LAT >= 0, 1.0, -1.0))
    winter = _local_winter(grid, period)
    # 冬季：西风带南移；信风(北半球东北信风、南半球东南信风)
    w_west = np.clip((lat - 28) / 7, 0, 1) * np.clip((72 - lat) / 6, 0, 1)
    w_trade = np.clip((30 - lat) / 6, 0, 1)
    w_polar = np.clip((lat - 68) / 5, 0, 1)
    s_west = np.clip((lat - 38) / 7, 0, 1) * np.clip((74 - lat) / 6, 0, 1)
    s_trade = np.clip((25 - lat) / 6, 0, 1)
    s_polar = np.clip((lat - 70) / 4, 0, 1)
    u = np.where(winter, 1.0 * w_west - 0.6 * w_trade - 0.6 * w_polar, 0.9 * s_west - 0.4 * s_trade - 0.4 * s_polar)
    # 信风的经向分量指向赤道
    v = np.where(winter, -0.5 * w_trade, -0.3 * s_trade) * north
    if monsoon is not None:
        ex, ey, m = monsoon
        sgn = np.where(winter, -0.5, 1.0)  # 夏季向陆，冬季离陆且较弱
        u = u + 0.9 * m * sgn * ex
        v = v + 0.9 * m * sgn * ey
    for h in hints.get("wind", []):
        if _hint_period(h) == period:
            wgt = soft_box(grid, h["bbox"], h.get("edge"))
            u = u + h.get("du", 0) * wgt
            v = v + h.get("dv", 0) * wgt
    sp = np.hypot(u, v)
    u = np.where(sp < 0.3, u + 0.3, u)  # 防止静风区无平流
    return u, v


def _dynamic_factor(grid, period, hints, monsoon=None, land=None, subs=None):
    """大尺度垂直运动对降水效率的调制(辐合/风暴路径 >1，副热带下沉 <1)。"""
    lat = np.abs(grid.LAT)
    winter = _local_winter(grid, period)
    f_w = 0.35 + 1.0 * np.exp(-((lat - 48) / 12) ** 2) - 0.5 * np.exp(-((lat - 24) / 9) ** 2)
    f_s = 0.35 + 0.65 * np.exp(-((lat - 58) / 10) ** 2)
    f = np.where(winter, f_w, f_s)
    if subs is not None:
        f = f - np.where(winter, 0.0, 0.6 * subs)
    if monsoon is not None and land is not None:
        _, _, m = monsoon
        mm = ndimage.gaussian_filter(m, max(2.0 / grid.res, 1.0))
        f = f + np.where(winter, -0.3 * mm, 1.0 * mm)
    for h in hints.get("dynamic", []):
        if _hint_period(h) == period:
            f = f + h["add"] * soft_box(grid, h["bbox"], h.get("edge"))
    return np.clip(f, 0.03, None)


def _advect_precip(grid, terr, Tperiod, Tann, period, hints, monsoon, subs=None):
    land = terr["land"]
    sea = ~land
    k = _k(grid)
    elev = ndimage.gaussian_filter(np.where(land, terr["elev"], 0.0), _sig(grid, 1.0))
    u, v = _winds(grid, period, hints, monsoon)
    dyn = _dynamic_factor(grid, period, hints, monsoon, land, subs)
    # 每步位移：0.25° 时 1 格/步；更细的网格每步走多格(不超过 3 格)，保持同等的物理步长
    d = min(1.0 / k, 3.0)
    f = d * k                      # 每步相当于标定步长(28km)的比例
    steps = int(min(450 / f, 3000))
    u, v = u * d, v * d
    gy, gx = np.gradient(elev)
    uplift = np.maximum(u * gx - v * gy, 0)  # 每步爬升的米数

    # 降水发生在低层上方的气团里，冬季地表逆温使地面温度远低于气团温度
    T_air = 0.5 * (Tperiod + Tann)
    qs = _qsat(T_air)
    q_src = np.where(sea, 0.8 * _qsat(Tperiod), 0)
    q = q_src.copy()
    P = np.zeros(grid.shape)

    ny, nx = grid.shape
    I, J = np.mgrid[0:ny, 0:nx].astype(float)
    src_i = np.clip(I + v, 0, ny - 1)   # 上风点
    src_j = np.clip(J - u, 0, nx - 1)
    winter = _local_winter(grid, period)
    # 陆面水分再循环率：夏季植被蒸腾旺盛
    recycle = np.where(land, np.where(winter, 0.55, 0.8), 0.0)
    oro = 0.00004 * np.minimum(uplift, 400 * f)
    if subs is not None:
        # 副高下沉区夏季：蒸散的水汽不在本地成雨，地形抬升也难以触发降水
        s_sum = np.where(winter, 0.0, subs)
        recycle = recycle * (1 - 0.7 * s_sum)
        oro = oro * (1 - 0.7 * s_sum)
    rate = (0.0065 * dyn) * f + oro
    for _ in range(steps):
        q_up = ndimage.map_coordinates(q, [src_i, src_j], order=1, mode="nearest")
        q_up = ndimage.uniform_filter(q_up, 3) * 0.6 + q_up * 0.4  # 侧向扩散
        # 降水效率随相对湿度上升：炎热干燥空气即便有水汽也难成雨
        rh = np.clip(q_up / qs / 0.6, 0.05, 1.0)
        rain = q_up * np.clip(rate * rh, 0, 0.25)
        excess = 0.08 * f * np.maximum(q_up - rain - qs * 1.2, 0)  # 冷高地上过饱和凝结
        rain = rain + excess
        q_new = q_up - rain * (1 - recycle)
        q_new = np.minimum(q_new, 1.2 * qs)  # 翻越高山时多余水汽在迎风坡耗尽
        q = np.where(sea, np.maximum(q_new, q_src), np.maximum(q_new, 0))
        P = rain
    return P / f  # 换算回"每标定步长"的降水强度，与分辨率无关


def precipitation(grid, terr, temp, hints=None, generic=True):
    hints = hints or {}
    Tm = temp["Tm"]
    T_djf = Tm[[11, 0, 1]].mean(0)
    T_jja = Tm[[5, 6, 7]].mean(0)
    subs = subtropical_subsidence(grid, terr)
    monsoon = generic_monsoon(grid, terr) if generic else None
    if monsoon is not None:  # 副高控制的地方没有夏季风
        monsoon = (monsoon[0], monsoon[1], monsoon[2] * (1 - subs))
    P_djf = _advect_precip(grid, terr, T_djf, temp["Tann"], "DJF", hints, monsoon, subs)
    P_jja = _advect_precip(grid, terr, T_jja, temp["Tann"], "JJA", hints, monsoon, subs)
    K = 1300.0  # 换算为 mm/月（标定系数由参考站点拟合）
    w = (1 + np.cos(2 * np.pi * MONTHS / 12)) / 2  # 1月=1(DJF), 7月=0(JJA)
    Pm = w[:, None, None] * (K * P_djf)[None] + (1 - w)[:, None, None] * (K * P_jja)[None]
    Pm = ndimage.gaussian_filter(Pm, (0, _sig(grid, 1), _sig(grid, 1)))
    return {"Pm": Pm, "Pann": Pm.sum(0)}


def koppen(Tm, Pm, lat):
    """柯本-盖格分类 (Peel et al. 2007 规则，C/D 界取 0°C)。返回代码字符串数组。"""
    Tann = Tm.mean(0)
    Pann = Pm.sum(0)
    Thot = Tm.max(0)
    Tcold = Tm.min(0)
    summer = summer_months(lat)  # 南北半球夏半年不同
    big = 1e9
    Ps_half = np.where(summer, Pm, 0).sum(0)
    Psdry = np.where(summer, Pm, big).min(0)
    Pwdry = np.where(~summer, Pm, big).min(0)
    Pswet = np.where(summer, Pm, -big).max(0)
    Pwwet = np.where(~summer, Pm, -big).max(0)
    Pdry = Pm.min(0)

    frac_s = Ps_half / np.maximum(Pann, 1e-6)
    Pth = np.where(frac_s >= 0.7, 2 * Tann + 28, np.where(frac_s <= 0.3, 2 * Tann, 2 * Tann + 14)) * 10
    nwarm = (Tm > 10).sum(0)

    out = np.empty(Tann.shape, dtype=object)
    out[:] = ""

    isE = Thot < 10
    isB = ~isE & (Pann < Pth)
    isA = ~isE & ~isB & (Tcold >= 18)
    isC = ~isE & ~isB & ~isA & (Tcold > 0)

    out[isE & (Thot > 0)] = "ET"
    out[isE & (Thot <= 0)] = "EF"

    bw = Pann < Pth / 2
    hot = Tann >= 18
    out[isB & bw & hot] = "BWh"
    out[isB & bw & ~hot] = "BWk"
    out[isB & ~bw & hot] = "BSh"
    out[isB & ~bw & ~hot] = "BSk"

    af = Pdry >= 60
    am = ~af & (Pdry >= 100 - Pann / 25)
    out[isA & af] = "Af"
    out[isA & am] = "Am"
    out[isA & ~af & ~am] = "Aw"

    isD = ~isE & ~isB & ~isA & ~isC
    s_dry = (Psdry < 40) & (Psdry < Pwwet / 3)
    w_dry = Pwdry < Pswet / 10
    third = np.where(Thot >= 22, "a", np.where(nwarm >= 4, "b", np.where(Tcold < -38, "d", "c")))
    for grp, letter in ((isC, "C"), (isD, "D")):
        sec = np.where(s_dry, "s", np.where(w_dry, "w", "f"))
        codes = np.char.add(np.char.add(letter, sec.astype(str)), third.astype(str))
        out[grp] = codes[grp]
    return out


def _aridity_feedback(grid, temp, prec):
    """晴空干燥区夏季更热、湿润多云区夏季偏凉(地表能量平衡的粗略近似)。

    按夏半年降水判断(换算成全年当量)：地中海式气候冬雨夏干，夏季晴空增温，年降水量却不少。
    湿润一侧最多偏凉约 2.5°C(纬向基准气温已含典型云量)。
    """
    peak = summer_peak(grid.LAT) + 0.5
    w_summer = np.clip(np.cos(2 * np.pi * (MONTHS[:, None, None] - peak[None]) / 12), 0, None)
    P_summer = np.where(summer_months(grid.LAT), prec["Pm"], 0).sum(0)
    arid = np.clip(1 - 2 * P_summer / 900, 0, 1) - 0.35
    temp["Tm"] = temp["Tm"] + w_summer * (7 * arid)[None]
    temp["Tann"] = temp["Tm"].mean(0)


def run(grid, terr, hints=None, generic_monsoon_on=None):
    hints = hints or {}
    if generic_monsoon_on is None:
        # 地图自带风场修正(如欧亚的季风)时，不再叠加通用季风，避免重复
        generic_monsoon_on = not hints.get("wind")
    temp = temperature(grid, terr, hints)
    prec = precipitation(grid, terr, temp, hints, generic_monsoon_on)
    _aridity_feedback(grid, temp, prec)
    kp = koppen(temp["Tm"], prec["Pm"], grid.LAT)
    return {**temp, **prec, "koppen": kp}
