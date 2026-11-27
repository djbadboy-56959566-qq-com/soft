// WebDAV 备份 / 恢复：与 Multi Mail Notifier 插件同一套逻辑
// 预设：坚果云 https://dav.jianguoyun.com/dav/（应用密码），
// 日本 InfiniCLOUD https://higa.teracloud.jp/dav/（注意：dav.infini-cloud.net 是错的，不要用）。
// 备份只含自定义规则 + 订阅 + 白名单 + 设置 + 主题，绝不含 WebDAV 密码和任何 token。
package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

var webdavPresets = map[string]string{
	"jianguoyun":  "https://dav.jianguoyun.com/dav/",
	"infinicloud": "https://higa.teracloud.jp/dav/",
	"custom":      "",
}

func webdavBaseURL() string {
	mu.RLock()
	defer mu.RUnlock()
	if u := webdavPresets[cfg.WebDAV.Preset]; u != "" {
		return u
	}
	return strings.TrimSpace(cfg.WebDAV.URL)
}

func webdavClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

func webdavAuth(req *http.Request) {
	mu.RLock()
	defer mu.RUnlock()
	if cfg.WebDAV.User != "" {
		req.SetBasicAuth(cfg.WebDAV.User, cfg.WebDAV.Pass)
	}
}

func backupFileName(t time.Time) string {
	return fmt.Sprintf("dj-adblock-backup-%s.json", t.Format("20060102-150405"))
}

type backupPayload struct {
	App       string         `json:"app"`
	Version   string         `json:"version"`
	BackupAt  string         `json:"backupAt"`
	Custom    []string       `json:"customRules"`
	Subscribe []Subscription `json:"subscriptions"`
	Whitelist []string       `json:"whitelist"`
	Settings  map[string]any `json:"settings"`
}

// buildBackupPayload 备份内容：绝不含 webdav 密码
func buildBackupPayload() backupPayload {
	mu.RLock()
	defer mu.RUnlock()
	return backupPayload{
		App: "DjJingjie", Version: Version,
		BackupAt:  time.Now().Format("2006-01-02 15:04:05"),
		Custom:    append([]string{}, cfg.CustomRules...),
		Subscribe: append([]Subscription{}, cfg.Subscriptions...),
		Whitelist: append([]string{}, cfg.Whitelist...),
		Settings: map[string]any{
			"theme": cfg.Theme, "updateInterval": cfg.UpdateInterval,
			"updateTime": cfg.UpdateTime,
			"enabled": cfg.Enabled, "upstream": cfg.Upstream,
			"groups": cfg.Groups,
			"deviceNames": cfg.DeviceNames, "deviceTypes": cfg.DeviceTypes,
		},
	}
}

// webdavTest 测试连接：PROPFIND Depth:0
// 207/200 成功，401 账号密码错，404 地址错
func webdavTest() (bool, string) {
	base := webdavBaseURL()
	if base == "" {
		return false, "请先选择预设或填写 WebDAV 地址"
	}
	req, err := http.NewRequest("PROPFIND", base, strings.NewReader(""))
	if err != nil {
		return false, err.Error()
	}
	req.Header.Set("Depth", "0")
	webdavAuth(req)
	resp, err := webdavClient().Do(req)
	if err != nil {
		return false, "连接失败，请检查地址和网络：" + err.Error()
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200, 207:
		return true, "连接成功 ✓ 服务器通，账号密码正确"
	case 401:
		return false, "401：账号或密码不对（坚果云请用应用密码，不是登录密码）"
	case 404:
		return false, "404：地址不对，请检查 WebDAV 地址"
	default:
		return false, fmt.Sprintf("服务器返回 %d，请检查地址和账号", resp.StatusCode)
	}
}

