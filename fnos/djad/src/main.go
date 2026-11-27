// Dj净界 v1.0.0 — NAS DNS 去广告核心
// 纯标准库，无外部依赖。DNS 拦截 + 规则 + 白名单 + 智能日志 + WebDAV 备份 + 主题 + 在线升级检查
package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed ui/index.html
var uiHTML []byte

// v1.0.2：浏览器标签页图标用 Dj净界自己的盾牌（src/ui/images/，与 fpk 桌面图标同一份文件）
// 64px 文件供 16/32/64 三档缩放使用，256px 供高分屏和 apple-touch-icon 使用
//
//go:embed ui/images/icon-64.png
var icon64 []byte

//go:embed ui/images/icon-256.png
var icon256 []byte

const Version = "1.0.9"

var (
	dataDir   string
	dnsPort   int // 实际在监听的 DNS 端口（53 优先，失败落 5354 / 15353）
	dnsPortOK bool
	uiPort    int
	startTime = time.Now()
	mu        sync.RWMutex
	cfg       Config
	blocked   map[string]uint8 // 域名 -> 来源编码（值是 blockSources 下标，省内存）
	blockSrcs []string         // 来源名表
	stats     Stats
	// 内存优化：日志内存只留最新 100 条环形缓冲（v1.0.6：20→100，供日志分页用）；统计用计数器；设备集有界
	logRing      [100]LogEntry
	logRingIdx   int
	logRingCount int
	logFileCount int64 // 磁盘日志条数（3 天内）
	hourCounts   = map[string]*hourCounter{}
	devices      = map[string]struct{}{}
	ruleCap      = 300000 // 规则总数上限（NAS 默认 30 万；OpenWrt 由启动脚本设 5 万）
	// v1.0.2：仪表盘趋势图和设备列表用的有界计数（不存全量请求，内存不涨）
	todayHourly     [24]int // 今日按小时拦截数（0-23 时），跨天自动清零
	todayHourlyDate string
	deviceBlocked = map[string]int64{}      // 客户端 IP → 累计拦截数（上限 256 个设备）
	deviceLastSeen = map[string]time.Time{} // 客户端 IP → 最后一次查询时间
)

type hourCounter struct {
	count  int
	source string
	hour   string // "2006-01-02-15"
}

type Stats struct {
	TotalBlocked int64 `json:"totalBlocked"`
	TodayBlocked int64 `json:"todayBlocked"`
	TodayDate    string `json:"-"`
}

type RuleGroup struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Enabled    bool   `json:"enabled"`
	Count      int    `json:"count"`
	LastUpdate string `json:"lastUpdate"`
	Error      string `json:"error,omitempty"`
}

type Subscription struct {
	URL        string `json:"url"`
	Enabled    bool   `json:"enabled"`
	Count      int    `json:"count"`
	LastUpdate string `json:"lastUpdate"`
}

type WebDAVConfig struct {
	Preset string `json:"preset"` // jianguoyun | infinicloud | custom
	URL    string `json:"url"`
	User   string `json:"user"`
	Pass   string `json:"pass"`
}

type Config struct {
	Enabled        bool           `json:"enabled"`
	Theme          string         `json:"theme"` // green | white | black | red
	UpdateInterval string         `json:"updateInterval"` // 1h | 6h | 12h | daily | weekly | manual
	UpdateTime     string         `json:"updateTime"`     // 每天/每周自动更新的时间点 "HH:MM"，默认 "03:00"；按小时更新时不生效
	Upstream       []string       `json:"upstream"`
	CustomRules    []string       `json:"customRules"`
	Whitelist      []string       `json:"whitelist"`
	Groups         []RuleGroup    `json:"groups"`
	Subscriptions  []Subscription `json:"subscriptions"`
	WebDAV         WebDAVConfig   `json:"webdav"`
	DeviceNames    map[string]string `json:"deviceNames"` // v1.0.4：IP → 用户手动起的设备名
	DeviceTypes    map[string]string `json:"deviceTypes"` // v1.0.4：IP → 用户手动选的类型 phone|tablet|tv|router|pc|other
	DismissedAlerts []string      `json:"dismissedAlerts"` // v1.0.6：用户点了「不再提醒」的域名，横幅不再出现
	NextUpdate     time.Time      `json:"nextUpdate"`
}

type LogEntry struct {
	Time     time.Time `json:"time"`
	Client   string    `json:"client"`
	Domain   string    `json:"domain"`
	Qtype    string    `json:"qtype"`
	Blocked  bool      `json:"blocked"`
	Source   string    `json:"source"`
	Expiring bool      `json:"expiring"` // 24 小时内过期 → 明天删除
	Kind     string    `json:"kind,omitempty"`  // "" 或 "dns" = DNS 查询；"event" = 事件
	Event    string    `json:"event,omitempty"` // 事件文字（kind=event 时用）
}

