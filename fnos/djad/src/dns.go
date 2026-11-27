// DNS 线路：解析查询、拦截命中域名、其余原样转发上游并回传应答
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

// DNS 缓存：上限条数（默认 5000，OpenWrt 默认 1000，0 关闭），TTL 过期清理，FIFO 淘汰
type dnsCacheEntry struct {
	resp   []byte
	expire time.Time
}

var (
	dnsCache    = map[string]dnsCacheEntry{}
	dnsCacheOrd []string
	dnsCacheCap = 5000
	dnsCacheMu  sync.Mutex
)

func dnsCacheLen() int {
	dnsCacheMu.Lock()
	defer dnsCacheMu.Unlock()
	return len(dnsCache)
}

func dnsCacheGet(key string) ([]byte, bool) {
	if dnsCacheCap <= 0 {
		return nil, false
	}
	dnsCacheMu.Lock()
	defer dnsCacheMu.Unlock()
	e, ok := dnsCache[key]
	if !ok || time.Now().After(e.expire) {
		if ok {
			delete(dnsCache, key)
		}
		return nil, false
	}
	out := make([]byte, len(e.resp))
	copy(out, e.resp)
	return out, true
}

func dnsCachePut(key string, resp []byte, ttlSec int) {
	if dnsCacheCap <= 0 {
		return
	}
	if ttlSec < 30 {
		ttlSec = 30
	}
	if ttlSec > 3600 {
		ttlSec = 3600
	}
	dnsCacheMu.Lock()
	defer dnsCacheMu.Unlock()
	if _, ok := dnsCache[key]; !ok {
		dnsCacheOrd = append(dnsCacheOrd, key)
		for len(dnsCacheOrd) > dnsCacheCap {
			old := dnsCacheOrd[0]
			dnsCacheOrd = dnsCacheOrd[1:]
			delete(dnsCache, old)
		}
	}
	cp := make([]byte, len(resp))
	copy(cp, resp)
	dnsCache[key] = dnsCacheEntry{resp: cp, expire: time.Now().Add(time.Duration(ttlSec) * time.Second)}
}

// parseTTL 从应答里取第一条 A 记录的 TTL，取不到默认 60 秒
func parseTTL(resp []byte, qend int) int {
	if len(resp) < 12 || qend+10 > len(resp) {
		return 60
	}
	if binary.BigEndian.Uint16(resp[6:8]) < 1 {
		return 60
	}
	off := qend
	// 跳过应答域名（指针 2 字节或标签串）
	if off < len(resp) && resp[off]&0xC0 == 0xC0 {
		off += 2
	} else {
		for off < len(resp) && resp[off] != 0 {
			off += 1 + int(resp[off])
		}
		off++
	}
	if off+10 > len(resp) {
		return 60
	}
	ttl := int(binary.BigEndian.Uint32(resp[off+4 : off+8]))
	if ttl <= 0 {
		return 60
	}
	return ttl
}

func startDNS(port int) error {
	addr := &net.UDPAddr{Port: port}
	uc, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	go serveDNSUDP(uc)
	tc, err := net.ListenTCP("tcp", &net.TCPAddr{Port: port})
	if err != nil {
		log.Printf("DNS TCP %d 监听失败：%v（仅 UDP 生效）", port, err)
	} else {
		go serveDNSTCP(tc)
	}
	return nil
}

