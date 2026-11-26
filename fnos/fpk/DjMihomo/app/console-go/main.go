// DjMihomo console (djconsole) — single static Go binary.
//
// Nikki-style config console for the FnOS DjMihomo app: profiles + mixin
// merge into the running config.yaml, subscription management, service
// control, logs, and the embedded Zashboard (served by Mihomo itself).
//
// Environment:
//   DJMIHOMO_CONSOLE_PORT  listen port (default 9091)
//   DJMIHOMO_DATA          data dir (default /vol1/@appshare/DjMihomo)
//   DJMIHOMO_BIN           mihomo binary (default <app>/bin/mihomo)
//   DJMIHOMO_PID_FILE      extra pid file (cmd/main pid file, optional)
package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

const appVersion = "1.2.60"

const (
	defaultPort     = 9091
	defaultDataDir  = "/vol1/@appshare/DjMihomo"
	controller      = "http://127.0.0.1:9090"
)

//go:embed ui
var uiFS embed.FS

var (
	port              int
	dataDir           string
	appDir            string
	mihomoBin         string
	extraPidFile      string
	pidFile           string
	configPath        string
	defaultConfigPath string
	subsPath          string
	logPath           string
	backendLogPath    string
	profilesDir       string
	activeProfilePath string
	mixinPath         string
	mixinFilePath     string
	appPath           string
	disabledPath      string
)

var settingKeys = []string{
	"mixed-port", "allow-lan", "mode", "log-level",
	"ipv6", "external-controller", "secret",
}

var mu sync.Mutex

func logf(format string, args ...interface{}) {
	fmt.Printf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// ensureDashboard makes sure the bundled Zashboard files are present at
// $DATA_DIR/ui/ange (Mihomo serves them at /ui/ on the controller port).
// If they are missing (install/upgrade script did not copy them), copy them
// from the bundled dashboard next to the console binary. This self-heals
// the "404 page not found" live panel.
// The live panel is AnGe-ClashBoard by default (built from source with the
// server-proxy fix so it talks to Mihomo directly). The official Zashboard
// is the backup: clicking 更新面板 downloads it and records panel=zashboard
// so the console won't overwrite it back to AnGe on restart.
// $DATA/panel records the choice: "ange" (default) or "zashboard".
func ensureDashboard() {
	dstDir := filepath.Join(dataDir, "ui", "ange")
	if b, err := os.ReadFile(filepath.Join(dataDir, "panel")); err == nil {
		if strings.TrimSpace(string(b)) == "zashboard" {
			return // 用户选了官方面板（更新面板下载的），不动它
		}
	}
	// Zashboard 带 THIRD_PARTY_NOTICES.md，AnGe 没有：用来认出当前装的是哪个。
	isZashboard := false
	if _, err := os.Stat(filepath.Join(dstDir, "THIRD_PARTY_NOTICES.md")); err == nil {
		isZashboard = true
	}
	// 面板版本标记：.djversion 跟着内置面板走。旧版只认 index.html 存不
	// 存在，升级新版时已装的旧 AnGe 不会被换掉（面板还是旧的，修好的
	// 127.0.0.1 默认地址问题就一直存在）。现在比对版本，不一样就重装。
	if _, err := os.Stat(filepath.Join(dstDir, "index.html")); err == nil && !isZashboard {
		bundled, _ := os.ReadFile(filepath.Join(appDir, "dashboard", ".djversion"))
		installed, _ := os.ReadFile(filepath.Join(dstDir, ".djversion"))
		if len(bundled) > 0 && string(bundled) == string(installed) {
			return // 已是当前版本的 AnGe 面板
		}
	}
	srcDir := filepath.Join(appDir, "dashboard")
	if _, err := os.Stat(filepath.Join(srcDir, "index.html")); err != nil {
		logf("dashboard: bundled files not found at %s", srcDir)
		return
	}
	if err := os.RemoveAll(dstDir); err != nil {
		logf("dashboard: remove %s failed: %v", dstDir, err)
		return
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		logf("dashboard: mkdir %s failed: %v", dstDir, err)
		return
	}
	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dstDir, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0644)
	})
	if err != nil {
		logf("dashboard: copy failed: %v", err)
		return
	}
	os.WriteFile(filepath.Join(dataDir, "panel"), []byte("ange"), 0644)
	logf("dashboard: restored bundled AnGe-ClashBoard to %s", dstDir)
}

// geodataFiles are the Mihomo geodata files the core needs to parse GEOIP
// and GEOSITE rules. The data dir lost them when it was deleted (2026-11-23);
// without them Mihomo tries to download geoip.metadb from GitHub itself,
// times out, and exits with a fatal "Parse config error" — so the core
// looks like it "stopped by itself". This self-heals them before the core
// starts: first from the bundled copy in the .fpk (fast, no network),
// then by downloading from the official MetaCubeX release via the ghproxy
// mirror (GitHub is slow/blocked from many home networks in China),
// finally direct from GitHub.
var geodataFiles = []string{"GeoIP.dat", "GeoSite.dat", "Country.mmdb", "geoip.metadb"}

