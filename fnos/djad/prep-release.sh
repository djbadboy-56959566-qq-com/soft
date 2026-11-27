#!/bin/bash
# 组装待发布树（对齐 soft 仓库布局），只做文件拷贝，不推 GitHub。
# 用法: bash prep-release.sh
# 产物:
#   release/fnos/fpk/DjJingjie/   ← 本项目完整拷贝（源码 + fpk 骨架 + 构建脚本 + README/LICENSE + dist/*.fpk）
#   release/wrt/apk/              ← 6 个 OpenWrt apk + README.md
# 发布时把 release/ 下的 fnos/ 和 wrt/ 整个拷进 soft 仓库对应位置即可。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
REL="$ROOT/release"

[ -f "$ROOT/dist/DjJingjie-v1.0.0.fpk" ] || { echo "先跑 bash build.sh"; exit 1; }
[ -f "$ROOT/dist-apk/DjJingjie-v1.0.0-openwrt-amd64.apk" ] || { echo "先跑 bash build-apk.sh"; exit 1; }

rm -rf "$REL"
mkdir -p "$REL/fnos/fpk/DjJingjie" "$REL/wrt/apk"

# fpk 项目：排除构建中间产物和发布树本身；dist/（三个 .fpk）要带上
rsync -a --delete \
    --exclude='/build/' --exclude='/build-apk/' --exclude='/dist-apk/' --exclude='/release/' \
    "$ROOT"/ "$REL/fnos/fpk/DjJingjie"/

# wrt/apk：6 个包 + README
cp "$ROOT/wrt/README.md" "$REL/wrt/apk/README.md"
cp "$ROOT"/dist-apk/*.apk "$REL/wrt/apk/"

echo "== 发布树 =="
find "$REL" -type f | sort
echo ""
echo "大小："
du -sh "$REL/fnos" "$REL/wrt"