func defaultGroups() []RuleGroup {
	return []RuleGroup{
		{ID: "adguard-cn", Name: "AdGuard 中文", URL: "https://filters.adtidy.org/windows/filters/224.txt", Enabled: true},
		{ID: "easylist-china", Name: "EasyList China", URL: "https://easylist-downloads.adblockplus.org/easylistchina.txt", Enabled: true},
		{ID: "anti-ad", Name: "anti-AD", URL: "https://anti-ad.net/easylist.txt", Enabled: true},
		{ID: "adguard-base", Name: "AdGuard Base", URL: "https://filters.adtidy.org/windows/filters/2.txt", Enabled: true},
		{ID: "easylist", Name: "EasyList", URL: "https://easylist.to/easylist/easylist.txt", Enabled: true},
		{ID: "easyprivacy", Name: "EasyPrivacy", URL: "https://easylist.to/easylist/easyprivacy.txt", Enabled: true},
		{ID: "adguard-mobile", Name: "AdGuard 手机广告过滤", URL: "https://filters.adtidy.org/windows/filters/11.txt", Enabled: true},
		{ID: "adguard-dns", Name: "AdGuard DNS 过滤", URL: "https://adguardteam.github.io/AdGuardSDNSFilter/Filters/filter.txt", Enabled: true},
	}
}

func defaultConfig() Config {
	return Config{
		Enabled:        true,
		Theme:          "green",
		UpdateInterval: "daily",
		UpdateTime:     "03:00",
		Upstream:       []string{"223.5.5.5", "119.29.29.29"},
		CustomRules:    []string{},
		Whitelist:      []string{},
		Groups:         defaultGroups(),
		Subscriptions:  []Subscription{},
		WebDAV:         WebDAVConfig{Preset: "jianguoyun", URL: "https://dav.jianguoyun.com/dav/"},
		DeviceNames:    map[string]string{},
		DeviceTypes:    map[string]string{},
	}
}

func configPath() string { return filepath.Join(dataDir, "config.json") }
func logsPath() string   { return filepath.Join(dataDir, "logs.jsonl") }
func statsPath() string  { return filepath.Join(dataDir, "stats.json") }

func loadConfig() {
	cfg = defaultConfig()
	b, err := os.ReadFile(configPath())
	if err != nil {
		saveConfig()
		return
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		log.Printf("config 损坏，用默认配置: %v", err)
		return
	}
	// 合并：新版默认组缺失时补上，旧字段保留
	merged := defaultConfig()
	merged.Enabled = c.Enabled
	if c.Theme != "" {
		merged.Theme = c.Theme
	}
	if c.UpdateInterval != "" {
		merged.UpdateInterval = c.UpdateInterval
	}
	if c.UpdateTime != "" && validUpdateTime(c.UpdateTime) {
		merged.UpdateTime = c.UpdateTime
	}
	if len(c.Upstream) > 0 {
		merged.Upstream = c.Upstream
	}
	merged.CustomRules = c.CustomRules
	merged.Whitelist = c.Whitelist
	merged.DismissedAlerts = c.DismissedAlerts
	merged.Subscriptions = c.Subscriptions
	merged.WebDAV = c.WebDAV
	if c.DeviceNames != nil {
		merged.DeviceNames = c.DeviceNames
	}
	if c.DeviceTypes != nil {
		merged.DeviceTypes = c.DeviceTypes
	}
	if !c.NextUpdate.IsZero() {
		merged.NextUpdate = c.NextUpdate
	}
	if len(c.Groups) > 0 {
		merged.Groups = c.Groups
		// 补新组
		known := map[string]bool{}
		for _, g := range merged.Groups {
			known[g.ID] = true
		}
		for _, g := range defaultGroups() {
			if !known[g.ID] {
				merged.Groups = append(merged.Groups, g)
			}
		}
	}
	cfg = merged
}

// saveConfig 每次写盘前先重读磁盘文件再合并，避免后台定时任务覆盖用户刚改的字段
func saveConfig() {
	mu.Lock()
	defer mu.Unlock()
	saveConfigLocked()
}

func saveConfigLocked() {
	b, _ := json.MarshalIndent(cfg, "", "  ")
	tmp := configPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err == nil {
		_ = os.Rename(tmp, configPath())
	}
}

func loadStats() {
	b, err := os.ReadFile(statsPath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &stats)
	if stats.TodayDate != time.Now().Format("2006-01-02") {
		stats.TodayBlocked = 0
		stats.TodayDate = time.Now().Format("2006-01-02")
	}
}