func ensureGeodata() {
	for _, name := range geodataFiles {
		dst := filepath.Join(dataDir, name)
		if _, err := os.Stat(dst); err == nil {
			continue // 已有
		}
		// 1) 内置副本（.fpk 里的 app/geodata/）
		if b, err := os.ReadFile(filepath.Join(appDir, "geodata", name)); err == nil {
			if err := os.WriteFile(dst, b, 0644); err == nil {
				logf("geodata: %s 缺失，已从内置副本恢复", name)
				continue
			}
			logf("geodata: %s 内置副本写入失败：%v", name, err)
		}
		// 2) 加速源下载（ghproxy 镜像），失败再直连官网
		official := "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/" + name
		mirror := "https://ghproxy.com/" + official
		if err := downloadFile(mirror, dst); err != nil {
			logf("geodata: %s 镜像下载失败（%v），直连官网再试", name, err)
			if err2 := downloadFile(official, dst); err2 != nil {
				logf("geodata: %s 下载失败：镜像 %v；官网 %v。核心用到 GEOIP/GEOSITE 规则时会启动失败。", name, err, err2)
				continue
			}
		}
		logf("geodata: %s 缺失，已下载补齐", name)
	}
	// 小写软链接（兼容另一种拼法）
	for _, pair := range [][2]string{{"geosite.dat", "GeoSite.dat"}, {"geoip.dat", "GeoIP.dat"}, {"country.mmdb", "Country.mmdb"}} {
		link := filepath.Join(dataDir, pair[0])
		if _, err := os.Lstat(link); err != nil {
			_ = os.Symlink(pair[1], link)
		}
	}
}

// downloadFile fetches fileURL into dst. It writes to a temp file first so
// a failed download never leaves a half-written geodata file behind.
// A mirror sometimes answers with its own HTML landing/error page and
// HTTP 200 — that is not a database, so reject it (the caller then tries
// the next source) instead of writing a corrupt file.
func downloadFile(fileURL, dst string) error {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(fileURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dataDir, ".geo-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	// 校验：数据库文件不是 HTML。镜像出错时常返回自己的首页/错误页。
	if f, err := os.Open(tmpPath); err == nil {
		head := make([]byte, 16)
		n, _ := f.Read(head)
		f.Close()
		if n > 0 && (strings.HasPrefix(string(head[:n]), "<!") || strings.HasPrefix(string(head[:n]), "<html")) {
			return fmt.Errorf("下载到的是网页不是数据库文件")
		}
	}
	return os.Rename(tmpPath, dst)
}

func initPaths() {
	port = defaultPort
	if v := os.Getenv("DJMIHOMO_CONSOLE_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			port = p
		}
	}
	dataDir = os.Getenv("DJMIHOMO_DATA")
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)
	appDir = filepath.Dir(exeDir)
	mihomoBin = os.Getenv("DJMIHOMO_BIN")
	if mihomoBin == "" {
		mihomoBin = filepath.Join(appDir, "bin", "mihomo")
	}
	extraPidFile = os.Getenv("DJMIHOMO_PID_FILE")
	pidFile = filepath.Join(dataDir, "mihomo.pid")
	configPath = filepath.Join(dataDir, "config.yaml")
	defaultConfigPath = filepath.Join(appDir, "config", "config.yaml")
	subsPath = filepath.Join(dataDir, "subscriptions.json")
	logPath = filepath.Join(dataDir, "info.log")
	backendLogPath = filepath.Join(dataDir, "backend.log")
	profilesDir = filepath.Join(dataDir, "profiles")
	activeProfilePath = filepath.Join(dataDir, "active-profile")
	mixinPath = filepath.Join(dataDir, "mixin.json")
	mixinFilePath = filepath.Join(dataDir, "mixin.yaml")
	appPath = filepath.Join(dataDir, "app.json")
	disabledPath = filepath.Join(dataDir, "disabled")
}

// ---------------------------------------------------------------- JSON helpers

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(b.String()))
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]interface{}{"ok": false, "error": err.Error()})
}

func okResp(extra map[string]interface{}) map[string]interface{} {
	m := map[string]interface{}{"ok": true}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func readJSONBody(r *http.Request) map[string]interface{} {
	if r.Body == nil {
		return map[string]interface{}{}
	}
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil || len(b) == 0 {
		return map[string]interface{}{}
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]interface{}{}
	}
	if m == nil {
		return map[string]interface{}{}
	}
	return m
}

func readJSONBodyInto(r *http.Request, v interface{}) bool {
	if r.Body == nil {
		return false
	}
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil || len(b) == 0 {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

// ---------------------------------------------------------------- handlers

func handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, getStatus())
}

