# AeonMap

世界推演引擎。线上地址：https://aeonmap.novakitestudio.com

上传地形(高程图或手绘草图)，推演出气候、水文、资源、人口、政权兴衰史与交通网络，以标准格式交付：人用网页，程序用接口。

| 层 | 内容 | 实现 |
|---|---|---|
| 1 | 地形 → 气候(柯本)、水文(河网)、资源 | Python 预处理 |
| 2 | 人口承载力与生计方式 | Python 预处理 |
| 3 | 家族繁衍、遗传与变异、迁移、文化传播 | Go 模拟 |
| 4 | 征服、统治关系网、仇恨、叛乱 | Go 模拟 |
| 5 | 政权影响力场、战争、吞并、继承危机、超级帝国 | Go 模拟 |
| + | 用户约束(指定首都与领土)、交通网络、导出 | Go |

同一世界 + 同一引擎版本 + 同样参数与种子 = 完全相同的历史；重复请求直接复用结果。

## 运行

需要 Go ≥ 1.25 与 conda(首次运行自动创建 `mapsim` 环境)。

```bash
cd server && go build -o ../mapsim ./cmd/mapsim && cd ..
./mapsim
```

- 控制台：http://127.0.0.1:8765/console.html(上传地图、发起演化、下载导出包、查看用量)
- 回放：http://127.0.0.1:8765/index.html
- 接口文档：http://127.0.0.1:8765/v1/openapi.yaml(文件在 `docs/openapi.yaml`)
- 首次启动会创建本地租户，接口密钥保存在 `data/local.key`

界面语言：英文(默认)与中文，在每个页面右上角切换，选择保存在浏览器里。词条在 `server/web/static/i18n_en.js`(键为中文原文，服务端返回的错误/进度/警告按模式匹配翻译)；新增界面文字时用 `t('中文')` 包起来并补一条英文。接口返回的消息与文档仍为中文。

Docker：`docker compose up -d --build`(数据在 `./data`，预处理缓存在 `./cache`)。

### 线上部署(aeonmap.novakitestudio.com)

服务只监听 HTTP，由反向代理负责 HTTPS 证书与转发(WebSocket 回放通道 `/ws` 也经同一域名)。

1. DNS：在 novakitestudio.com 下添加 `aeonmap` 的 A/AAAA 记录(或 CNAME)指向服务器。
2. 启动服务，只让本机的代理访问：`MAPSIM_BIND=127.0.0.1 docker compose up -d --build`
3. 反向代理：`deploy/Caddyfile` 可直接使用(自动申请证书)：`sudo caddy run --config deploy/Caddyfile`，或参照其中注释改用 Nginx。
4. 线上请关闭管理后台里的"允许回调到内网地址"，并用环境变量 `MAPSIM_ADMIN_TOKEN` 设置足够长的管理员令牌。

- 控制台：https://aeonmap.novakitestudio.com/console.html
- 回放：https://aeonmap.novakitestudio.com/index.html
- 管理后台：https://aeonmap.novakitestudio.com/admin.html
- 接口文档：https://aeonmap.novakitestudio.com/v1/openapi.yaml

## 接口速览

```bash
KEY=$(cat data/local.key)
# 上传 16 位高程图(等距经纬度投影)
curl -H "Authorization: Bearer $KEY" -F file=@dem.png -F input=grayscale \
  -F 'bounds={"west":110,"east":160,"south":-45,"north":-10}' -F 'elevation={"min":-3000,"max":5000}' \
  http://127.0.0.1:8765/v1/worlds
# 演化 2000 年(北境王国的领土锁定)并导出全部格式，完成后回调
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -d '{
  "simulate": {"years": 2000, "seed": 42, "constraints": {"polities": [
     {"name": "北境王国", "capital": [135, -30], "region": [[[128,-34],[142,-34],[142,-26],[128,-26],[128,-34]]]}]}},
  "export": {"formats": ["geotiff","geojson","csv","engine"], "engineSize": 1025},
  "webhook": "https://example.com/hook"}' http://127.0.0.1:8765/v1/worlds/<世界ID>/jobs
```

Python SDK(只依赖标准库)：`sdk/python/mapsim_client.py`，用法见文件开头。

导出包内容：`world/` 静态 GeoTIFF(海拔、气候、柯本、地表、河流流量、资源、潜在人口)与河道 GeoJSON；`run/year_N/` 人口密度、政权、部落、生计 GeoTIFF，疆域多边形、道路与城市 GeoJSON，政权表 CSV；`run/` 逐年指标与事件 CSV；`engine/` 16 位高程图(PNG/RAW)、地表混合贴图；`README.md` 记录引擎版本、参数与复现键。

## AI 识图(可选)

管理后台启用后，`POST /v1/analyze`(上传 PNG/JPG)会让多模态大模型判断地图类型、读出图例色带或草图配色、从经纬网标注推算范围，返回可直接用于 `POST /v1/worlds` 的 `input`、`elevation`、`bounds` 建议。颜色由服务端在模型指出的位置从原图取样；分层设色图会把图例按原分辨率裁出再读一遍。结果需用户确认后再提交，像素级的高程反推仍由预处理完成。

```bash
curl -H "Authorization: Bearer $KEY" -F file=@map.png http://127.0.0.1:8765/v1/analyze
```

## 色带自动识别

分层设色图不需要手填色带：控制台选择"分层设色地形图"后点"自动识别色带"，或调用 `POST /v1/colorbands`(PNG/JPG，不扣点)。

