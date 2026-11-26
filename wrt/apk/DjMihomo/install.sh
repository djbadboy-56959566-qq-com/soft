#!/bin/sh
# DjMihomo v1.2.18.50 OpenWrt 安装脚本
# 用法: 解压后在解压目录里执行./install.sh
# 安装位置: 程序 /usr/share/djmihomo, 数据 /etc/djmihomo
# 卸载: /usr/share/djmihomo/uninstall.sh

set -e

APP_DIR="/usr/share/djmihomo"
DATA_DIR="/etc/djmihomo"
INIT="/etc/init.d/djmihomo"

echo "== DjMihomo v1.2.18.50 OpenWrt 安装 =="

# 1. 检查架构, 选对二进制
ARCH="$(uname -m)"
case "$ARCH" in
aarch64|arm64) BIN_ARCH="arm64";;
armv7*|armv6*) BIN_ARCH="armv7";;
x86_64|amd64) BIN_ARCH="amd64";;
*) echo "不支持的架构: $ARCH (仅支持 arm64/armv7/x86_64)"; exit 1;;
esac
echo "检测到架构: $ARCH -> $BIN_ARCH"

# 2. 停掉旧服务 (覆盖安装时)
if [ -x "$INIT"]; then
"$INIT" stop >/dev/null 2>&1 || true
fi
# 杀掉残留进程 (按完整路径, 避免误杀)
pkill -f "$APP_DIR/bin/djconsole" 2>/dev/null || true
pkill -f "$APP_DIR/bin/mihomo" 2>/dev/null || true
sleep 1

# 3. 装程序文件
mkdir -p "$APP_DIR/bin" "$APP_DIR/dashboard" "$APP_DIR/geodata"
cp "bin/djconsole-$BIN_ARCH" "$APP_DIR/bin/djconsole"
cp "bin/mihomo-$BIN_ARCH" "$APP_DIR/bin/mihomo"
cp -r dashboard/. "$APP_DIR/dashboard/"
cp -r geodata/. "$APP_DIR/geodata/"
chmod 755 "$APP_DIR/bin/djconsole" "$APP_DIR/bin/mihomo"
echo "程序文件已装到 $APP_DIR"

# 4. 数据目录 (覆盖安装不清空, 保留订阅和配置)
mkdir -p "$DATA_DIR/profiles" "$DATA_DIR/ui"
if [! -f "$DATA_DIR/app.json"]; then
printf '{\n "enabled": true,\n "profile": "",\n "start_delay": 0,\n "scheduled_restart": false,\n "scheduled_restart_cron": "0 3 * * *",\n "core_only": false\n}\n' > "$DATA_DIR/app.json"
echo "已创建默认 app.json (enabled=true)"
else
echo "保留现有数据: $DATA_DIR"
fi

# 5. init 脚本 + 开机自启
cp djmihomo.init "$INIT"
chmod 755 "$INIT"
"$INIT" enable
echo "已安装 init 脚本, 开机自启已开启"

# 6. 卸载脚本
cp uninstall.sh "$APP_DIR/uninstall.sh"
chmod 755 "$APP_DIR/uninstall.sh"

# 7. 启动
"$INIT" start
sleep 2
if pgrep -f "$APP_DIR/bin/djconsole" >/dev/null; then
echo ""
echo "安装完成! 控制台: http://$(uci get network.lan.ipaddr 2>/dev/null || hostname -I | awk '{print $1}'):9091"
echo "Mihomo 控制器: 9090, 混合端口: 7890"
echo "首次使用: 打开控制台, 在里添加订阅, 再设为当前生效配置。"
else
echo "警告: 控制台没有跑起来, 看日志: logread -e djmihomo"
exit 1
fi
