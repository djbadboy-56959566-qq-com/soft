#!/bin/bash
# Dj净界构建脚本：单一份源码 → 三架构 fpk（arm64/v8、armv7/v7、amd64/x86）
# 用法：bash build.sh [--skip-fnpack]
# 产物：dist/DjJingjie-v1.0.0.fpk（arm64）、dist/DjJingjie-v1.0.0-armv7.fpk、dist/DjJingjie-v1.0.0-amd64.fpk
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
VERSION="1.0.9"
GO_BIN="${GO_BIN:-$HOME/go/go/bin/go}"
FNPACK="${FNPACK:-$HOME/workspace/djmihomo-fpk/fnpack}"
SRC="$ROOT/src"
TMPL="$ROOT/fpk/DjJingjie"
DIST="$ROOT/dist"
BUILD="$ROOT/build"

command -v "$GO_BIN" >/dev/null || { echo "找不到 go：$GO_BIN"; exit 1; }
mkdir -p "$DIST" "$BUILD"

build_one() {
    local goarch="$1" goarm="$2" out="$3"
    echo "== 编译 $out (GOARCH=$goarch GOARM=$goarm) =="
    (cd "$SRC" && env CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" ${goarm:+GOARM="$goarm"} \
        "$GO_BIN" build -trimpath -ldflags="-s -w" -o "$out" .)
    ls -la "$out"
}

stage_one() {
    local name="$1" platform="$2" bin="$3"
    local dir="$BUILD/$name"
    rm -rf "$dir"
    mkdir -p "$dir"
    cp -r "$TMPL"/. "$dir"/
    # 平台：arm64/armv7 用 arm，amd64 用 x86（与 DjMihomo 的 manifest 写法一致）
    sed -i "s/^platform *=.*/platform                   = $platform/" "$dir/manifest"
    mkdir -p "$dir/app/bin"
    cp "$bin" "$dir/app/bin/djingjie"
    chmod +x "$dir/app/bin/djingjie"
    # 注意：不要在这里给二进制 setcap——v1.0.4 实测 fnpack 打包会丢
    # security.capability xattr（解包后 getcap 为空），53 授权只能靠安装后
    # root 手动补一次；install/upgrade_init 里已加身份日志用于定位。
    echo "== 打包 $name =="
    (cd "$BUILD" && "$FNPACK" build -d "$name")
    # fnpack 把包写到工作目录（$BUILD/DjJingjie.fpk），及时移走避免下个架构捡到旧包
    local produced=""
    for cand in "$dir"/*.fpk "$BUILD"/*.fpk; do
        if [ -f "$cand" ]; then produced="$cand"; break; fi
    done
    if [ -z "$produced" ]; then
        echo "fnpack 未生成 fpk（$name）"; exit 1
    fi
    mv "$produced" "$DIST/$4"
    echo "产物：$DIST/$4"
}

build_one arm64 "" "$BUILD/djingjie-arm64"
build_one arm   7    "$BUILD/djingjie-armv7"
build_one amd64 "" "$BUILD/djingjie-amd64"

if [ "${1:-}" != "--skip-fnpack" ]; then
    stage_one "arm64" "arm" "$BUILD/djingjie-arm64" "DjJingjie-v${VERSION}.fpk"
    stage_one "armv7" "arm" "$BUILD/djingjie-armv7" "DjJingjie-v${VERSION}-armv7.fpk"
    stage_one "amd64" "x86" "$BUILD/djingjie-amd64" "DjJingjie-v${VERSION}-amd64.fpk"
fi

echo
echo "== 构建产物 =="
ls -la "$DIST"/
echo
echo "== 二进制大小 =="
ls -la "$BUILD"/djingjie-*
