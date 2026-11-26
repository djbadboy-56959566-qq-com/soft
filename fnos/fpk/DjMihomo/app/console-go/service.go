// Mihomo service control, status, config file access.
// Faithful port of the same logic in backend.py.
package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func pidAlive(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err == nil {
		s := string(data)
		if i := strings.LastIndex(s, ")"); i >= 0 {
			fields := strings.Fields(s[i+1:])
			if len(fields) > 0 {
				return fields[0] != "Z"
			}
		}
		return true
	}
	return syscall.Kill(pid, 0) == nil
}

func readPid() int {
	for _, path := range []string{pidFile, extraPidFile} {
		if path == "" {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		fields := strings.Fields(string(b))
		if len(fields) == 0 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		if pidAlive(pid) {
			return pid
		}
	}
	return 0
}

func writePid(pid int) {
	_ = os.MkdirAll(dataDir, 0755)
	for _, path := range []string{pidFile, extraPidFile} {
		if path == "" {
			continue
		}
		_ = os.WriteFile(path, []byte(strconv.Itoa(pid)), 0644)
	}
}

func removePidFiles() {
	for _, path := range []string{pidFile, extraPidFile} {
		if path != "" {
			_ = os.Remove(path)
		}
	}
}

func findMihomoPids() []int {
	var pids []int
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return pids
	}
	for _, e := range entries {
		name := e.Name()
		if _, err := strconv.Atoi(name); err != nil {
			continue
		}
		cmd, err := os.ReadFile(filepath.Join("/proc", name, "cmdline"))
		if err != nil {
			continue
		}
		if strings.Contains(string(cmd), mihomoBin) && strings.Contains(string(cmd), "-d") {
			if pid, err := strconv.Atoi(name); err == nil {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

func serviceStart() (bool, int) {
	if pid := readPid(); pid != 0 {
		return true, pid
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		logf("mihomo start failed: mkdir: %v", err)
		return false, 0
	}
	ensureDashboard()
	ensureGeodata()
	lf, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logf("mihomo start failed: open log: %v", err)
		return false, 0
	}
	cmd := exec.Command(mihomoBin, "-d", dataDir)
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		lf.Close()
		logf("mihomo start failed: %v", err)
		return false, 0
	}
	lf.Close()
	pid := cmd.Process.Pid
	// Reap the child when it exits. Release() does NOT reparent a live
	// parent's child to init, so without Wait() an exited core stays a
	// zombie under the console (seen 2026-11-24: 11:26 mihomo <defunct>).
	// Wait in a goroutine; outputs are plain files, so it returns on exit.
	go func() { _ = cmd.Wait() }()
	time.Sleep(300 * time.Millisecond)
	// If the process already died (e.g. bad config), report failure.
	if !pidAlive(pid) {
		logf("mihomo exited immediately after start (pid=%d)", pid)
		return false, 0
	}
	writePid(pid)
	logf("mihomo started pid=%d", pid)
	return true, pid
}

func serviceStop(timeout time.Duration) bool {
	var pids []int
	seen := map[int]bool{}
	if pid := readPid(); pid != 0 {
		pids = append(pids, pid)
		seen[pid] = true
	}
	for _, p := range findMihomoPids() {
		if !seen[p] {
			pids = append(pids, p)
			seen[p] = true
		}
	}
	for _, p := range pids {
		_ = syscall.Kill(p, syscall.SIGTERM)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allDead := true
		for _, p := range pids {
			if pidAlive(p) {
				allDead = false
				break
			}
		}
		if allDead {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, p := range pids {
		if pidAlive(p) {
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
	}
	removePidFiles()
	if len(pids) > 0 {
		logf("mihomo stopped pids=%v", pids)
	}
	return true
}

func serviceRestart() (bool, int) {
	serviceStop(10 * time.Second)
	time.Sleep(500 * time.Millisecond)
	return serviceStart()
}

func serviceRunning() bool {
	if readPid() != 0 {
		return true
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(controller + "/version")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

var (
	versionMu    sync.Mutex
	versionCache *string
)

var versionRe = regexp.MustCompile(`v[0-9]+\.[0-9]+\.[0-9]+[^\s]*`)

func mihomoVersion() *string {
	versionMu.Lock()
	defer versionMu.Unlock()
	if versionCache != nil {
		return versionCache
	}
	var v *string
	out, err := exec.Command(mihomoBin, "-v").Output()
	if err == nil {
		s := strings.TrimSpace(string(out))
		if m := versionRe.FindString(s); m != "" {
			v = &m
		} else if s != "" {
			first := strings.Split(s, "\n")[0]
			if len(first) > 64 {
				first = first[:64]
			}
			v = &first
		}
	}
	versionCache = v
	return v
}

type statusResp struct {
	Running        bool    `json:"running"`
	Pid            *int    `json:"pid"`
	Version        *string `json:"version"`
	AppVersion     string  `json:"appVersion"`
	CoreVersion    *string `json:"coreVersion"`
	MixedPort      *int    `json:"mixedPort"`
	ControllerPort *int    `json:"controllerPort"`
	Mode           *string `json:"mode"`
}

func getStatus() statusResp {
	pid := readPid()
	running := pid != 0 || serviceRunning()
	cfg := map[string]interface{}{}
	if text, err := readConfigTextE(); err == nil {
		cfg = getTopValues(splitLines(text), settingKeys)
	}
	var pidPtr *int
	if pid != 0 {
		pidPtr = &pid
	}
	ver := mihomoVersion()
	st := statusResp{
		Running:     running,
		Pid:         pidPtr,
		Version:     ver,
		AppVersion:  appVersion,
		CoreVersion: ver,
	}
	if v, ok := cfg["mixed-port"]; ok {
		if i, ok := v.(int); ok {
			st.MixedPort = &i
		}
	}
	if ctl, ok := cfg["external-controller"]; ok {
		if s, ok := ctl.(string); ok {
			if i := strings.LastIndex(s, ":"); i >= 0 {
				if p, err := strconv.Atoi(s[i+1:]); err == nil {
					st.ControllerPort = &p
				}
			}
		}
	}
	if v, ok := cfg["mode"]; ok {
		if s, ok := v.(string); ok {
			st.Mode = &s
		}
	}
	return st
}

// ---------------------------------------------------------------- config file

func readConfigTextE() (string, error) {
	if b, err := os.ReadFile(configPath); err == nil {
		return string(b), nil
	}
	if b, err := os.ReadFile(defaultConfigPath); err == nil {
		return string(b), nil
	}
	return "", nil
}

func readConfigText() string {
	s, _ := readConfigTextE()
	return s
}

func writeConfigText(text string) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	if old, err := os.ReadFile(configPath); err == nil {
		_ = os.WriteFile(configPath+".bak", old, 0644)
	}
	return os.WriteFile(configPath, []byte(text), 0644)
}
