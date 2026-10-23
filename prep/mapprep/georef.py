"""像素 <-> 经纬度。

equirectangular：x 线性对应经度，y 线性对应纬度（标准输入）。
control_points：非等距投影的图(如当前欧亚图)，用控制点拟合多项式做适配。
"""
import numpy as np


def _design(u, v, deg):
    cols = []
    for i in range(deg + 1):
        for j in range(deg + 1 - i):
            cols.append(u ** i * v ** j)
    return np.stack(cols, axis=-1)


class Equirect:
    def __init__(self, cfg, width, height):
        self.cfg, self.W, self.H = cfg, width, height

    def geo_to_px(self, lon, lat):
        c = self.cfg
        x = (np.asarray(lon) - c.west) / (c.east - c.west) * self.W - 0.5
        y = (c.north - np.asarray(lat)) / (c.north - c.south) * self.H - 0.5
        return x, y

    def px_to_geo(self, x, y):
        c = self.cfg
        lon = c.west + (np.asarray(x) + 0.5) / self.W * (c.east - c.west)
        lat = c.north - (np.asarray(y) + 0.5) / self.H * (c.north - c.south)
        return lon, lat


class ControlPoints:
    def __init__(self, points, deg=3):
        p = np.array(points, dtype=float)
        self.deg = deg
        self.px, self.py, self.lon, self.lat = p.T
        self._pn = (self.px.mean(), self.px.std(), self.py.mean(), self.py.std())
        self._gn = (self.lon.mean(), self.lon.std(), self.lat.mean(), self.lat.std())
        A = _design(*self._norm(self.px, self.py, self._pn), deg)
        self.c_lon = np.linalg.lstsq(A, self.lon, rcond=None)[0]
        self.c_lat = np.linalg.lstsq(A, self.lat, rcond=None)[0]
        B = _design(*self._norm(self.lon, self.lat, self._gn), deg)
        self.c_x = np.linalg.lstsq(B, self.px, rcond=None)[0]
        self.c_y = np.linalg.lstsq(B, self.py, rcond=None)[0]

    @staticmethod
    def _norm(a, b, n):
        return (np.asarray(a, float) - n[0]) / n[1], (np.asarray(b, float) - n[2]) / n[3]

    def px_to_geo(self, x, y):
        A = _design(*self._norm(x, y, self._pn), self.deg)
        return A @ self.c_lon, A @ self.c_lat

    def geo_to_px(self, lon, lat):
        B = _design(*self._norm(lon, lat, self._gn), self.deg)
        return B @ self.c_x, B @ self.c_y

    def residual_km(self):
        lon, lat = self.px_to_geo(self.px, self.py)
        return np.hypot((lon - self.lon) * 111 * np.cos(np.radians(self.lat)), (lat - self.lat) * 111)


def make_georef(cfg, width, height):
    proj = cfg.raw.get("projection", {"type": "equirectangular"})
    if proj["type"] == "control_points":
        return ControlPoints(proj["points"], proj.get("degree", 3))
    return Equirect(cfg, width, height)
