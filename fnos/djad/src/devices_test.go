package main

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseGatewayFromRoute(t *testing.T) {
	in := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
		"end0\t00000000\t0102A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
		"end0\t0002A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n"
	if got := parseGatewayFromRoute(strings.NewReader(in)); got != "192.168.2.1" {
		t.Fatalf("gateway = %q, want 192.168.2.1", got)
	}
	if got := parseGatewayFromRoute(strings.NewReader("Iface\tDestination\tGateway\tFlags\n")); got != "" {
		t.Fatalf("no default route, got %q", got)
	}
}

func TestSanitizeHostname(t *testing.T) {
	cases := []struct{ in, ip, want string }{
		{"oec-t.", "192.168.2.90", "oec-t"},
		{"mat30.lan.", "192.168.2.112", "mat30"},
		{"HUAWEI-P30.lan.", "192.168.2.112", "HUAWEI-P30"},
		{"n1.home.", "192.168.2.5", "n1"},
		{"192-168-2-112.", "192.168.2.112", ""},
		{"192.168.2.112", "192.168.2.112", ""},
		{"", "192.168.2.112", ""},
	}
	for _, c := range cases {
		if got := sanitizeHostname(c.in, c.ip); got != c.want {
			t.Errorf("sanitizeHostname(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOuiVendor(t *testing.T) {
	cases := []struct{ mac, want string }{
		{"00:03:93:AA:BB:CC", "Apple"},
		{"00:18:82:11:22:33", "华为"},
		{"00:9E:C8:11:22:33", "小米"},
		{"00:12:FB:11:22:33", "Samsung"},
		{"00:1D:0F:11:22:33", "TP-Link"},
		{"02:42:AC:11:00:02", ""}, // 本地随机 MAC，不查表
		{"AA:BB:CC:11:22:33", ""},
		{"not-a-mac", ""},
	}
	for _, c := range cases {
		if got := ouiVendor(c.mac); got != c.want {
			t.Errorf("ouiVendor(%q) = %q, want %q", c.mac, got, c.want)
		}
	}
}

func TestParseArpTable(t *testing.T) {
	in := "IP address       HW type     Flags       HW address            Mask     Device\n" +
		"192.168.2.1      0x1         0x2         aa:bb:cc:11:22:33     *        end0\n" +
		"192.168.2.112    0x1         0x2         00:18:82:aa:bb:cc     *        end0\n" +
		"192.168.2.200    0x1         0x0         00:00:00:00:00:00     *        end0\n"
	m := parseArpTable(strings.NewReader(in))
	if len(m) != 2 || m["192.168.2.112"] != "00:18:82:AA:BB:CC" {
		t.Fatalf("arp = %v", m)
	}
}

func TestGuessDeviceType(t *testing.T) {
	gw := gatewayIP() // 实机网关，测试里只断言“网关就是路由器”
	cases := []struct{ ip, host, want string }{
		{gw, "", "router"},
		{"192.168.2.112", "mat30", "phone"},
		{"192.168.2.112", "HUAWEI-P30", "phone"},
		{"192.168.2.112", "iPad", "tablet"},
		{"192.168.2.112", "Mi-TV", "tv"},
		{"192.168.2.112", "oec-t", "pc"},
		{"192.168.2.112", "", "pc"},
	}
	for _, c := range cases {
		if got := guessDeviceType(c.ip, c.host, ""); got != c.want {
			t.Errorf("guessDeviceType(%q,%q) = %q, want %q", c.ip, c.host, got, c.want)
		}
	}
}

// 记录型 stub：只收查询不回包（本机沙箱丢 UDP 回包，见 dns_forward_test.go 的注释，
// 所以这里只断言“反查确实发到了网关、问题是对的 PTR”；回包解析走 Go 标准库，
// 成功路径待 NAS 实机验证）
type ptrQuery struct {
	domain string
	qtype  uint16
}

func startStubPTRServer(t *testing.T) (string, chan ptrQuery) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	got := make(chan ptrQuery, 16)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pkt := append([]byte{}, buf[:n]...)
			domain, qtype, _, ok := parseQuestion(pkt)
			if !ok {
				continue
			}
			got <- ptrQuery{domain, qtype}
		}
	}()
	return pc.LocalAddr().String(), got
}