func handleService(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	action, _ := body["action"].(string)
	mu.Lock()
	defer mu.Unlock()
	names := map[string]string{"start": "启动服务", "stop": "停止服务", "restart": "重启服务", "reload": "重载配置"}
	name := names[action]
	if name == "" {
		name = action
	}
	switch action {
	case "start":
		if !loadAppSettings().Enabled {
			logf("操作：%s被拒绝：【启用服务】没勾选，先在【应用配置】勾选再启动", name)
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "service is disabled, enable it in App Config first", "running": serviceRunning()})
			return
		}
		ok, _ := serviceStart()
		if ok {
			logf("操作：%s成功，核心已启动", name)
		} else {
			logf("操作：%s失败，核心没起来，请看核心日志找原因", name)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": ok, "running": serviceRunning()})
	case "stop":
		ok := serviceStop(10 * time.Second)
		if ok {
			logf("操作：%s成功，核心已停止", name)
		} else {
			logf("操作：%s失败，核心可能还在运行", name)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": ok, "running": serviceRunning()})
	case "restart":
		// 启用服务是总开关：没勾选时重启也不启动内核，否则勾选框就没意义了。
		if !loadAppSettings().Enabled {
			logf("操作：%s被拒绝：【启用服务】没勾选，先在【应用配置】勾选再重启", name)
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "service is disabled, enable it in App Config first", "running": serviceRunning()})
			return
		}
		// Restart regenerates the config first (same as reload), so a
		// restart always picks up profile/mixin changes. A bare restart
		// would keep a stale config.yaml and silently ignore fixes.
		if err := restartAfterChange(); err != nil {
			logf("操作：%s失败：%s", name, err.Error())
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error(), "running": serviceRunning()})
			return
		}
		// restartAfterChange only restarts when already running; an
		// explicit restart click must also start a stopped core, or the
		// button appears to do nothing while the core stays stopped.
		if !serviceRunning() {
			ok, _ := serviceStart()
			if !ok {
				logf("操作：%s失败，核心没起来，请看核心日志找原因", name)
				writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "mihomo failed to start (check the log)", "running": false})
				return
			}
		}
		logf("操作：%s成功，核心已重启（新进程）", name)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "running": serviceRunning()})
	case "reload":
		// 启用服务是总开关：没勾选时重载也不启动内核。
		if !loadAppSettings().Enabled {
			logf("操作：%s被拒绝：【启用服务】没勾选，先在【应用配置】勾选再重载", name)
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "service is disabled, enable it in App Config first", "running": serviceRunning()})
			return
		}
		// 重载 = 重新生成配置 + 让运行中的核心热加载新配置，不重启进程
		// （PID 不变）。和重启的区别：重启是停掉进程再起一个新进程。
		if err := generateRunningConfig(); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error(), "running": serviceRunning()})
			return
		}
		if serviceRunning() {
			if ok := reloadMihomoConfig(); ok {
				logf("操作：%s成功，配置已热加载（进程没重启，PID 不变）", name)
				writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "running": true})
				return
			}
			// 热加载失败时退回重启，保证配置一定生效。
			logf("操作：%s热加载失败，改为重启核心", name)
			ok, _ := serviceRestart()
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": ok, "running": serviceRunning()})
			return
		}
		// 核心停着：重载就是启动它（服务已启用）。
		ok, _ := serviceStart()
		if !ok {
			logf("操作：%s失败，核心没起来，请看核心日志找原因", name)
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "mihomo failed to start (check the log)", "running": false})
			return
		}
		logf("操作：%s成功，核心已启动（之前是停着的）", name)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "running": true})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "bad action"})
	}
}

// reloadMihomoConfig asks the running Mihomo to hot-reload config.yaml via
// PUT /configs {"path": ...} — no process restart, the PID stays the same.
// Returns false when the core is not reachable or rejects the reload.
func reloadMihomoConfig() bool {
	body, _ := json.Marshal(map[string]string{"path": configPath})
	b, code, err := mihomoAPI("PUT", "/configs", body)
	if err != nil || code != 204 {
		logf("reload config via API failed (code=%d err=%v body=%s)", code, err, string(b))
		return false
	}
	logf("reloaded mihomo config (hot reload, no restart)")
	return true
}

// restartAfterChange regenerates the running config and restarts Mihomo if
// it is running.
func restartAfterChange() error {
	if err := generateRunningConfig(); err != nil {
		return err
	}
	if !serviceRunning() {
		return nil
	}
	ok, _ := serviceRestart()
	if !ok {
		return fmt.Errorf("mihomo failed to restart (check the log)")
	}
	return nil
}

func handleAppGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, loadAppSettings())
}

func handleAppPut(w http.ResponseWriter, r *http.Request) {
	var s AppSettings
	if !readJSONBodyInto(r, &s) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("bad json"))
		return
	}
	mu.Lock()
	defer mu.Unlock()
	old := loadAppSettings()
	if err := saveAppSettings(s); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Apply the Enabled toggle immediately: enabling starts Mihomo,
	// disabling stops it. The disabled marker file is written/removed
	// by saveAppSettings and is honored by cmd/main on app start.
	if s.Enabled != old.Enabled {
		if s.Enabled {
			if err := generateRunningConfig(); err != nil {
				writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
				return
			}
			ok, _ := serviceStart()
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": ok, "running": serviceRunning()})
			return
		}
		ok := serviceStop(10 * time.Second)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": ok, "running": serviceRunning()})
		return
	}
	if s.Profile != "" && s.Profile != old.Profile {
		if err := writeActiveProfileRef(s.Profile); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if err := restartAfterChange(); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, okResp(nil))
}

