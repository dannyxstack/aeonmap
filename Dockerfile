# 构建 Go 程序
FROM golang:1.25 AS build
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mapsim ./cmd/mapsim

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
