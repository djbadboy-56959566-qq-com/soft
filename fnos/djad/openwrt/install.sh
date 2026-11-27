#!/bin/sh
# Dj净界 v1.0.0 OpenWrt 安装脚本（apk 安装后的兜底 / 手动安装）
# apk 装完会自动跑 .post-install 调这个脚本；如果没自动跑，手动执行一次即可。
# 安装位置: 程序 /usr/share/djingjie, 数据 /etc/djingjie
# 卸载: /usr/share/djingjie/uninstall.sh
# 本地测试可用环境变量改路径: APP_DIR / DATA_DIR / INIT
set -e

APP_DIR="${APP_DIR:-/usr/share/djingjie}"
DATA_DIR="${DATA_DIR:-/etc/djingjie}"
INIT="${INIT:-/etc/init.d/djingjie}"

echo "== Dj净界 v1.0.0 OpenWrt 安装 =="

# 1. 停掉旧服务（覆盖安装时）
if [ -x "$INIT" ]; then
    "$INIT" stop >/dev/null 2>&1 || true
fi
pkill -f "$APP_DIR/bin/[d]jingjie" 2>/dev/null || true
sleep 1

# 2. 程序文件（apk 已放到位；手动安装时从当前目录拷）
if [ ! -x "$APP_DIR/bin/djingjie" ] && [ -f "bin/djingjie" ]; then
    mkdir -p "$APP_DIR/bin"
    cp "bin/djingjie" "$APP_DIR/bin/djingjie"
    chmod 755 "$APP_DIR/bin/djingjie"
fi
if [ ! -x "$APP_DIR/bin/djingjie" ]; then
    echo "找不到程序文件 $APP_DIR/bin/djingjie，请先用 apk 安装或确认解压目录"
    exit 1
fi
chmod 755 "$APP_DIR/bin/djingjie" 2>/dev/null || true

# 3. 数据目录（覆盖安装不清空，保留规则、白名单和配置）
mkdir -p "$DATA_DIR"
echo "数据目录: $DATA_DIR（规则、白名单、日志都在这里，覆盖安装会保留）"

# 4. init 脚本 + 开机自启（OpenWrt 环境才有 procd；没有就跳过并提示）
if [ -f "djingjie.init" ] && [ ! -x "$INIT" ]; then
    mkdir -p "$(dirname "$INIT")"
    cp "djingjie.init" "$INIT"
    chmod 755 "$INIT"
fi
if [ -x "$INIT" ]; then
    "$INIT" enable 2>/dev/null || echo "（开机自启设置失败，可能是非 OpenWrt 环境）"
    "$INIT" start 2>/dev/null || echo "（启动失败，可能是非 OpenWrt 环境）"
    sleep 2
else
    echo "（没找到 $INIT，跳过开机自启；手动启动：DJINGJIE_DATA=$DATA_DIR $APP_DIR/bin/djingjie）"
fi

# 5. 验证
if pgrep -f "$APP_DIR/bin/[d]jingjie" >/dev/null 2>&1; then
    echo ""
    echo "安装完成！面板: http://$(uci get network.lan.ipaddr 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}'):9092"
    echo "DNS: 53（绑不上自动落 5353）。把路由器 DHCP 的 DNS 改成路由器 IP 即可全屋生效。"
else
    echo "警告: Dj净界没有跑起来，看日志: logread -e djingjie（或检查 53/9092 端口占用）"
    exit 1
fi