func handleConfigGet(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	// ?ref=file:name or ?ref=subscription:name edits a chosen profile;
	// without it, the active profile is used.
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = readActiveProfileRef()
	}
	if ref == "" {
		ref = loadAppSettings().Profile
	}
	_, text, err := resolveProfile(ref)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error(), "yaml": "", "ref": ref})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"yaml": text, "ref": ref})
}

func handleConfigPut(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	yamlText, _ := body["yaml"].(string)
	if _, ok := body["yaml"]; !ok {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "missing yaml"})
		return
	}
	mu.Lock()
	defer mu.Unlock()
	// Optional "ref" saves to a chosen profile; otherwise the active one.
	ref, _ := body["ref"].(string)
	if ref == "" {
		ref = readActiveProfileRef()
	}
	if ref == "" {
		ref = loadAppSettings().Profile
	}
	kind, name := splitRef(ref)
	path := profileFilePath(name)
	if kind != "file" && kind != "subscription" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "unknown profile type"})
		return
	}
	if err := os.MkdirAll(profilesDir, 0755); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := os.WriteFile(path, []byte(yamlText), 0644); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := restartAfterChange(); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error(), "running": serviceRunning()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "running": serviceRunning()})
}

func handleMixinGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, loadMixin())
}

func handleMixinPut(w http.ResponseWriter, r *http.Request) {
	var m Mixin
	if !readJSONBodyInto(r, &m) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("bad json"))
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if err := saveMixin(m); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := restartAfterChange(); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResp(nil))
}

func handleMixinFileGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"yaml": loadMixinFile()})
}

func handleMixinFilePut(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	yamlText, _ := body["yaml"].(string)
	if _, ok := body["yaml"]; !ok {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "missing yaml"})
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if err := saveMixinFile(yamlText); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := restartAfterChange(); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResp(nil))
}

func handleProfilesGet(w http.ResponseWriter, r *http.Request) {
	active := readActiveProfileRef()
	if active == "" {
		active = loadAppSettings().Profile
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"active":   active,
		"profiles": listProfiles(),
	})
}

func handleProfilesCreate(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	name, _ := body["name"].(string)
	yamlText, _ := body["yaml"].(string)
	name = sanitizeProfileName(name)
	if name == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "name is required"})
		return
	}
	if yamlText == "" {
		yamlText = defaultProfileYAML
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(profilesDir, 0755); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := os.WriteFile(profileFilePath(name), []byte(yamlText), 0644); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, okResp(map[string]interface{}{"name": name}))
}

// handleProfilesUpload accepts a YAML file upload (multipart form, field
// "file", optional "name") and saves it as a profile. The file name
// (without .yaml/.yml) is used when no name is given.
func handleProfilesUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "无法读取上传文件：" + err.Error()})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "请选择一个 YAML 文件"})
		return
	}
	defer file.Close()
	// sanitizeProfileName("") returns "default", so only use the form
	// name when one was actually given; otherwise use the file name.
	name := ""
	if raw := strings.TrimSpace(r.FormValue("name")); raw != "" {
		name = sanitizeProfileName(raw)
	}
	if name == "" {
		name = sanitizeProfileName(strings.TrimSuffix(strings.TrimSuffix(header.Filename, ".yaml"), ".yml"))
	}
	if name == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "请填写配置文件名"})
		return
	}
	content, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	if len(strings.TrimSpace(string(content))) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "文件内容为空"})
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(profilesDir, 0755); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := os.WriteFile(profileFilePath(name), content, 0644); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, okResp(map[string]interface{}{"name": name}))
}

func handleProfilesDelete(w http.ResponseWriter, r *http.Request) {
	name := sanitizeProfileName(r.URL.Query().Get("name"))
	if name == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "missing name"})
		return
	}
	mu.Lock()
	defer mu.Unlock()
	active := readActiveProfileRef()
	if active == "file:"+name {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "profile is active, switch to another profile first"})
		return
	}
	if err := os.Remove(profileFilePath(name)); err != nil && !os.IsNotExist(err) {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, okResp(nil))
}

func handleProfilesActive(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	ref, _ := body["ref"].(string)
	ref = strings.TrimSpace(ref)
	if ref == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "missing ref"})
		return
	}
	mu.Lock()
	defer mu.Unlock()
	// resolve (fetches the subscription file when needed)
	if _, _, err := resolveProfile(ref); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	if err := writeActiveProfileRef(ref); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s := loadAppSettings()
	s.Profile = ref
	if err := saveAppSettings(s); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := restartAfterChange(); err != nil {
		logf("操作：切换活动配置到「%s」失败：%s", ref, err.Error())
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	logf("操作：活动配置已切换到「%s」", ref)
	writeJSON(w, http.StatusOK, okResp(nil))
}

func handleSubsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"subscriptions": publicSubs(loadSubscriptions())})
}

