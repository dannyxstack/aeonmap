"""预处理入口(由 Go 调用)：python prep/main.py --map maps/eurasia --out cache/<key>

标准输出每行一条 JSON 进度；标准错误输出错误信息。
"""
import argparse
import json
import math
import sys
import time
import traceback
from pathlib import Path

import numpy as np

sys.path.insert(0, str(Path(__file__).resolve().parent))

from mapprep import VERSION, climate, export, hydrology, resources  # noqa: E402
from mapprep.config import MapConfig  # noqa: E402
from mapprep.terrain import Grid, build_terrain  # noqa: E402


def progress(stage, p, msg=""):
    print(json.dumps({"stage": stage, "progress": round(p, 3), "message": msg}, ensure_ascii=False), flush=True)


def previews(out_dir, out, sim_land, manifest):
    import matplotlib
    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
    from matplotlib.colors import ListedColormap, LogNorm

    pv = out_dir / "preview"
    pv.mkdir(exist_ok=True)
    g = manifest["grid"]
    ext = [g["west"], g["west"] + g["nx"] * g["res"], g["north"] - g["ny"] * g["res"], g["north"]]
    by_id = {f["id"]: f for f in manifest["fields"]}
    for fid in ["koppen", "mode", "density", "p_ann", "t_ann", "discharge", "landcover", "friction",
                "cap_dry", "cap_rice", "cap_past", "cap_fish"]:
        f = by_id[fid]
        fig, ax = plt.subplots(figsize=(15, 6))
        a = np.where(sim_land, out[fid], np.nan)
        if f["style"] == "categorical":
            cm = ListedColormap(f["colors"])
            im = ax.imshow(a, cmap=cm, vmin=-0.5, vmax=len(f["colors"]) - 0.5, extent=ext, interpolation="nearest")
            present = sorted(set(np.unique(out[fid][sim_land]).astype(int)))
            from matplotlib.patches import Patch
            ax.legend(handles=[Patch(color=f["colors"][k], label=f["labels"][k]) for k in present],
                      loc="lower left", fontsize=7, ncol=2)
        elif fid in ("density", "discharge"):
            im = ax.imshow(np.where(a > 0, a, np.nan), cmap="magma" if fid == "density" else "Blues",
                           norm=LogNorm(vmin=0.01 if fid == "density" else 10, vmax=f["range"][1]),
                           extent=ext, interpolation="nearest")
            plt.colorbar(im, ax=ax, shrink=0.8)
        else:
            cmap = f["colormap"].replace("log_", "")
            im = ax.imshow(a, cmap=cmap, vmin=f["range"][0], vmax=f["range"][1], extent=ext, interpolation="nearest")
            plt.colorbar(im, ax=ax, shrink=0.8)
        ax.set_facecolor("#9cc3e0")
        ax.set_title(f"{fid}  {f['unit']}")
        ax.grid(alpha=0.25)
        fig.savefig(pv / f"{fid}.png", dpi=80, bbox_inches="tight")
        plt.close(fig)


def choose_resolution(cfg, sim_res):
    """自动分辨率：计算网格约 20 万格(欧亚 190°×66° 时为 0.25°)，模拟格为其 2 倍。"""
    if sim_res not in (None, "auto"):
        sim_res = float(sim_res)
        return sim_res / 2, sim_res
    span = (cfg.east - cfg.west) * (cfg.north - cfg.south)
    base = math.sqrt(span / 200000.0)
    base = min(max(round(base, 3), 0.01), 0.25)
    return base, round(base * 2, 4)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--map", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--params", default="{}")
    ap.add_argument("--no-preview", action="store_true")
    args = ap.parse_args()
    params = json.loads(args.params)
    out_dir = Path(args.out)
    t0 = time.time()

    cfg = MapConfig(args.map)
    base_res, sim_res = choose_resolution(cfg, params.get("sim_res", "auto"))
    factor = 2
    progress("terrain", 0.0, f"解析地形图(计算网格 {base_res}°，模拟格 {sim_res}°)")
    grid = Grid(cfg.west, cfg.east, cfg.south, cfg.north, base_res)
    terr = build_terrain(grid, cfg)
    progress("terrain", 1.0, f"陆地格 {int(terr['land'].sum())}")

    progress("climate", 0.0, "气温与降水平流")
    clim = climate.run(grid, terr, cfg.hints)
    progress("climate", 1.0, "柯本分类完成")

    progress("hydrology", 0.0, "填洼与汇流")
    hyd = hydrology.run(grid, terr, clim, cfg.rivers, cfg.external_inflow)
    progress("hydrology", 1.0, "")

    progress("resources", 0.0, "资源与承载力")
    res = resources.run(grid, terr, clim, hyd)
    progress("resources", 1.0, "")

    progress("export", 0.0, f"汇总到 {sim_res}° 模拟格")
    out, sim_land, lon, lat = export.aggregate(grid, terr, res, factor)
    pop = out["density"] * out["area_km2"]
    by_mode = {}
    for k, name in enumerate(resources.MODES):
        m = sim_land & (out["mode"] == k)
        by_mode[name] = {"cells": int(m.sum()), "potential_pop": float(pop[m].sum())}
    summary = {"land_cells": int(sim_land.sum()), "potential_pop_total": float(pop[sim_land].sum()), "by_mode": by_mode}
    rivers = hydrology.river_geojson(grid, hyd["discharge"], hyd["down"], terr["land"])
    (out_dir).mkdir(parents=True, exist_ok=True)
    # 计算网格分辨率的高程与地表覆盖(供游戏引擎导出)；海面以下按水深给负值
    elev_base = np.where(terr["land"], terr["elev"], -50.0 - 2000.0 * terr["depth"]).astype("<f4")
    elev_base.tofile(out_dir / "elev_base.bin")
    res["landcover"].astype(np.uint8).tofile(out_dir / "landcover_base.bin")
    (out_dir / "rivers.geojson").write_text(json.dumps(rivers, ensure_ascii=False), encoding="utf-8")
    manifest = export.write(out_dir, grid, sim_res, out, sim_land, lon, lat, {
        "map": cfg.raw.get("name", ""), "prep_version": VERSION, "params": params, "summary": summary,
        "bounds": {"west": cfg.west, "east": cfg.east, "south": cfg.south, "north": cfg.north},
        "base_res": base_res,
        "base": {"nx": int(grid.shape[1]), "ny": int(grid.shape[0]), "west": cfg.west, "north": cfg.north,
                 "res": base_res, "files": {"elev": "elev_base.bin", "landcover": "landcover_base.bin"}},
        "modes": resources.MODES, "mobility": resources.MOBILITY, "landcover": resources.LANDCOVER,
    })
    if not args.no_preview:
        progress("preview", 0.0, "生成预览图")
        previews(out_dir, out, sim_land, manifest)
    progress("done", 1.0, f"{time.time() - t0:.1f}s")
    print(json.dumps({"stage": "done", "manifest": str(out_dir / "manifest.json")}, ensure_ascii=False), flush=True)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        traceback.print_exc()
        sys.exit(1)
