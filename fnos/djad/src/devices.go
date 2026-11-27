// 设备识别：名字与类型图标
//
// 技术实情（先说清楚）：DNS 服务器只看得到客户端 IP，看不出设备型号，
// 型号是猜不出来的，不要编。设备名有五层来源，按优先级：
//  1. 用户手动改名（最准，存在 config.json）
//  2. 本机：127.0.0.1 或本机网卡 IP 就是 NAS 自己，名字取 NAS 主机名
//     （/etc/hostname，读不到用 "NAS"），类型取服务器；127.0.0.1 和
//     NAS 局域网 IP 是同一台机器，但仍是两行，不合并
//  3. 向局域网网关发 PTR 反查（路由器是 DHCP 服务器，设备拿 IP 时把主机名
//     Option 12 报给它，家用路由器一般会用租约里的主机名回答，如 oec-t、mat30）
//  4. MAC 厂商（ARP 表 + OUI 小表）+ 主机名拼成提示
//  5. 只显示 IP
// 类型图标优先级：用户手动选的类型 > 本机=服务器 > 按主机名关键词自动猜 > 默认电脑图标。
// PTR 反查异步做、结果缓存 1 小时、超时 1 秒，查不到静默 fallback，
// 不阻塞 DNS 查询主流程，不写错误日志刷屏。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var validDeviceTypes = map[string]bool{
	"phone": true, "tablet": true, "tv": true, "router": true, "pc": true, "server": true, "other": true,
}

// ---------- 网关：从 /proc/net/route 读默认路由 ----------

var (
	gatewayOnce   sync.Once
	cachedGateway string
)

func gatewayIP() string {
	gatewayOnce.Do(func() {
		if f, err := os.Open("/proc/net/route"); err == nil {
			cachedGateway = parseGatewayFromRoute(f)
			_ = f.Close()
		}
		if cachedGateway == "" {
			cachedGateway = "192.168.2.1"
		}
	})
	return cachedGateway
}

// parseGatewayFromRoute：Destination 为 00000000 的那行就是默认路由，
// Gateway 字段是小端 hex（如 0102A8C0 = 192.168.2.1）
func parseGatewayFromRoute(r io.Reader) string {
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[1] != "00000000" || len(f[2]) != 8 {
			continue
		}
		b := make([]byte, 4)
		ok := true
		for i := 0; i < 4; i++ {
			v, err := strconv.ParseUint(f[2][i*2:i*2+2], 16, 8)
			if err != nil {
				ok = false
				break
			}
			b[3-i] = byte(v)
		}
		if ok {
			return net.IP(b).String()
		}
	}
	return ""
}

// ---------- PTR 反查：问网关要 DHCP 主机名（异步 + 缓存） ----------

type ptrEntry struct {
	host    string
	expires time.Time
}

var (
	ptrMu       sync.Mutex
	ptrCache    = map[string]ptrEntry{}
	ptrInflight = map[string]bool{}
	ptrLastTry  = map[string]time.Time{}
)

// ptrNameAsync 返回已缓存的主机名；没缓存就后台起一个反查，本次先返回 ""，
// 下次面板刷新（2 秒后）就能显示。失败静默，不写日志。
func ptrNameAsync(ip string) string {
	ptrMu.Lock()
	if e, ok := ptrCache[ip]; ok && time.Now().Before(e.expires) {
		ptrMu.Unlock()
		return e.host
	}
	if ptrInflight[ip] || time.Since(ptrLastTry[ip]) < 10*time.Minute {
		ptrMu.Unlock()
		return ""
	}
	ptrInflight[ip] = true
	ptrLastTry[ip] = time.Now()
	ptrMu.Unlock()
	go func() {
		host, err := ptrLookup(ip, net.JoinHostPort(gatewayIP(), "53"))
		ptrMu.Lock()
		delete(ptrInflight, ip)
		if err == nil && host != "" {
			ptrCache[ip] = ptrEntry{host: host, expires: time.Now().Add(time.Hour)}
		}
		ptrMu.Unlock()
	}()
	return ""
}

// ptrLookup 用 Go 原生 resolver + 自定义 Dial，直接向指定 DNS 服务器发 PTR 查询
func ptrLookup(ip, serverAddr string) (string, error) {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, serverAddr)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	names, err := r.LookupAddr(ctx, ip)
	if err != nil || len(names) == 0 {
		return "", err
	}
	return sanitizeHostname(names[0], ip), nil
}

