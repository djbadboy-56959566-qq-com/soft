# Dj净界（DjJingjie）v1.0.9

> **已停止更新。** DNS 只能拦广告域名，App 自己的广告（信息流、片头、开屏）去不掉，这个项目到此为止，不再加新功能。开屏广告想跳过，用手机上的 GKD（GitHub gkd-kit/gkd，无障碍服务自动点「跳过」）；想拦更多 App 内广告链接，用 AdGuard 安卓版（本地 VPN + HTTPS 解密，要装证书）。

NAS 全屋 DNS 去广告，飞牛 FnOS fpk + OpenWrt apk 双版本。比 AdGuard Home 更简单：装好、打开开关、把路由器 DNS 指到 NAS，就完了。

> 开源地址：https://github.com/djbadboy-56959566-qq-com/soft（`fnos/djad`，源码、构建脚本、fpk 与 apk 都在这里；Release 标签 `djingjie-v1.0.9`）

## 能拦什么

- 网页广告：国内外横幅、弹窗、追踪域名，直接不加载
- App 里调用第三方广告平台的（AdMob、穿山甲、广点通等）：走系统 DNS 查这些域名时被拦，广告位空白或加载失败
- 规则默认全开：AdGuard 中文、EasyList China、anti-AD、AdGuard Base、EasyList、EasyPrivacy、AdGuard 手机广告过滤、AdGuard DNS 过滤

## 诚实说明（拦不了的）

DNS 只能拦广告域名。App 自己的广告拦不了：微信朋友圈广告、淘宝京东抖音的信息流广告、优酷爱奇艺腾讯视频和 YouTube 的片头广告——广告和内容走同一个域名，DNS 分不出来，拦了会连视频一起断。自带 HTTPDNS / DoH 的 App（百度 App）根本不走本机 DNS。开屏广告很多走 App 自己的 CDN，也拦不了。少数 App 会等广告超时才进，要多等几秒空白。遇到 App 检测拦截弹“网络异常”的，用白名单放行。

## 原理

NAS 当全屋 DNS 服务器。设备查域名时先过 Dj净界：命中广告规则返回 `0.0.0.0`，正常域名转发给上游 DNS（默认 223.5.5.5 / 119.29.29.29）。不解密、不改流量、不装 App。

## 3 步安装（飞牛 fpk）

1. 应用中心手动安装对应架构的 `.fpk`（arm64 不带后缀、armv7、amd64）
2. 打开 Dj净界面板（桌面图标或 `http://NAS-IP:9092`），打开总开关
3. 路由器 DHCP 的 DNS 改成 NAS 的 IP，全屋设备连 WiFi 自动生效

不想动路由器也可以：只在电视、手机的 WiFi 设置里把 DNS 改成 NAS 的 IP。
建议主 DNS 填 NAS，备用填 223.5.5.5，NAS 关机也能上网，只是暂时不过滤。

## 端口

| 服务 | 端口 | 说明 |
| --- | --- | --- |
| DNS | 53（优先）/ 5354 | 53 绑不上（包用户无权限）自动落 5354，面板会明示；5353 是系统 avahi（mDNS）占用的，不能用。53 需系统 iptables 转发到 5354（见 `djmihomo-tun-setup.sh` 思路或自建 oneshot 服务） |
| Web 面板 | 9092 | 仪表盘、规则、白名单、日志、WebDAV、设置 |

不和 DjMihomo（7890/9090/9091）、Hermes（8648）抢端口。

## 功能

- **规则管理**：内置 8 组国内外规则一键开关；自定义规则粘贴、导入订阅链接；自动更新默认每天 03:00，可改每 1 / 6 / 12 小时、每周、只手动
- **白名单**：输入域名一键放行，日志里被拦的站也能一键加白
- **智能日志**：2 秒自动刷新，只显示最新 20 条；日志只存 3 天，每天 00:00 自动清过期，临期 24 小时内的标“明天删除”；一个域名 1 小时内被拦 ≥3 次，顶部弹黄条提醒“一键加白名单”并附放行教程
- **WebDAV 备份**：预设坚果云 `https://dav.jianguoyun.com/dav/`（密码用应用密码，不是登录密码）和日本 InfiniCLOUD `https://higa.teracloud.jp/dav/`（注意：`dav.infini-cloud.net` 是错的，不要用），再加自定义。测试连接（PROPFIND：207/200 成功、401 密码错、404 地址错）；每次备份存一个带日期的文件 `dj-adblock-backup-YYYYMMDD-HHmmss.json`；恢复列出最新 3 个，选一个一键恢复。备份只含自定义规则、订阅、白名单、设置和主题，**不含 WebDAV 密码和任何 token**
- **主题**：绿、白、黑、红 4 档一键换，默认墨绿
- **在线升级**：面板检查 soft 仓库的 Dj净界专属 Release（tag 以 `djingjie-` 开头），只提示和给下载链接，不自动替换运行中的 fpk

