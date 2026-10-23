"""汇总到模拟格(默认0.5°)并输出 manifest.json + cells.bin。"""
import json

import numpy as np

from .resources import KOPPEN_CODES, LANDCOVER, MODES

KOPPEN_COLORS = {
    "Af": "#0000fe", "Am": "#0077ff", "Aw": "#46a9fa", "BWh": "#fe0000", "BWk": "#fe9695",
    "BSh": "#f5a301", "BSk": "#ffdb63", "Csa": "#ffff00", "Csb": "#c6c700", "Csc": "#969600",
    "Cwa": "#96ff96", "Cwb": "#63c764", "Cwc": "#329633", "Cfa": "#c6ff4e", "Cfb": "#66ff33",
    "Cfc": "#33c701", "Dsa": "#ff00fe", "Dsb": "#c600c7", "Dsc": "#963295", "Dsd": "#966495",
    "Dwa": "#abb1ff", "Dwb": "#5a77db", "Dwc": "#4c51b5", "Dwd": "#320087", "Dfa": "#00ffff",
    "Dfb": "#38c7ff", "Dfc": "#007e7d", "Dfd": "#00455e", "ET": "#b2b2b2", "EF": "#686868", "": "#000000"}
LANDCOVER_COLORS = ["#2b5d8a", "#e8e8f0", "#a8b8a0", "#2f5d3a", "#4f8a3c", "#1f7a3f", "#c8c46a",
                    "#e8d49a", "#a8a058", "#8fa88a"]
MODE_COLORS = ["#d8d4cc", "#2e9e5b", "#d9b64a", "#3aa6b8", "#a8b85a", "#d9823b", "#c25b4a",
               "#8e6bb8", "#7fa3c4", "#2f6b3a", "#2f6fb5"]

# (字段id, 名称, 单位, 分组, 样式, 色带/范围, 说明, 汇总方式)
FIELDS = [
    ("elev", "海拔", "m", "基础", "continuous", ("terrain", [-100, 5500]), "由输入地图解析(分层设色、高程图或草图)", "mean"),
    ("rugged", "崎岖度", "m", "基础", "continuous", ("magma", [0, 800]), "邻域海拔标准差，影响通行和防守", "mean"),
    ("friction", "迁移阻力", "倍", "基础", "continuous", ("viridis_r", [0.5, 8]), "相对平原步行的时间倍数；远洋不可通行", "mean"),
    ("landcover", "地表覆盖", "", "基础", "categorical", None, "由气候推断的自然植被", "mode"),
    ("coast", "沿海", "", "基础", "categorical", None, "是否临海", "max"),
    ("t_ann", "年均温", "°C", "气候", "continuous", ("RdBu_r", [-20, 30]), "纬度基准-海拔递减±大陆度", "mean"),
    ("t_cold", "最冷月均温", "°C", "气候", "continuous", ("RdBu_r", [-45, 30]), "北半球约为1月，南半球约为7月", "mean"),
    ("t_warm", "最暖月均温", "°C", "气候", "continuous", ("RdBu_r", [-5, 38]), "北半球约为7月，南半球约为1月", "mean"),
    ("p_ann", "年降水", "mm", "气候", "continuous", ("YlGnBu", [0, 2500]), "季风/西风水汽平流模型", "mean"),
    ("p_summer_frac", "夏雨比例", "", "气候", "continuous", ("PuOr", [0.2, 0.9]), "夏季(北半球5-9月，南半球11-3月)降水占全年比例", "mean"),
    ("mi", "湿润指数", "P/PET", "气候", "continuous", ("BrBG", [0, 2]), "<0.2 荒漠，0.2-0.5 草原，>1 湿润", "mean"),
    ("gdd5", "≥5°C积温", "°C·日", "气候", "continuous", ("inferno", [0, 7000]), "作物热量条件", "mean"),
    ("koppen", "柯本气候", "", "气候", "categorical", None, "柯本-盖格分类", "mode"),
    ("runoff", "径流深", "mm", "水", "continuous", ("Blues", [0, 1200]), "降水-蒸散(含融雪)", "mean"),
    ("discharge", "河流流量", "m³/s", "水", "continuous", ("Blues", [0, 20000]), "D8汇流，干旱区河道损耗", "max"),
    ("freshwater", "淡水指数", "0-1", "水", "continuous", ("Blues", [0, 1]), "径流、河流、湖泊综合", "mean"),
    ("npp", "净初级生产力", "g/m²/年", "资源", "continuous", ("YlGn", [0, 2000]), "Miami模型", "mean"),
    ("cap_dry", "旱作承载", "人/km²", "资源", "continuous", ("YlOrBr", [0, 60]), "麦粟等旱作物", "mean"),
    ("cap_rice", "水稻承载", "人/km²", "资源", "continuous", ("YlGn", [0, 250]), "需高积温、夏雨或河水", "mean"),
    ("cap_irr", "灌溉承载", "人/km²", "资源", "continuous", ("PuBu", [0, 120]), "干旱区引河灌溉", "mean"),
    ("cap_past", "牧草承载", "人/km²", "资源", "continuous", ("YlOrRd", [0, 3]), "畜牧可养活人口", "mean"),
    ("cap_rein", "驯鹿承载", "人/km²", "资源", "continuous", ("PuBuGn", [0, 0.3]), "苔原地衣", "mean"),
    ("cap_hg", "狩猎采集承载", "人/km²", "资源", "continuous", ("Greens", [0, 0.4]), "野生动植物", "mean"),
    ("cap_fish", "渔业承载", "人/km²", "资源", "continuous", ("GnBu", [0, 4]), "海岸大陆架、河流、湖泊", "mean"),
    ("density", "潜在人口密度", "人/km²", "承载", "continuous", ("log_magma", [0.01, 300]), "最优生计方式下的承载人口", "mean"),
    ("mode", "主导生计方式", "", "承载", "categorical", None, "承载人口最多的生计方式", "mode"),
    ("mobility", "流动范围", "km/年", "承载", "continuous", ("plasma", [0, 350]), "该生计方式的典型年流动范围", "mean"),
    ("area_km2", "陆地面积", "km²", "基础", "continuous", ("Greys", [0, 3500]), "格子内陆地面积", "sum"),
    ("land_frac", "陆地比例", "", "基础", "continuous", ("Greys", [0, 1]), "", "mean"),
]