func saveStats() {
	stats.TodayDate = time.Now().Format("2006-01-02")
	b, _ := json.Marshal(stats)
	_ = os.WriteFile(statsPath(), b, 0644)
}

func loadLogs() {
	f, err := os.Open(logsPath())
	if err != nil {
		return
	}
	defer f.Close()
	cutoff := time.Now().Add(-72 * time.Hour)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e LogEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if e.Time.Before(cutoff) {
			continue
		}
		logFileCount++
		pushRing(e)
		if e.Kind != "event" {
			trackQuery(e) // 事件不进设备统计、不算拦截、不触发智能提醒
		}
	}
}

func pushRing(e LogEntry) {
	logRing[logRingIdx] = e
	logRingIdx = (logRingIdx + 1) % len(logRing)
	if logRingCount < len(logRing) {
		logRingCount++
	}
}

func ringLatest() []LogEntry {
	out := make([]LogEntry, 0, logRingCount)
	for i := 0; i < logRingCount; i++ {
		idx := (logRingIdx - 1 - i + len(logRing)*2) % len(logRing)
		out = append(out, logRing[idx])
	}
	return out
}

// trackQuery 更新小时计数（智能提醒用，上限 500 个域名）和有界设备集（上限 256）
// v1.0.2：同时记今日按小时拦截（趋势图）和每设备拦截数/最后活跃（设备列表）
func trackQuery(e LogEntry) {
	if len(devices) < 256 {
		devices[e.Client] = struct{}{}
	}
	deviceLastSeen[e.Client] = e.Time
	if !e.Blocked {
		return
	}
	if _, ok := deviceBlocked[e.Client]; !ok && len(deviceBlocked) >= 256 {
		// 设备数已达上限，不再新增条目（已有设备照常计数）
	} else {
		deviceBlocked[e.Client]++
	}
	day := e.Time.Format("2006-01-02")
	if todayHourlyDate != day {
		todayHourly = [24]int{}
		todayHourlyDate = day
	}
	todayHourly[e.Time.Hour()]++
	h := e.Time.Format("2006-01-02-15")
	c, ok := hourCounts[e.Domain]
	if !ok {
		if len(hourCounts) >= 500 {
			return
		}
		c = &hourCounter{}
		hourCounts[e.Domain] = c
	}
	if c.hour != h {
		c.hour = h
		c.count = 0
		c.source = e.Source
	}
	c.count++
	if c.source == "" {
		c.source = e.Source
	}
}

func persistLogLocked(e LogEntry) {
	b, _ := json.Marshal(e)
	f, err := os.OpenFile(logsPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err == nil {
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}
}

func appendLog(e LogEntry) {
	mu.Lock()
	defer mu.Unlock()
	_, seenBefore := devices[e.Client]
	pushRing(e)
	trackQuery(e)
	logFileCount++
	if e.Blocked {
		stats.TotalBlocked++
		if e.Time.Format("2006-01-02") == time.Now().Format("2006-01-02") {
			stats.TodayBlocked++
		}
	}
	persistLogLocked(e)
	saveStats()
	// 设备首次出现：记一条上线事件（只记一次，设备数到上限后不再记，避免刷屏）
	if _, seenAfter := devices[e.Client]; !seenBefore && seenAfter {
		name, _, _, _, _, _ := enrichDevice(e.Client)
		if name != "" && name != e.Client {
			pushEventLocked("设备上线：" + e.Client + "（" + name + "）")
		} else {
			pushEventLocked("设备上线：" + e.Client)
		}
	}
}

// appendEvent 记一条事件：和 DNS 查询进同一个列表、按时间排、最新 100 条、3 天清理；
// 不进设备统计、不算拦截、不触发智能提醒。线程安全。
func appendEvent(text string) {
	mu.Lock()
	defer mu.Unlock()
	pushEventLocked(text)
}

func pushEventLocked(text string) {
	e := LogEntry{Time: time.Now(), Kind: "event", Event: text}
	pushRing(e)
	logFileCount++
	persistLogLocked(e)
}

// pruneLogs 删除 3 天前的日志（每天 00:00 + 启动时各跑一次），流式重写不全量读入内存
func pruneLogs() {
	mu.Lock()
	defer mu.Unlock()
	cutoff := time.Now().Add(-72 * time.Hour)
	in, err := os.Open(logsPath())
	if err != nil {
		return
	}
	defer in.Close()
	tmp := logsPath() + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return
	}
	var kept int64
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 4096), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e LogEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if e.Time.Before(cutoff) {
			continue
		}
		_, _ = out.Write(append([]byte(line), '\n'))
		kept++
	}
	_ = out.Close()
	_ = os.Rename(tmp, logsPath())
	logFileCount = kept
	log.Printf("日志清理：保留 %d 条（3 天内）", kept)
}