## 内存（实测）

- 二进制：Go 纯标准库，`CGO_ENABLED=0 -trimpath -ldflags="-s -w"`，amd64 5.7MB、arm64/armv7 5.5MB；不装 UPX，不为压体积牺牲启动
- 规则用 `map[域名]uint8`（来源编码，不存重复字符串），上限默认 30 万条（`DJINGJIE_RULE_CAP` 可调），超了截断并记日志
- DNS 缓存上限默认 5000 条（`DJINGJIE_DNS_CACHE` 可调，0 关闭），TTL 过期清理，FIFO 淘汰；只缓存 NOERROR 应答
- 日志内存只留最新 20 条环形缓冲，统计用计数器，设备集上限 256；磁盘日志 3 天滚动
- 规则拉取流式逐行解析（不把整个文件读进内存），WebDAV 恢复限 5MB
- 本地实测（262,606 条规则）：启动峰值 RSS 约 56MB，稳定后约 30MB；1000 次 DNS 查询（205ms）后 RSS +68KB，基本不涨
- OpenWrt 版默认更保守：`DJINGJIE_RULE_CAP=50000`、`DJINGJIE_DNS_CACHE=1000`

## 目录结构

```
fnos/djad/
├── README.md          # 本文件
├── LICENSE            # MIT
├── build.sh           # 一键编三架构 + 打 fpk
├── build-apk.sh       # 打 OpenWrt apk（三架构，Dj净界部分）
├── src/               # Go 源码（纯标准库）+ 内嵌 UI
│   ├── main.go        # 配置、HTTP API、定时任务
│   ├── dns.go         # DNS 解析 / 拦截 / 转发 / 缓存
│   ├── rules.go       # 内置规则 + 远程规则拉取解析
│   ├── webdav.go      # WebDAV 备份恢复 + 版本检查
│   ├── api.go         # WebDAV API 端点
│   ├── *_test.go      # 单测
│   └── ui/index.html  # 单页面板（6 页 + 4 主题）
├── fpk/DjJingjie/     # fpk 骨架（manifest、cmd、config、app）
├── openwrt/           # OpenWrt init 与安装/卸载脚本
├── dist/              # 构建产物（三个 .fpk，见 Releases）
└── dist-apk/          # 构建产物（三个 OpenWrt .apk，见 Releases）
```

## 自己构建

```bash
bash build.sh
# 产物：dist/DjJingjie-v1.0.9.fpk（arm64）
#       dist/DjJingjie-v1.0.9-armv7.fpk
#       dist/DjJingjie-v1.0.9-amd64.fpk
bash build-apk.sh
# 产物：dist-apk/DjJingjie-v1.0.9-openwrt-arm64.apk 等三架构
```

需要 Go 1.22+ 和飞牛 `fnpack`。单测：`cd src && go test .`

## 已知限制（本地已验证 / 真机已验证 / 未验证）

已在开发机本地验证：TCP DNS 拦截（`ads.google.com → 0.0.0.0`）、转发、白名单、自定义规则、订阅更新、日志 20 条上限与 3 天清理、智能提醒横幅、主题切换、WebDAV 测试/备份/恢复、规则上限、三架构交叉编译与验包；v1.0.9 的 AAAA 一律回 NODATA（只给 IPv4，避免上游假 IPv6 让手机卡住）。
本机沙箱限制（非代码问题）：沙箱丢回环 UDP 回包，UDP DNS 回包本地测不到，TCP 全通；上游 UDP 出不去，转发回包用同进程 mock + mock 收包日志验证。
飞牛真机已验证（v1.0.9，2026-11-27）：应用中心安装后自启，面板 9092 正常，规则 273,585 条，DNS 落 5354（包用户绑不了 53，靠系统 iptables 转发；5353 是 avahi 的，不能用）。手机 DNS 从未到过 Dj净界（局域网 53 被 Mihomo 劫持到 1053，加手机自带 DoH 绕过），需把 53 转发改到 5354、上游指 Mihomo 1053 才能接上。
未验证（需真机）：路由器上真实电视/手机的去广告效果、OpenWrt 上 `apk add` 实装。

## 许可证

MIT，见 LICENSE。
