"""第1层资源 + 第2层承载力与生计方式。

所有承载力单位都是"人/km²"，对应前工业时代(约公元1000年)的技术水平。
参考量级：中世纪欧洲旱作 10-25；长江/恒河水稻区 80-150；
蒙古草原游牧约 1；北方针叶林狩猎 0.05-0.2；北极 0.01-0.05。
"""
import numpy as np
from scipy import ndimage

KOPPEN_CODES = ["", "Af", "Am", "Aw", "BWh", "BWk", "BSh", "BSk",
                "Csa", "Csb", "Csc", "Cwa", "Cwb", "Cwc", "Cfa", "Cfb", "Cfc",
                "Dsa", "Dsb", "Dsc", "Dsd", "Dwa", "Dwb", "Dwc", "Dwd", "Dfa", "Dfb", "Dfc", "Dfd",
                "ET", "EF"]

LANDCOVER = ["水体", "冰原裸岩", "苔原", "北方针叶林", "温带森林", "亚热带森林", "草原", "荒漠", "灌丛稀树", "高山草甸"]

MODES = ["无人区", "水稻农耕", "旱作农耕", "灌溉农业", "农牧混合", "草原游牧", "荒漠游牧",
         "高原游牧", "苔原驯鹿", "森林狩猎采集", "渔猎"]
# 各生计方式的典型年流动范围(km)
MOBILITY = [0, 2, 5, 3, 30, 250, 350, 150, 300, 60, 15]


def _miami_npp(T, P):
    """Miami 模型：净初级生产力 (g 干物质/m²/年)。"""
    npp_t = 3000 / (1 + np.exp(1.315 - 0.119 * T))
    npp_p = 3000 * (1 - np.exp(-0.000664 * P))
    return np.minimum(npp_t, npp_p)


