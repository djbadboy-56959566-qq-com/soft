#!/bin/sh
# DjMihomo OpenWrt 卸载脚本
# 用法: /usr/share/djmihomo/uninstall.sh
# 默认保留数据 (/etc/djmihomo); 加 --purge 连数据一起删

set -e
APP_DIR="/usr/share/djmihomo"
DATA_DIR="/etc/djmihomo"
INIT="/etc/init.d/djmihomo"

echo "== 卸载 DjMihomo =="
[ -x "$INIT" ] && "$INIT" stop >/dev/null 2>&1 || true
"$INIT" disable >/dev/null 2>&1 || true
pkill -f "$APP_DIR/bin/djconsole" 2>/dev/null || true
pkill -f "$APP_DIR/bin/mihomo" 2>/dev/null || true
sleep 1
rm -f "$INIT"
rm -rf "$APP_DIR"
if [ "$1" = "--purge" ]; then
  rm -rf "$DATA_DIR"
  echo "已删除程序和数据 ($DATA_DIR)"
else
  echo "已删除程序, 数据保留在 $DATA_DIR (重装可继续用; 彻底删除请加 --purge)"
fi
echo "卸载完成。"
