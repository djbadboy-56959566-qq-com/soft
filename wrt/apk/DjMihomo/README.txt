DjMihomo v1.2.18.60 for OpenWrt
================================
Mihomo 内核 v1.19.34, Nikki 风格控制台 + AnGe-ClashBoard 面板。
与飞牛.fpk 版同一套控制台源码, 订阅更新按北京时间整点走。

安装 (推荐, apk 包)
-----------------
OpenWrt 25.12 / SNAPSHOT (apk-tools 3) 直接装 .apk, 不用解压:
  apk add --allow-untrusted ./djmihomo-1.2.18.60-r0.<架构>.apk
装完自动建数据目录、开自启并启动服务。控制台: http://路由器IP:9091
Mihomo 控制器: 9090, 混合端口: 7890

LuCI 界面 (装包后自动出现)
----------------------------
装完刷新 LuCI, 在 服务 菜单里会出现 DjMihomo。
LuCI 页只有三样: 服务状态 (运行/停止、版本、PID、端口)、启动/停止/重启、
打开控制台 (:9091) 的按钮。订阅、节点、规则都在控制台里管, 不在 LuCI 里改。
LuCI 用经典 Lua controller + 模板, 兼容所有 OpenWrt LuCI 版本
(15.05~24.10/SNAPSHOT, 含 ImmortalWrt), 不依赖 menu.d JSON / JS view。
包已签名 (密钥 djmihomo), 先把公钥放到路由器 /etc/apk/keys/ 后可免
--allow-untrusted 安装: 公钥在仓库 wrt/apk/DjMihomo/djmihomo.rsa.pub。
没装公钥时仍用 apk add --allow-untrusted 安装。

架构对照 (apk 的 arch 必须和路由器 /etc/apk/arch 里的一致)
------------------------------------------------------------
- aarch64_cortex-a53 -> arm64, 如诺基亚 XG-040G-MF (airoha/an7583)
- arm_cortex-a7_neon-vfpv4 -> armv7 32 位 ARM 路由器
- x86_64 -> x86_64 软路由 (如 J4125/N100 小主机)
不知道架构: 路由器上执行 cat /etc/apk/arch, 或 uname -m
(aarch64 选 aarch64_cortex-a53, armv7l 选 arm_cortex-a7_neon-vfpv4, x86_64 选 x86_64)。

手动安装 (不用 apk 时)
----------------------
解压 tar.gz 后在解压目录里执行 ./install.sh (脚本在仓库里, 随源码提供)。
装完同样是 /usr/share/djmihomo + /etc/djmihomo + /etc/init.d/djmihomo。

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
- 卸载 (保留数据): apk del djmihomo, 或 /usr/share/djmihomo/uninstall.sh
- 卸载 (连数据一起删): /usr/share/djmihomo/uninstall.sh --purge

注意
----
- 首次使用要在控制台里添加订阅, 再设为当前生效配置, 核心才会跑起来。
- 覆盖安装新版本直接 apk add 新包, 数据和订阅都在, 不会丢。
- 不要和 luci-app-mihomo / OpenClash 同时跑, 都会抢 7890/9090 端口。
- 订阅更新: 1 小时间隔的整点更新, 半小时间隔的整点/半点更新 (北京时间)。
- 打包: mkapk.sh (apk-tools v3 打 v3 包; 旧的拼接 tar.gz 是 v2 格式, 在 apk-tools 3 上装不了)。
