// 规则库：内置首发拦截域名（离线可用）+ 远程规则组拉取与解析 + 缓存
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// starterBlocklist 内置首发域名：国内外知名广告 / 统计 / 追踪域名。
// 只收广告专用域名，不收可能影响正常功能的域名（如 static.qq.com、cdn 域名不在此列）。
var starterBlocklist = []string{
	// Google 广告
	"ads.google.com", "adservice.google.com", "googleads.g.doubleclick.net",
	"doubleclick.net", "pagead2.googlesyndication.com", "tpc.googlesyndication.com",
	"ad.doubleclick.net", "stats.g.doubleclick.net", "cm.g.doubleclick.net",
	"google-analytics.com", "ssl.google-analytics.com", "www.google-analytics.com",
	"analytics.google.com", "googletagmanager.com", "googletagservices.com",
	"partner.googleadservices.com", "adservice.google.co.jp",
	// 百度广告 / 统计
	"hm.baidu.com", "pos.baidu.com", "cpro.baidu.com", "cbjs.baidu.com",
	"hmma.baidu.com", "afd.baidu.com", "als.baidu.com", "cm.pos.baidu.com",
	// 阿里 / 友盟 / 腾讯统计
	"atanx.alicdn.com", "atanx2.alicdn.com", "log.ummeng.com", "umeng.com",
	"alog.umeng.com", "admobile.top", "adx.feng.com",
	// 字节 / 抖音 / TikTok 广告与统计
	"analytics.tiktok.com", "ads.tiktok.com", "log.tiktokv.com",
	"is.snssdk.com", "pstatp.com", "bytecdn.cn",
	// 小米 / vivo / oppo 系统广告
	"api.ad.xiaomi.com", "api.ad.intl.xiaomi.com", "sdkconfig.ad.xiaomi.com",
	"ad.mi.com", "tracking.miui.com",
	// 常见广告联盟 / 追踪
	"ads.yahoo.com", "ads.bing.com", "bat.bing.com", "clarity.ms",
	"ads.facebook.com", "connect.facebook.net", "graph.facebook.com",
	"ads-twitter.com", "analytics.twitter.com",
	"ads.linkedin.com", "px.ads.linkedin.com",
	"ads.pinterest.com", "ct.pinterest.com",
	"sentry.io", "o0.ingest.sentry.io",
	"hotjar.com", "static.hotjar.com", "script.hotjar.com",
	"fullstory.com", "rs.fullstory.com",
	"mixpanel.com", "api.mixpanel.com",
	"segment.com", "api.segment.io", "cdn.segment.com",
	"amplitude.com", "api2.amplitude.com",
	"matomo.cloud", "countly.com",
	// 国内广告联盟
	"ad.10086.cn", "ad.cmvideo.cn", "ad.360.cn", "ads.360.cn",
	"union.baidu.com", "cpro.baidustatic.com",
	"ad.sogou.com", "galaxy.sogou.com",
	"ads.weibo.com", "sax.sina.com.cn", "saxn.sina.com.cn",
	"ad.jd.com", "ads.jd.com", "im-x.jd.com",
	"ad.taobao.com", "ad.alimama.com", "atanx.alimama.com",
	"ad.pdd.com", "ads.pinduoduo.com",
	"ad.ksapisrv.com", "ads.kuaishou.com", "log.kuaishou.com",
	"ad.xiaohongshu.com", "ads.xiaohongshu.com",
	"ad.bilibili.com", "cm.bilibili.com",
	"ad.qq.com", "ads.qq.com", "l.qq.com", "mi.gdt.qq.com", "e.qq.com",
	"adservice.sigmob.cn", "sdk.sigmob.cn",
	// 国外广告联盟
	"ads.amazon.com", "aax.amazon-adsystem.com", "fls-na.amazon.com",
	"ads.yahooinc.com", "ads.yap.yahoo.com",
	"criteo.com", "cas.criteo.com", "gum.criteo.com",
	"outbrain.com", "widgets.outbrain.com",
	"taboola.com", "cdn.taboola.com", "trc.taboola.com",
	"pubmatic.com", "ads.pubmatic.com",
	"rubiconproject.com", "fastlane.rubiconproject.com",
	"openx.net", "openx.com",
	"moatads.com", "z.moatads.com",
	"scorecardresearch.com", "sb.scorecardresearch.com",
	"quantserve.com", "pixel.quantserve.com",
	"adnxs.com", "ib.adnxs.com", "secure.adnxs.com",
	"advertising.com", "tag.aticdn.net",
	"serving-sys.com", "bs.serving-sys.com",
	"zedo.com", "g1.zedo.com",
	"revcontent.com", "trends.revcontent.com",
	"mgid.com", "cm.mgid.com",
	"exoclick.com", "ads.exoclick.com",
	"popads.net", "popcash.net",
	"propellerads.com", "ad.propellerads.com",
	// 应用内广告 SDK
	"app-measurement.com", "firebase-settings.crashlytics.com",
	"settings.crashlytics.com", "reports.crashlytics.com",
	"applovin.com", "a.applovin.com", "rt.applovin.com",
	"unity3d.com", "config.unity3d.com", "ads.unity3d.com",
	"ironsource.com", "supersonicads.com",
	"vungle.com", "ads.vungle.com",
	"chartboost.com", "live.chartboost.com",
	"inmobi.com", "api.inmobi.com",
	"mopub.com", "ads.mopub.com",
	"tapjoy.com", "ws.tapjoyads.com",
	"adcolony.com", "ads.adcolony.com",
	"fyber.com", "engine.fyber.com",
	"startapp.com", "info.startappservice.com",
	"airpush.com", "api.airpush.com",
}

