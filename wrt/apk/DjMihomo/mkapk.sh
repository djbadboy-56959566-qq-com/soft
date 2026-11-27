#!/bin/sh
# DjMihomo OpenWrt .apk 打包脚本 (apk-tools v3, apk v3 格式)
# 用法: ./mkapk.sh [arm64|armv7|amd64 ...]   默认三个架构都打
# 输出: dist/djmihomo-1.2.18.60-r0.<openwrt-arch>.apk
# 安装 (路由器上): apk add --allow-untrusted ./djmihomo-1.2.18.60-r0.<arch>.apk
# 依赖: apk-tools v3 静态二进制 (APK 变量指向它), 由脚本自动下载
# 注意: 必须用 apk-tools v3 (apk mkpkg) 打 v3 包; v2 的拼接 tar.gz 格式
#       在 OpenWrt 25.12+/PonWrt SNAPSHOT (apk-tools 3) 上会报 "v2 package format error"。
set -e
cd "$(dirname "$0")"

VERSION="1.2.18.60"
PKGREL="r0"
APK_VER="3.0.11-r0"
APK_URL="https://dl-cdn.alpinelinux.org/alpine/edge/main/x86_64/apk-tools-static-${APK_VER}.apk"
APK="${APK:-/tmp/apkstatic3/sbin/apk.static}"
# 签名密钥 (有则签名, 否则打无签名包; 公钥放设备 /etc/apk/keys/ 后可免 --allow-untrusted 安装)
SIGN_KEY="${SIGN_KEY:-keys/djmihomo-sign.key}"

# 1. 准备 apk-tools v3 (静态二进制, x86_64 宿主)
if [ ! -x "$APK" ]; then
  echo "== 下载 apk-tools ${APK_VER} =="
  mkdir -p /tmp/apkstatic3
  curl -fsSL -o /tmp/apk-tools3.apk "$APK_URL"
  tar -xzf /tmp/apk-tools3.apk -C /tmp/apkstatic3 2>/dev/null || true
  APK="/tmp/apkstatic3/sbin/apk.static"
fi
"$APK" --version

# 2. 架构映射: 我们的标签 -> OpenWrt apk arch (必须和设备 /etc/apk/arch 里的值一致)
#    airoha/an7583 (诺基亚 XG-040G-MF) 的 arch 是 aarch64_cortex-a53,
#    已用官方 index.json 验证过 (downloads.openwrt.org/snapshots/targets/airoha/an7583/packages/index.json)
openwrt_arch() {
  case "$1" in
    arm64) echo "aarch64_cortex-a53" ;;
    armv7) echo "arm_cortex-a7_neon-vfpv4" ;;
    amd64) echo "x86_64" ;;
    *) echo "未知架构: $1" >&2; exit 1 ;;
  esac
}

