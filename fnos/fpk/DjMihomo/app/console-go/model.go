// Nikki-style data model: profiles/, subscriptions.json (metadata only),
// active-profile, mixin.json, mixin.yaml, and running-config generation
// (profile + mixin.yaml + mixin.json -> config.yaml).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ---------------------------------------------------------------- app settings

type AppSettings struct {
	Enabled           bool   `json:"enabled"`
	Profile           string `json:"profile"`
	StartDelay        int    `json:"start_delay"`
	ScheduledRestart  bool   `json:"scheduled_restart"`
	ScheduledCron     string `json:"scheduled_restart_cron"`
	CoreOnly          bool   `json:"core_only"`
}

func defaultAppSettings() AppSettings {
	return AppSettings{
		Enabled:          false,
		Profile:          "file:default",
		StartDelay:       0,
		ScheduledRestart: false,
		ScheduledCron:    "0 3 * * *",
		CoreOnly:         false,
	}
}

func loadAppSettings() AppSettings {
	s := defaultAppSettings()
	b, err := os.ReadFile(appPath)
	if err != nil {
		return s
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return s
	}
	if v, ok := m["enabled"].(bool); ok {
		s.Enabled = v
	}
	if v, ok := m["profile"].(string); ok && v != "" {
		s.Profile = v
	}
	if v, ok := m["start_delay"].(float64); ok {
		s.StartDelay = int(v)
	}
	if v, ok := m["scheduled_restart"].(bool); ok {
		s.ScheduledRestart = v
	}
	if v, ok := m["scheduled_restart_cron"].(string); ok && v != "" {
		s.ScheduledCron = v
	}
	if v, ok := m["core_only"].(bool); ok {
		s.CoreOnly = v
	}
	return s
}

func saveAppSettings(s AppSettings) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(appPath, b, 0644); err != nil {
		return err
	}
	// marker file consulted by cmd/main: skip starting mihomo when disabled
	if s.Enabled {
		_ = os.Remove(disabledPath)
	} else {
		_ = os.WriteFile(disabledPath, []byte("disabled by console\n"), 0644)
	}
	return nil
}

// ---------------------------------------------------------------- profiles

type ProfileInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // "file" | "subscription"
	Updated string `json:"updated"`
	HasFile bool   `json:"has_file"`
}

func profileFilePath(name string) string {
	return filepath.Join(profilesDir, sanitizeProfileName(name)+".yaml")
}

func sanitizeProfileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".yaml")
	name = strings.TrimSuffix(name, ".yml")
	var b strings.Builder
	for _, r := range name {
		// Unicode letters (incl. Chinese) and digits are kept as-is, so a
		// subscription named 公益mia / 网友买 keeps its name instead of
		// degrading to __mia / ___. Only path separators and control chars
		// are unsafe in a filename and become '_'.
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" || s == "." || s == ".." {
		s = "default"
	}
	return s
}

// legacySanitizeProfileName is the pre-1.2.57 ASCII-only sanitizer, kept only
// so migrateProfileNames can find files written under the old names.
func legacySanitizeProfileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".yaml")
	name = strings.TrimSuffix(name, ".yml")
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" || s == "." || s == ".." {
		s = "default"
	}
	return s
}

// migrateProfileNames renames subscription profile files written under the old
// ASCII-only sanitizer (e.g. __mia.yaml for 公益mia, ___.yaml for 网友买) to
// their new Unicode-preserving names, and updates the active-profile ref if
// it pointed at an old name. Safe to run on every startup: it only renames
// when the old file exists and the new one does not.
func migrateProfileNames() {
	subs := loadSubscriptions()
	if len(subs) == 0 {
		return
	}
	active := readActiveProfileRef()
	for _, sub := range subs {
		newName := sanitizeProfileName(sub.Name)
		oldName := legacySanitizeProfileName(sub.Name)
		if newName == oldName {
			continue
		}
		oldPath := filepath.Join(profilesDir, oldName+".yaml")
		newPath := filepath.Join(profilesDir, newName+".yaml")
		if _, err := os.Stat(oldPath); err != nil {
			continue
		}
		if _, err := os.Stat(newPath); err == nil {
			continue
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			logf("migrate profile %s -> %s failed: %v", oldName, newName, err)
			continue
		}
		logf("migrated profile %s -> %s", oldName, newName)
		if active == "subscription:"+oldName {
			active = "subscription:" + newName
		}
	}
	if active != readActiveProfileRef() {
		_ = writeActiveProfileRef(active)
	}
}