func handleSubsAdd(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	name, _ := body["name"].(string)
	url, _ := body["url"].(string)
	ua, _ := body["user_agent"].(string)
	name = strings.TrimSpace(name)
	url = strings.TrimSpace(url)
	if name == "" || url == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "name and url are required"})
		return
	}
	if ua == "" {
		ua = "clash"
	}
	// interval_hours / use_proxy are optional: only overwrite when the
	// request actually sends them, so a per-row "更新" keeps stored values.
	// Interval is in hours and may be fractional (0.5 = 30 minutes);
	// when auto-update is on the minimum is 0.5h, 0 = off.
	intervalSet := false
	intervalHours := 0.0
	if v, ok := body["interval_hours"].(float64); ok {
		intervalSet = true
		intervalHours = v
		if intervalHours < 0 {
			intervalHours = 0
		} else if intervalHours > 0 && intervalHours < 0.5 {
			intervalHours = 0.5
		}
	}
	proxySet := false
	useProxy := false
	if v, ok := body["use_proxy"].(bool); ok {
		proxySet = true
		useProxy = v
	}
	mu.Lock()
	defer mu.Unlock()
	subs := loadSubscriptions()
	var entry *Sub
	for i := range subs {
		if subs[i].Name == name {
			entry = &subs[i]
			break
		}
	}
	if entry == nil {
		subs = append(subs, Sub{Name: name, URL: url, UserAgent: ua})
		entry = &subs[len(subs)-1]
	} else {
		entry.URL = url
		entry.UserAgent = ua
	}
	if intervalSet {
		entry.IntervalHours = intervalHours
	}
	if proxySet {
		entry.UseProxy = useProxy
	}
	// Save the settings FIRST: editing the interval/URL must stick even
	// when the subscription link itself is dead and the content pull fails.
	if err := saveSubscriptions(subs); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	count, err := updateSubscriptionFile(entry)
	if err != nil {
		// Settings are saved; only the content pull failed.
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "count": 0, "total": len(subs), "warning": err.Error()})
		return
	}
	// 更新成功后要再存一次：updateSubscriptionFile 只改了内存里的 Updated
	// 时间，上面那次 save 是在更新之前。不补这一行，单个订阅点「更新」后
	// 更新时间永远写不进盘，重启后就空白（eooce 就是这么丢的；mia 是走
	// 自动更新，那条路更新完会存，所以有时间）。
	_ = saveSubscriptions(subs)
	if readActiveProfileRef() == "subscription:"+sanitizeProfileName(name) {
		if err := restartAfterChange(); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "count": count, "total": len(subs)})
}

func handleSubsUpdateAll(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	subs := loadSubscriptions()
	var failed []map[string]interface{}
	updated := 0
	for i := range subs {
		if _, err := updateSubscriptionFile(&subs[i]); err != nil {
			failed = append(failed, map[string]interface{}{"name": subs[i].Name, "error": err.Error()})
		} else {
			updated++
		}
	}
	_ = saveSubscriptions(subs)
	if err := restartAfterChange(); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error(), "total": updated, "failed": failed})
		return
	}
	if failed == nil {
		failed = []map[string]interface{}{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "total": updated, "failed": failed})
}

func handleSubsDelete(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "missing name"})
		return
	}
	mu.Lock()
	defer mu.Unlock()
	safe := sanitizeProfileName(name)
	active := readActiveProfileRef()
	needFallback := active == "subscription:"+safe
	var kept []Sub
	for _, s := range loadSubscriptions() {
		if s.Name != name {
			kept = append(kept, s)
		}
	}
	if err := saveSubscriptions(kept); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = os.Remove(profileFilePath(safe))
	if needFallback {
		_ = writeActiveProfileRef("file:default")
		s := loadAppSettings()
		s.Profile = "file:default"
		_ = saveAppSettings(s)
	}
	if err := restartAfterChange(); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, okResp(nil))
}

func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	n := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			n = i
		}
	}
	if n < 1 {
		n = 1
	}
	if n > 2000 {
		n = 2000
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"lines":   tailFile(logPath, n),
		"backend": tailFile(backendLogPath, n),
	})
}

func handleLogsClear(w http.ResponseWriter, r *http.Request) {
	// which: "app" 只清空应用日志， "core" 只清空核心日志，空/其他则两者都清（兼容旧前端）。
	body := readJSONBody(r)
	which, _ := body["which"].(string)
	mu.Lock()
	defer mu.Unlock()
	switch which {
	case "app":
		_ = os.WriteFile(backendLogPath, []byte{}, 0644)
	case "core":
		_ = os.WriteFile(logPath, []byte{}, 0644)
	default:
		_ = os.WriteFile(logPath, []byte{}, 0644)
		_ = os.WriteFile(backendLogPath, []byte{}, 0644)
	}
	writeJSON(w, http.StatusOK, okResp(nil))
}