# 3. 打包一个架构
build_one() {
  local tag="$1"          # arm64 | armv7 | amd64
  local arch
  arch="$(openwrt_arch "$tag")"
  local src="staging/$tag"
  local root="build-apk/$tag"
  local out="dist/djmihomo-${VERSION}-${PKGREL}.${arch}.apk"
  [ -d "$src/djmihomo/bin" ] || { echo "缺少 $src/djmihomo/bin, 先跑 .60 的 tar.gz 打包流程" >&2; exit 1; }

  echo "== 打包 $tag -> $arch =="
  rm -rf "$root"
  mkdir -p "$root/usr/share/djmihomo" "$root/etc/init.d"

  # 程序文件 (按装机后的路径摆)
  cp -r "$src/djmihomo/bin" "$src/djmihomo/dashboard" "$src/djmihomo/geodata" "$root/usr/share/djmihomo/"
  cp "$src/uninstall.sh" "$root/usr/share/djmihomo/uninstall.sh"
  cp "$src/djmihomo.init" "$root/etc/init.d/djmihomo"
  chmod 755 "$root/usr/share/djmihomo/bin/djconsole" "$root/usr/share/djmihomo/bin/mihomo" \
            "$root/usr/share/djmihomo/uninstall.sh" "$root/etc/init.d/djmihomo"

  # LuCI 界面 (架构无关, 三个包都带): 经典 Lua controller + 模板,
  # 兼容所有 OpenWrt LuCI (15.05~24.10/SNAPSHOT, 含 ImmortalWrt)。
  # 文件在 luci/ 下: controller/djmihomo.lua, view/djmihomo/status.htm
  mkdir -p "$root/usr/lib/lua/luci/controller" "$root/usr/lib/lua/luci/view/djmihomo"
  cp luci/controller/djmihomo.lua "$root/usr/lib/lua/luci/controller/djmihomo.lua"
  cp luci/view/djmihomo/status.htm "$root/usr/lib/lua/luci/view/djmihomo/status.htm"

  # post-install: 建数据目录 + 默认 app.json (已有不覆盖), 开机自启 + 启动
  cat > "$root/.post-install" <<'EOF'
#!/bin/sh
DATA_DIR="/etc/djmihomo"
INIT="/etc/init.d/djmihomo"
mkdir -p "$DATA_DIR/profiles" "$DATA_DIR/ui"
if [ ! -f "$DATA_DIR/app.json" ]; then
  printf '{\n "enabled": true,\n "profile": "",\n "start_delay": 0,\n "scheduled_restart": false,\n "scheduled_restart_cron": "0 3 * * *",\n "core_only": false\n}\n' > "$DATA_DIR/app.json"
fi
[ -x "$INIT" ] && "$INIT" enable
[ -x "$INIT" ] && "$INIT" start
# 让 LuCI 立即认出新菜单 (清 dispatcher 缓存, 不用重启 uhttpd)
rm -f /tmp/luci-indexcache 2>/dev/null
rm -rf /tmp/luci-modulecache 2>/dev/null
EOF
  # pre-deinstall: 停服务 + 关自启 (数据 /etc/djmihomo 不在包里, 卸载后保留)
  cat > "$root/.pre-deinstall" <<'EOF'
#!/bin/sh
INIT="/etc/init.d/djmihomo"
APP_DIR="/usr/share/djmihomo"
[ -x "$INIT" ] && "$INIT" stop >/dev/null 2>&1 || true
[ -x "$INIT" ] && "$INIT" disable >/dev/null 2>&1 || true
pkill -f "$APP_DIR/bin/djconsole" 2>/dev/null || true
pkill -f "$APP_DIR/bin/mihomo" 2>/dev/null || true
# 清 LuCI 缓存, 让菜单项随卸载消失
rm -f /tmp/luci-indexcache 2>/dev/null
rm -rf /tmp/luci-modulecache 2>/dev/null
EOF
  chmod 755 "$root/.post-install" "$root/.pre-deinstall"

  mkdir -p dist
  # 签名: 有 keys/djmihomo-sign.key 就签名 (公钥放设备 /etc/apk/keys/ 后可免 --allow-untrusted)
  local sign_args=""
  [ -f "$SIGN_KEY" ] && sign_args="--sign-key $SIGN_KEY"
  # 注意: -I 用 apk v3 字段名 (name/version/description), 不是 v2 的 pkgname/pkgver/pkgdesc
  "$APK" mkpkg \
    -F "$root" \
    -o "$out" \
    $sign_args \
    -I "name:djmihomo" \
    -I "version:${VERSION}-${PKGREL}" \
    -I "arch:${arch}" \
    -I "description:DjMihomo - Mihomo console with AnGe-ClashBoard panel (Nikki-style)" \
    -I "url:https://github.com/djbadboy-56959566-qq-com/soft" \
    -I "license:GPL-3.0-only" \
    -I "origin:djmihomo" \
    -I "maintainer:djkyc" \
    -s "post-install:$root/.post-install" \
    -s "pre-deinstall:$root/.pre-deinstall"
  rm -f "$root/.post-install" "$root/.pre-deinstall"
  ls -lh "$out"
}

if [ "$#" -eq 0 ]; then set -- arm64 armv7 amd64; fi
for t in "$@"; do build_one "$t"; done
echo "== 完成 =="
ls -lh dist/
