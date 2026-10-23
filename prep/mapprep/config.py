"""地图说明文件(map.json)加载。"""
import json
from pathlib import Path


class MapConfig:
    def __init__(self, map_dir):
        self.dir = Path(map_dir)
        self.raw = json.loads((self.dir / "map.json").read_text(encoding="utf-8"))
        self.image = self.dir / self.raw["image"]
        b = self.raw.get("bounds", "auto")
        if b == "auto" or b is None:
            b = geotiff_bounds(self.image)
            if b is None:
                raise ValueError("map.json 没有 bounds，且图片不是带地理参考的 GeoTIFF")
            self.raw["bounds"] = b
        self.west, self.east, self.south, self.north = b["west"], b["east"], b["south"], b["north"]
        if not (-180 <= self.west < self.east <= 360 and -90 <= self.south < self.north <= 90):
            raise ValueError(f"经纬度范围无效: {b}")

    def _side(self, key, default):
        name = self.raw.get(key)
        if not name:
            return default
        p = self.dir / name
        return json.loads(p.read_text(encoding="utf-8")) if p.exists() else default

    @property
    def lakes(self):
        return {k: v for k, v in self._side("lakes", {}).items() if not k.startswith("_")}

    @property
    def rivers(self):
        return {k: v for k, v in self._side("rivers", {}).items() if not k.startswith("_")}

    @property
    def hints(self):
        return self._side("hints", {})

    @property
    def external_inflow(self):
        return self.raw.get("external_inflow", [])


def geotiff_bounds(path):
    """从 GeoTIFF 的 ModelTiepoint/ModelPixelScale 读取经纬度范围(只支持 EPSG:4326 等距经纬度)。"""
    if not str(path).lower().endswith((".tif", ".tiff")):
        return None
    try:
        import tifffile
        with tifffile.TiffFile(path) as tf:
            page = tf.pages[0]
            tags = page.tags
            if 33922 not in tags or 33550 not in tags:
                return None
            tie = tags[33922].value
            scale = tags[33550].value
            W, H = page.imagewidth, page.imagelength
            west, north = tie[3], tie[4]
            return {"west": float(west), "north": float(north),
                    "east": float(west + scale[0] * W), "south": float(north - scale[1] * H)}
    except Exception:
        return None
