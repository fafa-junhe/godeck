package rtc

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pion/stun/v3"
	"github.com/pion/webrtc/v4"
)

// TransportMode selects the underlying network transport / NAT traversal strategy.
type TransportMode string

const (
	TransportAuto      TransportMode = "auto"
	TransportUDP       TransportMode = "udp"
	TransportTCP       TransportMode = "tcp"
	TransportWebSocket TransportMode = "websocket"
)

// ParseTransport normalizes a configuration or wire transport string.
func ParseTransport(s string) TransportMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "udp":
		return TransportUDP
	case "tcp":
		return TransportTCP
	case "websocket", "ws", "wss":
		return TransportWebSocket
	default:
		return TransportAuto
	}
}

// ConfigString returns the canonical config identifier.
func (m TransportMode) ConfigString() string {
	switch m {
	case TransportUDP, TransportTCP, TransportWebSocket:
		return string(m)
	default:
		return string(TransportAuto)
	}
}

// String returns a user-friendly display label without emojis.
func (m TransportMode) String() string {
	switch m {
	case TransportUDP:
		return "UDP"
	case TransportTCP:
		return "TCP"
	case TransportWebSocket:
		return "WebSocket"
	default:
		return "自动优选"
	}
}

// ProbeMetrics holds pre-connection measurements for UDP, TCP, and WebSocket paths.
type ProbeMetrics struct {
	UDPAttempted bool
	UDPReachable bool
	UDPRTT       time.Duration
	UDPLoss      float64 // 0.0 ~ 1.0
	UDPJitter    time.Duration

	TCPAttempted bool
	TCPReachable bool
	TCPRTT       time.Duration

	WSAttempted bool
	WSReachable bool
	WSRTT       time.Duration
	WSDirect    bool // true for direct host WSS, false for hub-relayed WSS
}

// TransportDecision describes the selected transport, ordered fallbacks, and rationale.
type TransportDecision struct {
	Requested TransportMode
	Selected  TransportMode
	Ranked    []TransportMode
	Reason    string
	Metrics   ProbeMetrics
}

type historyEntry struct {
	mode     TransportMode
	rtt      time.Duration
	loss     float64
	failed   bool
	recorded time.Time
}

// TransportAdvisor records recent link performance and executes multi-criteria Auto selection.
type TransportAdvisor struct {
	mu      sync.Mutex
	history map[string][]historyEntry
}

func NewTransportAdvisor() *TransportAdvisor {
	return &TransportAdvisor{
		history: make(map[string][]historyEntry),
	}
}