// mihomoAPI calls the Mihomo external-controller API (address + secret
// read from the running config, defaults 127.0.0.1:9090 and no secret).
// It returns the response body bytes and HTTP status code.
func mihomoAPI(method, path string, body []byte) ([]byte, int, error) {
	addr := controller
	secret := ""
	if text, err := readConfigTextE(); err == nil {
		if cfg := getTopValues(splitLines(text), settingKeys); cfg != nil {
			if v, ok := cfg["external-controller"]; ok {
				if s, ok := v.(string); ok && s != "" {
					// 0.0.0.0 is a listen address; connect via loopback.
					s = strings.Replace(s, "0.0.0.0", "127.0.0.1", 1)
					addr = "http://" + s
				}
			}
			if v, ok := cfg["secret"]; ok {
				if s, ok := v.(string); ok {
					secret = s
				}
			}
		}
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, addr+path, rd)
	if err != nil {
		return nil, 0, err
	}
	// Fresh connection per call: a pooled connection the server already
	// closed makes non-idempotent PATCH/PUT fail with EOF instead of
	// being retried (GET is retried, PATCH/PUT are not).
	req.Close = true
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, err
	}
	return b, resp.StatusCode, nil
}

// handleModeGet returns the live Mihomo running mode (rule/global/direct).
func handleModeGet(w http.ResponseWriter, r *http.Request) {
	b, code, err := mihomoAPI("GET", "/configs", nil)
	if err != nil || code != 200 {
		// fall back to the mode in the running config
		mode := "rule"
		if text, err := readConfigTextE(); err == nil {
			if cfg := getTopValues(splitLines(text), settingKeys); cfg != nil {
				if v, ok := cfg["mode"]; ok {
					if s, ok := v.(string); ok && s != "" {
						mode = s
					}
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"mode": mode})
		return
	}
	var v map[string]interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"mode": "rule"})
		return
	}
	mode, _ := v["mode"].(string)
	if mode == "" {
		mode = "rule"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"mode": mode})
}

// handleModePut switches the live Mihomo mode and persists it to the
// mixin, so a later restart keeps the chosen mode.
func handleModePut(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	mode, _ := body["mode"].(string)
	if mode != "rule" && mode != "global" && mode != "direct" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "mode 必须是 rule、global 或 direct"})
		return
	}
	mu.Lock()
	defer mu.Unlock()
	payload, _ := json.Marshal(map[string]string{"mode": mode})
	if _, code, err := mihomoAPI("PATCH", "/configs", payload); err != nil || code >= 300 {
		msg := "切换失败：Mihomo 未运行或接口不可用"
		if err != nil {
			msg = "切换失败：" + err.Error()
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": msg})
		return
	}
	// persist the mode in the mixin so restarts keep it
	mx := loadMixin()
	mx.Mode = &mode
	if err := saveMixin(mx); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	logf("操作：模式已切换到 %s", mode)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "mode": mode})
}

// handleProxySelect switches a proxy group's selected node:
// PUT /api/proxies/{group} {"name": "<node>"}
func handleProxySelect(w http.ResponseWriter, r *http.Request) {
	group := strings.TrimPrefix(r.URL.Path, "/api/proxies/")
	group, _ = url.PathUnescape(group)
	body := readJSONBody(r)
	name, _ := body["name"].(string)
	if group == "" || name == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "group and name are required"})
		return
	}
	payload, _ := json.Marshal(map[string]string{"name": name})
	if _, code, err := mihomoAPI("PUT", "/proxies/"+url.PathEscape(group), payload); err != nil || code >= 300 {
		msg := "切换失败：Mihomo 未运行或接口不可用"
		if err != nil {
			msg = "切换失败：" + err.Error()
		}
		logf("操作：代理组「%s」切换节点到「%s」失败：%s。建议：先确认核心在运行，再选节点。", group, name, msg)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": msg})
		return
	}
	logf("操作：代理组「%s」已切换到节点「%s」", group, name)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func handleProxies(w http.ResponseWriter, r *http.Request) {
	b, code, err := mihomoAPI("GET", "/proxies", nil)
	if err != nil || code != 200 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"proxies": map[string]interface{}{}})
		return
	}
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"proxies": map[string]interface{}{}})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleProxyDelay tests every node in a proxy group concurrently and
// returns name -> delay in ms (-1 = timeout/unreachable), so the user can
// see which nodes actually work before switching to one.
func handleProxyDelay(w http.ResponseWriter, r *http.Request) {
	group := r.URL.Query().Get("group")
	if group == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "missing group"})
		return
	}
	b, code, err := mihomoAPI("GET", "/proxies", nil)
	if err != nil || code != 200 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "Mihomo 未运行或无法连接"})
		return
	}
	var all struct {
		Proxies map[string]struct {
			All []string `json:"all"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal(b, &all); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "解析代理组失败"})
		return
	}
	g, ok := all.Proxies[group]
	if !ok {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "没有这个代理组"})
		return
	}
	delays := make(map[string]int64, len(g.All))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, name := range g.All {
		// DIRECT/REJECT are not real nodes; skip them.
		if name == "DIRECT" || name == "REJECT" {
			continue
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			delay := testProxyDelay(name)
			mu.Lock()
			delays[name] = delay
			mu.Unlock()
		}(name)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "delays": delays})
}

// testProxyDelay asks Mihomo for one node's delay (ms) to generate_204.
// Returns -1 on timeout or any error.
func testProxyDelay(name string) int64 {
	path := "/proxies/" + url.PathEscape(name) + "/delay?timeout=5000&url=" + url.QueryEscape("http://www.gstatic.com/generate_204")
	b, code, err := mihomoAPI("GET", path, nil)
	if err != nil || code != 200 {
		return -1
	}
	var v struct {
		Delay int64 `json:"delay"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return -1
	}
	return v.Delay
}

