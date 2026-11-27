# wrt/apk — Dj净界 + DjMihomo 的 OpenWrt apk 版

这里的 **apk 是 OpenWrt 24.10+ 的包格式**（基于 Alpine apk-tools v3），不是安卓 APK。
文件本身是 gzip 压缩的 tar：第一个成员是 `.PKGINFO`（包名、版本、架构），
其余按最终安装路径存放（`usr/share/...`、`etc/init.d/...`），`.post-install`
是装完自动执行的脚本（调 `install.sh` 做开机自启和启动）。包未签名，安装用：

```sh
apk add --allow-untrusted DjJingjie-v1.0.0-openwrt-<架构>.apk
apk add --allow-untrusted DjMihomo-v1.2.18.50-openwrt-<架构>.apk
```

如果 `.post-install` 没自动跑（个别版本行为差异），手动执行一次即可：

```sh
/usr/share/djingjie/install.sh
/usr/share/djmihomo/install.sh
```

## 文件

| 文件 | 说明 |
| --- | --- |
| `DjJingjie-v1.0.0-openwrt-arm64.apk` | Dj净界，aarch64 路由器（NanoPi R4S/R5S/R6S、树莓派 4/5） |
| `DjJingjie-v1.0.0-openwrt-armv7.apk` | Dj净界，32 位 ARM 路由器 |
| `DjJingjie-v1.0.0-openwrt-amd64.apk` | Dj净界，x86_64 软路由（J4125/N100 小主机） |
| `DjMihomo-v1.2.18.50-openwrt-arm64.apk` | DjMihomo，aarch64 |
| `DjMihomo-v1.2.18.50-openwrt-armv7.apk` | DjMihomo，32 位 ARM |
| `DjMihomo-v1.2.18.50-openwrt-amd64.apk` | DjMihomo，x86_64 |

不知道架构：在路由器上执行 `uname -m`，aarch64 选 arm64，armv7l 选 armv7，x86_64 选 amd64。

## Dj净界（OpenWrt 版）

- 面板：`http://路由器IP:9092`；DNS：53（绑不上自动落 5353）
- 程序：`/usr/share/djingjie`，数据：`/etc/djingjie`（规则、白名单、日志；覆盖安装会保留）
- 开机自启：`/etc/init.d/djingjie`（procd 托管，崩溃自动重启）
- 路由器内存比 NAS 小，默认更保守：规则上限 5 万条、DNS 缓存 1000 条
  （init 脚本里 `DJINGJIE_RULE_CAP=50000 DJINGJIE_DNS_CACHE=1000`）
- 用法：把路由器 DHCP 的 DNS 改成路由器 IP，全屋生效；或只在某台设备的 WiFi 设置里把 DNS 改成路由器 IP
- 卸载：`/usr/share/djingjie/uninstall.sh`（默认保留数据；加 `--purge` 连数据一起删）
- 规则、白名单、WebDAV 备份、主题等功能与飞牛 fpk 版一致，见 `fnos/fpk/DjJingjie/README.md`
- 源码和打包脚本：`fnos/fpk/DjJingjie/`（`build-apk.sh` 一键打 6 个包）

## DjMihomo（OpenWrt 版）

- 控制台：`http://路由器IP:9091`；Mihomo 控制器 9090，混合端口 7890
- 程序：`/usr/share/djmihomo`，数据：`/etc/djmihomo`（订阅、配置、日志；覆盖安装会保留）
- 开机自启：`/etc/init.d/djmihomo`（procd 托管）
- 卸载：`/usr/share/djmihomo/uninstall.sh`（默认保留数据；加 `--purge` 连数据一起删）
- 首次使用要在控制台里添加订阅，再设为当前生效配置，核心才会跑起来
- 不要和 luci-app-mihomo / OpenClash 同时跑，都会抢 7890/9090 端口
- 本包由已发版的 v1.2.18.50 OpenWrt tar.gz 直接转换（bin/djconsole、bin/mihomo、dashboard、geodata 原样复用，核心未重造），只把安装方式从 tar.gz + install.sh 换成了 apk

## 本地验证情况（开发机，未上真机）

- 6 个包都已验证：`.PKGINFO` 在首位，包名/版本/架构正确，payload 路径正确
- Dj净界 amd64 包：包内二进制确认 x86-64；install.sh/uninstall.sh/init 脚本语法检查通过
- DjMihomo amd64 包：包内 djconsole、mihomo 双二进制确认 x86-64；共 340 个条目
- 模拟安装（假根目录 + 环境变量改路径）：数据目录创建、init 拷贝成功；`enable`/`start` 在非 OpenWrt 环境按预期失败并提示（路由器上走 procd 正常）
- **未验证**：路由器上真实 `apk add` 安装、`apk add` 是否执行 `.post-install`、procd 自启、电视/手机实际去广告效果。建议先在一台路由器上装 amd64 包验证，再推 GitHub。
