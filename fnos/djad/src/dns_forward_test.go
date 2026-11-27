package main

import (
	"net"
	"strings"
	"testing"
	"time"
)

// mock 上游：同进程内 UDP，只记录收到的查询（本机沙箱丢 UDP 回包，
// 故本测试断言“查询确实发到上游”；回包中继逻辑由 TCP 端到端实测覆盖）
func startMockUpstream(t *testing.T) (string, chan string) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	got := make(chan string, 16)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pkt := append([]byte{}, buf[:n]...)
			domain, _, _, ok := parseQuestion(pkt)
			if !ok {
				continue
			}
			got <- domain
		}
	}()
	return pc.LocalAddr().String(), got
}

func buildQuery(t *testing.T, name string) []byte {
	t.Helper()
	pkt := []byte{0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	for _, p := range strings.Split(name, ".") {
		pkt = append(pkt, byte(len(p)))
		pkt = append(pkt, p...)
	}
	pkt = append(pkt, 0, 0, 1, 0, 1)
	return pkt
}

func TestForwardDNS(t *testing.T) {
	up, got := startMockUpstream(t)
	mu.Lock()
	cfg.Upstream = []string{up}
	mu.Unlock()
	_ = forwardDNS(buildQuery(t, "example.com"))
	select {
	case d := <-got:
		if d != "example.com" {
			t.Fatalf("upstream got %q, want example.com", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never received the forwarded query")
	}
}

func TestBlockedResponseWellFormed(t *testing.T) {
	pkt := buildQuery(t, "ads.google.com")
	// 模拟 dig 的 EDNS OPT 附加段：应答里不应出现 OPT
	pkt = append(pkt, 0x00, 0x00, 0x29, 0x04, 0xD0, 0x00, 0x00, 0x80, 0x00, 0x00, 0x00)
	_, _, qend, ok := parseQuestion(pkt)
	if !ok {
		t.Fatal("parseQuestion failed")
	}
	resp := buildBlockedResponse(pkt, 1, qend)
	// 长度 = 12 头 + 问题段 + 16 应答；不应含 OPT 的 11 字节
	want := 12 + (qend - 12) + 16
	if len(resp) != want {
		t.Fatalf("resp len %d, want %d (OPT leaked?)", len(resp), want)
	}
	if resp[len(resp)-4] != 0 || resp[len(resp)-3] != 0 || resp[len(resp)-2] != 0 || resp[len(resp)-1] != 0 {
		t.Fatalf("blocked IP != 0.0.0.0: %v", resp[len(resp)-4:])
	}
}

func TestParseRuleText(t *testing.T) {
	sample := "! comment\n||ads.example.com^\n0.0.0.0 track.example.net\nplain.example.org\n@@||allow.example.com^\n||sub.example.com^$third-party\n"
	got := parseRuleText(sample)
	want := map[string]bool{"ads.example.com": true, "track.example.net": true, "plain.example.org": true, "sub.example.com": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, d := range got {
		if !want[d] {
			t.Fatalf("unexpected %q", d)
		}
	}
}

var _ = time.Now

func TestDNSCache(t *testing.T) {
	// 清空缓存后放一条、命中、过期
	dnsCacheMu.Lock()
	dnsCache = map[string]dnsCacheEntry{}
	dnsCacheOrd = nil
	dnsCacheMu.Unlock()
	oldCap := dnsCacheCap
	dnsCacheCap = 3
	defer func() { dnsCacheCap = oldCap }()
	resp := []byte{0x12, 0x34, 0x81, 0x80, 0, 1, 0, 1, 0, 0, 0, 0}
	dnsCachePut("example.com|A", resp, 60)
	if got, ok := dnsCacheGet("example.com|A"); !ok || len(got) != len(resp) {
		t.Fatalf("cache miss after put")
	}
	// 超过上限 FIFO 淘汰最旧的
	dnsCachePut("a.example|A", resp, 60)
	dnsCachePut("b.example|A", resp, 60)
	dnsCachePut("c.example|A", resp, 60)
	if _, ok := dnsCacheGet("example.com|A"); ok {
		t.Fatalf("oldest entry should have been evicted")
	}
	if n := dnsCacheLen(); n != 3 {
		t.Fatalf("cache len %d, want 3", n)
	}
}

func TestParseTTL(t *testing.T) {
	// 构造带一条 A 应答的包：问题段后 TTL=300
	q := buildQuery(t, "example.com")
	qend := len(q)
	resp := append(append([]byte{}, q...), 0xC0, 0x0C, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x01, 0x2C, 0x00, 0x04, 1, 2, 3, 4)
	resp[6] = 0
	resp[7] = 1 // ANCOUNT=1
	if ttl := parseTTL(resp, qend); ttl != 300 {
		t.Fatalf("ttl=%d, want 300", ttl)
	}
}