// sanitizeHostname：去尾点、只取首段、过滤路由器合成的 IP 衍生假名
func sanitizeHostname(name, ip string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" || name == ip {
		return ""
	}
	if i := strings.Index(name, "."); i >= 0 {
		name = name[:i]
	}
	if name == "" {
		return ""
	}
	// 全是数字/横杠（如 192-168-2-112）是合成名，不要
	stripped := strings.ReplaceAll(strings.ReplaceAll(name, "-", ""), ".", "")
	if len(stripped) == 0 {
		return ""
	}
	allDigit := true
	for _, c := range stripped {
		if c < '0' || c > '9' {
			allDigit = false
			break
		}
	}
	if allDigit {
		return ""
	}
	return name
}

// ---------- ARP 表 + OUI 厂商 ----------

var (
	arpMu      sync.Mutex
	arpCache   map[string]string
	arpCacheAt time.Time
)

// arpTable 读 /proc/net/arp（包用户可读，世界可读），30 秒缓存
func arpTable() map[string]string {
	arpMu.Lock()
	defer arpMu.Unlock()
	if arpCache != nil && time.Since(arpCacheAt) < 30*time.Second {
		return arpCache
	}
	m := map[string]string{}
	if f, err := os.Open("/proc/net/arp"); err == nil {
		m = parseArpTable(f)
		_ = f.Close()
	}
	arpCache = m
	arpCacheAt = time.Now()
	return m
}

// parseArpTable：只取 Flags 0x2（已解析完成）的表项
func parseArpTable(r io.Reader) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 6 {
			continue
		}
		ip, flags, mac := f[0], f[2], f[3]
		if net.ParseIP(ip) == nil || flags != "0x2" || mac == "00:00:00:00:00:00" {
			continue
		}
		m[ip] = strings.ToUpper(mac)
	}
	return m
}

// ouiVendors 只收录高置信度 OUI，宁缺毋滥；查不到返回 ""，不编厂商
var ouiVendors = map[string]string{
	// Apple（经典 OUI）
	"00:03:93": "Apple", "00:05:02": "Apple", "00:0A:27": "Apple", "00:0A:95": "Apple",
	"00:0D:93": "Apple", "00:11:24": "Apple", "00:14:51": "Apple", "00:16:CB": "Apple",
	"00:17:F2": "Apple", "00:19:E3": "Apple", "00:1B:63": "Apple", "00:1C:B3": "Apple",
	"00:1D:4F": "Apple", "00:1E:52": "Apple", "00:1F:5B": "Apple", "00:1F:F3": "Apple",
	"00:21:E9": "Apple", "00:22:41": "Apple", "00:23:32": "Apple", "00:23:6C": "Apple",
	"00:23:DF": "Apple", "00:24:36": "Apple", "00:25:00": "Apple", "00:26:08": "Apple",
	"00:26:4A": "Apple", "00:26:B0": "Apple", "00:26:BB": "Apple",
	// Samsung
	"00:12:FB": "Samsung", "00:16:6B": "Samsung", "00:17:D5": "Samsung", "00:18:AF": "Samsung",
	"00:1A:8A": "Samsung", "00:1B:98": "Samsung", "00:1D:F6": "Samsung", "00:21:19": "Samsung",
	"00:21:4C": "Samsung", "00:23:39": "Samsung", "00:24:54": "Samsung", "00:26:37": "Samsung",
	"00:26:5D": "Samsung", "00:27:64": "Samsung", "00:80:6F": "Samsung",
	// Huawei
	"00:18:82": "华为", "00:1E:10": "华为", "00:25:68": "华为", "00:46:4B": "华为",
	"04:02:1F": "华为", "0C:37:DC": "华为", "10:1B:54": "华为", "18:1E:78": "华为",
	"20:2B:C1": "华为", "24:69:68": "华为", "28:6E:D4": "华为", "30:55:ED": "华为",
	"34:6B:D3": "华为", "48:46:FB": "华为", "58:44:98": "华为", "5C:4C:A9": "华为",
	"60:DE:44": "华为", "68:A0:F6": "华为", "70:2E:22": "华为", "7C:1D:D9": "华为",
	"84:A1:34": "华为", "88:33:BE": "华为", "8C:34:FD": "华为", "90:94:97": "华为",
	"A0:91:C2": "华为", "AC:4E:2E": "华为", "B4:15:13": "华为", "BC:26:C7": "华为",
	"C0:70:09": "华为", "C8:3D:D4": "华为", "CC:53:B5": "华为", "D4:6A:A8": "华为",
	"E0:24:7F": "华为", "E8:CD:2D": "华为", "F4:C7:14": "华为", "FC:48:EF": "华为",
	// Xiaomi
	"00:9E:C8": "小米", "0C:1D:AF": "小米", "14:F6:5A": "小米", "18:59:36": "小米",
	"28:6C:07": "小米", "34:80:B3": "小米", "38:A4:ED": "小米", "50:64:2B": "小米",
	"54:40:AD": "小米", "64:09:80": "小米", "64:CC:2E": "小米", "68:DF:DD": "小米",
	"74:23:44": "小米", "78:02:F8": "小米", "78:11:DC": "小米", "80:AD:16": "小米",
	"84:38:38": "小米", "88:63:DF": "小米", "8C:BE:BE": "小米", "90:1A:8B": "小米",
	"98:FA:E3": "小米", "A4:50:46": "小米", "AC:C1:EE": "小米", "B0:E2:35": "小米",
	"C0:EE:FB": "小米", "D4:97:0B": "小米", "DC:71:96": "小米", "E4:46:DA": "小米",
	"EC:D0:9F": "小米", "F0:B4:29": "小米", "F4:09:D8": "小米", "F8:A4:5F": "小米",
	"FC:64:BA": "小米",
	// TP-Link
	"00:1D:0F": "TP-Link", "14:CC:20": "TP-Link", "18:A6:F7": "TP-Link",
	"30:B5:C2": "TP-Link", "50:C7:BF": "TP-Link", "60:E3:27": "TP-Link",
	"64:66:B3": "TP-Link", "84:D8:1B": "TP-Link", "A0:F3:C1": "TP-Link",
	// 虚拟机
	"00:05:69": "VMware", "00:0C:29": "VMware", "00:50:56": "VMware",
	"08:00:27": "VirtualBox", "00:15:5D": "Hyper-V",
}