def run(grid, terr, clim, hyd):
    land = terr["land"]
    ocean = terr["ocean"]
    lake = terr["lake"]
    elev = terr["elev"]
    Tm, Pm = clim["Tm"], clim["Pm"]
    Tann, Pann = Tm.mean(0), Pm.sum(0)
    Tjan, Tjul = Tm.min(0), Tm.max(0)
    # 夏季(北半球 5-9 月，南半球 11-3 月)降水
    nh = np.zeros(12, bool)
    nh[4:9] = True
    sh = np.zeros(12, bool)
    sh[[10, 11, 0, 1, 2]] = True
    Psum = np.where(grid.LAT >= 0, Pm[nh].sum(0), Pm[sh].sum(0))
    gdd5 = (np.maximum(Tm - 5, 0) * 30.4).sum(0)
    MI = hyd["MI"]
    Q = hyd["discharge"]
    runoff = hyd["runoff"]

    k = grid.res / 0.25  # 参数按 0.25° 格(约 28km)标定
    win = max(3, int(round(3 / k)) | 1)        # 邻域窗口保持约 80km
    near = max(1, int(round(1 / k)))           # "相邻"保持约 28km

    # 崎岖度：约 80km 邻域海拔标准差(m)
    e = np.where(land, elev, 0.0)
    m1 = ndimage.uniform_filter(e, win)
    m2 = ndimage.uniform_filter(e * e, win)
    rugged = np.sqrt(np.maximum(m2 - m1 * m1, 0))

    npp = _miami_npp(Tann, Pann)

    coast = land & ndimage.binary_dilation(ocean, structure=np.ones((3, 3)), iterations=near)
    lake_adj = land & ndimage.binary_dilation(lake, structure=np.ones((3, 3)), iterations=2 * near)
    # 相邻海域的浅海程度(大陆架)
    shallow = np.where(ocean, 1 - terr["depth"], 0.0)
    shelf = ndimage.maximum_filter(shallow, 2 * near + 1)
    floodplain = (Q > 50) & (rugged < 150)

    # ---- 资源：各生计方式的承载上限 ----
    fA = np.clip((4300 - elev) / 1300, 0, 1)
    fS = np.clip(1 - rugged / 600, 0.05, 1)
    perma = np.clip((Tann + 8) / 4, 0, 1)

    fT = np.clip((gdd5 - 800) / 1400, 0, 1)
    # 雨养农业在半干旱区风险极高(旱年绝收)，湿润指数 0.35 以下基本不可行
    fW = np.clip((MI - 0.35) / 0.45, 0, 1) ** 1.5 * (1 - 0.3 * np.clip((MI - 2.5) / 2, 0, 1))
    fsoil = 1 - 0.35 * np.clip((3 - Tann) / 8, 0, 1)
    cap_dry = 25 * fT * fW * fS * fA * fsoil * perma * (1 + 0.5 * floodplain)

    fTr = (np.clip((gdd5 - 2600) / 1400, 0, 1) * np.clip((Tjul - 20) / 4, 0, 1)
           * (1 + 0.4 * np.clip((gdd5 - 5500) / 1500, 0, 1)))
    # 水稻要么靠季风夏雨，要么靠湿润区大河泛滥平原；干旱区引河种植归入"灌溉农业"
    river_w = np.clip((np.log10(1 + Q) - 2) / 1.5, 0, 1) * (rugged < 200) * np.clip((MI - 0.4) / 0.3, 0, 1)
    fWr = np.maximum(np.clip((Psum - 600) / 500, 0, 1), river_w)
    cap_rice = 120 * fTr * fWr * np.clip(1 - rugged / 300, 0.1, 1) * fA

    # 灌溉：干旱区靠河流取水，可灌面积 = 可用水量 / 作物缺水量
    need_m = np.maximum(hyd["PET"] - Pann, 200) / 1000
    irr_km2 = Q * 3.15e7 * 0.25 / need_m / 1e6
    # 前工业时代的渠灌只能依托常年大河(流量>30m³/s)
    irr_frac = np.where(Q > 30, np.clip(irr_km2 / grid.area_km2, 0, 0.6), 0)
    irr_frac = np.maximum(irr_frac, 0.5 * ndimage.maximum_filter(irr_frac, 2 * near + 1))  # 渠道惠及两岸
    arid = np.clip((0.8 - MI) / 0.5, 0, 1)
    cap_irr = 100 * irr_frac * np.clip((gdd5 - 2000) / 1500, 0, 1) * np.clip(1 - rugged / 250, 0, 1) * arid * fA

    # 北方针叶林(积温低且不太干)林下草场很少；冻土保水，针叶林在湿润指数 0.35 以上即可成林
    boreal = (gdd5 >= 500) & (gdd5 < 1400) & ((MI >= 0.3) | (Tann < -2))
    grass = np.select(
        [gdd5 < 500, boreal, MI < 0.08, MI < 0.25, MI < 0.85, MI < 1.2],
        [0.5, 0.12, 0.25, 0.75, 1.0, 0.65], default=0.35)
    cap_past = (2.2 * np.clip(npp / 700, 0, 1.2) * grass * np.clip(1 - rugged / 1500, 0.2, 1)
                * np.clip((5200 - elev) / 1000, 0, 1))
    # 苔原(积温<700)上的畜牧就是驯鹿放牧，单独计
    tundra = (gdd5 < 700) & (np.abs(grid.LAT) > 50)
    cap_rein = np.where(tundra, 0.35 * np.clip(npp / 400, 0, 1), 0)
    cap_past = np.where(tundra, 0, cap_past)
    cap_hg = 0.03 + 0.3 * np.clip(npp / 1500, 0, 1)

    f_m = 0.6 + 0.6 * np.clip((np.abs(grid.LAT) - 25) / 35, 0, 1)
    ice = np.where(Tjan < -15, 0.5, 1.0)
    river_fish = 0.6 * np.log10(1 + Q / 50) * (Q > 20)
    cap_fish = 2.5 * coast * shelf * f_m * ice + river_fish + 1.0 * lake_adj

    # 极寒与高原缺氧对所有定居方式的惩罚
    cold = np.where(Tjan < -40, 0.7, 1.0)
    hypoxia = np.clip(1 - (elev - 3500) / 2000, 0.4, 1)
    for a in (cap_dry, cap_rice, cap_irr, cap_hg, cap_fish):
        a *= cold * hypoxia

    freshwater = np.maximum.reduce([
        np.clip(np.log10(1 + runoff) / 2.7, 0, 1),
        np.clip(np.log10(1 + Q) / 4, 0, 1),
        lake_adj.astype(float)])

    # ---- 第2层：生计方式与潜在人口密度 ----
    desert = (MI < 0.15) & (gdd5 > 1500)
    high = elev >= 3000
    cand = np.stack([
        np.zeros(grid.shape),                                   # 0 无人区
        cap_rice + 0.3 * cap_fish,                              # 1 水稻
        cap_dry + 0.2 * cap_fish,                               # 2 旱作
        cap_irr + 0.2 * cap_past,                               # 3 灌溉
        0.55 * cap_dry + 0.8 * cap_past,                        # 4 农牧混合
        np.where(~desert & ~high, cap_past, 0),                 # 5 草原游牧
        np.where(desert & ~high, cap_past, 0),                  # 6 荒漠游牧
        np.where(high, cap_past, 0),                            # 7 高原游牧
        cap_rein + 0.3 * cap_hg,                                # 8 苔原驯鹿
        cap_hg,                                                 # 9 森林狩猎采集
        cap_fish + 0.5 * cap_hg,                                # 10 渔猎
    ])
    cand[0] = 0.01
    mode = np.argmax(cand, axis=0)
    density = cand.max(0)
    density = np.where(mode == 0, 0.0, density)
    mode = np.where(land, mode, 0)
    density = np.where(land, density, 0)
    mobility = np.array(MOBILITY, float)[mode]

    # ---- 地表覆盖 ----
    lc = np.select(
        [~land, (Tjul < 0) | ((elev > 4800) & (gdd5 < 50)), (elev > 3300) & (gdd5 < 900), Tjul < 10,
         boreal, MI < 0.12, MI < 0.45, (MI < 0.75) & (Tann > 15), MI < 0.75, gdd5 < 1300, Tann < 16],
        [0, 1, 9, 2, 3, 7, 6, 8, 6, 3, 4], default=5)

    # ---- 迁移阻力(相对步行时间倍数) ----
    lc_mul = np.array([1, 5.0, 1.4, 1.3, 1.2, 1.4, 0.8, 1.8, 1.0, 1.3])[lc]
    fr = (1 + rugged / 300 + np.clip((elev - 3000) / 1500, 0, None)) * lc_mul
    fr = np.where(hyd["channel"] | (Q > 500), fr * 0.7, fr)
    near_land = ndimage.binary_dilation(land, structure=np.ones((3, 3)), iterations=3 * near)
    sea_fr = np.where(near_land & (terr["depth"] < 0.6), 6.0, 1e4)  # 近岸浅海可行舟，远洋不可通行
    friction = np.where(land, fr, np.where(lake, 4.0, sea_fr))

    ksel = {c: i for i, c in enumerate(KOPPEN_CODES)}
    koppen = np.vectorize(lambda c: ksel.get(c, 0))(clim["koppen"]).astype(np.uint8)
    koppen = np.where(land, koppen, 0)

    return {
        "elev": elev, "rugged": rugged, "friction": friction, "landcover": lc.astype(np.uint8),
        "coast": coast.astype(np.uint8),
        "t_ann": Tann, "t_cold": Tjan, "t_warm": Tjul, "p_ann": Pann,
        "p_summer_frac": Psum / np.maximum(Pann, 1), "mi": MI, "gdd5": gdd5, "koppen": koppen,
        "runoff": runoff, "discharge": Q, "freshwater": freshwater, "npp": npp,
        "cap_dry": cap_dry, "cap_rice": cap_rice, "cap_irr": cap_irr, "cap_past": cap_past,
        "cap_rein": cap_rein, "cap_hg": cap_hg, "cap_fish": cap_fish,
        "density": density, "mode": mode.astype(np.uint8), "mobility": mobility,
    }
