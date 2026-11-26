// Connectivity test: probe a few well-known sites through the Mihomo mixed
// proxy and report reachability + latency. Shown on the App Config page.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type testSite struct {
	name   string
	url    string
	direct bool // true: test via a direct connection (domestic sites);
	// false: test through the Mihomo mixed proxy (foreign sites).
}

var testSites = []testSite{
	{"百度", "https://www.baidu.com", true},
	{"阿里云", "https://www.aliyun.com", true},
	{"Google", "https://www.google.com", false},
	{"YouTube", "https://www.youtube.com", false},
	{"GitHub", "https://github.com", false},
}

type siteResult struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Direct bool   `json:"direct"`
	OK     bool   `json:"ok"`
	Status int    `json:"status"`
	Ms     int64  `json:"ms"`
	Error  string `json:"error,omitempty"`
	Advice string `json:"advice,omitempty"`
	Route  string `json:"route,omitempty"`
}

// matchRuleGroup 根据运行配置的 rules，找出域名命中的第一个策略组。
// 支持 DOMAIN / DOMAIN-SUFFIX / DOMAIN-KEYWORD / GEOSITE（按名字包含启发式匹配）/ MATCH。
// 找不到返回 ""。
func matchRuleGroup(domain string) string {
	text, err := readConfigTextE()
	if err != nil {
		return ""
	}
	return matchRuleGroupIn(text, domain)
}

func matchRuleGroupIn(text, domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	lines := splitLines(text)
	inRules := false
	for _, ln := range lines {
		trim := strings.TrimSpace(ln)
		if !inRules {
			if trim == "rules:" {
				inRules = true
			}
			continue
		}
		if len(ln) > 0 && ln[0] != ' ' && ln[0] != '\t' && !strings.HasPrefix(trim, "-") {
			break
		}
		if !strings.HasPrefix(trim, "- ") {
			continue
		}
		rule := strings.TrimSpace(strings.TrimPrefix(trim, "- "))
		parts := strings.Split(rule, ",")
		if len(parts) < 2 {
			continue
		}
		typ := strings.ToUpper(strings.TrimSpace(parts[0]))
		switch typ {
		case "MATCH", "FINAL":
			return strings.TrimSpace(parts[1])
		case "DOMAIN":
			if len(parts) >= 3 && strings.ToLower(strings.TrimSpace(parts[1])) == domain {
				return strings.TrimSpace(parts[2])
			}
		case "DOMAIN-SUFFIX":
			if len(parts) >= 3 {
				suf := strings.ToLower(strings.TrimSpace(parts[1]))
				if domain == suf || strings.HasSuffix(domain, "."+suf) {
					return strings.TrimSpace(parts[2])
				}
			}
		case "DOMAIN-KEYWORD":
			if len(parts) >= 3 && strings.Contains(domain, strings.ToLower(strings.TrimSpace(parts[1]))) {
				return strings.TrimSpace(parts[2])
			}
		case "GEOSITE":
			if len(parts) >= 3 {
				cat := strings.ToLower(strings.TrimSpace(parts[1]))
				if strings.Contains(domain, cat) {
					return strings.TrimSpace(parts[2])
				}
			}
		}
	}
	return ""
}

