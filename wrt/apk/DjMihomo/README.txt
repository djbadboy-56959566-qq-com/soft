DjMihomo v1.2.18.50 for OpenWrt
================================
Mihomo 内核 v1.19.34, Nikki 风格控制台 + AnGe-ClashBoard 面板。
与飞牛.fpk 版同一套控制台源码, 订阅更新按北京时间整点走。

安装
----
1. 把对应架构的 tar.gz 传到路由器上 (scp 或 WinSCP), 解压:
tar -xzf DjMihomo-v1.2.18.50-openwrt-<架构>.tar.gz
cd DjMihomo-v1.2.18.50-openwrt-<架构>
2. 执行安装:
./install.sh
3. 打开控制台: http://路由器IP:9091
Mihomo 控制器: 9090, 混合端口: 7890

架构对照
--------
- arm64 -> aarch64 路由器 (如 NanoPi R4S/R5S/R6S, 树莓派 4/5)
- armv7 -> 32 位 ARM 路由器 (如老款 armv7 设备)
- amd64 -> x86_64 软路由 (如 J4125/N100 小主机)
不知道架构: 路由器上执行 uname -m, aarch64 选 arm64, armv7l 选 armv7, x86_64 选 amd64。
install.sh 也会自动按 uname -m 校验, 架构不对会拒绝安装。

文件位置
--------
- 程序: /usr/share/djmihomo (bin/djconsole, bin/mihomo, dashboard, geodata)
- 数据: /etc/djmihomo (订阅、配置文件、日志; 覆盖安装会保留)
- 开机自启: /etc/init.d/djmihomo (START=99, procd 托管, 崩溃自动重启)

常用命令
--------
- 启动/停止/重启: /etc/init.d/djmihomo start|stop|restart
- 开机自启开/关: /etc/init.d/djmihomo enable|disable
- 看日志: logread -e djmihomo
- 卸载 (保留数据): /usr/share/djmihomo/uninstall.sh
- 卸载 (连数据一起删): /usr/share/djmihomo/uninstall.sh --purge

注意
----
- 首次使用要在控制台里添加订阅, 再设为当前生效配置, 核心才会跑起来。
- 覆盖安装新版本直接解压后重跑./install.sh, 数据和订阅都在, 不会丢。
- 不要和 luci-app-mihomo / OpenClash 同时跑, 都会抢 7890/9090 端口。
- 订阅更新: 1 小时间隔的整点更新, 半小时间隔的整点/半点更新 (北京时间)。