func TestPtrLookupSendsQuery(t *testing.T) {
	server, got := startStubPTRServer(t)
	errCh := make(chan error, 1)
	go func() {
		_, err := ptrLookup("192.168.2.112", server)
		errCh <- err
	}()
	select {
	case q := <-got:
		if q.domain != "112.2.168.192.in-addr.arpa" {
			t.Fatalf("PTR query domain = %q", q.domain)
		}
		if q.qtype != 12 {
			t.Fatalf("PTR query qtype = %d, want 12", q.qtype)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stub never received PTR query")
	}
	// 无回包时 1 秒超时返回错误，不 hang、不刷日志
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("ptrLookup without reply should error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ptrLookup did not return after timeout")
	}
}

func TestIsLocalIP(t *testing.T) {
	if !isLocalIP("127.0.0.1") {
		t.Fatal("127.0.0.1 should be local")
	}
	if isLocalIP("192.168.2.112") {
		t.Fatal("192.168.2.112 should not be local")
	}
	if isLocalIP("8.8.8.8") {
		t.Fatal("8.8.8.8 should not be local")
	}
}

func TestNasHostname(t *testing.T) {
	if got := nasHostname(); got == "" {
		t.Fatal("nasHostname empty")
	}
}

func TestEnrichDeviceLocal(t *testing.T) {
	// 本机行：名字取 NAS 主机名、类型取服务器；手动改名改类型仍优先
	name, _, _, dtype, _, _ := enrichDevice("127.0.0.1")
	if name != nasHostname() {
		t.Fatalf("local name = %q, want hostname %q", name, nasHostname())
	}
	if dtype != "server" {
		t.Fatalf("local type = %q, want server", dtype)
	}
	// 非本机不受影响：仍默认电脑图标
	_, _, _, dtype2, _, _ := enrichDevice("192.168.2.112")
	if dtype2 != "pc" {
		t.Fatalf("non-local type = %q, want pc", dtype2)
	}
}

func TestIsOnline(t *testing.T) {
	now := time.Now()
	arp := map[string]string{"192.168.2.112": "00:18:82:AA:BB:CC"}
	// 2 分钟前有查询 → 在线
	if !isOnline("192.168.2.112", now.Add(-2*time.Minute), now, arp) {
		t.Error("recent query should be online")
	}
	// 10 分钟前有查询、但还在 ARP 表里（熄屏手机）→ 在线
	if !isOnline("192.168.2.112", now.Add(-10*time.Minute), now, arp) {
		t.Error("in ARP table should be online even without recent query")
	}
	// 10 分钟前有查询、也不在 ARP 表里 → 离线
	if isOnline("192.168.2.113", now.Add(-10*time.Minute), now, arp) {
		t.Error("old query and not in ARP should be offline")
	}
	// 从没查过、也不在 ARP 表里 → 离线
	if isOnline("192.168.2.114", time.Time{}, now, arp) {
		t.Error("never seen and not in ARP should be offline")
	}
}

func TestOfflineTransitions(t *testing.T) {
	prev := map[string]bool{"192.168.2.112": true, "192.168.2.113": true, "192.168.2.114": false}
	now := map[string]bool{"192.168.2.112": false, "192.168.2.113": true, "192.168.2.114": true}
	off := offlineTransitions(prev, now)
	if len(off) != 1 || off[0] != "192.168.2.112" {
		t.Fatalf("offlineTransitions = %v, want [192.168.2.112]", off)
	}
	// 全在线→全在线：不记；全离线→全离线：不记
	if got := offlineTransitions(now, now); len(got) != 0 {
		t.Fatalf("no transition, got %v", got)
	}
}

func TestAppendEvent(t *testing.T) {
	dataDir = t.TempDir()
	appendEvent("测试事件：保护已开启")
	found := false
	for _, e := range ringLatest() {
		if e.Kind == "event" && e.Event == "测试事件：保护已开启" {
			found = true
		}
	}
	if !found {
		t.Fatal("event not in ring")
	}
	if _, ok := devices[""]; ok {
		t.Fatal("event polluted devices with empty client")
	}
}

func TestLoadLogsSkipsEventTracking(t *testing.T) {
	dataDir = t.TempDir()
	lines := "{\"time\":\"" + time.Now().Format(time.RFC3339Nano) + "\",\"client\":\"192.168.2.200\",\"domain\":\"ads.google.com\",\"qtype\":\"A\",\"blocked\":true,\"source\":\"内置规则\"}\n" +
		"{\"time\":\"" + time.Now().Format(time.RFC3339Nano) + "\",\"kind\":\"event\",\"event\":\"测试事件：主题已切换为红色\"}\n"
	if err := os.WriteFile(logsPath(), []byte(lines), 0644); err != nil {
		t.Fatal(err)
	}
	loadLogs()
	if _, ok := devices[""]; ok {
		t.Fatal("replayed event polluted devices with empty client")
	}
	found := false
	for _, e := range ringLatest() {
		if e.Kind == "event" && e.Event == "测试事件：主题已切换为红色" {
			found = true
		}
	}
	if !found {
		t.Fatal("replayed event not in ring")
	}
}