// ouiVendor：本地随机 MAC（首字节 bit1 置位，如 02:xx）不参与查表，直接返回 ""
func ouiVendor(mac string) string {
	mac = strings.ToUpper(strings.TrimSpace(mac))
	parts := strings.Split(mac, ":")
	if len(parts) != 6 {
		return ""
	}
	b0, err := strconv.ParseUint(parts[0], 16, 8)
	if err != nil {
		return ""
	}
	if b0&0x02 != 0 {
		return ""
	}
	return ouiVendors[strings.Join(parts[:3], ":")]
}

// ---------- 类型自动猜（尽力而为，关键词启发式） ----------

func guessDeviceType(ip, host, vendor string) string {
	if ip != "" && ip == gatewayIP() {
		return "router"
	}
	h := strings.ToLower(host)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(h, s) {
				return true
			}
		}
		return false
	}
	if has("ipad", "matepad", "mipad", "tablet", "galaxy-tab", "tab-") {
		return "tablet"
	}
	if has("tv", "电视", "mi-tv", "smart-tv", "atv", "projector", "box") {
		return "tv"
	}
	if has("router", "gateway", "openwrt", "merlin", "padavan", "repeater", "mesh") {
		return "router"
	}
	if has("iphone", "phone", "pixel", "galaxy", "redmi", "huawei", "honor", "mate", "mat30", "mat40", "mat20", "nova",
		"p30", "p40", "p50", "p60", "oppo", "vivo", "oneplus", "xiaomi", "mi-", "realme", "iqoo", "android") {
		return "phone"
	}
	if has("macbook", "imac", "mac-", "macmini", "thinkpad", "ideapad", "laptop", "desktop", "windows", "ubuntu", "nas") {
		return "pc"
	}
	return "pc" // 猜不出默认电脑图标
}

// ---------- 本机识别：127.0.0.1 / 本机网卡 IP 就是 NAS 自己 ----------

var (
	localIPOnce  sync.Once
	localIPSet   map[string]bool
	hostnameOnce sync.Once
	cachedHost   string
)

// isLocalIP：客户端 IP 是本机回环或本机任意网卡地址
func isLocalIP(ip string) bool {
	localIPOnce.Do(func() {
		localIPSet = map[string]bool{"127.0.0.1": true, "::1": true}
		if addrs, err := net.InterfaceAddrs(); err == nil {
			for _, a := range addrs {
				if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP != nil {
					localIPSet[ipnet.IP.String()] = true
				}
			}
		}
	})
	return localIPSet[ip]
}

// nasHostname：读 /etc/hostname，读不到就用 os.Hostname()，再读不到用 "NAS"
func nasHostname() string {
	hostnameOnce.Do(func() {
		if b, err := os.ReadFile("/etc/hostname"); err == nil {
			if f := strings.Fields(string(b)); len(f) > 0 {
				cachedHost = f[0]
			}
		}
		if cachedHost == "" {
			if h, err := os.Hostname(); err == nil {
				cachedHost = strings.TrimSpace(h)
			}
		}
		if cachedHost == "" {
			cachedHost = "NAS"
		}
	})
	return cachedHost
}

// ---------- 聚合：一个设备的所有展示信息 ----------

