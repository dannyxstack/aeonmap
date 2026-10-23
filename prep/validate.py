"""对照参考气象站检验气候模型：python prep/validate.py maps/eurasia"""
import sys
import numpy as np
from mapprep.config import MapConfig
from mapprep.terrain import Grid, build_terrain
from mapprep import climate
from mapprep.stations import STATIONS


def main(map_dir, no_hints=False):
    cfg = MapConfig(map_dir)
    g = Grid(cfg.west, cfg.east, cfg.south, cfg.north, 0.25)
    T = build_terrain(g, cfg)
    C = climate.run(g, T, {} if no_hints else cfg.hints)
    eT, eP, hit, n = [], [], 0, 0
    for name, lo, la, tj, tjul, p, kp in STATIONS:
        if not (cfg.west < lo < cfg.east and cfg.south < la < cfg.north):
            continue
        i = int((cfg.north - la) / g.res); j = int((lo - cfg.west) / g.res)
        # 配准误差约50km，气象站多位于河谷：取 ±2 格内海拔最低的陆地格
        win = np.where(T['land'][i-2:i+3, j-2:j+3], T['elev'][i-2:i+3, j-2:j+3], 1e9)
        di, dj = np.unravel_index(np.argmin(win), win.shape); i += di - 2; j += dj - 2
        mj, ml, mp, mk = C['Tm'][0, i, j], C['Tm'][6, i, j], C['Pann'][i, j], C['koppen'][i, j]
        eT += [mj - tj, ml - tjul]; eP.append(np.log(max(mp, 1) / p)); hit += mk[:2] == kp[:2]; n += 1
        flag = '' if mk[:2] == kp[:2] else '  <--'
        print(f'{name:8s} Jan {mj:6.1f}({tj:6.1f}) Jul {ml:5.1f}({tjul:5.1f}) P {mp:6.0f}({p:5d}) {mk:4s}({kp}){flag}')
    eT, eP = np.array(eT), np.array(eP)
    print(f'T MAE {np.abs(eT).mean():.2f} bias {eT.mean():.2f} | P log-ratio MAE {np.abs(eP).mean():.2f} '
          f'bias {eP.mean():.2f} | Koppen2 {hit}/{n}')


if __name__ == "__main__":
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    main(args[0] if args else "maps/eurasia", no_hints="--no-hints" in sys.argv)