// zashboardRelease is the official Zashboard dist zip (same URL Nikki uses
// as its default ui_url). The update button downloads it and replaces the
// bundled panel files in $DATA/ui/ange. GitHub is slow or blocked from many
// home networks in China, so the update falls back to a ghproxy mirror.
const zashboardRelease = "https://github.com/Zephyruso/zashboard/releases/latest/download/dist-cdn-fonts.zip"
const zashboardMirror = "https://ghproxy.com/https://github.com/Zephyruso/zashboard/releases/latest/download/dist-cdn-fonts.zip"

// handleDashboardRestoreAnge puts the bundled AnGe-ClashBoard back as the
// live panel (removes the "zashboard" marker so ensureDashboard restores
// it). Used when the user switched to Zashboard via 更新面板 and wants
// AnGe back.
func handleDashboardRestoreAnge(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	logf("操作：恢复 AnGe 面板（内置版本）……")
	os.Remove(filepath.Join(dataDir, "panel"))
	ensureDashboard()
	// 确认面板文件确实回来了
	if _, err := os.Stat(filepath.Join(dataDir, "ui", "ange", "index.html")); err != nil {
		logf("操作：恢复 AnGe 面板失败：面板文件没回来")
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "面板文件恢复失败，请重启一次服务再试"})
		return
	}
	logf("操作：AnGe 面板已恢复")
	writeJSON(w, http.StatusOK, okResp(nil))
}

func handleDashboardUpdate(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	dstDir := filepath.Join(dataDir, "ui", "ange")
	logf("操作：开始更新面板（下载官方 Zashboard）……")
	if err := updateDashboardFromURL(zashboardRelease, dstDir); err != nil {
		logf("操作：面板直连下载失败（%v），换镜像再试", err)
		if err2 := updateDashboardFromURL(zashboardMirror, dstDir); err2 != nil {
			logf("操作：面板更新失败：直连和镜像都下不下来。建议：检查 NAS 能不能上 GitHub，或等一会儿再点更新。")
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error() + "；镜像也失败：" + err2.Error()})
			return
		}
		logf("操作：面板已通过镜像更新成功（官方 Zashboard）")
	} else {
		logf("操作：面板更新成功（官方 Zashboard）")
	}
	// 记下现在用的是官方面板，免得控制台重启时又把它换回 AnGe。
	os.WriteFile(filepath.Join(dataDir, "panel"), []byte("zashboard"), 0644)
	writeJSON(w, http.StatusOK, okResp(nil))
}

// updateDashboardFromURL downloads a dashboard zip and extracts it into
// dstDir (replacing the old files). It writes to a temp dir first so a
// failed download never leaves a half-updated panel.
func updateDashboardFromURL(zipURL, dstDir string) error {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(zipURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载面板失败：HTTP %d", resp.StatusCode)
	}
	tmpZip, err := os.CreateTemp("", "dash-*.zip")
	if err != nil {
		return err
	}
	tmpPath := tmpZip.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmpZip, resp.Body); err != nil {
		tmpZip.Close()
		return err
	}
	tmpZip.Close()
	zr, err := zip.OpenReader(tmpPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	tmpDir, err := os.MkdirTemp("", "dash-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// Zip-slip guard: stay inside tmpDir.
		name := filepath.Clean(f.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		target := filepath.Join(tmpDir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	// The zip may wrap files in a top folder (dist/); use it if present.
	srcDir := tmpDir
	entries, _ := os.ReadDir(tmpDir)
	if len(entries) == 1 && entries[0].IsDir() {
		if _, err := os.Stat(filepath.Join(tmpDir, entries[0].Name(), "index.html")); err == nil {
			srcDir = filepath.Join(tmpDir, entries[0].Name())
		}
	}
	if _, err := os.Stat(filepath.Join(srcDir, "index.html")); err != nil {
		return fmt.Errorf("下载的面板里没有 index.html")
	}
	if err := os.RemoveAll(dstDir); err != nil {
		return err
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDir, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0644)
	})
}

func handleDashboardURL(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	port := 9090
	if text, err := readConfigTextE(); err == nil {
		if cfg := getTopValues(splitLines(text), settingKeys); cfg != nil {
			if ctl, ok := cfg["external-controller"]; ok {
				if s, ok := ctl.(string); ok {
					if i := strings.LastIndex(s, ":"); i >= 0 {
						if p, err := strconv.Atoi(s[i+1:]); err == nil && p > 0 {
							port = p
						}
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"url": fmt.Sprintf("http://%s:%d/ui/", host, port)})
}

// ---------------------------------------------------------------- static

func handleStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" || path == "" {
		path = "/index.html"
	}
	var name, ctype string
	switch path {
	case "/index.html":
		name, ctype = "ui/index.html", "text/html; charset=utf-8"
	case "/app.js":
		name, ctype = "ui/app.js", "text/javascript; charset=utf-8"
	case "/style.css":
		name, ctype = "ui/style.css", "text/css; charset=utf-8"
	case "/favicon.png":
		name, ctype = "ui/favicon.png", "image/png"
	case "/favicon.ico":
		name, ctype = "ui/favicon.png", "image/png"
	default:
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	b, err := uiFS.ReadFile(name)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func recoverWrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logf("panic on %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": fmt.Sprint(rec)})
			}
		}()
		h(w, r)
	}
}

