// WebDAV API 端点
package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

func apiWebDAVTest(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Preset string `json:"preset"`
			URL    string `json:"url"`
			User   string `json:"user"`
			Pass   string `json:"pass"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		if req.Preset != "" {
			cfg.WebDAV.Preset = req.Preset
		}
		if req.URL != "" {
			cfg.WebDAV.URL = req.URL
		}
		if req.User != "" {
			cfg.WebDAV.User = req.User
		}
		if req.Pass != "" {
			cfg.WebDAV.Pass = req.Pass
		}
		// 预设切换时自动填入对应地址
		if u := webdavPresets[cfg.WebDAV.Preset]; u != "" {
			cfg.WebDAV.URL = u
		}
		mu.Unlock()
		saveConfig()
	}
	ok, msg := webdavTest()
	// 绝不把密码返回给前端
	writeJSON(w, map[string]any{"ok": ok, "msg": msg})
}

func apiWebDAVBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	name, err := webdavBackup()
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "msg": "备份成功 ✓", "file": name})
}

func apiWebDAVBackups(w http.ResponseWriter, r *http.Request) {
	list, err := webdavListBackups()
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "msg": err.Error(), "backups": []string{}})
		return
	}
	if list == nil {
		list = []string{}
	}
	writeJSON(w, map[string]any{"ok": true, "backups": list})
}

func apiWebDAVRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	name := strings.TrimSpace(req.Name)
	if err := webdavRestore(name); err != nil {
		writeJSON(w, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "msg": "恢复成功 ✓ 规则、白名单和设置已恢复"})
}
