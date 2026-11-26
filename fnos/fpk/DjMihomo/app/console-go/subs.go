// Subscription fetching, link parsing (vless/vmess/trojan/ss) and merging
// into the config.yaml managed section. Faithful port of backend.py.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Sub struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	UserAgent     string `json:"user_agent"`
	Updated       string `json:"updated"`
	IntervalHours float64 `json:"interval_hours"`
	UseProxy      bool   `json:"use_proxy"`
}

func loadSubscriptions() []Sub {
	data, err := os.ReadFile(subsPath)
	if err != nil {
		return nil
	}
	var subs []Sub
	if err := json.Unmarshal(data, &subs); err != nil {
		return nil
	}
	if subs == nil {
		return []Sub{}
	}
	return subs
}

func saveSubscriptions(subs []Sub) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(subs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(subsPath, b, 0644)
}

func publicSubs(subs []Sub) []Sub {
	if subs == nil {
		return []Sub{}
	}
	return subs
}

// buildProfileYAML converts a parsed node list (link subscription) into a
// minimal Mihomo profile YAML.
func buildProfileYAML(nodes []Obj) string {
	var lines []string
	if len(nodes) == 0 {
		lines = append(lines, "proxies: []")
	} else {
		lines = append(lines, "proxies:")
		items := make([]interface{}, len(nodes))
		for i, n := range nodes {
			items[i] = n
		}
		lines = append(lines, emitYAML(items, 0)...)
	}
	lines = append(lines, "proxy-groups:")
	lines = append(lines, "  - name: PROXY")
	lines = append(lines, "    type: select")
	lines = append(lines, "    proxies:")
	lines = append(lines, "      - DIRECT")
	for _, n := range nodes {
		lines = append(lines, "      - "+yamlStr(n.getString("name")))
	}
	lines = append(lines, "rules:")
	lines = append(lines, "  - MATCH,PROXY")
	return strings.Join(lines, "\n") + "\n"
}

// subscriptionToProfile converts fetched subscription content into profile
// YAML text. Raw YAML is kept as-is; a link list (or base64 blob of links)
// is converted into a minimal profile. Returns the text and node count
// (0 for raw YAML).
func subscriptionToProfile(text string) (string, int) {
	nodes := parseSubscriptionText(text)
	if len(nodes) > 0 {
		return buildProfileYAML(nodes), len(nodes)
	}
	return text, 0
}

// ---------------------------------------------------------------- fetch

func fetchURL(rawurl string, timeout time.Duration) (string, error) {
	return fetchURLWithUA(rawurl, "Mozilla/5.0 (compatible; DjMihomo/1.0)", timeout)
}

func fetchURLWithUA(rawurl, ua string, timeout time.Duration) (string, error) {
	return fetchURLWithUAProxy(rawurl, ua, timeout, false)
}

// mixedProxyAddr returns the local Mihomo mixed proxy (127.0.0.1:port),
// reading the effective mixed-port from the running config (default 7890).
func mixedProxyAddr() string {
	port := 7890
	if text, err := readConfigTextE(); err == nil {
		if cfg := getTopValues(splitLines(text), settingKeys); cfg != nil {
			if v, ok := cfg["mixed-port"]; ok {
				if i, ok := v.(int); ok && i > 0 {
					port = i
				}
			}
		}
	}
	return "127.0.0.1:" + strconv.Itoa(port)
}