// listProfiles returns file profiles (from profiles/*.yaml) plus subscription
// profiles (from subscriptions.json). A profile file backed by a subscription
// is listed as type "subscription".
func listProfiles() []ProfileInfo {
	subs := loadSubscriptions()
	subNames := map[string]bool{}
	subUpdated := map[string]string{}
	for _, s := range subs {
		n := sanitizeProfileName(s.Name)
		subNames[n] = true
		subUpdated[n] = s.Updated
	}
	seen := map[string]bool{}
	var out []ProfileInfo
	entries, _ := os.ReadDir(profilesDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		seen[name] = true
		updated := ""
		if fi, err := e.Info(); err == nil {
			updated = fi.ModTime().Format("2006-01-02 15:04:05")
		}
		typ := "file"
		if subNames[name] {
			typ = "subscription"
			if subUpdated[name] != "" {
				updated = subUpdated[name]
			}
		}
		out = append(out, ProfileInfo{Name: name, Type: typ, Updated: updated, HasFile: true})
	}
	for _, s := range subs {
		name := sanitizeProfileName(s.Name)
		if seen[name] {
			continue
		}
		seen[name] = true
		_, err := os.Stat(profileFilePath(name))
		out = append(out, ProfileInfo{Name: name, Type: "subscription", Updated: s.Updated, HasFile: err == nil})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if out == nil {
		out = []ProfileInfo{}
	}
	return out
}

func readActiveProfileRef() string {
	b, err := os.ReadFile(activeProfilePath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeActiveProfileRef(ref string) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(activeProfilePath, []byte(ref+"\n"), 0644)
}

// resolveProfile returns (ref, yamlText, error). ref is "file:<name>" or
// "subscription:<name>". Missing subscription files are fetched first.
func resolveProfile(ref string) (string, string, error) {
	if ref == "" {
		ref = "file:default"
	}
	kind, name := splitRef(ref)
	switch kind {
	case "subscription":
		path := profileFilePath(name)
		if b, err := os.ReadFile(path); err == nil {
			return ref, string(b), nil
		}
		// fetch the subscription to create the profile file
		subs := loadSubscriptions()
		var sub *Sub
		for i := range subs {
			if sanitizeProfileName(subs[i].Name) == name {
				sub = &subs[i]
				break
			}
		}
		if sub == nil {
			return "", "", fmt.Errorf("subscription %q not found", name)
		}
		if _, err := updateSubscriptionFile(sub); err != nil {
			return "", "", err
		}
		_ = saveSubscriptions(subs)
		b, err := os.ReadFile(path)
		if err != nil {
			return "", "", err
		}
		return ref, string(b), nil
	default: // file
		b, err := os.ReadFile(profileFilePath(name))
		if err != nil {
			return "", "", fmt.Errorf("profile %q not found", name)
		}
		return ref, string(b), nil
	}
}

func splitRef(ref string) (kind, name string) {
	if i := strings.Index(ref, ":"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return "file", ref
}

// updateSubscriptionFile fetches one subscription and saves it as its
// profile file. It updates sub.Updated in memory (the caller saves
// subscriptions.json). Returns the node count (0 for raw YAML).
func updateSubscriptionFile(sub *Sub) (int, error) {
	ua := sub.UserAgent
	if ua == "" {
		ua = "clash"
	}
	logf("操作：开始更新订阅「%s」（%s）", sub.Name, map[bool]string{true: "走代理", false: "直连"}[sub.UseProxy])
	text, err := fetchURLWithUAProxy(sub.URL, ua, 30*time.Second, sub.UseProxy)
	if err != nil {
		logf("操作：订阅「%s」更新失败：%s。%s", sub.Name, err.Error(), fetchErrAdvice(err, sub.UseProxy))
		return 0, err
	}
	profileText, count := subscriptionToProfile(text)
	if err := os.MkdirAll(profilesDir, 0755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(profileFilePath(sub.Name), []byte(profileText), 0644); err != nil {
		return 0, err
	}
	sub.Updated = time.Now().Format("2006-01-02 15:04:05")
	if count > 0 {
		logf("操作：订阅「%s」更新成功，共 %d 个节点", sub.Name, count)
	} else {
		logf("操作：订阅「%s」更新成功（YAML 配置已保存）", sub.Name)
	}
	return count, nil
}

// ---------------------------------------------------------------- mixin

type Mixin struct {
	LogLevel           *string  `json:"log_level,omitempty"`
	Mode               *string  `json:"mode,omitempty"`
	IPv6               *bool    `json:"ipv6,omitempty"`
	AllowLan           *bool    `json:"allow_lan,omitempty"`
	MixedPort          *int     `json:"mixed_port,omitempty"`
	HTTPPort           *int     `json:"http_port,omitempty"`
	SocksPort          *int     `json:"socks_port,omitempty"`
	RedirPort          *int     `json:"redir_port,omitempty"`
	TproxyPort         *int     `json:"tproxy_port,omitempty"`
	APIListen          *string  `json:"api_listen,omitempty"`
	SelectionCache     *bool    `json:"selection_cache,omitempty"`
	DNSEnabled         *bool    `json:"dns_enabled,omitempty"`
	DNSListen          *string  `json:"dns_listen,omitempty"`
	DNSMode            *string  `json:"dns_mode,omitempty"`
	FakeIPRange        *string  `json:"fake_ip_range,omitempty"`
	Nameserver         []string `json:"nameserver,omitempty"`
	DefaultNameserver  []string `json:"default_nameserver,omitempty"`
	TunEnabled         *bool    `json:"tun_enabled,omitempty"`
	TunDevice          *string  `json:"tun_device,omitempty"`
	TunStack           *string  `json:"tun_stack,omitempty"`
	TunMTU             *int     `json:"tun_mtu,omitempty"`
}

func loadMixin() Mixin {
	var m Mixin
	b, err := os.ReadFile(mixinPath)
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func saveMixin(m Mixin) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(mixinPath, b, 0644)
}

func loadMixinFile() string {
	b, err := os.ReadFile(mixinFilePath)
	if err != nil {
		return ""
	}
	return string(b)
}

func saveMixinFile(text string) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(mixinFilePath, []byte(text), 0644)
}

// mixinCoveredKeys returns the top-level Mihomo keys the mixin.json settings
// control (mixin.json wins over mixin.yaml for these).
func mixinCoveredKeys(m Mixin) map[string]bool {
	keys := map[string]bool{}
	if m.LogLevel != nil {
		keys["log-level"] = true
	}
	if m.Mode != nil {
		keys["mode"] = true
	}
	if m.IPv6 != nil {
		keys["ipv6"] = true
	}
	if m.AllowLan != nil {
		keys["allow-lan"] = true
	}
	if m.MixedPort != nil {
		keys["mixed-port"] = true
	}
	if m.HTTPPort != nil {
		keys["port"] = true
	}
	if m.SocksPort != nil {
		keys["socks-port"] = true
	}
	if m.RedirPort != nil {
		keys["redir-port"] = true
	}
	if m.TproxyPort != nil {
		keys["tproxy-port"] = true
	}
	if m.APIListen != nil {
		keys["external-controller"] = true
	}
	if m.SelectionCache != nil {
		keys["profile"] = true
	}
	if m.DNSEnabled != nil || m.DNSListen != nil || m.DNSMode != nil || m.FakeIPRange != nil || len(m.Nameserver) > 0 || len(m.DefaultNameserver) > 0 {
		keys["dns"] = true
	}
	if m.TunEnabled != nil || m.TunDevice != nil || m.TunStack != nil || m.TunMTU != nil {
		keys["tun"] = true
	}
	return keys
}

func strList(items []string) []interface{} {
	out := make([]interface{}, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

// applyMixin merges mixin.yaml (lower priority) and mixin.json (higher
// priority) into the profile text.
func applyMixin(profileText string, m Mixin, mixinYAML string) string {
	lines := splitLines(profileText)
	covered := mixinCoveredKeys(m)

	// 1. mixin.yaml: apply whole top-level blocks for keys NOT covered by
	// mixin.json.
	if strings.TrimSpace(mixinYAML) != "" {
		myLines := splitLines(mixinYAML)
		for _, b := range splitTopBlocks(myLines) {
			if covered[b.key] {
				continue
			}
			lines = replaceTopBlock(lines, b.key, myLines[b.start:b.end])
		}
	}

	// 2. mixin.json scalar keys.
	var updates []KV
	addStr := func(key string, v *string) {
		if v != nil {
			updates = append(updates, KV{key, yamlStr(*v)})
		}
	}
	addBool := func(key string, v *bool) {
		if v != nil {
			if *v {
				updates = append(updates, KV{key, "true"})
			} else {
				updates = append(updates, KV{key, "false"})
			}
		}
	}
	addInt := func(key string, v *int) {
		if v != nil {
			updates = append(updates, KV{key, strconv.Itoa(*v)})
		}
	}
	addStr("log-level", m.LogLevel)
	addStr("mode", m.Mode)
	addBool("ipv6", m.IPv6)
	addBool("allow-lan", m.AllowLan)
	addInt("mixed-port", m.MixedPort)
	addInt("port", m.HTTPPort)
	addInt("socks-port", m.SocksPort)
	addInt("redir-port", m.RedirPort)
	addInt("tproxy-port", m.TproxyPort)
	addStr("external-controller", m.APIListen)
	if len(updates) > 0 {
		lines = setTopKeys(lines, updates)
	}

	// 3. mixin.json nested blocks: profile / dns / tun.
	if m.SelectionCache != nil {
		block := append([]string{"profile:"}, emitYAML(Obj{{"store-selected", *m.SelectionCache}}, 2)...)
		lines = replaceTopBlock(lines, "profile", block)
	}
	var dns Obj
	if m.DNSEnabled != nil {
		dns = append(dns, KV{"enable", *m.DNSEnabled})
	}
	if m.DNSListen != nil {
		dns = append(dns, KV{"listen", *m.DNSListen})
	}
	if m.DNSMode != nil {
		dns = append(dns, KV{"enhanced-mode", *m.DNSMode})
	}
	if m.FakeIPRange != nil {
		dns = append(dns, KV{"fake-ip-range", *m.FakeIPRange})
	}
	if len(m.Nameserver) > 0 {
		dns = append(dns, KV{"nameserver", strList(m.Nameserver)})
	}
	if len(m.DefaultNameserver) > 0 {
		dns = append(dns, KV{"default-nameserver", strList(m.DefaultNameserver)})
	}
	if len(dns) > 0 {
		block := append([]string{"dns:"}, emitYAML(dns, 2)...)
		lines = replaceTopBlock(lines, "dns", block)
	}
	var tun Obj
	if m.TunEnabled != nil {
		tun = append(tun, KV{"enable", *m.TunEnabled})
	}
	if m.TunDevice != nil {
		tun = append(tun, KV{"device", *m.TunDevice})
	}
	if m.TunStack != nil {
		tun = append(tun, KV{"stack", *m.TunStack})
	}
	if m.TunMTU != nil {
		tun = append(tun, KV{"mtu", *m.TunMTU})
	}
	if len(tun) > 0 {
		block := append([]string{"tun:"}, emitYAML(tun, 2)...)
		lines = replaceTopBlock(lines, "tun", block)
	}

	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

// ---------------------------------------------------------------- generation

// essentialDefaults are filled in when neither the profile nor the mixin
// sets them, so the console API and the dashboard always stay reachable.
var essentialDefaults = []KV{
	{"mixed-port", "7890"},
	{"allow-lan", "true"},
	{"mode", "rule"},
	{"log-level", "info"},
	{"ipv6", "true"},
	{"external-controller", "0.0.0.0:9090"},
	{"external-ui", "ui/ange"},
	{"secret", `""`},
}

func ensureEssentialKeys(lines []string) []string {
	keys := topBlockKeys(lines)
	var missing []KV
	for _, kv := range essentialDefaults {
		if !keys[kv.Key] {
			missing = append(missing, kv)
		}
	}
	if len(missing) == 0 {
		return lines
	}
	return setTopKeys(lines, missing)
}

// safeDNSBlock is the anti-leak DNS config applied when the active profile
// has no dns: block at all. Without it Mihomo falls back to the system DNS
// and queries go straight to the ISP, leaking every domain visited.
// The block uses fake-ip (clients ask Mihomo, Mihomo resolves), DoH for
// nameservers, and proxy-server-nameserver so foreign DNS goes through the
// proxy node. Domestic sites stay in fake-ip-filter (direct, fast).
// Profiles that already carry their own dns: block are left untouched.
var safeDNSBlock = []string{
	"dns:",
	"  enable: true",
	"  ipv6: false",
	"  enhanced-mode: fake-ip",
	"  fake-ip-range: 198.18.0.0/16",
	"  fake-ip-filter:",
	"    - '*.lan'",
	"    - '*.local'",
	"    - '*.localhost'",
	"    - '*.arpa'",
	"    - 'geosite:cn'",
	"  default-nameserver:",
	"    - 119.29.29.29",
	"    - 223.5.5.5",
	"  proxy-server-nameserver:",
	"    - https://doh.pub/dns-query",
	"  nameserver:",
	"    - https://doh.pub/dns-query",
}

func ensureSafeDNS(lines []string) []string {
	if topBlockKeys(lines)["dns"] {
		return lines
	}
	logf("dns: profile has no dns block, applying anti-leak default (fake-ip + DoH via proxy)")
	return replaceTopBlock(lines, "dns", safeDNSBlock)
}

// tunBlock is the bypass-router config, forced on so the NAS works as a
// gateway. Nikki (luci-app-mihomo / OpenWrt-nikki) split: TCP does NOT go
// through TUN — it is REDIRECTed by iptables into Mihomo's redir-port
// (7891, see generateRunningConfig). TUN system stack stalls TCP: the
// phone's Google connections over TUN downloaded only 854 bytes then
// stopped, while the same proxy serves a full YouTube page in <1s over
// the HTTP proxy. REDIRECT is kernel-level and never touches the TUN
// user-space stack, so it does not stall. TUN now only carries UDP
// (QUIC, DNS) plus dns-hijack. stack mixed = Nikki's default (gVisor
// for UDP, system for the rest). dns-hijack is ON (v1.2.18.58): every
// DNS query from a gateway device is intercepted by Mihomo's DNS
// (fake-ip) instead of reaching the client's configured server. Without
// it the phone's DNS goes to AliDNS, which answers YouTube's AAAA with a
// bogus IPv6 (2001::1); the phone then tries IPv6 first and stalls,
// because the proxy nodes cannot do IPv6 outbound. With dns-hijack +
// dns.ipv6=false (see forceDNSIPv6False) the phone only ever gets IPv4
// and YouTube/Google load through the proxy. When DjJingjie is enabled
// its iptables REDIRECT (53->5354, PREROUTING) still wins over TUN, so
// DNS keeps flowing to DjJingjie then; dns-hijack only takes over when
// DjJingjie is off. auto-route is OFF on purpose (v1.2.18.54): Mihomo's
// auto-route captures the NAS's own outbound traffic and breaks its TLS
// to Cloudflare (proxy nodes live there), which killed the proxy,
// subscriptions and the phone's foreign sites in v1.2.18.53. LAN UDP is
// routed into tun0 by manual policy routing on the NAS (table 100, set
// up by djmihomo-tun-setup.sh); LAN TCP is REDIRECTed to 7891 by the
// same script. The NAS itself stays on the main table. Requires the
// mihomo binary to have CAP_NET_ADMIN (granted by a root helper on the
// NAS, not by the package) and IP forwarding + NAT on the NAS, otherwise
// Mihomo fails to start.
var tunBlock = []string{
	"tun:",
	"  enable: true",
	"  stack: mixed",
	"  device: tun0",
	"  auto-route: false",
	"  auto-detect-interface: false",
	"  dns-hijack:",
	"    - any:53",
	"  mtu: 1500",
}

// forceDNSIPv6False sets ipv6: false inside the dns: block. The proxy
// nodes cannot do IPv6 outbound (NAS test: IPv6 YouTube/Google via proxy
// always returns 000), so any AAAA answer makes the phone try IPv6 first
// and stall the page. With ipv6: false Mihomo's DNS returns no AAAA at
// all and clients use IPv4 through the proxy. Top-level ipv6 stays true
// (forced elsewhere) so direct IPv6 traffic is still handled, only DNS
// stops advertising IPv6.
func forceDNSIPv6False(lines []string) []string {
	for _, b := range splitTopBlocks(lines) {
		if b.key != "dns" {
			continue
		}
		out := append([]string{}, lines[:b.start]...)
		replaced := false
		for _, ln := range lines[b.start:b.end] {
			if strings.HasPrefix(ln, "  ipv6:") {
				out = append(out, "  ipv6: false")
				replaced = true
				continue
			}
			out = append(out, ln)
		}
		if !replaced {
			// Insert right after the "dns:" line.
			out = append([]string{}, out[:b.start+1]...)
			out = append(out, "  ipv6: false")
			out = append(out, lines[b.start+1:b.end]...)
		}
		out = append(out, lines[b.end:]...)
		logf("dns: forcing ipv6: false (proxy nodes are IPv4-only)")
		return out
	}
	return lines
}

func ensureTUN(lines []string) []string {
	logf("tun: bypass-router mode, forcing TUN on (stack system, manual routing, no auto-route)")
	return replaceTopBlock(lines, "tun", tunBlock)
}

// snifferBlock forces domain sniffing with override-destination on, so a
// bare-IP connection (no hostname) is re-targeted by its sniffed domain
// before it reaches the proxy node. The phone's DoH (Tencent dns.qq.com)
// bypasses Mihomo's DNS hijack and returns Google's real IPv6, so the
// phone connects to a bare IPv6 (e.g. 2607:f8b0:...) instead of a domain;
// proxy nodes fail on bare-IP targets but work on domains, so without
// this override the TLS handshake stalls (phone YouTube stuck at 854
// bytes, ERR_CONNECTION_CLOSED). With it on, IPv6 works fine through
// the proxy. This MUST live in the generator (not just config.yaml):
// a config rewrite (e.g. 2026-11-26 20:32) wipes a hand-edited
// config.yaml and the override is lost, breaking YouTube again.
var snifferBlock = []string{
	"sniffer:",
	"  enable: true",
	"  override-destination: true",
	"  sniff:",
	"    QUIC: { ports: [443], override-destination: true }",
	"    TLS: { ports: [443, 8443], override-destination: true }",
	"    HTTP: { ports: [80, 8080-8880], override-destination: true }",
}

func ensureSnifferOverride(lines []string) []string {
	logf("sniffer: forcing override-destination on (bare-IP -> sniffed domain, nodes need domains)")
	return replaceTopBlock(lines, "sniffer", snifferBlock)
}

// generateRunningConfig builds $DATA/config.yaml from the active profile +
// mixin.yaml + mixin.json. On failure the old config.yaml is kept.
func generateRunningConfig() error {
	ref := readActiveProfileRef()
	if ref == "" {
		ref = loadAppSettings().Profile
	}
	_, profileText, err := resolveProfile(ref)
	if err != nil {
		return err
	}
	mixin := loadMixin()
	mixinYAML := loadMixinFile()
	merged := applyMixin(profileText, mixin, mixinYAML)
	merged = strings.Join(ensureEssentialKeys(splitLines(merged)), "\n") + "\n"
	// Anti-leak: a profile without any dns: block would make Mihomo use the
	// system DNS directly, leaking every visited domain to the ISP. Fill
	// in the safe default (fake-ip + DoH via proxy) when missing.
	merged = strings.Join(ensureSafeDNS(splitLines(merged)), "\n") + "\n"
	// IPv4-only DNS: proxy nodes cannot do IPv6 outbound, so any AAAA
	// answer stalls the phone on YouTube/Google. Force ipv6: false in
	// the dns: block (profile's own block or the safe default above).
	merged = strings.Join(forceDNSIPv6False(splitLines(merged)), "\n") + "\n"
	// Bypass router: force TUN on so the NAS captures gateway traffic.
	// Devices point their gateway at the NAS and Mihomo splits it by rule.
	merged = strings.Join(ensureTUN(splitLines(merged)), "\n") + "\n"
	// Bare-IP fix: force sniffer override-destination on (see
	// ensureSnifferOverride). Without it a phone on DoH that resolves
	// Google to a bare IPv6 stalls, because nodes fail on bare-IP
	// targets. This step keeps the override in every generated config,
	// even after a rewrite wipes a hand-edited config.yaml.
	merged = strings.Join(ensureSnifferOverride(splitLines(merged)), "\n") + "\n"
	// mixed-port 强制锁定 7890：订阅配置各自写的 mixed-port 不一样
	// （有的 7890 有的 7893），端口一变，Docker 里 euserv 等指着
	// 127.0.0.1:7890 的服务就断了。锁死 7890，代理地址永远不变。
	// redir-port 7891（Nikki 做法）：网关设备的 TCP 不走 TUN，改由
	// iptables REDIRECT 进这个端口。REDIRECT 是内核层转发，不经过 TUN
	// 的用户态栈，可靠得多。UDP（QUIC、DNS）仍走 TUN。
	// （旧注“手机谷歌连接只下 854 字节就停”已证实与 TUN 无关：那是
	// sniffer override 丢了 + DoH 裸 IPv6 的问题，见 ensureSnifferOverride。）
	merged = strings.Join(setTopKeys(splitLines(merged), []KV{{"mixed-port", "7890"}, {"redir-port", "7891"}}), "\n") + "\n"
	// mixed-port 和 port / socks-port 不能同时占同一个端口：订阅配置常把
	// 三者都写成 7890，Mihomo 启动时 mixed 服务就会报
	// "bind: address already in use" 起不来。mixed 一个口同时管 HTTP 和
	// SOCKS，所以有 mixed-port 时直接去掉 port 和 socks-port。
	lines := splitLines(merged)
	if topBlockKeys(lines)["mixed-port"] {
		merged = strings.Join(removeTopKeys(lines, "port", "socks-port"), "\n") + "\n"
	}
	// No password at all: the controller secret is always forced to empty.
	// The user reaches the console directly from the LAN, so the live
	// panel (Zashboard) opens with no password too. A subscription profile
	// can carry its own secret the user never sees; forcing it to empty
	// here stops that hidden secret from locking the panel out with
	// "Unauthorized".
	// The live panel is always served from the bundled Zashboard at
	// ui/ange: force external-ui so a profile cannot point it elsewhere
	// and break the panel with a 404.
	// The controller must listen on all interfaces (0.0.0.0): a profile
	// that sets 127.0.0.1:9090 makes the panel reachable only from the
	// NAS itself, so the browser on the LAN gets ERR_CONNECTION_REFUSED.
	// IPv6 anti-leak: ipv6 is forced ON, not off. With ipv6: false Mihomo
	// never touches IPv6, so any app that resolves an AAAA record on its
	// own connects straight out over the home IPv6 and leaks the real
	// address. With ipv6: true, AAAA answers and IPv6 connections go
	// through Mihomo and out the proxy node like IPv4 does. Nodes without
	// IPv6 just fall back to IPv4 (Happy Eyeballs), so this is safe even
	// on an IPv4-only line.
	merged = strings.Join(setTopKeys(splitLines(merged), []KV{{"secret", `""`}, {"external-ui", "ui/ange"}, {"external-controller", "0.0.0.0:9090"}, {"ipv6", "true"}}), "\n") + "\n"
	return writeConfigText(merged)
}

// ensureDefaultProfile creates profiles/default.yaml from the built-in
// default config on first run, and migrates a legacy config.yaml.
func ensureDefaultProfile() {
	_ = os.MkdirAll(profilesDir, 0755)
	if readActiveProfileRef() != "" {
		return
	}
	entries, _ := os.ReadDir(profilesDir)
	hasProfiles := false
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			hasProfiles = true
			break
		}
	}
	if !hasProfiles {
		// migrate legacy running config if present, else use built-in default
		text := ""
		if b, err := os.ReadFile(configPath); err == nil && len(b) > 0 {
			text = string(b)
		} else if b, err := os.ReadFile(defaultConfigPath); err == nil {
			text = string(b)
		}
		if text == "" {
			text = defaultProfileYAML
		}
		_ = os.WriteFile(profileFilePath("default"), []byte(text), 0644)
	}
	_ = writeActiveProfileRef("file:default")
	if err := generateRunningConfig(); err != nil {
		logf("initial config generation failed: %v", err)
	}
}

const defaultProfileYAML = `mixed-port: 7890
allow-lan: true
bind-address: "*"
mode: rule
log-level: info
ipv6: false
external-controller: 0.0.0.0:9090
external-ui: ui/ange
secret: ""
proxies: []
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`

// ---------------------------------------------------------------- cron



// cronMatch reports whether the 5-field cron expression matches time t.
// Supports *, numbers, lists, ranges and steps.
func cronMatch(expr string, t time.Time) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	vals := []int{t.Minute(), t.Hour(), t.Day(), int(t.Month()), int(t.Weekday())}
	mins := []int{0, 0, 1, 1, 0}
	maxs := []int{59, 23, 31, 12, 6}
	for i, f := range fields {
		if !cronFieldMatch(f, vals[i], mins[i], maxs[i]) {
			return false
		}
	}
	return true
}

func cronFieldMatch(field string, val, min, max int) bool {
	if field == "*" {
		return true
	}
	step := 1
	base := field
	if i := strings.Index(field, "/"); i >= 0 {
		base = field[:i]
		n, err := strconv.Atoi(field[i+1:])
		if err != nil || n <= 0 {
			return false
		}
		step = n
	}
	var ranges [][2]int
	if base == "*" || base == "" {
		ranges = append(ranges, [2]int{min, max})
	} else {
		for _, part := range strings.Split(base, ",") {
			if strings.Contains(part, "-") {
				se := strings.SplitN(part, "-", 2)
				lo, err1 := strconv.Atoi(se[0])
				hi, err2 := strconv.Atoi(se[1])
				if err1 != nil || err2 != nil {
					return false
				}
				ranges = append(ranges, [2]int{lo, hi})
			} else {
				n, err := strconv.Atoi(part)
				if err != nil {
					return false
				}
				ranges = append(ranges, [2]int{n, n})
			}
		}
	}
	for _, r := range ranges {
		for v := r[0]; v <= r[1]; v += step {
			if v == val {
				return true
			}
		}
	}
	return false
}

// subscriptionAutoUpdateLoop checks every minute whether a subscription
// with interval_hours set is due for an update (last update + interval
// has passed), and updates it. If the updated subscription is the active
// profile, Mihomo is restarted with the new config.
// subLastTry records the last attempt time of failed subscription
// auto-updates (in-memory only). Without it a subscription that never
// succeeded (Updated empty) is due on every 60s tick and retries forever,
// flooding the log — seen 2026-11-24 with eooce (dead link, HTTP 403).
var subLastTry = map[string]time.Time{}

func subscriptionAutoUpdateLoop() {
	for {
		time.Sleep(60 * time.Second)
		mu.Lock()
		subs := loadSubscriptions()
		now := time.Now()
		changed := false
		needRestart := false
		for i := range subs {
			if subs[i].IntervalHours <= 0 {
				continue
			}
			// Updated 存的是本地时间（北京时间）；time.Parse 会把它当 UTC，
			// 导致自动更新晚 8 小时才触发。按本地时区解析才对。
			// Updated 为空或解析失败（从没成功更新过）：当作已到期，立刻更新
			// 一次，而不是永远跳过——否则像 eooce 这样一次没成功过的订阅
			// 永远不会自动更新。
			last, err := time.ParseInLocation("2006-01-02 15:04:05", subs[i].Updated, time.Local)
			if err != nil {
				last = time.Time{}
			}
			interval := time.Duration(subs[i].IntervalHours * float64(time.Hour))
			if interval <= 0 {
				continue
			}
			// 跟着北京时间走：下一次更新定在下一个整点/半点边界（1h→整点，
			// 0.5h→整点或半点），而不是“上次更新+间隔”——后者会锚定在添加
			// 订阅的那一刻，变成 :29、:58 这种时间。Truncate 按本地时区截断，
			// NAS 是北京时间所以边界就是北京时间的整点/半点。
			next := last.Truncate(interval).Add(interval)
			if now.Before(next) {
				continue
			}
			// 失败退避：上次尝试失败后至少隔 30 分钟再试，否则从没成功过
			// 的订阅会每分钟重试一次，刷屏日志。
			if t, ok := subLastTry[subs[i].Name]; ok && now.Before(t.Add(30*time.Minute)) {
				continue
			}
			logf("auto-updating subscription %q (interval %gh)", subs[i].Name, subs[i].IntervalHours)
			if _, err := updateSubscriptionFile(&subs[i]); err != nil {
				logf("auto-update subscription %q failed: %v", subs[i].Name, err)
				subLastTry[subs[i].Name] = now
				continue
			}
			delete(subLastTry, subs[i].Name)
			changed = true
			if readActiveProfileRef() == "subscription:"+sanitizeProfileName(subs[i].Name) {
				needRestart = true
			}
		}
		if changed {
			_ = saveSubscriptions(subs)
			if needRestart {
				if err := restartAfterChange(); err != nil {
					logf("restart after auto-update failed: %v", err)
				}
			}
		}
		mu.Unlock()
	}
}

// scheduledRestartLoop checks every 30s whether a scheduled restart is due.
func scheduledRestartLoop() {
	var lastFired string
	for {
		time.Sleep(30 * time.Second)
		s := loadAppSettings()
		if !s.ScheduledRestart {
			continue
		}
		now := time.Now()
		key := now.Format("2006-01-02 15:04")
		if key == lastFired {
			continue
		}
		if cronMatch(s.ScheduledCron, now) {
			lastFired = key
			logf("scheduled restart (cron %q)", s.ScheduledCron)
			_, _ = serviceRestart()
		}
	}
}
