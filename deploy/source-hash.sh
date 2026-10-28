#!/bin/sh
# 源码指纹：对打进镜像的源码(server/ prep/ maps/ sdk/ docs/openapi.yaml)逐文件求 sha256，再汇总取前 16 位。
# Docker 构建时用它写入 /app/build.json；本地用它算任意目录(或某个提交的导出)的指纹做比对。
#   deploy/source-hash.sh [目录，默认当前目录]
set -e
cd "${1:-.}"
LC_ALL=C find server prep maps sdk docs/openapi.yaml -type f \
  ! -path '*/__pycache__/*' ! -name '*.pyc' ! -path 'server/mapsim' ! -path 'prep/tests/fixtures/*' 2>/dev/null \
  | LC_ALL=C sort | xargs -d '\n' sha256sum | sha256sum | cut -c1-16