func webdavBackup() (string, error) {
	base := strings.TrimSuffix(webdavBaseURL(), "/") + "/"
	if base == "/" {
		return "", fmt.Errorf("请先配置 WebDAV")
	}
	payload := buildBackupPayload()
	body, _ := json.MarshalIndent(payload, "", "  ")
	name := backupFileName(time.Now())
	req, err := http.NewRequest("PUT", base+name, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	webdavAuth(req)
	resp, err := webdavClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 && resp.StatusCode != 204 {
		return "", fmt.Errorf("备份失败，服务器返回 %d", resp.StatusCode)
	}
	return name, nil
}

type davMultistatus struct {
	XMLName   xml.Name `xml:"multistatus"`
	Responses []struct {
		Href string `xml:"href"`
	} `xml:"response"`
}

var backupNameRe = regexp.MustCompile(`dj-adblock-backup-\d{8}-\d{6}\.json`)

// webdavListBackups PROPFIND 目录，列出备份文件，最新 3 个
func webdavListBackups() ([]string, error) {
	base := strings.TrimSuffix(webdavBaseURL(), "/") + "/"
	req, err := http.NewRequest("PROPFIND", base, strings.NewReader(""))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", "1")
	webdavAuth(req)
	resp, err := webdavClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 && resp.StatusCode != 207 {
		return nil, fmt.Errorf("列出备份失败，服务器返回 %d", resp.StatusCode)
	}
	// 先按 XML 解析，失败时用正则兜底
	names := map[string]bool{}
	var ms davMultistatus
	if xml.Unmarshal(body, &ms) == nil {
		for _, r := range ms.Responses {
			if m := backupNameRe.FindString(r.Href); m != "" {
				names[m] = true
			}
		}
	}
	for _, m := range backupNameRe.FindAllString(string(body), -1) {
		names[m] = true
	}
	out := []string{}
	for n := range names {
		out = append(out, n)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	if len(out) > 3 {
		out = out[:3]
	}
	return out, nil
}

// toStringMap 把备份文件里 map[string]any 形式的字符串表转回 map[string]string
func toStringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}

func webdavRestore(name string) error {
	if !backupNameRe.MatchString(name) {
		return fmt.Errorf("文件名不对")
	}
	base := strings.TrimSuffix(webdavBaseURL(), "/") + "/"
	req, err := http.NewRequest("GET", base+name, nil)
	if err != nil {
		return err
	}
	webdavAuth(req)
	resp, err := webdavClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("下载备份失败，服务器返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 备份文件很小，5MB 上限防爆内存
	if err != nil {
		return err
	}
	var p backupPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return fmt.Errorf("备份文件损坏：%v", err)
	}
	mu.Lock()
	cfg.CustomRules = p.Custom
	cfg.Subscriptions = p.Subscribe
	cfg.Whitelist = p.Whitelist
	if v, ok := p.Settings["theme"].(string); ok && v != "" {
		cfg.Theme = v
	}
	if v, ok := p.Settings["updateInterval"].(string); ok && v != "" {
		cfg.UpdateInterval = v
	}
	if v, ok := p.Settings["updateTime"].(string); ok && validUpdateTime(v) {
		cfg.UpdateTime = v
	}
	if m := toStringMap(p.Settings["deviceNames"]); m != nil {
		cfg.DeviceNames = m
	}
	if m := toStringMap(p.Settings["deviceTypes"]); m != nil {
		cfg.DeviceTypes = m
	}
	if v, ok := p.Settings["enabled"].(bool); ok {
		cfg.Enabled = v
	}
	mu.Unlock()
	saveConfig()
	rebuildBlocked()
	return nil
}

// checkLatestRelease 查 soft 仓库的 Dj净界专属 Release（tag 以 djingjie- / DjJingjie- 开头）。
// 只做版本提示，不自动安装。仓库当前最新 Release 是 DjMihomo 的，不算 Dj净界的新版。
func checkLatestRelease() (string, string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/djbadboy-56959566-qq-com/soft/releases?per_page=30", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "DjJingjie/1.0.0")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
	}
	var list []struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return "", "", err
	}
	for _, r := range list {
		low := strings.ToLower(r.TagName)
		if strings.HasPrefix(low, "djingjie-") || strings.HasPrefix(low, "djingjie_v") {
			return r.TagName, r.HTMLURL, nil
		}
	}
	return "", "", nil // 暂无 Dj净界专属发布
}
