# 构建 Go 程序
FROM golang:1.25 AS build
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mapsim ./cmd/mapsim

# 版本信息(GET /v1/version)：镜像里没有 .git，构建时对打包的源码算指纹，连同构建时间写进 build.json。
# 提交号可选：GIT_COMMIT=$(git rev-parse HEAD) docker compose build；没传也能用 deploy/which-version.sh 按指纹找到提交。
ARG GIT_COMMIT=""
ARG GIT_COMMIT_TIME=""
COPY prep/ /ctx/prep/
COPY maps/ /ctx/maps/
COPY sdk/ /ctx/sdk/
COPY docs/openapi.yaml /ctx/docs/openapi.yaml
COPY deploy/source-hash.sh /ctx/source-hash.sh
RUN cp -r /src /ctx/server \
 && h=$(sh /ctx/source-hash.sh /ctx) \
 && printf '{"commit":"%s","commitTime":"%s","sourceHash":"%s","buildTime":"%s"}\n' \
      "$GIT_COMMIT" "$GIT_COMMIT_TIME" "$h" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > /out/build.json \
 && cat /out/build.json

# 运行环境：Python 负责第 1、2 层预处理，Go 负责模拟与网页
FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 \
    PIP_NO_CACHE_DIR=1 \
    MPLCONFIGDIR=/tmp/matplotlib \
    MAPSIM_PYTHON=/usr/local/bin/python \
    MAPSIM_ADDR=0.0.0.0:8765
COPY prep/requirements.txt /tmp/requirements.txt
RUN pip install -r /tmp/requirements.txt
WORKDIR /app
COPY --from=build /out/mapsim /app/mapsim
COPY --from=build /out/build.json /app/build.json
COPY prep/ /app/prep/
COPY maps/ /app/maps/
COPY docs/openapi.yaml /app/docs/openapi.yaml
COPY sdk/ /app/sdk/
# 容器以宿主机用户(非 root)运行：源文件必须对所有用户可读(源图原本是 0600)
RUN chmod -R a+rX /app/prep /app/maps /app/docs /app/sdk \
 && mkdir -p /app/cache /app/data && chmod 777 /app/cache /app/data
EXPOSE 8765
ENTRYPOINT ["/app/mapsim"]
CMD ["serve"]