func serveDNSUDP(c *net.UDPConn) {
	buf := make([]byte, 4096)
	for {
		n, client, err := c.ReadFromUDP(buf)
		if err != nil {
			return
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		go func() {
			resp := handleDNS(pkt, client.IP.String())
			if resp != nil {
				if _, err := c.WriteToUDP(resp, client); err != nil {
					log.Printf("DNS UDP 回包失败 %s: %v", client.String(), err)
				}
			}
		}()
	}
}

func serveDNSTCP(l *net.TCPListener) {
	for {
		conn, err := l.AcceptTCP()
		if err != nil {
			return
		}
		go func(c *net.TCPConn) {
			defer c.Close()
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			var lenBuf [2]byte
			if _, err := io.ReadFull(c, lenBuf[:]); err != nil {
				return
			}
			n := int(binary.BigEndian.Uint16(lenBuf[:]))
			if n <= 0 || n > 4096 {
				return
			}
			pkt := make([]byte, n)
			if _, err := io.ReadFull(c, pkt); err != nil {
				return
			}
			resp := handleDNS(pkt, c.RemoteAddr().(*net.TCPAddr).IP.String())
			if resp != nil {
				var out [2]byte
				binary.BigEndian.PutUint16(out[:], uint16(len(resp)))
				_, _ = c.Write(out[:])
				_, _ = c.Write(resp)
			}
		}(conn)
	}
}

// parseQuestion 返回域名、QTYPE、question 段结束位置
func parseQuestion(pkt []byte) (string, uint16, int, bool) {
	if len(pkt) < 12 {
		return "", 0, 0, false
	}
	qd := binary.BigEndian.Uint16(pkt[4:6])
	if qd < 1 {
		return "", 0, 0, false
	}
	off := 12
	var labels []string
	for {
		if off >= len(pkt) {
			return "", 0, 0, false
		}
		l := int(pkt[off])
		if l == 0 {
			off++
			break
		}
		if l&0xC0 != 0 {
			return "", 0, 0, false // 问题段不应有压缩指针
		}
		off++
		if off+l > len(pkt) {
			return "", 0, 0, false
		}
		labels = append(labels, string(pkt[off:off+l]))
		off += l
	}
	if off+4 > len(pkt) {
		return "", 0, 0, false
	}
	qtype := binary.BigEndian.Uint16(pkt[off : off+2])
	return strings.ToLower(strings.Join(labels, ".")), qtype, off + 4, true
}

func handleDNS(pkt []byte, client string) []byte {
	domain, qtype, qend, ok := parseQuestion(pkt)
	if !ok || domain == "" {
		return forwardDNS(pkt)
	}
	qtypeName := "A"
	if qtype == 28 {
		qtypeName = "AAAA"
	} else if qtype != 1 {
		// 非 A/AAAA 查询直接转发
		return forwardDNS(pkt)
	}
	if qtype == 28 {
		// AAAA 一律返回 NODATA：代理节点不支持 IPv6 出站，上游（阿里 DNS）对境外域名
		// 返回的假 IPv6（2001::1）会让手机先走 IPv6 卡死。只返回 IPv4，手机走代理正常。
		appendLog(LogEntry{Time: time.Now(), Client: client, Domain: domain, Qtype: "AAAA", Blocked: false, Source: "AAAA 已禁用（只走 IPv4）"})
		return buildBlockedResponse(pkt, qtype, qend)
	}
	if src, blocked := matchBlocked(domain); blocked {
		appendLog(LogEntry{Time: time.Now(), Client: client, Domain: domain, Qtype: qtypeName, Blocked: true, Source: src})
		return buildBlockedResponse(pkt, qtype, qend)
	}
	// 放行：先查 DNS 缓存
	cacheKey := domain + "|" + qtypeName
	if cached, ok := dnsCacheGet(cacheKey); ok {
		appendLog(LogEntry{Time: time.Now(), Client: client, Domain: domain, Qtype: qtypeName, Blocked: false, Source: "放行（缓存）"})
		return cached
	}
	appendLog(LogEntry{Time: time.Now(), Client: client, Domain: domain, Qtype: qtypeName, Blocked: false, Source: "放行"})
	resp := forwardDNS(pkt)
	// 只缓存 NOERROR 应答，TTL 从应答里取
	if len(resp) >= 12 && resp[3]&0x0F == 0 {
		dnsCachePut(cacheKey, resp, parseTTL(resp, qend))
	}
	return resp
}

func buildBlockedResponse(pkt []byte, qtype uint16, qend int) []byte {
	id := pkt[0:2]
	// 头：ID + flags 0x8180 (标准响应，无错误) + QD 1
	resp := make([]byte, 0, len(pkt)+16)
	resp = append(resp, id...)
	resp = append(resp, 0x81, 0x80)
	resp = append(resp, 0x00, 0x01) // QDCOUNT
	if qtype == 1 {
		resp = append(resp, 0x00, 0x01) // ANCOUNT 1
	} else {
		resp = append(resp, 0x00, 0x00) // AAAA：无应答（NODATA），不断网
	}
	resp = append(resp, 0x00, 0x00, 0x00, 0x00) // NSCOUNT ARCOUNT
	// 只拷问题段，不拷 EDNS OPT 附加段（否则应答解析错位）
	resp = append(resp, pkt[12:qend]...)
	if qtype == 1 {
		// answer：指针 0xC00C 指向问题域名 + TYPE A + CLASS IN + TTL 60 + RDLENGTH 4 + 0.0.0.0
		resp = append(resp, 0xC0, 0x0C, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x3C, 0x00, 0x04, 0, 0, 0, 0)
	}
	return resp
}

func forwardDNS(pkt []byte) []byte {
	mu.RLock()
	upstreams := append([]string{}, cfg.Upstream...)
	mu.RUnlock()
	if len(upstreams) == 0 {
		upstreams = []string{"223.5.5.5", "119.29.29.29"}
	}
	for _, up := range upstreams {
		// 上游支持 "IP" 或 "IP:端口"（本地测试可用 127.0.0.1:15353 做 mock 上游）
		target := up
		if !strings.Contains(target, ":") {
			target = net.JoinHostPort(target, "53")
		}
		conn, err := net.DialTimeout("udp", target, 1*time.Second)
		if err != nil {
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write(pkt); err != nil {
			_ = conn.Close()
			continue
		}
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		_ = conn.Close()
		if err != nil {
			continue
		}
		out := make([]byte, n)
		copy(out, buf[:n])
		return out
	}
	// 上游全失败：返回 SERVFAIL（只拷问题段）
	resp := make([]byte, 0, 12)
	resp = append(resp, pkt[0:2]...)
	resp = append(resp, 0x81, 0x82) // SERVFAIL
	resp = append(resp, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	if _, _, qend, ok := parseQuestion(pkt); ok {
		resp = append(resp, pkt[12:qend]...)
	}
	return resp
}

var _ = fmt.Sprintf
