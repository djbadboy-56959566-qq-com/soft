#!/bin/bash
# 打 OpenWrt apk 包（Dj净界 v1.0.0 + DjMihomo v1.2.18.50，各三架构）
# 用法: bash build-apk.sh
# 产物: dist-apk/DjJingjie-v1.0.0-openwrt-<arch>.apk
#       dist-apk/DjMihomo-v1.2.18.50-openwrt-<arch>.apk
#
# 关于“apk”格式（本脚本的理解，写清楚以便核对）：
#   这里的 apk 是 OpenWrt 24.10+ 的包格式（基于 Alpine apk-tools v3），
#   不是安卓 APK。文件本身是 gzip 压缩的 tar：
#     - 第一个成员必须是 .PKGINFO（包名、版本、架构、大小等）
#     - 其余成员按最终安装路径存放（usr/share/...、etc/init.d/...）
#     - .post-install 是装完自动执行的脚本（这里调 install.sh 做开机自启+启动）
#   包未签名，路由器上安装用：apk add --allow-untrusted <文件>.apk
#   注意：本机没有 OpenWrt 真机，apk add 实装未验证；本地只验证了解包、
#   脚本语法和模拟安装流程，见 README。
#
# DjMihomo 不重造核心：直接从已发版的 v1.2.18.50 OpenWrt tar.gz
# （~/workspace/DjMihomo-v1.2.18.50-openwrt-<arch>.tar.gz）取二进制、
# dashboard、geodata 和卸载脚本，只换一套适配 apk 的 install.sh。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
OUT="$ROOT/dist-apk"
STAGE="$ROOT/build-apk"
DJ_VER="1.0.9"
MH_VER="1.2.18.50"
REPO_URL="https://github.com/djbadboy-56959566-qq-com/soft"

mkdir -p "$OUT"
rm -rf "$STAGE"
mkdir -p "$STAGE"

# ---- 通用：把一个 staging 目录打成 .apk（.PKGINFO 必须排第一）----
# 参数: $1 包名 $2 版本 $3 apk 架构 $4 描述 $5 staging 目录 $6 输出文件
make_apk() {
    local pkgname="$1" ver="$2" arch="$3" desc="$4" stage="$5" outfile="$6"
    local size
    size="$(du -sb "$stage/usr" "$stage/etc" 2>/dev/null | awk '{s+=$1} END {print s}')"
    cat > "$stage/.PKGINFO" <<EOF
pkgname = $pkgname
pkgver = $ver-r0
pkgdesc = $desc
url = $REPO_URL
arch = $arch
size = $size
license = MIT
builddate = $(date +%s)
EOF
    # 顺序固定：.PKGINFO 第一，.post-install 第二，其余按路径
    tar -czf "$outfile" -C "$stage" .PKGINFO -C "$stage" .post-install -C "$stage" usr -C "$stage" etc
    echo "已生成 $(basename "$outfile") ($(du -h "$outfile" | cut -f1)) arch=$arch size=$size"
}

# ---- DjMihomo 的 apk 版 install.sh（文件已由 apk 放到位，只做数据目录+自启+启动）----
# 与 tar.gz 版 install.sh 的区别：不再拷文件、不再按 uname -m 选架构（包本身就是单架构）。
write_mihomo_install() {
    local app_dir="$1"
    cat > "$app_dir/install.sh" <<'EOS'
#!/bin/sh
# DjMihomo OpenWrt 安装脚本（apk 版：文件已由 apk 放到位）
# apk 装完自动跑 .post-install 调这个脚本；没自动跑可手动执行一次。
# 安装位置: 程序 /usr/share/djmihomo, 数据 /etc/djmihomo
# 卸载: /usr/share/djmihomo/uninstall.sh
set -e
APP_DIR="/usr/share/djmihomo"
DATA_DIR="/etc/djmihomo"
INIT="/etc/init.d/djmihomo"
echo "== DjMihomo OpenWrt 安装（apk 版）=="
if [ -x "$INIT" ]; then "$INIT" stop >/dev/null 2>&1 || true; fi
pkill -f "$APP_DIR/bin/[d]jconsole" 2>/dev/null || true
pkill -f "$APP_DIR/bin/[m]ihomo" 2>/dev/null || true
sleep 1
mkdir -p "$DATA_DIR/profiles" "$DATA_DIR/ui"
if [ ! -f "$DATA_DIR/app.json" ]; then
printf '{\n "enabled": true,\n "profile": "",\n "start_delay": 0,\n "scheduled_restart": false,\n "scheduled_restart_cron": "0 3 * * *",\n "core_only": false\n}\n' > "$DATA_DIR/app.json"
echo "已创建默认 app.json (enabled=true)"
else
echo "保留现有数据: $DATA_DIR"
fi
chmod 755 "$APP_DIR/bin/djconsole" "$APP_DIR/bin/mihomo" 2>/dev/null || true
if [ -x "$INIT" ]; then
    "$INIT" enable 2>/dev/null || echo "（开机自启设置失败，可能是非 OpenWrt 环境）"
    "$INIT" start 2>/dev/null || echo "（启动失败，可能是非 OpenWrt 环境）"
    sleep 2
fi
if pgrep -f "$APP_DIR/bin/[d]jconsole" >/dev/null 2>&1; then
    echo ""
    echo "安装完成！控制台: http://$(uci get network.lan.ipaddr 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}'):9091"
    echo "Mihomo 控制器: 9090，混合端口: 7890"
else
    echo "警告: 控制台没有跑起来，看日志: logread -e djmihomo"
    exit 1
fi
EOS
    chmod 755 "$app_dir/install.sh"
}

