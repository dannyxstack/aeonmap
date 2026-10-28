#!/bin/sh
# 查看某个部署运行的是哪个版本：读取 /v1/version，用源码指纹在本地最近的提交里找对应的那个。
#   deploy/which-version.sh [地址，默认 http://127.0.0.1:8765] [往前查的提交数，默认 50]
set -e
url="${1:-http://127.0.0.1:8765}"
n="${2:-50}"
here="$(cd "$(dirname "$0")/.." && pwd)"
info="$(curl -fsS --max-time 15 "$url/v1/version")" || { echo "无法访问 $url/v1/version(旧版本没有这个接口)"; exit 1; }
echo "$info" | python3 -m json.tool
remote="$(echo "$info" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("build",{}).get("sourceHash",""))')"
[ -n "$remote" ] || { echo "该部署没有源码指纹(不是用 Docker 构建的)，以上方 commit 字段为准"; exit 0; }
cd "$here"
local_wt="$(sh deploy/source-hash.sh .)"
[ "$local_wt" = "$remote" ] && echo "✓ 与本地工作区(含未提交改动)一致"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
for c in $(git rev-list -n "$n" HEAD); do
  rm -rf "$tmp"/*
  git archive "$c" server prep maps sdk docs/openapi.yaml | tar -x -C "$tmp"
  if [ "$(sh deploy/source-hash.sh "$tmp")" = "$remote" ]; then
    echo "✓ 对应提交 $(git log -1 --format='%h %ad %s' --date=format:'%Y-%m-%d %H:%M' "$c")"
    behind="$(git rev-list --count "$c"..HEAD)"
    [ "$behind" = 0 ] && echo "  已是本地 main 最新提交" || echo "  比本地 HEAD 落后 $behind 个提交"
    exit 0
  fi
done
[ "$local_wt" = "$remote" ] || echo "✗ 最近 $n 个提交里没有匹配的源码指纹(可能部署的是未提交的改动，或更早的版本)"