// RecordResult feeds real session telemetry or failure outcomes back into future Auto decisions.
func (a *TransportAdvisor) RecordResult(targetKey string, mode TransportMode, rtt time.Duration, loss float64, failed bool) {
	if a == nil || mode == "" || mode == TransportAuto {
		return
	}
	if targetKey == "" {
		targetKey = "default"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	list := append(a.history[targetKey], historyEntry{
		mode:     mode,
		rtt:      rtt,
		loss:     loss,
		failed:   failed,
		recorded: time.Now(),
	})
	if len(list) > 12 {
		list = list[len(list)-12:]
	}
	a.history[targetKey] = list
}

func (a *TransportAdvisor) recentPenalty(targetKey string, mode TransportMode) (penalty float64, extraLoss float64) {
	if a == nil {
		return 0, 0
	}
	if targetKey == "" {
		targetKey = "default"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	cutoff := time.Now().Add(-3 * time.Minute)
	for _, h := range a.history[targetKey] {
		if h.mode != mode || h.recorded.Before(cutoff) {
			continue
		}
		if h.failed {
			penalty += 2.5
		} else if h.loss > extraLoss {
			extraLoss = h.loss * 0.5
		}
	}
	return penalty, extraLoss
}

// Decide evaluates UDP, TCP, and WebSocket candidates based on latency, packet loss,
// protocol efficiency, and recent link health.
func (a *TransportAdvisor) Decide(targetKey string, requested TransportMode, m ProbeMetrics) TransportDecision {
	req := ParseTransport(string(requested))
	if req != TransportAuto {
		return TransportDecision{
			Requested: req,
			Selected:  req,
			Ranked:    []TransportMode{req},
			Reason:    fmt.Sprintf("手动指定 %s 模式", req.String()),
			Metrics:   m,
		}
	}

	type candidate struct {
		mode  TransportMode
		score float64
		rtt   time.Duration
		loss  float64
	}
	var pool []candidate

	add := func(mode TransportMode, reachable bool, attempted bool, baseEff float64, rtt time.Duration, loss float64, jitter time.Duration) {
		if attempted && !reachable {
			return
		}
		// Unattempted modes get a neutral baseline so they remain ordered fallbacks.
		if !attempted {
			rtt = 60 * time.Millisecond
		}
		if rtt <= 0 {
			rtt = time.Millisecond
		}
		penalty, histLoss := a.recentPenalty(targetKey, mode)
		effLoss := math.Max(loss, histLoss)
		rttMs := float64(rtt.Microseconds()) / 1000.0
		jitterMs := float64(jitter.Microseconds()) / 1000.0

		// Multi-factor utility score: higher efficiency and lower latency/loss/jitter yields higher score.
		cost := 1.0 + (rttMs / 100.0) + (effLoss * 12.0) + (jitterMs / 80.0) + penalty
		score := baseEff / cost
		pool = append(pool, candidate{
			mode:  mode,
			score: score,
			rtt:   rtt,
			loss:  effLoss,
		})
	}

	// Base efficiency weights reflect header/framing overhead, head-of-line blocking, and hop count:
	// UDP: no HOL blocking, minimal header (1.00)
	// TCP: ordered stream prevents UDP QoS drop & handles loss well, slight HOL overhead (0.82)
	// WebSocket: WSS TLS + HTTP upgrade + framing (0.76 direct / 0.68 hub relay)
	wsEff := 0.68
	if m.WSDirect {
		wsEff = 0.76
	}

	add(TransportUDP, m.UDPReachable, m.UDPAttempted, 1.00, m.UDPRTT, m.UDPLoss, m.UDPJitter)
	add(TransportTCP, m.TCPReachable, m.TCPAttempted, 0.82, m.TCPRTT, 0, 0)
	add(TransportWebSocket, m.WSReachable, m.WSAttempted, wsEff, m.WSRTT, 0, 0)

	// Ensure deterministic fallback cascade even if probes timed out everywhere.
	if len(pool) == 0 {
		return TransportDecision{
			Requested: TransportAuto,
			Selected:  TransportWebSocket,
			Ranked:    []TransportMode{TransportWebSocket, TransportTCP, TransportUDP},
			Reason:    "探测未响应，回退至 WebSocket 穿透",
			Metrics:   m,
		}
	}

	sort.SliceStable(pool, func(i, j int) bool {
		return pool[i].score > pool[j].score
	})

	ranked := make([]TransportMode, 0, 3)
	seen := map[TransportMode]bool{}
	for _, c := range pool {
		ranked = append(ranked, c.mode)
		seen[c.mode] = true
	}
	for _, fallback := range []TransportMode{TransportUDP, TransportTCP, TransportWebSocket} {
		if !seen[fallback] {
			ranked = append(ranked, fallback)
		}
	}

	best := pool[0]
	var reason string
	switch best.mode {
	case TransportUDP:
		reason = fmt.Sprintf("自动优选 UDP (延迟 %.1fms · 丢包 %.1f%% · 效率最优)", float64(best.rtt.Microseconds())/1000.0, best.loss*100)
	case TransportTCP:
		if m.UDPAttempted && !m.UDPReachable {
			reason = fmt.Sprintf("自动切换 TCP (UDP 受限 · 延迟 %.1fms)", float64(best.rtt.Microseconds())/1000.0)
		} else if m.UDPLoss >= 0.04 {
			reason = fmt.Sprintf("自动切换 TCP (UDP 丢包 %.1f%% · TCP 更稳)", m.UDPLoss*100)
		} else {
			reason = fmt.Sprintf("自动优选 TCP (延迟 %.1fms · 可靠低丢包)", float64(best.rtt.Microseconds())/1000.0)
		}
	case TransportWebSocket:
		if m.UDPAttempted && !m.UDPReachable && m.TCPAttempted && !m.TCPReachable {
			reason = fmt.Sprintf("自动穿透 WebSocket (UDP/TCP 均受限 · WSS %.1fms)", float64(best.rtt.Microseconds())/1000.0)
		} else {
			reason = fmt.Sprintf("自动优选 WebSocket (WSS 延迟 %.1fms · 稳定穿透)", float64(best.rtt.Microseconds())/1000.0)
		}
	}

	return TransportDecision{
		Requested: TransportAuto,
		Selected:  best.mode,
		Ranked:    ranked,
		Reason:    reason,
		Metrics:   m,
	}
}

// ProbeUDPBurst sends 5 lightweight probes over UDP and measures reachability, RTT, packet loss, and jitter.
// If useSTUN is true, RFC 5389 STUN Binding Requests are used; otherwise GoDeck GDKPROBE echo frames are used.
func ProbeUDPBurst(ctx context.Context, addr string, useSTUN bool) (reachable bool, avgRTT time.Duration, loss float64, jitter time.Duration) {
	const count = 5
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return false, 0, 1.0, 0
	}
	conn, err := net.DialUDP("udp4", nil, udpAddr)
	if err != nil {
		return false, 0, 1.0, 0
	}
	defer conn.Close()

	type probeMeta struct {
		sent time.Time
		txID [12]byte
		seq  uint32
	}
	sentMap := make(map[uint32]probeMeta, count)
	var nonce [4]byte
	_, _ = rand.Read(nonce[:])

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	var rtts []time.Duration
	for i := uint32(0); i < count; i++ {
		if ctx.Err() != nil {
			break
		}
		var payload []byte
		var meta probeMeta
		meta.seq = i
		if useSTUN {
			msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
			meta.txID = msg.TransactionID
			payload = msg.Raw
		} else {
			payload = make([]byte, 20)
			copy(payload[0:8], "GDKPROBE")
			copy(payload[8:12], nonce[:])
			binary.BigEndian.PutUint32(payload[12:16], i)
			binary.BigEndian.PutUint32(payload[16:20], uint32(time.Now().UnixNano()))
		}
		meta.sent = time.Now()
		sentMap[i] = meta
		_, _ = conn.Write(payload)
		if i+1 < count {
			t := time.NewTimer(25 * time.Millisecond)
			select {
			case <-ctx.Done():
				t.Stop()
			case <-t.C:
			}
		}
	}

	deadline := time.Now().Add(320 * time.Millisecond)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)

	buf := make([]byte, 1500)
	seen := map[uint32]bool{}
	for len(seen) < count {
		n, err := conn.Read(buf)
		if err != nil || n <= 0 {
			break
		}
		now := time.Now()
		if useSTUN {
			if stun.IsMessage(buf[:n]) {
				m := &stun.Message{Raw: buf[:n]}
				if m.Decode() == nil {
					for seq, pm := range sentMap {
						if !seen[seq] && m.TransactionID == pm.txID {
							seen[seq] = true
							rtts = append(rtts, now.Sub(pm.sent))
							break
						}
					}
				}
			}
		} else if n >= 20 && string(buf[0:8]) == "GDKPROBE" && string(buf[8:12]) == string(nonce[:]) {
			seq := binary.BigEndian.Uint32(buf[12:16])
			if pm, ok := sentMap[seq]; ok && !seen[seq] {
				seen[seq] = true
				rtts = append(rtts, now.Sub(pm.sent))
			}
		}
	}

	if len(rtts) == 0 {
		return false, 0, 1.0, 0
	}

	var total time.Duration
	for _, r := range rtts {
		total += r
	}
	avgRTT = total / time.Duration(len(rtts))
	if len(rtts) > 1 {
		var jSum time.Duration
		for i := 1; i < len(rtts); i++ {
			diff := rtts[i] - rtts[i-1]
			if diff < 0 {
				diff = -diff
			}
			jSum += diff
		}
		jitter = jSum / time.Duration(len(rtts)-1)
	}
	loss = float64(count-len(rtts)) / float64(count)
	return true, avgRTT, loss, jitter
}

