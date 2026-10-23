"""生成色带校准用的参考 DEM(npz：z/west/north/res)。

    # 从 NOAA ERDDAP 下载 ETOPO1(1′)按步长抽稀，默认 5′(约 2160×4320，int16 约 18 MB)
    python prep/colorband/fetch_refdem.py --out data/refdem/etopo.npz --stride 5
    # 或使用本地文件：ETOPO 2022 / GEBCO 的 GeoTIFF，或 NetCDF3(.nc)
    python prep/colorband/fetch_refdem.py --file ETOPO_2022_v1_60s_N90W180_surface.tif --stride 5

数据：NOAA NCEI ETOPO1 Global Relief Model(公有领域)，经 NOAA CoastWatch ERDDAP 提供。
"""
import argparse
import sys
import tempfile
import urllib.request
from pathlib import Path

import numpy as np

ERDDAP = ("https://coastwatch.pfeg.noaa.gov/erddap/griddap/etopo180.nc"
          "?altitude%5B(-90.0):{s}:(90.0)%5D%5B(-180.0):{s}:(180.0)%5D")


def from_netcdf(path, stride=1):
    from scipy.io import netcdf_file
    with netcdf_file(str(path), "r", mmap=False) as f:
        names = set(f.variables)
        zname = next(n for n in ("altitude", "z", "elevation", "Band1") if n in names)
        latn = next(n for n in ("latitude", "lat", "y") if n in names)
        lonn = next(n for n in ("longitude", "lon", "x") if n in names)
        z = np.asarray(f.variables[zname][:])[::stride, ::stride].astype(np.float32)
        lat = np.asarray(f.variables[latn][:])[::stride].astype(float)
        lon = np.asarray(f.variables[lonn][:])[::stride].astype(float)
    if lat[0] < lat[-1]:
        z, lat = z[::-1], lat[::-1]
    res = float(abs(lat[1] - lat[0]))
    return z, float(lon[0] - res / 2), float(lat[0] + res / 2), res


def from_geotiff(path, stride=1):
    import tifffile
    with tifffile.TiffFile(str(path)) as tf:
        page = tf.pages[0]
        tie, scale = page.tags[33922].value, page.tags[33550].value
        z = page.asarray()
    z = z[::stride, ::stride].astype(np.float32)
    return z, float(tie[3]), float(tie[4]), float(scale[0] * stride)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=str(Path(__file__).resolve().parents[2] / "data" / "refdem" / "etopo.npz"))
    ap.add_argument("--file", help="本地 GeoTIFF / NetCDF3；不给则从 ERDDAP 下载")
    ap.add_argument("--stride", type=int, default=5, help="抽稀步长(源数据为 1′ 时，5 = 5′)")
    a = ap.parse_args()
    if a.file:
        p = Path(a.file)
        z, west, north, res = (from_geotiff if p.suffix.lower() in (".tif", ".tiff") else from_netcdf)(p, a.stride)
    else:
        url = ERDDAP.format(s=a.stride)
        print("下载", url, file=sys.stderr)
        with tempfile.TemporaryDirectory() as td:
            tmp = Path(td) / "etopo.nc"
            with urllib.request.urlopen(url, timeout=600) as r, open(tmp, "wb") as f:
                while True:
                    b = r.read(1 << 20)
                    if not b:
                        break
                    f.write(b)
                    print(f"\r{f.tell() / 1e6:.1f} MB", end="", file=sys.stderr)
            print(file=sys.stderr)
            z, west, north, res = from_netcdf(tmp)
    z = np.nan_to_num(z, nan=0.0)
    out = Path(a.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    np.savez_compressed(out, z=np.clip(np.round(z), -11000, 9000).astype(np.int16), west=west, north=north, res=res)
    print(f"已保存 {out}：{z.shape[1]}×{z.shape[0]}，分辨率 {res * 60:.2f}′，范围 西 {west:.2f} 北 {north:.2f}，"
          f"{out.stat().st_size / 1e6:.1f} MB")


if __name__ == "__main__":
    main()
