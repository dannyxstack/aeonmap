"""AeonMap 世界推演引擎 Python SDK(只依赖标准库)。

    from mapsim_client import Client
    c = Client("https://aeonmap.novakitestudio.com", "msk_...")  # 本地部署用 http://127.0.0.1:8765
    world, job = c.create_world("dem.png", input="grayscale", bounds=(110, 160, -45, -10),
                                elevation={"min": -3000, "max": 5000})
    c.wait(job["id"])
    job = c.submit(world["id"], simulate={"years": 2000, "seed": 42},
                   export={"formats": ["geotiff", "geojson", "engine"]})
    job = c.wait(job["id"], on_progress=lambda j: print(j["stage"], j["progress"]))
    c.download(job["result"]["exportUrl"], "out.zip")
"""
import hashlib
import hmac
import json
import mimetypes
import os
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

__all__ = ["Client", "ApiError", "verify_webhook"]


class ApiError(Exception):
    def __init__(self, status, code, message):
        super().__init__(f"HTTP {status} {code}: {message}")
        self.status, self.code, self.message = status, code, message


class Client:
    def __init__(self, base_url, api_key, timeout=60):
        self.base = base_url.rstrip("/")
        self.key = api_key
        self.timeout = timeout

    # ---- 底层 ----
    def _req(self, method, path, body=None, headers=None, raw=False):
        h = {"Authorization": f"Bearer {self.key}"}
        h.update(headers or {})
        if isinstance(body, (dict, list)):
            body = json.dumps(body, ensure_ascii=False).encode()
            h["Content-Type"] = "application/json"
        req = urllib.request.Request(self.base + path, data=body, method=method, headers=h)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as r:
                data = r.read()
                return data if raw else (json.loads(data) if data else None)
        except urllib.error.HTTPError as e:
            data = e.read()
            try:
                err = json.loads(data)["error"]
                raise ApiError(e.code, err.get("code"), err.get("message")) from None
            except (ValueError, KeyError, TypeError):
                raise ApiError(e.code, "http_error", data.decode(errors="replace")) from None

    def get(self, path, **params):
        if params:
            path += "?" + urllib.parse.urlencode(params)
        return self._req("GET", path)

    # ---- 账户 ----
    def me(self):
        return self.get("/v1/me")

    def usage(self):
        return self.get("/v1/usage")

    # ---- 世界 ----
    def worlds(self):
        return self.get("/v1/worlds")["worlds"]

    def world(self, world_id):
        return self.get(f"/v1/worlds/{world_id}")

    def create_world(self, path, input, bounds=None, name=None, elevation=None, hints=None, webhook=None):
        """上传地图。input: grayscale | sketch | hypsometric；bounds=(西, 东, 南, 北)，GeoTIFF 可省略。"""
        fields = {"input": input}
        if name:
            fields["name"] = name
        if bounds is not None:
            w, e, s, n = bounds
            fields["bounds"] = json.dumps({"west": w, "east": e, "south": s, "north": n})
        if elevation:
            fields["elevation"] = json.dumps(elevation, ensure_ascii=False)
        if hints:
            fields["hints"] = json.dumps(hints, ensure_ascii=False)
        if webhook:
            fields["webhook"] = webhook
        boundary = uuid.uuid4().hex
        parts = []
        for k, v in fields.items():
            parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode())
        fname = os.path.basename(path)
        ctype = mimetypes.guess_type(fname)[0] or "application/octet-stream"
        with open(path, "rb") as f:
            data = f.read()
        parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{fname}"\r\n'
                     f'Content-Type: {ctype}\r\n\r\n'.encode() + data + b"\r\n")
        parts.append(f"--{boundary}--\r\n".encode())
        r = self._req("POST", "/v1/worlds", b"".join(parts),
                      {"Content-Type": f"multipart/form-data; boundary={boundary}"})
        return r["world"], r["job"]

    def delete_world(self, world_id):
        return self._req("DELETE", f"/v1/worlds/{world_id}")

    def layer_geotiff(self, world_id, name, out_path):
        """下载单个静态图层，例如 name='koppen'。"""
        with open(out_path, "wb") as f:
            f.write(self._req("GET", f"/v1/worlds/{world_id}/layers/{name}.tif", raw=True))
        return out_path

    # ---- 任务 ----
    def submit(self, world_id, simulate=None, export=None, webhook=None):
        body = {}
        if simulate is not None:
            body["simulate"] = simulate
        if export is not None:
            body["export"] = export
        if webhook:
            body["webhook"] = webhook
        return self._req("POST", f"/v1/worlds/{world_id}/jobs", body)

    def job(self, job_id):
        return self.get(f"/v1/jobs/{job_id}")

    def cancel(self, job_id):
        return self._req("DELETE", f"/v1/jobs/{job_id}")

    def wait(self, job_id, poll=2.0, timeout=3600, on_progress=None):
        """轮询直到任务结束；失败时抛出 ApiError。"""
        t0 = time.time()
        while True:
            j = self.job(job_id)
            if on_progress:
                on_progress(j)
            if j["status"] == "succeeded":
                return j
            if j["status"] == "failed":
                raise ApiError(200, "job_failed", j.get("error", ""))
            if time.time() - t0 > timeout:
                raise TimeoutError(job_id)
            time.sleep(poll)

    def download(self, url_path, out_path):
        with open(out_path, "wb") as f:
            f.write(self._req("GET", url_path, raw=True))
        return out_path

    # ---- 演化结果 ----
    def runs(self, world=None):
        return self.get("/v1/runs", **({"world": world} if world else {}))["runs"]

    def metrics(self, run_id):
        return self.get(f"/v1/runs/{run_id}/metrics")

    def events(self, run_id, start=None, end=None):
        p = {}
        if start is not None:
            p["from"] = start
        if end is not None:
            p["to"] = end
        return self.get(f"/v1/runs/{run_id}/events", **p)

    def borders(self, run_id, year=None):
        return self.get(f"/v1/runs/{run_id}/borders", **({"year": year} if year is not None else {}))

    def roads(self, run_id, year=None, cities=False):
        p = {"layer": "cities"} if cities else {}
        if year is not None:
            p["year"] = year
        return self.get(f"/v1/runs/{run_id}/roads", **p)

    def inspect(self, run_id, year, cell):
        return self.get(f"/v1/runs/{run_id}/inspect", year=year, cell=cell)


def verify_webhook(secret, timestamp, body, signature, tolerance=300):
    """校验回调签名。signature 为请求头 X-Mapsim-Signature 的值(sha256=...)。"""
    if abs(time.time() - int(timestamp)) > tolerance:
        return False
    mac = hmac.new(secret.encode(), f"{timestamp}.".encode() + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest("sha256=" + mac, signature)