def _cat_meta(fid):
    if fid == "koppen":
        return KOPPEN_CODES, [KOPPEN_COLORS[c] for c in KOPPEN_CODES]
    if fid == "landcover":
        return LANDCOVER, LANDCOVER_COLORS
    if fid == "mode":
        return MODES, MODE_COLORS
    if fid == "coast":
        return ["否", "是"], ["#ffffff", "#2f6fb5"]
    return None, None


def aggregate(grid, terr, res, factor):
    """把 0.25° 字段按 factor×factor 块汇总到模拟格。"""
    land = terr["land"]
    ny, nx = grid.shape
    Ny, Nx = ny // factor, nx // factor
    sl = (slice(0, Ny * factor), slice(0, Nx * factor))

    def blocks(a):
        return a[sl].reshape(Ny, factor, Nx, factor).transpose(0, 2, 1, 3).reshape(Ny, Nx, -1)

    L = blocks(land)
    nland = L.sum(-1)
    area = blocks(np.where(land, grid.area_km2, 0)).sum(-1)
    out = {"land_frac": nland / (factor * factor), "area_km2": area}
    sim_land = nland * 2 >= factor * factor
    for fid, _, _, _, style, _, _, how in FIELDS:
        if fid in out:
            continue
        a = blocks(res[fid].astype(float))
        if fid == "friction":
            # 水域格保留水上阻力，陆地格取陆地均值
            water_val = np.min(a, axis=-1)
            lv = np.where(L, a, np.nan)
            with np.errstate(invalid="ignore"):
                land_val = np.nanmean(lv, -1)
            out[fid] = np.where(sim_land, land_val, water_val)
            continue
        lv = np.where(L, a, np.nan)
        with np.errstate(invalid="ignore", all="ignore"):
            if how == "mean":
                v = np.nanmean(lv, -1)
            elif how == "max":
                v = np.nanmax(lv, -1)
            elif how == "sum":
                v = np.nansum(lv, -1)
            else:  # mode
                ai = np.where(L, a, -1).astype(int)
                v = np.zeros((Ny, Nx))
                best = np.zeros((Ny, Nx))
                for k in np.unique(ai):
                    if k < 0:
                        continue
                    c = (ai == k).sum(-1)
                    v = np.where(c > best, k, v)
                    best = np.maximum(best, c)
        out[fid] = np.where(sim_land, np.nan_to_num(v), 0)
    out["friction"] = np.where(np.isnan(out["friction"]), 1e4, out["friction"])
    lat = grid.lat[sl[0]].reshape(Ny, factor).mean(1)
    lon = grid.lon[sl[1]].reshape(Nx, factor).mean(1)
    return out, sim_land, lon, lat


def write(out_dir, grid, sim_res, out, sim_land, lon, lat, meta_extra):
    out_dir.mkdir(parents=True, exist_ok=True)
    Ny, Nx = sim_land.shape
    fields = []
    offset = 0
    with open(out_dir / "cells.bin", "wb") as f:
        def put(fid, arr, dtype, desc):
            nonlocal offset
            b = np.ascontiguousarray(arr.astype(dtype)).tobytes()
            f.write(b)
            desc.update({"id": fid, "dtype": {"float32": "f32", "uint8": "u8"}[dtype], "offset": offset, "bytes": len(b)})
            fields.append(desc)
            offset += len(b)

        put("land", sim_land, "uint8", {"name": "陆地", "group": "基础", "style": "categorical",
                                         "labels": ["水域", "陆地"], "colors": ["#2b5d8a", "#d8d4cc"], "unit": "", "desc": "模拟格是否为陆地(陆地占比≥50%)"})
        for fid, name, unit, group, style, cmap, desc, _ in FIELDS:
            d = {"name": name, "unit": unit, "group": group, "style": style, "desc": desc}
            if style == "categorical":
                labels, colors = _cat_meta(fid)
                d.update({"labels": labels, "colors": colors})
                put(fid, out[fid], "uint8", d)
            else:
                d.update({"colormap": cmap[0], "range": cmap[1]})
                put(fid, out[fid], "float32", d)
    manifest = {
        "version": 1,
        "grid": {"nx": Nx, "ny": Ny, "west": float(lon[0] - sim_res / 2), "north": float(lat[0] + sim_res / 2),
                 "res": sim_res, "layout": "row-major, 北→南, 西→东, 小端"},
        "fields": fields,
        **meta_extra,
    }
    (out_dir / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=1), encoding="utf-8")
    return manifest