// groupSelections 从核心 /proxies 取回每个代理组当前选中的节点（now 字段）。
func groupSelections() map[string]string {
	b, code, err := mihomoAPI("GET", "/proxies", nil)
	if err != nil || code != 200 {
		return nil
	}
	var all struct {
		Proxies map[string]struct {
			Now string `json:"now"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal(b, &all); err != nil {
		return nil
	}
	m := make(map[string]string, len(all.Proxies))
	for name, g := range all.Proxies {
		m[name] = g.Now
	}
	return m
}

// routeFor 找出域名走的线路：规则命中的策略组 + 该组当前选中的节点（逐级展开子组）。
// 如 "谷歌 → 日本-故转 → SG-01"，直连返回 "直连"，找不到返回 ""。
func routeFor(domain string, sel map[string]string) string {
	g := matchRuleGroup(domain)
	if g == "" {
		return ""
	}
	if g == "DIRECT" {
		return "直连"
	}
	if g == "REJECT" {
		return "拒绝"
	}
	chain := []string{g}
	seen := map[string]bool{g: true}
	cur := g
	for i := 0; i < 3; i++ {
		next, ok := sel[cur]
		if !ok || next == "" {
			break
		}
		chain = append(chain, next)
		if next == "DIRECT" || next == "REJECT" {
			break
		}
		if seen[next] {
			break
		}
		seen[next] = true
		if _, isGroup := sel[next]; !isGroup {
			break
		}
		cur = next
	}
	return strings.Join(chain, " → ")
}

// connectivityAdvice 给出不通时的对应处方。代理站点的失败多半是当前出口
// IP 被对方限流/拉黑或节点不稳，处方是切到直连或换节点（点名具体策略组）；
// 直连站点不通则是家里网络/DNS 问题。
func connectivityAdvice(res siteResult) string {
	if res.OK {
		return ""
	}
	if res.Error == "核心未运行" {
		return "处方：先到【应用配置】把核心启动，再点测试。"
	}
	if res.Direct {
		return "处方：直连都不通，检查 NAS 的网络和 DNS 设置，或等一会儿再测。"
	}
	switch {
	case res.Status == 429:
		return "处方：对方限流了（429），这个出口 IP 请求太频繁。" + switchNodeAdvice() + "，或等一会儿再测。"
	case res.Status == 403:
		return "处方：对方拒绝了这个出口 IP（403）。" + switchNodeAdvice() + "，换个出口 IP 再测。"
	case res.Status >= 500:
		return "处方：对方服务器暂时不可用，等几分钟再测，不用换节点。"
	case strings.Contains(res.Error, "timeout") || strings.Contains(res.Error, "deadline") || strings.Contains(res.Error, "EOF") || strings.Contains(res.Error, "connection reset") || strings.Contains(res.Error, "connection refused"):
		return "处方：节点不通或超时。" + switchNodeAdvice() + "。"
	default:
		return "处方：" + switchNodeAdvice() + "；不行就等一会儿重试。"
	}
}

func mixedPort() int {
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
	return port
}

// probeUA is a browser User-Agent: some sites (notably aliyun.com) reject
// Go's default UA with HTTP 403, which looks like the site is down.
const probeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

func probeSite(s testSite, proxyURL *url.URL) siteResult {
	res := siteResult{Name: s.name, URL: s.url, Direct: s.direct}
	var client *http.Client
	if s.direct {
		// Domestic sites go direct (no proxy): faster, and avoids a
		// proxy exit IP being blocked (e.g. aliyun returning HTTP 403).
		client = &http.Client{Timeout: 10 * time.Second}
	} else {
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		client = &http.Client{Transport: transport, Timeout: 10 * time.Second}
	}
	req, err := http.NewRequest("GET", s.url, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("User-Agent", probeUA)
	start := time.Now()
	resp, err := client.Do(req)
	res.Ms = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode
	res.OK = resp.StatusCode >= 200 && resp.StatusCode < 400
	if !res.OK {
		res.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return res
}

func handleConnectivity(w http.ResponseWriter, r *http.Request) {
	// Domestic (direct) sites are tested even when the core is stopped;
	// only the foreign (proxy) sites need the core running.
	coreRunning := serviceRunning() || readPid() != 0
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", mixedPort()))
	results := make([]siteResult, len(testSites))
	var wg sync.WaitGroup
	for i, s := range testSites {
		if !s.direct && !coreRunning {
			results[i] = siteResult{Name: s.name, URL: s.url, Direct: false, Error: "核心未运行"}
			continue
		}
		wg.Add(1)
		go func(i int, s testSite) {
			defer wg.Done()
			results[i] = probeSite(s, proxyURL)
		}(i, s)
	}
	wg.Wait()
	sel := groupSelections()
	for i := range results {
		if u, err := url.Parse(results[i].URL); err == nil {
			results[i].Route = routeFor(u.Hostname(), sel)
		}
		results[i].Advice = connectivityAdvice(results[i])
		// 不通的站点也写进运行日志（带处方和线路），不只显示在页面上：
		// 测试是进系统就自动跑的，用户未必正盯着应用配置页，日志里才能留底。
		if !results[i].OK {
			if results[i].Route != "" {
				logf("操作：连通性测试「%s」不通（%s），线路：%s。%s", results[i].Name, results[i].Error, results[i].Route, results[i].Advice)
			} else {
				logf("操作：连通性测试「%s」不通（%s）。%s", results[i].Name, results[i].Error, results[i].Advice)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "results": results})
}
