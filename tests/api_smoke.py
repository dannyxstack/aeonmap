"""端到端冒烟测试：上传 → 预处理 → 带约束的演化 → 导出 → 复用 → 额度/权限 → 回调签名。

    python tests/api_smoke.py http://127.0.0.1:8765 $(cat data/local.key)

服务端需以 MAPSIM_ALLOW_PRIVATE_WEBHOOKS=1 启动(回调发到本机)。
"""
import io
import json
import os
import subprocess
import sys
import threading
import time
import zipfile
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "sdk" / "python"))
from mapsim_client import ApiError, Client, verify_webhook  # noqa: E402

base, key = sys.argv[1], sys.argv[2]
c = Client(base, key, timeout=600)
hooks = []


class Hook(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers["Content-Length"]))
        hooks.append((dict(self.headers), body))
        self.send_response(204)
        self.end_headers()

    def log_message(self, *a):
        pass


# 服务端在容器里时，回调要发到宿主机：SMOKE_HOOK_HOST=<docker 网桥网关>，监听所有网卡
hook_host = os.environ.get("SMOKE_HOOK_HOST", "127.0.0.1")
srv = HTTPServer(("0.0.0.0" if hook_host != "127.0.0.1" else "127.0.0.1", 0), Hook)
threading.Thread(target=srv.serve_forever, daemon=True).start()
hook_url = f"http://{hook_host}:{srv.server_port}/hook"


def check(cond, msg):
    print(("  ok  " if cond else "  FAIL ") + msg)
    if not cond:
        sys.exit(1)


print("1. 账户")
me = c.me()
check(me["plan"]["name"] in ("internal", "pro", "studio", "free"), f"租户 {me['id']} 套餐 {me['plan']['name']}")

print("2. 上传南半球高程图")
fx = ROOT / "prep" / "tests" / "fixtures" / "sh_dem"
mj = json.loads((fx / "map.json").read_text())
b = mj["bounds"]
world, job = c.create_world(str(fx / "dem.png"), input="grayscale", name="冒烟测试-南方大陆",
                            bounds=(b["west"], b["east"], b["south"], b["north"]),
                            elevation={"min": mj["elevation"]["min"], "max": mj["elevation"]["max"]})
job = c.wait(job["id"], poll=1)
world = c.world(world["id"])
check(world["status"] == "ready" and world["landCells"] > 1000, f"预处理完成，陆地格 {world['landCells']}")

print("3. 带约束的演化 + 全格式导出 + 回调")
sim_req = {"years": 300, "seed": int(time.time()) % 1000000 + 1,
           "constraints": {"polities": [{"name": "南十字王国", "capital": [135, -30],
                                         "region": [[[128, -34], [142, -34], [142, -26], [128, -26], [128, -34]]]}]}}
job = c.submit(world["id"], simulate=sim_req, export={"engineSize": 257}, webhook=hook_url)
job = c.wait(job["id"], poll=2, on_progress=lambda j: print(f"     {j['stage']:8s} {j['progress']*100:5.1f}% {j.get('message','')}", end="\r"))
print()
res = job["result"]
check(res["runId"] and res["exportUrl"] and not res.get("runReused"), f"演化 {res['runId']}，导出 {res['exportId']}，消耗 {job['units']} 点")
zbytes = c._req("GET", res["exportUrl"], raw=True)
z = zipfile.ZipFile(io.BytesIO(zbytes))
names = z.namelist()
need = ["README.md", "world/elev.tif", "world/koppen.tif", "world/rivers.geojson", "engine/heightmap.png",
        "engine/heightmap.raw", "engine/splatmap.png", "run/metrics.csv", "run/events.csv"]
check(all(n in names for n in need), f"导出包含 {len(names)} 个文件")
yr = [n for n in names if n.startswith("run/year_")]
check(any(n.endswith("borders.geojson") for n in yr) and any(n.endswith("roads.geojson") for n in yr), "含疆域/道路/城市")
borders = json.loads(z.read([n for n in yr if n.endswith("borders.geojson")][0]))
check(any(f["properties"]["name"] == "南十字王国" for f in borders["features"]), "约束政权出现在疆域里并保留名字")
roads = json.loads(z.read([n for n in yr if n.endswith("roads.geojson")][0]))
check(len(roads["features"]) > 10, f"道路 {len(roads['features'])} 条")
check(len(z.read("engine/heightmap.raw")) == 257 * 257 * 2, "引擎高程 RAW 尺寸正确")
# GeoTIFF 用 tifffile 读回并核对地理范围
tmp = Path("/tmp/mapsim_smoke_elev.tif")
tmp.write_bytes(z.read("world/elev.tif"))
code = ("import sys; sys.path.insert(0, %r); from mapprep.config import geotiff_bounds; import tifffile; "
        "a = tifffile.imread(%r); b = geotiff_bounds(%r); print(a.shape, b)") % (str(ROOT / "prep"), str(tmp), str(tmp))
