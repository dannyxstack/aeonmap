"""预处理回归测试：python -m pytest prep/tests  或  python prep/tests/test_prep.py"""
import json
import subprocess
import sys
from pathlib import Path

import numpy as np

HERE = Path(__file__).resolve().parent
PREP = HERE.parent
sys.path.insert(0, str(PREP))

from mapprep import climate  # noqa: E402
from mapprep.terrain import Grid  # noqa: E402


def _run(map_dir, out):
    r = subprocess.run([sys.executable, str(PREP / "main.py"), "--map", str(map_dir), "--out", str(out), "--no-preview"],
                       capture_output=True, text=True)
    assert r.returncode == 0, r.stderr[-3000:]
    m = json.loads((out / "manifest.json").read_text(encoding="utf-8"))
    raw = (out / "cells.bin").read_bytes()
    n = m["grid"]["nx"] * m["grid"]["ny"]

    def field(fid):
        f = next(x for x in m["fields"] if x["id"] == fid)
        dt = np.float32 if f["dtype"] == "f32" else np.uint8
        return np.frombuffer(raw[f["offset"]:f["offset"] + f["bytes"]], dt).reshape(m["grid"]["ny"], m["grid"]["nx"])
    return m, field


def test_sh_dem(tmp_path=Path("/tmp/mapsim_test_sh")):
    m, field = _run(HERE / "fixtures" / "sh_dem", tmp_path)
    land = field("land") > 0
    assert land.sum() > 1000
    kp = set(np.array(m["fields"][[f["id"] for f in m["fields"]].index("koppen")]["labels"])[np.unique(field("koppen")[land])])
    # 南半球大陆跨 10°-45°S：应同时出现干旱(B)与温湿(C)气候
    assert any(k.startswith("B") for k in kp), kp
    assert any(k.startswith("C") for k in kp), kp
    assert (field("t_warm")[land] > field("t_cold")[land]).all()
    # 南半球：纬度越高(越往南)越冷
    t = field("t_ann")
    rows = np.nonzero(land.any(1))[0]
    north_t = t[rows[: len(rows) // 4]][land[rows[: len(rows) // 4]]].mean()
    south_t = t[rows[-len(rows) // 4:]][land[rows[-len(rows) // 4:]]].mean()
    assert north_t > south_t + 3, (north_t, south_t)
    rivers = json.loads((tmp_path / "rivers.geojson").read_text())
    assert len(rivers["features"]) > 0


def test_sketch(tmp_path=Path("/tmp/mapsim_test_sketch")):
    m, field = _run(HERE / "fixtures" / "sketch", tmp_path)
    land = field("land") > 0
    elev = field("elev")
    assert land.sum() > 500
    assert elev[land].max() > 1800, "山脉笔画应生成高山"
    assert np.median(elev[land]) < 900


def test_hemisphere_symmetry():
    """把一块大陆镜像到南半球，年均温/年降水应与北半球基本对称。"""
    res = 0.5
    gn = Grid(0, 60, 10, 60, res)
    gs = Grid(0, 60, -60, -10, res)
    rng = np.random.default_rng(0)
    y, x = np.mgrid[0:gn.shape[0], 0:gn.shape[1]]
    land = (np.hypot((x - 60) / 45, (y - 50) / 35) < 1) | (rng.random(gn.shape) < 0)
    elev = np.where(land, 300 + 1500 * np.exp(-((x - 80) / 6.0) ** 2), 0.0)

    def terr(l, e):
        return {"land": l, "ocean": ~l, "lake": np.zeros_like(l), "elev": e, "depth": np.where(l, 0, 0.5)}
    cn = climate.run(gn, terr(land, elev), {})
    cs = climate.run(gs, terr(land[::-1], elev[::-1]), {})
    Tn, Ts = cn["Tann"], cs["Tann"][::-1]
    Pn, Ps = cn["Pann"], cs["Pann"][::-1]
    assert np.abs(Tn - Ts)[land].max() < 0.5
    assert (np.abs(Pn - Ps)[land] / (Pn[land] + 50)).max() < 0.1
    assert (cn["koppen"][land] == cs["koppen"][::-1][land]).mean() > 0.95


if __name__ == "__main__":
    import subprocess as sp
    sp.run([sys.executable, str(HERE / "make_fixtures.py")], check=True)
    for t in (test_hemisphere_symmetry, test_sh_dem, test_sketch):
        t()
        print("PASS", t.__name__)