1. **颜色邻接排序**：颜色聚类后，按"每层只与相邻两层接壤"求出从低到高的链，贴海的一端为低。晕渲拆开的同一层会合并，经纬网、文字、河流线条和 JPG 过渡色会被剔除。任何图都能给出顺序，数值按常见分级模板估计。
2. **参考高程校准**：服务端有参考 DEM、且填写了经纬度范围时，先按海岸线吻合度微调范围，再用相邻两层交界处的参考高程中位数作为分级阈值。
3. **图例**：自动找出排列整齐的图例色块；配合 AI 识图读出的刻度，逐层给出数值。

启用 AI 识图时，分层设色图会在 AI 读图后自动跑一遍识别，并合并两者的结果。参考 DEM 需另行准备(ETOPO1 5′，约 18 MB)：

```bash
python prep/colorband/fetch_refdem.py --out data/refdem/etopo.npz   # 从 NOAA ERDDAP 下载并抽稀
python prep/colorband/fetch_refdem.py --file ETOPO_2022_v1_60s_N90W180_surface.tif   # 或用本地 GeoTIFF
```

```bash
curl -H "Authorization: Bearer $KEY" -F file=@map.jpg \
  -F 'bounds={"west":20,"east":60,"south":10,"north":40}' http://127.0.0.1:8765/v1/colorbands
```

## 输入地图

| input | 说明 |
|---|---|
| `grayscale` | 灰度/数值高程：8/16 位 PNG、TIFF；`elevation.min/max` 给出映射，浮点 GeoTIFF 用 `encoding: "meters"`；GeoTIFF 可省略 bounds |
| `sketch` | 手绘草图：蓝=海/湖，青=河流，棕/红/黑=山脉，黄/橙=高地，其余=平原；可用 `elevation.palette` 自定义颜色 |
| `hypsometric` | 分层设色地形图：需 `elevation.legend` 色带。水体默认按蓝色识别；海或湖不是蓝色时用 `elevation.water` 列出颜色(如 `["#ffffff"]`)，透明像素一律算作水 |

全部要求等距经纬度投影，支持南北半球。气候按纬度与海陆分布推算(无区域修正时自动推导季风)，可选 `hints` 做区域修正。

## 管理后台

http://127.0.0.1:8765/admin.html，使用独立的管理员令牌(首次启动生成在 `data/admin.key`，或用环境变量 `MAPSIM_ADMIN_TOKEN` 指定)。

- **概览**：注册用户、付费用户、预估月收入、30 天活跃、本月算力点(按预处理/演化/导出)、24 小时任务与失败、存储占用；近 30 天用量、任务与注册趋势；用量前 10。
- **用户**：搜索与筛选(套餐/付费/停用)；新建用户(密钥只显示一次)；改套餐、单独额度、备注；停用(密钥立即失效)；新增/吊销密钥；按月用量、最近任务、世界与演化；删除用户(连同数据)。
- **任务**：所有用户的任务，按状态/用户筛选，可取消。
- **套餐与接口配置**：增删改套餐(月费、每月额度、单次年数、家族上限、上传上限)、算力点计价系数、货币、自助注册开关与注册套餐、是否允许内网回调。保存后立即生效。
- **AI 识图**：为 Claude、通义千问、豆包、智谱 GLM、Kimi、腾讯混元、文心(千帆)、硅基流动或任意 OpenAI 兼容接口(自建 vLLM/Ollama)分别填写接口地址、模型与 API Key，选定当前使用的一家；可用内置示例草图或自己的图直接测试。密钥单独保存在 `data/ai.json`(仅本机可读)，后台只显示首尾几位，审计日志不记录密钥。
- **审计日志**：所有管理操作(含 IP 与配置变更前后内容)。

管理接口在 `/admin/v1/*`(Bearer 管理员令牌)。开放自助注册后，用户可调用 `POST /v1/signup {"name","email"}` 获得接口密钥(每 IP 每小时 5 次)。

## 租户与套餐(命令行)

```bash
./mapsim tenant create -name 工作室A -plan pro    # 打印接口密钥(只显示一次)
./mapsim tenant list
./mapsim tenant plan -tenant <ID> -plan studio
./mapsim key add -tenant <ID>
./mapsim key revoke -tenant <ID> -prefix msk_xxxxxx
```

默认套餐(每月算力点 / 单次最长年数 / 月费)：free 100/1000/0，pro 2000/10000/99，studio 20000/10000/999，均可在管理后台修改。默认计价：预处理 = 陆地格/1 万，演化 = 陆地格×年数/200 万，导出 = 1；排队中的任务也计入占用。

## 目录

| 路径 | 内容 |
|---|---|
| `maps/eurasia/` | 内置欧亚地图与说明文件(图例、控制点、湖泊、大河干流、区域气候修正) |
| `prep/colorband/` | 色带自动识别(不参与预处理缓存键)与参考 DEM 下载脚本 |
| `prep/` | Python 预处理；`prep/tests/test_prep.py` 南半球/草图/半球对称回归测试；`prep/validate.py` 对照 62 个气象站 |
| `server/` | Go：`sim` 第3-5层与约束、`platform` 租户/密钥/任务/计量、`api` REST 与任务队列与导出、`transport` 交通网络、`export` GeoTIFF/GeoJSON/引擎资源、`web/static` 控制台与回放 |
| `sdk/python/` | Python SDK |
| `tests/api_smoke.py` | 端到端冒烟测试(需服务以 `MAPSIM_ALLOW_PRIVATE_WEBHOOKS=1` 启动) |
| `docs/` | 设计文档与 OpenAPI |
| `data/`、`cache/` | 运行数据与预处理缓存(不入库) |

测试：`cd server && go test ./...`；`python prep/tests/test_prep.py`；`python prep/colorband/test_colorband.py`(色带识别：合成图 + 欧亚图)；`python tests/api_smoke.py http://127.0.0.1:8765 $(cat data/local.key)`。