func isWhitelisted(domain string) bool {
	for _, w := range cfg.Whitelist {
		if domain == w || strings.HasSuffix(domain, "."+w) {
			return true
		}
	}
	return false
}

// matchBlocked 命中返回来源组名
func matchBlocked(domain string) (string, bool) {
	if !cfg.Enabled {
		return "", false
	}
	if isWhitelisted(domain) {
		return "", false
	}
	// 自定义规则优先
	for _, r := range cfg.CustomRules {
		if domain == r || strings.HasSuffix(domain, "."+r) {
			return "自定义规则", true
		}
	}
	// 内置 + 订阅规则：逐级后缀匹配（map 查找，不全表扫描）
	d := domain
	for {
		if code, ok := blocked[d]; ok {
			return blockSrcs[code], true
		}
		i := strings.Index(d, ".")
		if i < 0 {
			break
		}
		d = d[i+1:]
	}
	return "", false
}

func rebuildBlocked() {
	mu.Lock()
	defer mu.Unlock()
	blocked = map[string]uint8{}
	// 来源编码表：0 内置，1 自定义，2.. 规则组，末位 订阅
	blockSrcs = []string{"内置规则", "自定义规则"}
	for _, g := range cfg.Groups {
		blockSrcs = append(blockSrcs, g.Name)
	}
	subCode := uint8(len(blockSrcs))
	blockSrcs = append(blockSrcs, "订阅规则")
	add := func(d string, code uint8) {
		if len(blocked) >= ruleCap {
			return
		}
		if _, ok := blocked[d]; !ok {
			blocked[d] = code
		}
	}
	for _, d := range starterBlocklist {
		add(d, 0)
	}
	for _, r := range cfg.CustomRules {
		add(strings.ToLower(strings.TrimSpace(r)), 1)
	}
	for gi, g := range cfg.Groups {
		if !g.Enabled {
			continue
		}
		for _, d := range cachedGroupDomains(g.ID) {
			add(d, uint8(2+gi))
		}
	}
	for _, s := range cfg.Subscriptions {
		if !s.Enabled {
			continue
		}
		for _, d := range cachedGroupDomains("sub:" + s.URL) {
			add(d, subCode)
		}
	}
	if len(blocked) >= ruleCap {
		log.Printf("规则库已加载：%d 条域名（已达上限 %d，多余规则未加载）", len(blocked), ruleCap)
	} else {
		log.Printf("规则库已加载：%d 条域名", len(blocked))
	}
}