func TestApiAlertDismissRestore(t *testing.T) {
	dataDir = t.TempDir()
	mu.Lock()
	old := cfg.DismissedAlerts
	cfg.DismissedAlerts = []string{}
	mu.Unlock()
	defer func() { mu.Lock(); cfg.DismissedAlerts = old; mu.Unlock() }()
	post := func(path, body string) {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		if path == "/api/alerts/dismiss" {
			apiAlertDismiss(rec, req)
		} else {
			apiAlertRestore(rec, req)
		}
		if rec.Code != 200 {
			t.Fatalf("%s status %d", path, rec.Code)
		}
	}
	count := func() int {
		mu.RLock()
		defer mu.RUnlock()
		return len(cfg.DismissedAlerts)
	}
	post("/api/alerts/dismiss", `{"domain":"ads.example.com"}`)
	if count() != 1 {
		t.Fatalf("after dismiss count = %d, want 1", count())
	}
	post("/api/alerts/dismiss", `{"domain":"ads.example.com"}`) // 重复点不重复记
	if count() != 1 {
		t.Fatalf("after duplicate dismiss count = %d, want 1", count())
	}
	post("/api/alerts/restore", `{}`)
	if count() != 0 {
		t.Fatalf("after restore count = %d, want 0", count())
	}
}

func TestApiLogsDismissedAlerts(t *testing.T) {
	dataDir = t.TempDir()
	mu.Lock()
	hourCounts["ads.example.com"] = &hourCounter{hour: time.Now().Format("2006-01-02-15"), count: 5, source: "内置规则"}
	oldDismissed := cfg.DismissedAlerts
	cfg.DismissedAlerts = []string{"ads.example.com"}
	mu.Unlock()
	defer func() {
		mu.Lock()
		delete(hourCounts, "ads.example.com")
		cfg.DismissedAlerts = oldDismissed
		mu.Unlock()
	}()
	req := httptest.NewRequest("GET", "/api/logs", nil)
	rec := httptest.NewRecorder()
	apiLogs(rec, req)
	var resp struct {
		Alerts    []struct {
			Domain string `json:"domain"`
		} `json:"alerts"`
		Dismissed int `json:"dismissed"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Alerts) != 0 {
		t.Fatalf("dismissed alert still shown: %v", resp.Alerts)
	}
	if resp.Dismissed != 1 {
		t.Fatalf("dismissed = %d, want 1", resp.Dismissed)
	}
}

func TestValidUpdateTime(t *testing.T) {
	for _, good := range []string{"03:00", "00:00", "23:59", "15:30"} {
		if !validUpdateTime(good) {
			t.Errorf("validUpdateTime(%q) = false, want true", good)
		}
	}
	for _, bad := range []string{"3:00", "24:00", "03:60", "abc", "", "03:00:00"} {
		if validUpdateTime(bad) {
			t.Errorf("validUpdateTime(%q) = true, want false", bad)
		}
	}
}

func TestUpdateTimeSchedule(t *testing.T) {
	dataDir = t.TempDir() // scheduleNextUpdate 会写盘，别污染源码目录
	mu.Lock()
	cfg.UpdateInterval = "daily"
	cfg.UpdateTime = "15:30"
	mu.Unlock()
	scheduleNextUpdate()
	mu.RLock()
	nu := cfg.NextUpdate
	mu.RUnlock()
	if nu.Hour() != 15 || nu.Minute() != 30 {
		t.Fatalf("nextUpdate = %v, want 15:30", nu)
	}
	if !nu.After(time.Now()) {
		t.Fatalf("nextUpdate not in future: %v", nu)
	}
	// 非法时间回落 03:00
	mu.Lock()
	cfg.UpdateTime = "99:99"
	mu.Unlock()
	scheduleNextUpdate()
	mu.RLock()
	nu = cfg.NextUpdate
	mu.RUnlock()
	if nu.Hour() != 3 || nu.Minute() != 0 {
		t.Fatalf("bad time fallback nextUpdate = %v, want 03:00", nu)
	}
}