// autostartCoreOnBoot: the console is the long-running service, so it must
// ensure the core is up when it starts. The FnOS start script is the only
// other place that starts the core, and a boot that skips it (or runs it
// while the service is still marked disabled) leaves the core dead until
// someone opens the console and starts it by hand — seen 2026-11-24, when
// the core stayed down from the 06:33 boot until a manual start at 11:38.
func autostartCoreOnBoot() {
	s := loadAppSettings()
	delay := s.StartDelay
	if delay < 0 {
		delay = 0
	}
	if delay > 0 {
		time.Sleep(time.Duration(delay) * time.Second)
	}
	if !loadAppSettings().Enabled {
		logf("开机自启：服务未启用，跳过启动核心")
		return
	}
	if serviceRunning() {
		return
	}
	logf("开机自启：服务已启用，正在启动核心")
	if ok, _ := serviceStart(); ok {
		logf("开机自启：核心已启动")
	} else {
		logf("开机自启：核心启动失败，请看核心日志找原因")
	}
}

func main() {
	initPaths()
	_ = os.MkdirAll(dataDir, 0755)
	ensureDashboard()
	ensureGeodata()
	ensureDefaultProfile()
	migrateProfileNames()
	// Regenerate the running config on startup so upgrades that change how
	// the config is built (e.g. the forced secret) take effect immediately
	// instead of waiting for a manual reload. A stale config.yaml from the
	// previous version would otherwise keep running with the old secret
	// and Zashboard would keep reporting "Unauthorized".
	if err := generateRunningConfig(); err != nil {
		logf("startup config regenerate failed: %v", err)
	}
	go scheduledRestartLoop()
	go subscriptionAutoUpdateLoop()
	go autostartCoreOnBoot()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", recoverWrap(handleStatus))
	mux.HandleFunc("POST /api/service", recoverWrap(handleService))
	mux.HandleFunc("GET /api/app", recoverWrap(handleAppGet))
	mux.HandleFunc("PUT /api/app", recoverWrap(handleAppPut))
	mux.HandleFunc("GET /api/config", recoverWrap(handleConfigGet))
	mux.HandleFunc("PUT /api/config", recoverWrap(handleConfigPut))
	mux.HandleFunc("GET /api/mixin", recoverWrap(handleMixinGet))
	mux.HandleFunc("PUT /api/mixin", recoverWrap(handleMixinPut))
	mux.HandleFunc("GET /api/mixin-file", recoverWrap(handleMixinFileGet))
	mux.HandleFunc("PUT /api/mixin-file", recoverWrap(handleMixinFilePut))
	mux.HandleFunc("GET /api/profiles", recoverWrap(handleProfilesGet))
	mux.HandleFunc("POST /api/profiles", recoverWrap(handleProfilesCreate))
	mux.HandleFunc("POST /api/profiles/upload", recoverWrap(handleProfilesUpload))
	mux.HandleFunc("DELETE /api/profiles", recoverWrap(handleProfilesDelete))
	mux.HandleFunc("POST /api/profiles/active", recoverWrap(handleProfilesActive))
	mux.HandleFunc("GET /api/subscriptions", recoverWrap(handleSubsGet))
	mux.HandleFunc("POST /api/subscriptions", recoverWrap(handleSubsAdd))
	mux.HandleFunc("POST /api/subscriptions/update", recoverWrap(handleSubsUpdateAll))
	mux.HandleFunc("DELETE /api/subscriptions", recoverWrap(handleSubsDelete))
	mux.HandleFunc("GET /api/logs", recoverWrap(handleLogs))
	mux.HandleFunc("POST /api/logs/clear", recoverWrap(handleLogsClear))
	mux.HandleFunc("GET /api/proxies", recoverWrap(handleProxies))
	mux.HandleFunc("GET /api/proxies/delay", recoverWrap(handleProxyDelay))
	mux.HandleFunc("GET /api/connectivity", recoverWrap(handleConnectivity))
	mux.HandleFunc("PUT /api/proxies/", recoverWrap(handleProxySelect))
	mux.HandleFunc("GET /api/mode", recoverWrap(handleModeGet))
	mux.HandleFunc("PUT /api/mode", recoverWrap(handleModePut))
	mux.HandleFunc("POST /api/dashboard/update", recoverWrap(handleDashboardUpdate))
	mux.HandleFunc("POST /api/dashboard/restore-ange", recoverWrap(handleDashboardRestoreAnge))
	mux.HandleFunc("GET /api/dashboard/url", recoverWrap(handleDashboardURL))
	mux.HandleFunc("/", recoverWrap(handleStatic))

	logf("DjMihomo console %s listening on 0.0.0.0:%d data=%s bin=%s", appVersion, port, dataDir, mihomoBin)
	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		logf("server exited: %v", err)
		os.Exit(1)
	}
}