func main() {
	dataDir = os.Getenv("DJINGJIE_DATA")
	if dataDir == "" {
		dataDir = "./data"
	}
	// 内存上限（环境变量覆盖；OpenWrt 启动脚本设得更保守）
	if v := os.Getenv("DJINGJIE_RULE_CAP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ruleCap = n
		}
	}
	if v := os.Getenv("DJINGJIE_DNS_CACHE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			dnsCacheCap = n
		}
	}
	_ = os.MkdirAll(dataDir, 0755)
	uiPort = 9092
	if v := os.Getenv("DJINGJIE_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			uiPort = p
		}
	}
	loadConfig()
	loadStats()
	loadLogs()
	pruneLogs()
	rebuildBlocked()

	// 定时任务：规则更新 + 日志清理
	go scheduler()

	// 设备离线 watcher：每 30 秒扫一遍，只记「在线→离线」的变化，不刷屏
	go deviceOfflineWatcher()

	// DNS：依次试 53 → 5354 → 15353。注意 5353 不能用，FnOS 上 avahi-daemon
	// 已占 5353（mDNS），v1.0.0 就是死在这。DNS 全失败也不退出进程，
	// 面板必须能打开，面板里显示 DNS 状态和失败原因，否则用户连错都看不到。
	// 53 靠安装脚本 setcap cap_net_bind_service 授权给包用户。
	// v1.0.4 实测：打包前预置 setcap 也没用，fnpack 打包会丢 security.capability
	// xattr；且安装/升级回调都以包用户跑，setcap 报 Operation not permitted。
	// 所以升级后仍需 root 手动 setcap 一次（见 manifest changelog），
	// v1.0.4 在 install/upgrade_init 里加了身份日志，装完看 info.log 可定位。
	dnsPort = 0
	for _, p := range []int{53, 5354, 15353} {
		if err := startDNS(p); err != nil {
			log.Printf("DNS %d 绑定失败（%v），换下一个端口", p, err)
			continue
		}
		dnsPort = p
		dnsPortOK = true
		break
	}
	if !dnsPortOK {
		log.Printf("DNS 全部端口绑定失败，面板照常启动，请在面板查看 DNS 状态")
	}
	log.Printf("Dj净界 v%s 启动：DNS 端口 %d，Web 端口 %d，数据目录 %s", Version, dnsPort, uiPort, dataDir)

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveUI)
	mux.HandleFunc("/favicon.ico", serveIcon64)
	mux.HandleFunc("/icon-64.png", serveIcon64)
	mux.HandleFunc("/icon-256.png", serveIcon256)
	mux.HandleFunc("/api/status", apiStatus)
	mux.HandleFunc("/api/logs", apiLogs)
	mux.HandleFunc("/api/alerts/dismiss", apiAlertDismiss)
	mux.HandleFunc("/api/alerts/restore", apiAlertRestore)
	mux.HandleFunc("/api/toggle", apiToggle)
	mux.HandleFunc("/api/rules", apiRules)
	mux.HandleFunc("/api/rules/group", apiRulesGroup)
	mux.HandleFunc("/api/rules/update", apiRulesUpdate)
	mux.HandleFunc("/api/rules/custom", apiCustomRule)
	mux.HandleFunc("/api/rules/subscription", apiSubscription)
	mux.HandleFunc("/api/whitelist", apiWhitelist)
	mux.HandleFunc("/api/settings", apiSettings)
	mux.HandleFunc("/api/device", apiDevice)
	mux.HandleFunc("/api/webdav/test", apiWebDAVTest)
	mux.HandleFunc("/api/webdav/backup", apiWebDAVBackup)
	mux.HandleFunc("/api/webdav/backups", apiWebDAVBackups)
	mux.HandleFunc("/api/webdav/restore", apiWebDAVRestore)
	mux.HandleFunc("/api/version", apiVersionCheck)
	srv := &http.Server{Addr: fmt.Sprintf(":%d", uiPort), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func scheduler() {
	for {
		now := time.Now()
		// 每天 00:00 左右清理过期日志（每小时检查一次）
		if now.Hour() == 0 && now.Minute() < 5 {
			pruneLogs()
		}
		// 规则定时更新
		mu.RLock()
		next := cfg.NextUpdate
		interval := cfg.UpdateInterval
		mu.RUnlock()
		if interval != "manual" && !next.IsZero() && now.After(next) {
			log.Printf("定时更新规则…")
			updateAllRules(false)
			scheduleNextUpdate()
		}
		if interval != "manual" && next.IsZero() {
			scheduleNextUpdate()
		}
		time.Sleep(5 * time.Minute)
	}
}

// validUpdateTime 校验 "HH:MM"（00:00–23:59，两位数字）
func validUpdateTime(s string) bool {
	parts := strings.Split(s, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	return err1 == nil && err2 == nil && h >= 0 && h <= 23 && m >= 0 && m <= 59
}

// parseUpdateTime 取配置的更新时间点，非法时回落 03:00
func parseUpdateTime() (int, int) {
	if validUpdateTime(cfg.UpdateTime) {
		parts := strings.Split(cfg.UpdateTime, ":")
		h, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		return h, m
	}
	return 3, 0
}

// scheduleNextUpdate 排下次更新。时间与频率的关系：
//   - daily：每天 UpdateTime 执行一次；weekly：7 天后同一 UpdateTime 执行一次
//   - 1h/6h/12h：按小时隔固定间隔，UpdateTime 不生效；manual：不排期
func scheduleNextUpdate() {
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	h, m := parseUpdateTime()
	switch cfg.UpdateInterval {
	case "1h":
		cfg.NextUpdate = now.Add(time.Hour)
	case "6h":
		cfg.NextUpdate = now.Add(6 * time.Hour)
	case "12h":
		cfg.NextUpdate = now.Add(12 * time.Hour)
	case "weekly":
		cfg.NextUpdate = time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location()).Add(7 * 24 * time.Hour)
	case "manual":
		// 不排期
	default: // daily 每天 UpdateTime
		next := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		cfg.NextUpdate = next
	}
	saveConfigLocked()
}

func serveUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(uiHTML)
}

// serveIcon64/serveIcon256：标签页图标（favicon）。浏览器会自动请求 /favicon.ico，
// 之前这里 404，所以标签页只显示默认地球图标。现在返回 Dj净界盾牌 PNG。
func serveIcon64(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(icon64)
}