func cachePath(key string) string {
	safe := strings.NewReplacer("/", "_", ":", "_", ".", "_").Replace(key)
	return filepath.Join(dataDir, "rules-cache-"+safe+".json")
}

func cachedGroupDomains(key string) []string {
	b, err := os.ReadFile(cachePath(key))
	if err != nil {
		return nil
	}
	var list []string
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	return list
}

func saveGroupCache(key string, list []string) {
	b, _ := json.Marshal(list)
	_ = os.WriteFile(cachePath(key), b, 0644)
}

// parseRuleLine 解析一行 hosts / adblock 规则，返回域名
func parseRuleLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
		return "", false
	}
	if strings.HasPrefix(line, "@@") {
		return "", false // 白名单规则不导入拦截库
	}
	d := ""
	switch {
	case strings.HasPrefix(line, "||"):
		d = strings.TrimPrefix(line, "||")
		for i, c := range d {
			if c == '^' || c == '$' || c == '/' {
				d = d[:i]
				break
			}
		}
	case strings.HasPrefix(line, "0.0.0.0 "), strings.HasPrefix(line, "127.0.0.1 "):
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			d = parts[1]
		}
	default:
		if strings.Contains(line, " ") || strings.Contains(line, "*") || strings.Contains(line, "=") {
			return "", false
		}
		d = line
	}
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimSuffix(d, ".")
	if d == "" || d == "localhost" || strings.Contains(d, " ") || !strings.Contains(d, ".") {
		return "", false
	}
	return d, true
}

// parseRuleText 解析 hosts / adblock 格式规则文本，提取域名（供测试和小文本用）
func parseRuleText(body string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		if d, ok := parseRuleLine(line); ok && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// fetchRuleList 流式拉取并逐行解析（不把整个文件读进内存），上限 20MB
func fetchRuleList(url string) ([]string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "DjJingjie/1.0.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, &urlError{url, resp.Status}
	}
	out := []string{}
	seen := map[string]bool{}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 20<<20))
	sc.Buffer(make([]byte, 4096), 1<<20)
	for sc.Scan() {
		if d, ok := parseRuleLine(sc.Text()); ok && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type urlError struct {
	url, status string
}

func (e *urlError) Error() string { return e.url + ": " + e.status }

// updateAllRules 拉取所有启用的规则组和订阅，失败保留旧缓存。
// manual=true 是用户点的「立即更新」，false 是定时自动更新；开始和完成各记一条事件。
func updateAllRules(manual bool) {
	trigger := "定时更新"
	if manual {
		trigger = "手动更新"
	}
	now := time.Now().Format("2006-01-02 15:04")
	mu.Lock()
	groups := append([]RuleGroup{}, cfg.Groups...)
	subs := append([]Subscription{}, cfg.Subscriptions...)
	mu.Unlock()
	enabledN := 0
	for _, g := range groups {
		if g.Enabled {
			enabledN++
		}
	}
	appendEvent(fmt.Sprintf("%s规则开始：正在拉取 %d 个规则组…", trigger, enabledN))
	for i, g := range groups {
		if !g.Enabled {
			continue
		}
		list, err := fetchRuleList(g.URL)
		mu.Lock()
		if err != nil {
			cfg.Groups[i].Error = err.Error()
			log.Printf("规则组 %s 更新失败：%v（保留旧缓存）", g.Name, err)
		} else {
			saveGroupCache(g.ID, list)
			cfg.Groups[i].Count = len(list)
			cfg.Groups[i].LastUpdate = now
			cfg.Groups[i].Error = ""
			log.Printf("规则组 %s 更新完成：%d 条", g.Name, len(list))
		}
		mu.Unlock()
	}
	for i, s := range subs {
		if !s.Enabled {
			continue
		}
		list, err := fetchRuleList(s.URL)
		mu.Lock()
		if err != nil {
			log.Printf("订阅 %s 更新失败：%v（保留旧缓存）", s.URL, err)
		} else {
			saveGroupCache("sub:"+s.URL, list)
			cfg.Subscriptions[i].Count = len(list)
			cfg.Subscriptions[i].LastUpdate = now
			log.Printf("订阅 %s 更新完成：%d 条", s.URL, len(list))
		}
		mu.Unlock()
	}
	saveConfig()
	rebuildBlocked()
	// 完成事件：成功几组、失败几组、失败的组名、更新后规则总数。
	// 成功 = 本轮 LastUpdate 被写上且 Error 为空（失败会写 Error、保留旧 LastUpdate）。
	mu.RLock()
	okN, failN := 0, 0
	failed := []string{}
	for _, g := range cfg.Groups {
		if !g.Enabled {
			continue
		}
		if g.LastUpdate == now && g.Error == "" {
			okN++
		} else {
			failN++
			failed = append(failed, g.Name)
		}
	}
	total := len(blocked)
	mu.RUnlock()
	msg := fmt.Sprintf("%s规则完成：成功 %d 组，失败 %d 组", trigger, okN, failN)
	if failN > 0 {
		msg += "（" + strings.Join(failed, "、") + " 保留旧规则）"
	}
	msg += fmt.Sprintf("，规则总数 %d 条", total)
	appendEvent(msg)
}