// ProbeTCPConnect measures TCP dial reachability and connect latency.
func ProbeTCPConnect(ctx context.Context, addr string) (bool, time.Duration) {
	d := net.Dialer{Timeout: 550 * time.Millisecond}
	t0 := time.Now()
	c, err := d.DialContext(ctx, "tcp4", addr)
	if err != nil {
		c, err = d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return false, 0
		}
	}
	rtt := time.Since(t0)
	_ = c.Close()
	return true, rtt
}

// ExtractProbeTargets inspects ICE servers and extracts STUN/UDP and TURN/TCP host:port targets.
func ExtractProbeTargets(servers []webrtc.ICEServer) (udpTarget string, tcpTarget string) {
	for _, s := range servers {
		for _, raw := range s.URLs {
			u := strings.TrimSpace(raw)
			lower := strings.ToLower(u)
			if strings.HasPrefix(lower, "stun:") || strings.HasPrefix(lower, "turn:") {
				hostPort := strings.SplitN(u[5:], "?", 2)[0]
				if !strings.Contains(hostPort, ":") {
					hostPort += ":3478"
				}
				if strings.Contains(lower, "transport=tcp") {
					if tcpTarget == "" {
						tcpTarget = hostPort
					}
				} else {
					if udpTarget == "" {
						udpTarget = hostPort
					}
					if strings.HasPrefix(lower, "turn:") && tcpTarget == "" {
						tcpTarget = hostPort
					}
				}
			}
		}
	}
	return udpTarget, tcpTarget
}

// FilterICEServers adapts ICE URLs to match the chosen transport mode cleanly.
func FilterICEServers(servers []webrtc.ICEServer, mode TransportMode) []webrtc.ICEServer {
	if mode == TransportWebSocket {
		return nil
	}
	if mode != TransportUDP && mode != TransportTCP {
		return servers
	}
	var out []webrtc.ICEServer
	for _, s := range servers {
		var urls []string
		for _, raw := range s.URLs {
			lower := strings.ToLower(raw)
			if mode == TransportUDP {
				if !strings.Contains(lower, "transport=tcp") {
					urls = append(urls, raw)
				}
			} else if mode == TransportTCP {
				if strings.HasPrefix(lower, "turn:") || strings.HasPrefix(lower, "turns:") {
					if strings.Contains(lower, "transport=tcp") {
						urls = append(urls, raw)
					} else if !strings.Contains(lower, "transport=udp") {
						// Turn bare turn:host:port into ?transport=tcp for Coturn TCP relay
						if strings.Contains(raw, "?") {
							urls = append(urls, raw+"&transport=tcp")
						} else {
							urls = append(urls, raw+"?transport=tcp")
						}
					}
				}
			}
		}
		if len(urls) > 0 {
			cp := s
			cp.URLs = urls
			out = append(out, cp)
		}
	}
	return out
}

// ExtractHostPort extracts host:port from an HTTP/HTTPS URL cleanly.
func ExtractHostPort(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "https") || strings.EqualFold(u.Scheme, "wss") {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(host, port)
}