func serveIcon256(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(icon256)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func apiStatus(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	defer mu.RUnlock()
	// 今日拦截用计数器（跨天自动归零显示，不全量扫日志）
	today := time.Now().Format("2006-01-02")
	var todayN int64
	if stats.TodayDate == today {
		todayN = stats.TodayBlocked
	}
	// v1.0.2：仪表盘趋势图（今日按小时）+ 设备列表（IP、拦截数、最后活跃）
	// 在线 = 5 分钟内有过查询，或 IP 还在 ARP 表里（还在局域网内，手机熄屏也算在线）；
	// 只返回真实见过的设备，不编名字和数字
	hourly := make([]int, 24)
	copy(hourly, todayHourly[:])
	type device struct {
		IP         string `json:"ip"`
		Blocked    int64  `json:"blocked"`
		LastSeen   int64  `json:"lastSeen"`
		Online     bool   `json:"online"`
		Name       string `json:"name"`       // 显示名：手动改名 > 本机=NAS 主机名 > 路由器 PTR 主机名 > ""（只显示 IP）
		Vendor     string `json:"vendor"`     // v1.0.4 MAC 厂商（OUI 小表，查不到为空）
		Host       string `json:"host"`       // v1.0.4 PTR 反查到的主机名
		Type       string `json:"type"`       // 图标类型：手动 > 本机=server > 自动猜 > pc
		CustomName string `json:"customName"` // v1.0.4 用户手动起的名字（编辑框回填用）
		CustomType string `json:"customType"` // v1.0.4 用户手动选的类型（编辑框回填用）
	}
	devList := []device{}
	now := time.Now()
	onlineN := 0
	arp := arpTable() // 30 秒缓存，取厂商时本来就要读，顺手复用，不多一次贵操作
	for ip := range devices {
		if ip == "127.0.0.1" || ip == "::1" {
			continue // v1.0.7：本机回环不显示在设备列表里，没有必要
		}
		d := device{IP: ip, Blocked: deviceBlocked[ip]}
		var t time.Time
		if lt, ok := deviceLastSeen[ip]; ok {
			t = lt
			d.LastSeen = t.Unix()
		}
		d.Online = isOnline(ip, t, now, arp)
		d.Name, d.Vendor, d.Host, d.Type, d.CustomName, d.CustomType = enrichDevice(ip)
		if d.Online {
			onlineN++
		}
		devList = append(devList, d)
	}
	sort.Slice(devList, func(i, j int) bool { return devList[i].Blocked > devList[j].Blocked })
	writeJSON(w, map[string]any{
		"version": Version, "enabled": cfg.Enabled, "dnsPort": dnsPort, "dnsOK": dnsPortOK,
		"uiPort": uiPort, "uptime": time.Since(startTime).Round(time.Second).String(),
		"todayBlocked": todayN, "totalBlocked": stats.TotalBlocked,
		"devices": len(devList), "onlineDevices": onlineN, "rulesCount": len(blocked),
		"theme": cfg.Theme, "updateInterval": cfg.UpdateInterval,
		"updateTime": cfg.UpdateTime,
		"nextUpdate": cfg.NextUpdate.Format("2006-01-02 15:04"),
		"lastRuleUpdate": lastRuleUpdate(),
		"ruleCap": ruleCap, "dnsCacheCap": dnsCacheCap, "dnsCacheSize": dnsCacheLen(),
		"todayHourly": hourly, "deviceList": devList,
	})
}

func lastRuleUpdate() string {
	latest := ""
	for _, g := range cfg.Groups {
		if g.LastUpdate > latest {
			latest = g.LastUpdate
		}
	}
	return latest
}

// apiLogs 只返回环形缓冲里的最新 20 条；expiring 标记 24 小时内过期的行
func apiLogs(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	defer mu.RUnlock()
	now := time.Now()
	out := []LogEntry{}
	for _, e := range ringLatest() { // 环形缓冲最多 100 条，前端按 10 条分页
		e.Expiring = e.Time.Add(72 * time.Hour).Sub(now) < 24*time.Hour
		out = append(out, e)
	}
	// 智能提醒：小时计数器里近 1 小时内被拦 >=3 次的域名；
	// 用户点了「不再提醒」的域名过滤掉（dismissedAlerts，刷新、换设备都不再出现）
	type alert struct {
		Domain string `json:"domain"`
		Count  int    `json:"count"`
		Source string `json:"source"`
	}
	dismissed := map[string]bool{}
	for _, d := range cfg.DismissedAlerts {
		dismissed[d] = true
	}
	alerts := []alert{}
	h := now.Format("2006-01-02-15")
	for d, c := range hourCounts {
		if c.hour == h && c.count >= 3 && !dismissed[d] {
			alerts = append(alerts, alert{d, c.count, c.source})
		}
	}
	sort.Slice(alerts, func(i, j int) bool { return alerts[i].Count > alerts[j].Count })
	writeJSON(w, map[string]any{"logs": out, "total": logFileCount, "alerts": alerts, "dismissed": len(cfg.DismissedAlerts)})
}

// apiAlertDismiss：横幅点「不再提醒」→ 域名记进 config.json，刷新、换设备都不再出现
func apiAlertDismiss(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Domain string `json:"domain"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	d := strings.ToLower(strings.TrimSpace(req.Domain))
	if d == "" {
		writeJSON(w, map[string]any{"ok": false, "msg": "域名不对"})
		return
	}
	mu.Lock()
	found := false
	for _, x := range cfg.DismissedAlerts {
		if x == d {
			found = true
		}
	}
	if !found {
		cfg.DismissedAlerts = append(cfg.DismissedAlerts, d)
	}
	mu.Unlock()
	saveConfig()
	writeJSON(w, map[string]any{"ok": true, "dismissed": len(cfg.DismissedAlerts)})
}

// themeName：主题英文 key → 中文名（事件日志说人话用）
func themeName(t string) string {
	switch t {
	case "green":
		return "墨绿"
	case "white":
		return "纯白"
	case "black":
		return "深黑"
	case "red":
		return "红色"
	default:
		return t
	}
}

// apiAlertRestore：清空忽略列表，横幅恢复显示
func apiAlertRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	mu.Lock()
	cfg.DismissedAlerts = []string{}
	mu.Unlock()
	saveConfig()
	writeJSON(w, map[string]any{"ok": true, "dismissed": 0})
}

func apiToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	mu.Lock()
	old := cfg.Enabled
	cfg.Enabled = req.Enabled
	mu.Unlock()
	saveConfig()
	if old != req.Enabled {
		if req.Enabled {
			appendEvent("保护已开启")
		} else {
			appendEvent("保护已关闭")
		}
	}
	writeJSON(w, map[string]any{"ok": true, "enabled": cfg.Enabled})
}

func apiRules(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	defer mu.RUnlock()
	writeJSON(w, map[string]any{
		"groups": cfg.Groups, "custom": cfg.CustomRules,
		"subscriptions": cfg.Subscriptions, "total": len(blocked),
	})
}

func apiRulesGroup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	mu.Lock()
	found := false
	for i := range cfg.Groups {
		if cfg.Groups[i].ID == req.ID {
			cfg.Groups[i].Enabled = req.Enabled
			found = true
		}
	}
	mu.Unlock()
	if !found {
		writeJSON(w, map[string]any{"ok": false, "msg": "规则组不存在"})
		return
	}
	saveConfig()
	rebuildBlocked()
	writeJSON(w, map[string]any{"ok": true})
}

func apiRulesUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	go func() {
		updateAllRules(true)
		scheduleNextUpdate()
	}()
	writeJSON(w, map[string]any{"ok": true, "msg": "正在后台更新规则"})
}

func apiCustomRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	d := strings.ToLower(strings.TrimSpace(req.Domain))
	d = strings.TrimPrefix(d, "||")
	d = strings.TrimSuffix(d, "^")
	if d == "" || strings.Contains(d, " ") || strings.Contains(d, "/") {
		writeJSON(w, map[string]any{"ok": false, "msg": "域名格式不对"})
		return
	}
	mu.Lock()
	changed := false
	if r.Method == http.MethodDelete {
		before := len(cfg.CustomRules)
		kept := cfg.CustomRules[:0]
		for _, x := range cfg.CustomRules {
			if x != d {
				kept = append(kept, x)
			}
		}
		cfg.CustomRules = kept
		changed = len(kept) != before
	} else {
		found := false
		for _, x := range cfg.CustomRules {
			if x == d {
				found = true
			}
		}
		if !found {
			cfg.CustomRules = append(cfg.CustomRules, d)
			changed = true
		}
	}
	mu.Unlock()
	saveConfig()
	rebuildBlocked()
	if changed {
		if r.Method == http.MethodDelete {
			appendEvent("自定义规则已删除 " + d)
		} else {
			appendEvent("自定义规则已添加 " + d)
		}
	}
	writeJSON(w, map[string]any{"ok": true})
}

func apiSubscription(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	u := strings.TrimSpace(req.URL)
	mu.Lock()
	changed := false
	if r.Method == http.MethodDelete {
		before := len(cfg.Subscriptions)
		kept := cfg.Subscriptions[:0]
		for _, s := range cfg.Subscriptions {
			if s.URL != u {
				kept = append(kept, s)
			}
		}
		cfg.Subscriptions = kept
		changed = len(kept) != before
	} else {
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			mu.Unlock()
			writeJSON(w, map[string]any{"ok": false, "msg": "订阅地址必须是 http(s) 链接"})
			return
		}
		found := false
		for _, s := range cfg.Subscriptions {
			if s.URL == u {
				found = true
			}
		}
		if !found {
			cfg.Subscriptions = append(cfg.Subscriptions, Subscription{URL: u, Enabled: true})
			changed = true
		}
	}
	mu.Unlock()
	saveConfig()
	if changed {
		if r.Method == http.MethodDelete {
			appendEvent("订阅已删除 " + u)
		} else {
			appendEvent("订阅已添加 " + u)
		}
	}
	writeJSON(w, map[string]any{"ok": true})
}

func apiWhitelist(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	d := strings.ToLower(strings.TrimSpace(req.Domain))
	mu.Lock()
	changed := false
	if r.Method == http.MethodDelete {
		before := len(cfg.Whitelist)
		kept := cfg.Whitelist[:0]
		for _, x := range cfg.Whitelist {
			if x != d {
				kept = append(kept, x)
			}
		}
		cfg.Whitelist = kept
		changed = len(kept) != before
	} else if r.Method == http.MethodPost {
		if d == "" || strings.Contains(d, " ") || strings.Contains(d, "/") {
			mu.Unlock()
			writeJSON(w, map[string]any{"ok": false, "msg": "域名格式不对"})
			return
		}
		found := false
		for _, x := range cfg.Whitelist {
			if x == d {
				found = true
			}
		}
		if !found {
			cfg.Whitelist = append(cfg.Whitelist, d)
			changed = true
		}
	}
	list := cfg.Whitelist
	mu.Unlock()
	saveConfig()
	if changed {
		if r.Method == http.MethodDelete {
			appendEvent("白名单已删除 " + d)
		} else {
			appendEvent("白名单已添加 " + d)
		}
	}
	writeJSON(w, map[string]any{"ok": true, "whitelist": list})
}

func apiSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Theme          string `json:"theme"`
			UpdateInterval string `json:"updateInterval"`
			UpdateTime     string `json:"updateTime"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		oldTheme := cfg.Theme
		if req.Theme != "" {
			cfg.Theme = req.Theme
		}
		needReschedule := false
		if req.UpdateInterval != "" {
			switch req.UpdateInterval {
			case "1h", "6h", "12h", "daily", "weekly", "manual":
				if cfg.UpdateInterval != req.UpdateInterval {
					needReschedule = true
				}
				cfg.UpdateInterval = req.UpdateInterval
				cfg.NextUpdate = time.Time{}
			default:
				mu.Unlock()
				writeJSON(w, map[string]any{"ok": false, "msg": "更新频率不对"})
				return
			}
		}
		if req.UpdateTime != "" {
			if !validUpdateTime(req.UpdateTime) {
				mu.Unlock()
				writeJSON(w, map[string]any{"ok": false, "msg": "时间格式不对，请用 HH:MM（00:00–23:59）"})
				return
			}
			if cfg.UpdateTime != req.UpdateTime {
				// 每天/每周模式下时间变了才重排；按小时模式时间不生效，不重排
				if cfg.UpdateInterval == "daily" || cfg.UpdateInterval == "weekly" {
					needReschedule = true
				}
				cfg.UpdateTime = req.UpdateTime
			}
		}
		mu.Unlock()
		saveConfig()
		if req.Theme != "" && req.Theme != oldTheme {
			appendEvent("主题已切换为" + themeName(req.Theme))
		}
		if needReschedule {
			scheduleNextUpdate()
		}
	}
	mu.RLock()
	defer mu.RUnlock()
	writeJSON(w, map[string]any{"ok": true, "theme": cfg.Theme, "updateInterval": cfg.UpdateInterval, "updateTime": cfg.UpdateTime, "nextUpdate": cfg.NextUpdate.Format("2006-01-02 15:04")})
}

func apiVersionCheck(w http.ResponseWriter, r *http.Request) {
	latest, url, err := checkLatestRelease()
	if err != nil {
		writeJSON(w, map[string]any{"current": Version, "latest": "", "url": "", "error": "检查失败：" + err.Error()})
		return
	}
	if latest == "" {
		writeJSON(w, map[string]any{"current": Version, "latest": "", "url": "", "hasUpdate": false, "msg": "暂无 Dj净界专属发布"})
		return
	}
	writeJSON(w, map[string]any{"current": Version, "latest": latest, "url": url, "hasUpdate": latest != "" && latest != "v"+Version && latest != Version})
}

var _ = net.ParseIP