// enrichDevice 返回：显示名、厂商、PTR 主机名、图标类型、手动名、手动类型
// 名字优先级：手动改名 > 本机=NAS 主机名 > 路由器 PTR 主机名 > ""（只显示 IP）
// 类型优先级：手动类型 > 本机=server > 自动猜 > 默认 pc
// 注意：127.0.0.1 和 NAS 局域网 IP 只是名字/图标相同，仍是两行，不合并。
func enrichDevice(ip string) (name, vendor, host, dtype, customName, customType string) {
	if cfg.DeviceNames != nil {
		customName = cfg.DeviceNames[ip]
	}
	if cfg.DeviceTypes != nil {
		customType = cfg.DeviceTypes[ip]
		if !validDeviceTypes[customType] {
			customType = ""
		}
	}
	host = ptrNameAsync(ip)
	if mac, ok := arpTable()[ip]; ok {
		vendor = ouiVendor(mac)
	}
	name = customName
	if name == "" && isLocalIP(ip) {
		name = nasHostname()
	}
	if name == "" {
		name = host
	}
	dtype = customType
	if dtype == "" && isLocalIP(ip) {
		dtype = "server"
	}
	if dtype == "" {
		dtype = guessDeviceType(ip, host, vendor)
	}
	return name, vendor, host, dtype, customName, customType
}

// ---------- 在线判断 ----------

// isOnline：5 分钟内有 DNS 查询，或 IP 还在 ARP 表里（还在局域网内），都算在线。
// 手机熄屏后不发 DNS 查询，5 分钟一过按旧规则会误判离线——但只要它还连着 WiFi，
// ARP 表里就有它的表项，所以还在线。lastSeen 只按最后一次 DNS 查询算，不变。
func isOnline(ip string, lastSeen time.Time, now time.Time, arp map[string]string) bool {
	if !lastSeen.IsZero() && now.Sub(lastSeen) < 5*time.Minute {
		return true
	}
	_, ok := arp[ip]
	return ok
}

// ---------- 设备离线 watcher：每 30 秒扫一遍，只记「在线→离线」 ----------

var offlinePrev = map[string]bool{}

// offlineTransitions：上一轮在线、本轮离线的 IP（排序后返回，日志顺序稳定）
func offlineTransitions(prev, now map[string]bool) []string {
	var off []string
	for ip, was := range prev {
		if was && !now[ip] {
			off = append(off, ip)
		}
	}
	sort.Strings(off)
	return off
}

// deviceOfflineWatcher：状态放内存里；只记在线→离线，不刷屏。
// 上线由 appendLog 里的首次出现事件记，这里不管上线。
func deviceOfflineWatcher() {
	for range time.NewTicker(30 * time.Second).C {
		mu.RLock()
		arp := arpTable() // 30 秒缓存，顺手复用
		nowStates := make(map[string]bool, len(devices))
		now := time.Now()
		for ip := range devices {
			nowStates[ip] = isOnline(ip, deviceLastSeen[ip], now, arp)
		}
		mu.RUnlock()
		off := offlineTransitions(offlinePrev, nowStates)
		offlinePrev = nowStates
		for _, ip := range off {
			name, _, _, _, _, _ := enrichDevice(ip)
			if name != "" && name != ip {
				appendEvent("设备离线：" + ip + "（" + name + "）")
			} else {
				appendEvent("设备离线：" + ip)
			}
		}
	}
}

// ---------- 手动改名 / 改类型 ----------

func apiDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		IP   string `json:"ip"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	ip := ""
	if parsed := net.ParseIP(strings.TrimSpace(req.IP)); parsed != nil {
		ip = parsed.String()
	}
	if ip == "" {
		writeJSON(w, map[string]any{"ok": false, "msg": "IP 不对"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if len([]rune(name)) > 20 {
		writeJSON(w, map[string]any{"ok": false, "msg": "名字最多 20 个字"})
		return
	}
	typ := strings.TrimSpace(req.Type)
	if typ != "" && !validDeviceTypes[typ] {
		writeJSON(w, map[string]any{"ok": false, "msg": "类型不对"})
		return
	}
	mu.Lock()
	if cfg.DeviceNames == nil {
		cfg.DeviceNames = map[string]string{}
	}
	if cfg.DeviceTypes == nil {
		cfg.DeviceTypes = map[string]string{}
	}
	if name == "" {
		delete(cfg.DeviceNames, ip)
	} else {
		cfg.DeviceNames[ip] = name
	}
	if typ == "" {
		delete(cfg.DeviceTypes, ip)
	} else {
		cfg.DeviceTypes[ip] = typ
	}
	mu.Unlock()
	saveConfig()
	writeJSON(w, map[string]any{"ok": true})
}
