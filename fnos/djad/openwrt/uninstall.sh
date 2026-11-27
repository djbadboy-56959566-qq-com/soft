#!/bin/sh
# Dj净界 OpenWrt 卸载脚本
# 用法: /usr/share/djingjie/uninstall.sh
# 默认保留数据 (/etc/djingjie); 加 --purge 连数据一起删
# 本地测试可用环境变量改路径: APP_DIR / DATA_DIR / INIT
set -e

APP_DIR="${APP_DIR:-/usr/share/djingjie}"
DATA_DIR="${DATA_DIR:-/etc/djingjie}"
INIT="${INIT:-/etc/init.d/djingjie}"

echo "== 卸载 Dj净界 =="
[ -x "$INIT" ] && "$INIT" stop >/dev/null 2>&1 || true
[ -x "$INIT" ] && "$INIT" disable >/dev/null 2>&1 || true
pkill -f "$APP_DIR/bin/[d]jingjie" 2>/dev/null || true
sleep 1
rm -f "$INIT"
rm -rf "$APP_DIR"
if [ "$1" = "--purge" ]; then
    rm -rf "$DATA_DIR"
    echo "已删除程序和数据 ($DATA_DIR)"
else
    echo "已删除程序，数据保留在 $DATA_DIR（重装可继续用；彻底删除请加 --purge）"
fi
echo "卸载完成。"