# ================= Dj净界 =================
for arch in arm64 armv7 amd64; do
    case "$arch" in
        arm64) apk_arch="aarch64" ;;
        armv7) apk_arch="armv7" ;;
        amd64) apk_arch="x86_64" ;;
    esac
    bin="$ROOT/build/djingjie-$arch"
    [ -x "$bin" ] || { echo "缺少二进制 $bin，先跑 bash build.sh"; exit 1; }
    s="$STAGE/djingjie-$arch"
    mkdir -p "$s/usr/share/djingjie/bin" "$s/etc/init.d"
    cp "$bin" "$s/usr/share/djingjie/bin/djingjie"
    cp "$ROOT/openwrt/djingjie.init" "$s/etc/init.d/djingjie"
    cp "$ROOT/openwrt/install.sh" "$s/usr/share/djingjie/install.sh"
    cp "$ROOT/openwrt/uninstall.sh" "$s/usr/share/djingjie/uninstall.sh"
    chmod 755 "$s/usr/share/djingjie/bin/djingjie" "$s/etc/init.d/djingjie" \
              "$s/usr/share/djingjie/install.sh" "$s/usr/share/djingjie/uninstall.sh"
    cat > "$s/.post-install" <<'EOS'
#!/bin/sh
# apk 装完自动执行；失败不阻断安装（用户可手动跑 /usr/share/djingjie/install.sh）
/usr/share/djingjie/install.sh >/dev/null 2>&1 || true
exit 0
EOS
    chmod 755 "$s/.post-install"
    make_apk "djingjie" "$DJ_VER" "$apk_arch" "Dj净界 - NAS 全屋 DNS 去广告（OpenWrt 版）" \
        "$s" "$OUT/DjJingjie-v$DJ_VER-openwrt-$arch.apk"
done

# ================= DjMihomo（复用已发版 tar.gz，不重造核心）=====================
for arch in arm64 armv7 amd64; do
    case "$arch" in
        arm64) apk_arch="aarch64" ;;
        armv7) apk_arch="armv7" ;;
        amd64) apk_arch="x86_64" ;;
    esac
    tgz="$HOME/workspace/DjMihomo-v$MH_VER-openwrt-$arch.tar.gz"
    [ -f "$tgz" ] || { echo "缺少 $tgz"; exit 1; }
    s="$STAGE/djmihomo-$arch"
    mkdir -p "$s/usr/share/djmihomo/bin" "$s/etc/init.d" "$s/tmp-src"
    tar --no-same-owner -xzf "$tgz" -C "$s/tmp-src"
    top="$s/tmp-src/DjMihomo-v$MH_VER-openwrt-$arch"
    cp "$top/bin/djconsole-$arch" "$s/usr/share/djmihomo/bin/djconsole"
    cp "$top/bin/mihomo-$arch" "$s/usr/share/djmihomo/bin/mihomo"
    cp -r "$top/dashboard" "$s/usr/share/djmihomo/dashboard"
    cp -r "$top/geodata" "$s/usr/share/djmihomo/geodata"
    cp "$top/djmihomo.init" "$s/etc/init.d/djmihomo"
    cp "$top/uninstall.sh" "$s/usr/share/djmihomo/uninstall.sh"
    write_mihomo_install "$s/usr/share/djmihomo"
    chmod 755 "$s/usr/share/djmihomo/bin/djconsole" "$s/usr/share/djmihomo/bin/mihomo" \
              "$s/etc/init.d/djmihomo" "$s/usr/share/djmihomo/install.sh" \
              "$s/usr/share/djmihomo/uninstall.sh"
    rm -rf "$s/tmp-src"
    cat > "$s/.post-install" <<'EOS'
#!/bin/sh
# apk 装完自动执行；失败不阻断安装（用户可手动跑 /usr/share/djmihomo/install.sh）
/usr/share/djmihomo/install.sh >/dev/null 2>&1 || true
exit 0
EOS
    chmod 755 "$s/.post-install"
    make_apk "djmihomo" "$MH_VER" "$apk_arch" "DjMihomo - Mihomo 代理控制台（OpenWrt 版）" \
        "$s" "$OUT/DjMihomo-v$MH_VER-openwrt-$arch.apk"
done

echo ""
echo "== 产物 =="
ls -la "$OUT"