out = subprocess.run([sys.argv[3] if len(sys.argv) > 3 else sys.executable, "-c", code], capture_output=True, text=True)
check(out.returncode == 0 and "'west': 110" in out.stdout, "GeoTIFF 可被 tifffile 读取，地理参考正确: " + out.stdout.strip())

print("4. 相同请求复用已有演化")
job2 = c.wait(c.submit(world["id"], simulate=sim_req)["id"], poll=1)
check(job2["result"]["runReused"] and job2["result"]["runId"] == res["runId"] and job2["units"] == 0,
      f"复用 {job2['result']['runId']}，消耗 {job2['units']} 点")

print("5. 接口：疆域、道路、指标、事件、家族明细")
fc = c.borders(res["runId"])
check(len(fc["features"]) > 0, f"疆域 {len(fc['features'])} 个")
cities = c.roads(res["runId"], cities=True)
check(len(cities["features"]) > 5, f"城市 {len(cities['features'])} 座")
ms = c.metrics(res["runId"])
check(len(ms) == 300, "逐年指标 300 行")
insp = c.inspect(res["runId"], 295, int([f for f in cities["features"]][0]["properties"]["id"]) * 0 + 0)
check("families" in insp, "家族明细可读取")

print("6. 回调签名")
check(len(hooks) >= 1, f"收到 {len(hooks)} 次回调")
h, body = hooks[-1]
check(verify_webhook(me["webhookSecret"], h["X-Mapsim-Timestamp"], body, h["X-Mapsim-Signature"]), "签名校验通过")

print("7. 权限与额度")
try:
    Client(base, "msk_bad").me()
    check(False, "无效密钥应被拒绝")
except ApiError as e:
    check(e.status == 401, "无效密钥 → 401")
# 免费测试用户：有管理员令牌时走管理接口(服务在容器里也适用)，否则用本机命令行
admin_tok = os.environ.get("SMOKE_ADMIN_TOKEN")
if admin_tok:
    import urllib.request
    req = urllib.request.Request(base + "/admin/v1/tenants", method="POST",
                                 data=json.dumps({"name": "冒烟免费", "plan": "free"}).encode(),
                                 headers={"Authorization": f"Bearer {admin_tok}", "Content-Type": "application/json"})
    free_key = json.loads(urllib.request.urlopen(req).read())["key"]
else:
    out = subprocess.run([str(ROOT / "mapsim"), "tenant", "create", "-name", "冒烟免费", "-plan", "free", "-root", str(ROOT)],
                         capture_output=True, text=True)
    free_key = [l.split("：")[1].strip() for l in out.stdout.splitlines() if l.startswith("接口密钥")][0]
fc_ = Client(base, free_key)
try:
    fc_.world(world["id"])
    check(False, "其他租户不应看到该世界")
except ApiError as e:
    check(e.status == 404, "其他租户看不到你的世界 → 404")
try:
    fc_.submit("eurasia", simulate={"years": 4000})
    check(False, "免费套餐不允许 4000 年")
except ApiError as e:
    check(e.status == 400, "免费套餐超过年数上限 → 400")
# 免费套餐每月 100 点；欧亚 1000 年约 15 点/次。排队中的任务也计入占用，第 7 次提交应被拒绝。
queued = []
try:
    for k in range(10):
        queued.append(fc_.submit("eurasia", simulate={"years": 1000, "seed": 1000 + k})["id"])
    check(False, "应超出免费额度")
except ApiError as e:
    check(e.status == 402 and len(queued) == 6, f"排队 {len(queued)} 个后超出每月额度 → 402")
for jid in queued:
    try:
        fc_.cancel(jid)
    except ApiError:
        pass
time.sleep(3)
check(all(fc_.job(j)["status"] in ("cancelled", "failed") for j in queued), "排队/运行中的任务均可取消")
try:
    c.submit(world["id"], simulate={"years": 10, "params": {"notAParam": 1}})
    check(False, "未知参数应被拒绝")
except ApiError as e:
    check(e.status == 400, "未知参数 → 400")

print("8. 清理")
c.delete_world(world["id"])
check(all(w["id"] != world["id"] for w in c.worlds()), "世界已删除")
print("全部通过")