// fetchURLWithUAProxy fetches a URL, optionally through the local Mihomo
// mixed proxy (useProxy=true; Mihomo must be running). When the proxy fetch
// fails (e.g. Mihomo is not running, so the proxy connection is refused),
// it falls back to a direct fetch, so the subscription can still update
// when the URL is reachable without the proxy. A non-200 status or an
// empty body is an error, so a dead subscription link is reported instead
// of silently saving an empty profile.
func fetchURLWithUAProxy(rawurl, ua string, timeout time.Duration, useProxy bool) (string, error) {
	if strings.HasPrefix(rawurl, "file://") {
		p := strings.TrimPrefix(rawurl, "file://")
		if u, err := url.PathUnescape(p); err == nil {
			p = u
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	// NB: keep rt a nil interface (not a nil *http.Transport) when not
	// using a proxy, or http.Client panics in alternateRoundTripper.
	var rt http.RoundTripper
	if useProxy {
		proxyURL, err := url.Parse("http://" + mixedProxyAddr())
		if err != nil {
			return "", err
		}
		rt = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	}
	client := &http.Client{Timeout: timeout, Transport: rt}
	doGet := func(c *http.Client) (*http.Response, error) {
		req, err := http.NewRequest("GET", rawurl, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", ua)
		return c.Do(req)
	}
	// 拉取一次：502/503/504/429 这类临时错误重试 2 次（间隔 3 秒），
	// 订阅站偶尔抽风返回 503，重试一次往往就好了。
	tryFetch := func(c *http.Client) (string, error) {
		var resp *http.Response
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(3 * time.Second)
			}
			resp, err = doGet(c)
			if err != nil {
				return "", err
			}
			if resp.StatusCode == http.StatusOK {
				break
			}
			if resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 || resp.StatusCode == 429 {
				resp.Body.Close()
				if attempt < 2 {
					logf("订阅地址返回 HTTP %d，第 %d 次重试", resp.StatusCode, attempt+1)
					continue
				}
			}
			break
		}
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("订阅地址返回 HTTP %d，链接可能已失效", resp.StatusCode)
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		if len(strings.TrimSpace(string(b))) == 0 {
			return "", fmt.Errorf("订阅内容为空，链接可能已失效")
		}
		return string(b), nil
	}
	if !useProxy {
		return tryFetch(client)
	}
	// 走代理先试一次；不通就自动改直连再试一次。代理不通多半是当前选的
	// 节点不对（出口 IP 被订阅站拉黑、节点不通），直连往往能通。
	body, err := tryFetch(client)
	if err == nil {
		return body, nil
	}
	logf("经代理拉取订阅失败（%v），自动改直连再试一次", err)
	direct := &http.Client{Timeout: timeout}
	body, derr := tryFetch(direct)
	if derr == nil {
		logf("直连拉取订阅成功（代理方式失败，已自动切换）")
		return body, nil
	}
	return "", fmt.Errorf("经代理拉取失败（%v），直连拉取也失败：%v", err, derr)
}

// fetchErrAdvice 把一次订阅拉取失败翻成中文原因 + 接下来该怎么做，写进
// 日志里，免得用户对着一句英文报错不知道该点哪里。
// directGroupName 找出当前运行配置里能切到 DIRECT 的代理组（策略），
// 优先 GLOBAL，没有就按名字排序取第一个。核心没跑或没有这种组就返回 ""。
// 提示里不能写死某个节点名（比如 漏网之鱼）：它只属于某个配置文件，
// 别的配置里根本没有，照着提示找会找不到。
func directGroupName() string {
	b, code, err := mihomoAPI("GET", "/proxies", nil)
	if err != nil || code != 200 {
		return ""
	}
	return findDirectGroup(b)
}

// findDirectGroup 从 /proxies 的返回里找出能切到 DIRECT 的组。
func findDirectGroup(b []byte) string {
	var all struct {
		Proxies map[string]struct {
			All []string `json:"all"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal(b, &all); err != nil {
		return ""
	}
	var names []string
	for name, g := range all.Proxies {
		for _, n := range g.All {
			if n == "DIRECT" {
				names = append(names, name)
				break
			}
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	for _, n := range names {
		if n == "GLOBAL" {
			return n
		}
	}
	return names[0]
}

// directSwitchAdvice 返回一条“切到直连再试”的提示。能检测到具体策略组
// 就点名，没检测到就只说换个能用的节点，不写死节点名。
// 用于订阅更新：拉订阅只要能拉到就行，走直连也行。
func directSwitchAdvice() string {
	if g := directGroupName(); g != "" {
		return "先在【代理】里把「" + g + "」切到 DIRECT 再点更新"
	}
	return "先在【代理】里换一个能用的节点再点更新"
}

// switchNodeAdvice 返回一条“换个节点再测”的提示，点名具体策略组。
// 用于连通性测试：目的是测代理通不通，换节点（换出口 IP）才有意义；
// 切到 DIRECT 等于绕过代理，测的就不是代理了，所以不能复用上面那个。
func switchNodeAdvice() string {
	if g := directGroupName(); g != "" {
		return "先在【代理】里把「" + g + "」换一个节点再测"
	}
	return "先在【代理】里换一个节点再测"
}

func fetchErrAdvice(err error, useProxy bool) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "proxyconnect"):
		if useProxy {
			return "原因：走代理拉取失败，自动改直连也连不上订阅地址。建议：先确认 Mihomo 核心在运行；再检查订阅链接对不对，或等一会儿再试。"
		}
		return "原因：连不上订阅地址（connection refused）。建议：检查订阅链接对不对，或等一会儿再试。"
	case strings.Contains(msg, "HTTP 403"):
		return "原因：订阅站拒绝了请求（403），代理和直连两种方式都被拒了。建议：先" + directSwitchAdvice() + "，换个出口 IP 再点更新；如果还是 403，链接可能已过期，去频道拿一条新链接替换掉。"
	case strings.Contains(msg, "HTTP 404"):
		return "原因：订阅地址返回 404，链接已失效。建议：去频道拿一条新链接，把旧的替换掉再更新。"
	case strings.Contains(msg, "HTTP 503") || strings.Contains(msg, "HTTP 502") || strings.Contains(msg, "HTTP 504"):
		return "原因：订阅站暂时不可用（对方服务器抽风），已自动重试过。建议：等几分钟再点更新，不用换链接。"
	case strings.Contains(msg, "HTTP 429"):
		return "原因：请求太频繁被订阅站限流（429）。建议：等一会儿再点更新，别连续点。"
	case strings.Contains(msg, "EOF") || strings.Contains(msg, "connection reset"):
		return "原因：拉取中途连接被断开，可能是节点不稳。建议：" + directSwitchAdvice() + "；不行就等一会儿重试。"
	case strings.Contains(msg, "订阅内容为空"):
		return "原因：拉到了空内容，链接可能已失效。建议：去频道确认链接有没有换。"
	default:
		if useProxy {
			return "建议：先确认 Mihomo 核心在运行；不行就把这个订阅的【通过 VPN 更新订阅】关掉改直连，再点更新。"
		}
		return "建议：检查订阅链接对不对，或等一会儿再点更新。"
	}
}

// ---------------------------------------------------------------- base64

var wsRe = regexp.MustCompile(`\s+`)

func b64decode(s string) ([]byte, error) {
	s = wsRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return base64.StdEncoding.DecodeString(s)
}

// ---------------------------------------------------------------- hostport

func splitHostPort(netloc string) (userinfo, host string, port int) {
	userinfo, hp := "", netloc
	if i := strings.LastIndex(netloc, "@"); i >= 0 {
		userinfo, hp = netloc[:i], netloc[i+1:]
	}
	if strings.HasPrefix(hp, "[") {
		if j := strings.Index(hp, "]"); j >= 0 {
			h := hp[1:j]
			rest := hp[j+1:]
			if rest == "" {
				return userinfo, h, 443
			}
			if strings.HasPrefix(rest, ":") {
				if p, err := strconv.Atoi(rest[1:]); err == nil {
					return userinfo, h, p
				}
			}
			return userinfo, hp, 443
		}
		return userinfo, hp, 443
	}
	if i := strings.LastIndex(hp, ":"); i >= 0 {
		if p, err := strconv.Atoi(hp[i+1:]); err == nil {
			return userinfo, hp[:i], p
		}
		return userinfo, hp, 443
	}
	return userinfo, hp, 443
}

// rawNetloc rebuilds scheme://userinfo@host:port the way Python's
// urlparse().netloc does, so splitHostPort behaves identically.
func rawNetloc(u *url.URL) string {
	if u.User != nil {
		return u.User.String() + "@" + u.Host
	}
	return u.Host
}

func qp(q url.Values, k, d string) string {
	if v := q.Get(k); v != "" {
		return v
	}
	return d
}

func unq(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

// ---------------------------------------------------------------- link parsers

func parseVless(link string) Obj {
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}
	userinfo, host, port := splitHostPort(rawNetloc(u))
	if host == "" || userinfo == "" {
		return nil
	}
	q := u.Query()
	name := unq(u.EscapedFragment())
	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}
	node := Obj{
		{"type", "vless"},
		{"name", name},
		{"server", host},
		{"port", port},
		{"uuid", unq(userinfo)},
		{"udp", true},
	}
	net := qp(q, "type", "tcp")
	node = append(node, KV{"network", net})
	sec := qp(q, "security", "")
	if sec == "tls" {
		node = append(node, KV{"tls", true})
		if sni := qp(q, "sni", ""); sni != "" {
			node = append(node, KV{"servername", sni})
		}
	} else if sec == "reality" {
		node = append(node, KV{"tls", true})
		node = append(node, KV{"reality-opts", Obj{
			{"public-key", qp(q, "pb", "")},
			{"short-id", qp(q, "sid", "")},
		}})
		if sni := qp(q, "sni", ""); sni != "" {
			node = append(node, KV{"servername", sni})
		}
	}
	if flow := qp(q, "flow", ""); flow != "" {
		node = append(node, KV{"flow", flow})
	}
	if net == "ws" {
		path := unq(qp(q, "path", "/"))
		if path == "" {
			path = "/"
		}
		opts := Obj{{"path", path}}
		if h := qp(q, "host", ""); h != "" {
			opts = append(opts, KV{"headers", Obj{{"Host", h}}})
		}
		node = append(node, KV{"ws-opts", opts})
	} else if net == "grpc" {
		if sn := qp(q, "serviceName", ""); sn != "" {
			node = append(node, KV{"grpc-opts", Obj{{"grpc-service-name", sn}}})
		}
	}
	return node
}

func jsonInt(m map[string]interface{}, key string, def int) int {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return int(f)
		}
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return int(i)
		}
	}
	return def
}

func jsonStr(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func parseVmess(link string) Obj {
	rest := link[len("vmess://"):]
	if i := strings.Index(rest, "#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.Index(rest, "?"); i >= 0 {
		rest = rest[:i]
	}
	raw, err := b64decode(rest)
	if err != nil {
		return nil
	}
	var d map[string]interface{}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil
	}
	host := jsonStr(d, "add")
	port := jsonInt(d, "port", 0)
	if host == "" || port == 0 {
		return nil
	}
	name := jsonStr(d, "ps")
	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}
	node := Obj{
		{"type", "vmess"},
		{"name", name},
		{"server", host},
		{"port", port},
		{"uuid", jsonStr(d, "id")},
		{"alterId", jsonInt(d, "aid", 0)},
		{"cipher", "auto"},
		{"udp", true},
	}
	net := jsonStr(d, "net")
	if net == "" {
		net = "tcp"
	}
	node = append(node, KV{"network", net})
	if jsonStr(d, "tls") == "tls" {
		node = append(node, KV{"tls", true})
		if sni := jsonStr(d, "sni"); sni != "" {
			node = append(node, KV{"servername", sni})
		}
	}
	if net == "ws" {
		path := jsonStr(d, "path")
		if path == "" {
			path = "/"
		}
		opts := Obj{{"path", path}}
		if h := jsonStr(d, "host"); h != "" {
			opts = append(opts, KV{"headers", Obj{{"Host", h}}})
		}
		node = append(node, KV{"ws-opts", opts})
	} else if net == "grpc" {
		if p := jsonStr(d, "path"); p != "" {
			node = append(node, KV{"grpc-opts", Obj{{"grpc-service-name", p}}})
		}
	}
	return node
}

func parseTrojan(link string) Obj {
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}
	userinfo, host, port := splitHostPort(rawNetloc(u))
	if host == "" || userinfo == "" {
		return nil
	}
	q := u.Query()
	name := unq(u.EscapedFragment())
	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}
	node := Obj{
		{"type", "trojan"},
		{"name", name},
		{"server", host},
		{"port", port},
		{"password", unq(userinfo)},
		{"udp", true},
	}
	net := qp(q, "type", "tcp")
	node = append(node, KV{"network", net})
	if sni := qp(q, "sni", ""); sni != "" {
		node = append(node, KV{"sni", sni})
	}
	if qp(q, "security", "tls") == "tls" {
		node = append(node, KV{"tls", true})
	}
	if net == "ws" {
		path := unq(qp(q, "path", "/"))
		if path == "" {
			path = "/"
		}
		opts := Obj{{"path", path}}
		if h := qp(q, "host", ""); h != "" {
			opts = append(opts, KV{"headers", Obj{{"Host", h}}})
		}
		node = append(node, KV{"ws-opts", opts})
	}
	return node
}

func parseSS(link string) Obj {
	rest := link[len("ss://"):]
	name := ""
	if i := strings.Index(rest, "#"); i >= 0 {
		name = unq(rest[i+1:])
		rest = rest[:i]
	}
	var userinfo, hp string
	if strings.Contains(rest, "@") {
		i := strings.LastIndex(rest, "@")
		userinfo, hp = rest[:i], rest[i+1:]
		if !strings.Contains(userinfo, ":") {
			dec, err := b64decode(userinfo)
			if err != nil {
				return nil
			}
			userinfo = string(dec)
		}
		if !strings.Contains(userinfo, ":") {
			return nil
		}
	} else {
		dec, err := b64decode(rest)
		if err != nil {
			return nil
		}
		ds := string(dec)
		if !strings.Contains(ds, "@") {
			return nil
		}
		i := strings.LastIndex(ds, "@")
		userinfo, hp = ds[:i], ds[i+1:]
		if !strings.Contains(userinfo, ":") {
			return nil
		}
	}
	ci := strings.Index(userinfo, ":")
	method, password := userinfo[:ci], userinfo[ci+1:]
	_, host, port := splitHostPort(hp)
	if host == "" || port == 0 {
		return nil
	}
	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}
	return Obj{
		{"type", "ss"},
		{"name", name},
		{"server", host},
		{"port", port},
		{"cipher", unq(method)},
		{"password", unq(password)},
		{"udp", true},
	}
}

// parseHysteria2 handles hysteria2:// and hy2:// links:
// hysteria2://password@host:port?insecure=1&sni=x&obfs=salamander&obfs-password=y&alpn=h3#name
func parseHysteria2(link string) Obj {
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}
	userinfo, host, port := splitHostPort(rawNetloc(u))
	if host == "" || userinfo == "" {
		return nil
	}
	q := u.Query()
	name := unq(u.EscapedFragment())
	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}
	node := Obj{
		{"type", "hysteria2"},
		{"name", name},
		{"server", host},
		{"port", port},
		{"password", unq(userinfo)},
		{"udp", true},
	}
	if sni := qp(q, "sni", ""); sni != "" {
		node = append(node, KV{"sni", sni})
	}
	if v := qp(q, "insecure", ""); v == "1" || strings.EqualFold(v, "true") {
		node = append(node, KV{"skip-cert-verify", true})
	}
	if obfs := qp(q, "obfs", ""); obfs != "" {
		node = append(node, KV{"obfs", obfs})
	}
	if p := qp(q, "obfs-password", ""); p != "" {
		node = append(node, KV{"obfs-password", p})
	}
	if alpn := qp(q, "alpn", ""); alpn != "" {
		node = append(node, KV{"alpn", strings.Split(alpn, ",")})
	}
	return node
}

// parseHysteria handles hysteria:// (v1) links:
// hysteria://host:port?auth=pwd&protocol=udp&up=100&down=100&alpn=h3&insecure=1&sni=x#name
func parseHysteria(link string) Obj {
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}
	userinfo, host, port := splitHostPort(rawNetloc(u))
	if host == "" {
		return nil
	}
	q := u.Query()
	name := unq(u.EscapedFragment())
	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}
	auth := unq(userinfo)
	if auth == "" {
		auth = qp(q, "auth", "")
	}
	if auth == "" {
		auth = qp(q, "authstr", "")
	}
	node := Obj{
		{"type", "hysteria"},
		{"name", name},
		{"server", host},
		{"port", port},
		{"auth-str", auth},
		{"udp", true},
	}
	if proto := qp(q, "protocol", ""); proto != "" {
		node = append(node, KV{"protocol", proto})
	}
	if up := qp(q, "up", ""); up != "" {
		if i, err := strconv.Atoi(up); err == nil {
			node = append(node, KV{"up", i})
		}
	}
	if down := qp(q, "down", ""); down != "" {
		if i, err := strconv.Atoi(down); err == nil {
			node = append(node, KV{"down", i})
		}
	}
	if alpn := qp(q, "alpn", ""); alpn != "" {
		node = append(node, KV{"alpn", strings.Split(alpn, ",")})
	}
	if sni := qp(q, "sni", ""); sni != "" {
		node = append(node, KV{"sni", sni})
	}
	if v := qp(q, "insecure", ""); v == "1" || strings.EqualFold(v, "true") {
		node = append(node, KV{"skip-cert-verify", true})
	}
	return node
}

// parseTuic handles tuic:// links:
// tuic://uuid:password@host:port?congestion_control=bbr&alpn=h3&sni=x&udp_relay_mode=native#name
func parseTuic(link string) Obj {
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}
	userinfo, host, port := splitHostPort(rawNetloc(u))
	if host == "" || userinfo == "" {
		return nil
	}
	q := u.Query()
	name := unq(u.EscapedFragment())
	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}
	uuid, password := userinfo, ""
	if i := strings.Index(userinfo, ":"); i >= 0 {
		uuid, password = userinfo[:i], userinfo[i+1:]
	}
	node := Obj{
		{"type", "tuic"},
		{"name", name},
		{"server", host},
		{"port", port},
		{"uuid", unq(uuid)},
		{"password", unq(password)},
		{"udp", true},
	}
	if cc := qp(q, "congestion_control", ""); cc != "" {
		node = append(node, KV{"congestion-controller", cc})
	}
	if alpn := qp(q, "alpn", ""); alpn != "" {
		node = append(node, KV{"alpn", strings.Split(alpn, ",")})
	}
	if sni := qp(q, "sni", ""); sni != "" {
		node = append(node, KV{"sni", sni})
	}
	if m := qp(q, "udp_relay_mode", ""); m != "" {
		node = append(node, KV{"udp-relay-mode", m})
	}
	if v := qp(q, "allow_insecure", ""); v == "1" || strings.EqualFold(v, "true") {
		node = append(node, KV{"skip-cert-verify", true})
	}
	return node
}

// parseSSR handles ssr:// links: base64(host:port:protocol:method:obfs:password_b64/?params_b64)
func parseSSR(link string) Obj {
	raw := strings.TrimPrefix(link, "ssr://")
	raw = strings.TrimSpace(raw)
	dec, err := b64decode(raw)
	if err != nil || len(dec) == 0 {
		return nil
	}
	s := string(dec)
	// split params
	main := s
	params := ""
	if i := strings.Index(s, "/?"); i >= 0 {
		main, params = s[:i], s[i+2:]
	}
	parts := strings.Split(main, ":")
	if len(parts) != 6 {
		return nil
	}
	host, portStr, protocol, method, obfs, passB64 := parts[0], parts[1], parts[2], parts[3], parts[4], parts[5]
	port, err := strconv.Atoi(portStr)
	if err != nil || host == "" {
		return nil
	}
	pw, err := b64decode(passB64)
	if err != nil {
		return nil
	}
	name := fmt.Sprintf("%s:%d", host, port)
	node := Obj{
		{"type", "ssr"},
		{"name", name},
		{"server", host},
		{"port", port},
		{"password", string(pw)},
		{"cipher", method},
		{"protocol", protocol},
		{"obfs", obfs},
		{"udp", true},
	}
	if params != "" {
		q, _ := url.ParseQuery(params)
		if v := q.Get("remarks"); v != "" {
			if d, err := b64decode(v); err == nil && len(d) > 0 {
				node[1].Val = string(d)
			}
		}
		if v := q.Get("obfsparam"); v != "" {
			if d, err := b64decode(v); err == nil {
				node = append(node, KV{"obfs-param", string(d)})
			}
		}
		if v := q.Get("protoparam"); v != "" {
			if d, err := b64decode(v); err == nil {
				node = append(node, KV{"protocol-param", string(d)})
			}
		}
	}
	return node
}

// parseSocks5 handles socks5://user:pass@host:port#name and the
// base64-userinfo form socks5://base64(user:pass@host:port)#name
func parseSocks5(link string) Obj {
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}
	netloc := rawNetloc(u)
	userinfo, host, port := splitHostPort(netloc)
	if host == "" {
		// try base64-encoded userinfo@host:port
		if dec, err := b64decode(netloc); err == nil {
			if u2, err2 := url.Parse("socks5://" + string(dec)); err2 == nil {
				userinfo, host, port = splitHostPort(rawNetloc(u2))
			}
		}
	}
	if host == "" {
		return nil
	}
	q := u.Query()
	name := unq(u.EscapedFragment())
	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}
	user, pass := "", ""
	if i := strings.Index(userinfo, ":"); i >= 0 {
		user, pass = userinfo[:i], userinfo[i+1:]
	} else {
		user = userinfo
	}
	node := Obj{
		{"type", "socks5"},
		{"name", name},
		{"server", host},
		{"port", port},
		{"udp", true},
	}
	if user != "" {
		node = append(node, KV{"username", unq(user)})
	}
	if pass != "" {
		node = append(node, KV{"password", unq(pass)})
	}
	if v := qp(q, "tls", ""); v == "1" || strings.EqualFold(v, "true") {
		node = append(node, KV{"tls", true})
	}
	return node
}

// parseWireguard handles wireguard:// links:
// wireguard://privatekey@host:port?publickey=x&address=10.0.0.2/32&allowedips=0.0.0.0/0&presharedkey=y&mtu=1280&keepalive=25#name
func parseWireguard(link string) Obj {
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}
	userinfo, host, port := splitHostPort(rawNetloc(u))
	if host == "" || userinfo == "" {
		return nil
	}
	q := u.Query()
	name := unq(u.EscapedFragment())
	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}
	node := Obj{
		{"type", "wireguard"},
		{"name", name},
		{"server", host},
		{"port", port},
		{"private-key", unq(userinfo)},
		{"udp", true},
	}
	if pk := qp(q, "publickey", ""); pk != "" {
		node = append(node, KV{"public-key", pk})
	}
	if psk := qp(q, "presharedkey", ""); psk != "" {
		node = append(node, KV{"pre-shared-key", psk})
	}
	if addr := qp(q, "address", ""); addr != "" {
		node = append(node, KV{"ip", addr})
	}
	if ips := qp(q, "allowedips", ""); ips != "" {
		node = append(node, KV{"allowed-ips", strings.Split(ips, ",")})
	}
	if mtu := qp(q, "mtu", ""); mtu != "" {
		if i, err := strconv.Atoi(mtu); err == nil {
			node = append(node, KV{"mtu", i})
		}
	}
	if ka := qp(q, "keepalive", ""); ka != "" {
		if i, err := strconv.Atoi(ka); err == nil {
			node = append(node, KV{"persistent-keepalive", i})
		}
	}
	return node
}

// --- sing-box JSON -------------------------------------------------------
// parseSingboxJSON converts a sing-box config ({"outbounds": [...]}) into
// Mihomo proxy nodes. Non-proxy outbounds (direct, block, dns, selector,
// urltest, ...) are skipped.
func parseSingboxJSON(text string) []Obj {
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(text), &cfg); err != nil {
		return nil
	}
	raw, ok := cfg["outbounds"].([]interface{})
	if !ok {
		return nil
	}
	var nodes []Obj
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if n := singboxOutbound(m); n != nil {
			nodes = append(nodes, n)
		}
	}
	return nodes
}

func sbStr(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func sbInt(m map[string]interface{}, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func sbMap(m map[string]interface{}, key string) map[string]interface{} {
	if v, ok := m[key].(map[string]interface{}); ok {
		return v
	}
	return nil
}

func sbBool(m map[string]interface{}, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

// sbTLS applies a sing-box tls object to a Mihomo node (servername /
// skip-cert-verify / reality-opts).
func sbTLS(node Obj, tls map[string]interface{}) Obj {
	if tls == nil || !sbBool(tls, "enabled") {
		return node
	}
	node = append(node, KV{"tls", true})
	if sni := sbStr(tls, "server_name"); sni != "" {
		node = append(node, KV{"servername", sni})
	}
	if sbBool(tls, "insecure") {
		node = append(node, KV{"skip-cert-verify", true})
	}
	if alpn, ok := tls["alpn"].([]interface{}); ok && len(alpn) > 0 {
		var list []string
		for _, a := range alpn {
			if s, ok := a.(string); ok {
				list = append(list, s)
			}
		}
		if len(list) > 0 {
			node = append(node, KV{"alpn", list})
		}
	}
	if reality := sbMap(tls, "reality"); reality != nil && sbBool(reality, "enabled") {
		node = append(node, KV{"reality-opts", Obj{
			{"public-key", sbStr(reality, "public_key")},
			{"short-id", sbStr(reality, "short_id")},
		}})
	}
	return node
}

// sbTransport applies a sing-box transport object (ws / grpc) to a node.
func sbTransport(node Obj, tr map[string]interface{}) Obj {
	if tr == nil {
		return node
	}
	switch sbStr(tr, "type") {
	case "ws":
		path := sbStr(tr, "path")
		if path == "" {
			path = "/"
		}
		opts := Obj{{"path", path}}
		if h := sbMap(tr, "headers"); h != nil {
			if host := sbStr(h, "Host"); host != "" {
				opts = append(opts, KV{"headers", Obj{{"Host", host}}})
			}
		}
		node = append(node, KV{"ws-opts", opts})
	case "grpc":
		if sn := sbStr(tr, "service_name"); sn != "" {
			node = append(node, KV{"grpc-opts", Obj{{"grpc-service-name", sn}}})
		}
	}
	return node
}

func singboxOutbound(m map[string]interface{}) Obj {
	typ := sbStr(m, "type")
	name := sbStr(m, "tag")
	server := sbStr(m, "server")
	port := sbInt(m, "server_port")
	if name == "" {
		name = fmt.Sprintf("%s:%d", server, port)
	}
	base := func(t string) Obj {
		return Obj{
			{"type", t},
			{"name", name},
			{"server", server},
			{"port", port},
			{"udp", true},
		}
	}
	switch typ {
	case "vless":
		node := base("vless")
		node = append(node, KV{"uuid", sbStr(m, "uuid")})
		if flow := sbStr(m, "flow"); flow != "" {
			node = append(node, KV{"flow", flow})
		}
		if net := sbStr(sbMap(m, "transport"), "type"); net != "" {
			node = append(node, KV{"network", net})
		}
		node = sbTLS(node, sbMap(m, "tls"))
		node = sbTransport(node, sbMap(m, "transport"))
		return node
	case "vmess":
		node := base("vmess")
		node = append(node, KV{"uuid", sbStr(m, "uuid")})
		if aid := sbInt(m, "alter_id"); aid != 0 {
			node = append(node, KV{"alterId", aid})
		}
		if cipher := sbStr(m, "security"); cipher != "" {
			node = append(node, KV{"cipher", cipher})
		}
		if net := sbStr(sbMap(m, "transport"), "type"); net != "" {
			node = append(node, KV{"network", net})
		}
		node = sbTLS(node, sbMap(m, "tls"))
		node = sbTransport(node, sbMap(m, "transport"))
		return node
	case "trojan":
		node := base("trojan")
		node = append(node, KV{"password", sbStr(m, "password")})
		if net := sbStr(sbMap(m, "transport"), "type"); net != "" {
			node = append(node, KV{"network", net})
		}
		node = sbTLS(node, sbMap(m, "tls"))
		node = sbTransport(node, sbMap(m, "transport"))
		return node
	case "shadowsocks":
		node := base("ss")
		node = append(node, KV{"cipher", sbStr(m, "method")})
		node = append(node, KV{"password", sbStr(m, "password")})
		return node
	case "hysteria2":
		node := base("hysteria2")
		node = append(node, KV{"password", sbStr(m, "password")})
		if obfs := sbMap(m, "obfs"); obfs != nil {
			if t := sbStr(obfs, "type"); t != "" {
				node = append(node, KV{"obfs", t})
			}
			if p := sbStr(obfs, "password"); p != "" {
				node = append(node, KV{"obfs-password", p})
			}
		}
		node = sbTLS(node, sbMap(m, "tls"))
		return node
	case "hysteria":
		node := base("hysteria")
		node = append(node, KV{"auth-str", sbStr(m, "auth_str")})
		if up := sbStr(m, "up_mbps"); up != "" {
			if i, err := strconv.Atoi(up); err == nil {
				node = append(node, KV{"up", i})
			}
		}
		if down := sbStr(m, "down_mbps"); down != "" {
			if i, err := strconv.Atoi(down); err == nil {
				node = append(node, KV{"down", i})
			}
		}
		node = sbTLS(node, sbMap(m, "tls"))
		return node
	case "tuic":
		node := base("tuic")
		node = append(node, KV{"uuid", sbStr(m, "uuid")})
		node = append(node, KV{"password", sbStr(m, "password")})
		if cc := sbStr(m, "congestion_control"); cc != "" {
			node = append(node, KV{"congestion-controller", cc})
		}
		if m2 := sbStr(m, "udp_relay_mode"); m2 != "" {
			node = append(node, KV{"udp-relay-mode", m2})
		}
		node = sbTLS(node, sbMap(m, "tls"))
		return node
	case "wireguard":
		node := base("wireguard")
		node = append(node, KV{"private-key", sbStr(m, "private_key")})
		node = append(node, KV{"public-key", sbStr(m, "peer_public_key")})
		if psk := sbStr(m, "pre_shared_key"); psk != "" {
			node = append(node, KV{"pre-shared-key", psk})
		}
		if addrs, ok := m["local_address"].([]interface{}); ok && len(addrs) > 0 {
			if ip, ok := addrs[0].(string); ok {
				node = append(node, KV{"ip", ip})
			}
		}
		if mtu := sbInt(m, "mtu"); mtu != 0 {
			node = append(node, KV{"mtu", mtu})
		}
		return node
	case "socks":
		node := base("socks5")
		if u := sbStr(m, "username"); u != "" {
			node = append(node, KV{"username", u})
		}
		if p := sbStr(m, "password"); p != "" {
			node = append(node, KV{"password", p})
		}
		return node
	case "http":
		node := base("http")
		if u := sbStr(m, "username"); u != "" {
			node = append(node, KV{"username", u})
		}
		if p := sbStr(m, "password"); p != "" {
			node = append(node, KV{"password", p})
		}
		node = sbTLS(node, sbMap(m, "tls"))
		return node
	}
	return nil
}

func parseLink(line string) Obj {
	low := strings.ToLower(line)
	switch {
	case strings.HasPrefix(low, "vless://"):
		return parseVless(line)
	case strings.HasPrefix(low, "vmess://"):
		return parseVmess(line)
	case strings.HasPrefix(low, "trojan://"):
		return parseTrojan(line)
	case strings.HasPrefix(low, "ss://"):
		return parseSS(line)
	case strings.HasPrefix(low, "ssr://"):
		return parseSSR(line)
	case strings.HasPrefix(low, "hysteria2://") || strings.HasPrefix(low, "hy2://"):
		return parseHysteria2(line)
	case strings.HasPrefix(low, "hysteria://"):
		return parseHysteria(line)
	case strings.HasPrefix(low, "tuic://"):
		return parseTuic(line)
	case strings.HasPrefix(low, "socks5://"):
		return parseSocks5(line)
	case strings.HasPrefix(low, "wireguard://"):
		return parseWireguard(line)
	}
	return nil
}

func parseSubscriptionText(text string) []Obj {
	// sing-box JSON config: {"outbounds": [...]}
	if trimmed := strings.TrimSpace(text); strings.HasPrefix(trimmed, "{") {
		if nodes := parseSingboxJSON(trimmed); len(nodes) > 0 {
			return nodes
		}
	}
	var lines []string
	for _, ln := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			lines = append(lines, s)
		}
	}
	hasLink := false
	for _, ln := range lines {
		if strings.Contains(ln, "://") {
			hasLink = true
			break
		}
	}
	if !hasLink {
		if dec, err := b64decode(wsRe.ReplaceAllString(text, "")); err == nil {
			var decLines []string
			for _, ln := range strings.Split(string(dec), "\n") {
				if s := strings.TrimSpace(ln); s != "" {
					decLines = append(decLines, s)
				}
			}
			for _, l := range decLines {
				if strings.Contains(l, "://") {
					lines = decLines
					break
				}
			}
		}
	}
	var nodes []Obj
	seen := map[string]bool{}
	for _, ln := range lines {
		node := parseLink(ln)
		if node == nil {
			continue
		}
		base := node.getString("name")
		name := base
		i := 2
		for seen[name] {
			name = fmt.Sprintf("%s-%d", base, i)
			i++
		}
		for j := range node {
			if node[j].Key == "name" {
				node[j].Val = name
			}
		}
		seen[name] = true
		nodes = append(nodes, node)
	}
	return nodes
}

