package rtc

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// NetStats holds parsed WebRTC telemetry metrics from Pion.
type NetStats struct {
	RTT              time.Duration
	Jitter           time.Duration
	BytesReceived    uint64
	BytesSent        uint64
	PacketsReceived  uint32
	PacketsLost      int32
	PacketsDiscarded uint32
	LocalCandidate   string
	RemoteCandidate  string
	ConnectionType   string
	Protocol         string
	CandidatePair    string
	ICEState         string
	TransportMode    TransportMode
	TransportReason  string
}

// HostTelemetry is periodically sent from Host to Viewer over the control DataChannel.
type HostTelemetry struct {
	Type          string  `json:"type"` // "telemetry"
	EncodeFPS     float64 `json:"encode_fps"`
	EncodeMs      float64 `json:"encode_ms"`
	EncodeBitrate float64 `json:"encode_bitrate"`
	Encoder       string  `json:"encoder"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
}

// SessionStats is the aggregated real-time telemetry model for UI presentation.
type SessionStats struct {
	Active           bool
	Duration         time.Duration

	// Network / Bandwidth
	DownBitrateBps   float64
	UpBitrateBps     float64
	DownBytesPerSec  float64
	UpBytesPerSec    float64
	TotalBytesRecv   uint64
	TotalBytesSent   uint64

	// Latency & Quality
	RTT              time.Duration
	Jitter           time.Duration
	PacketsReceived  uint32
	PacketsLost      int32
	PacketsDiscarded uint32
	PacketLossRate   float64 // Percentage (0.00% ~ 100.00%)
	DiscardRate      float64 // Percentage

	// Topology
	ConnectionType   string
	Protocol         string
	TransportMode    string
	TransportReason  string
	LocalCandidate   string
	RemoteCandidate  string
	CandidatePair    string
	ICEState         string

	// Video & Display
	Resolution       string
	Width            int
	Height           int
	RenderFPS        float64

	// Codec & Hardware Performance
	EncoderName      string
	EncodeFPS        float64
	EncodeDuration   time.Duration
	EncodeBitrate    float64

	DecoderName      string
	DecodeFPS        float64
	DecodeDuration   time.Duration
}

// PerfTracker measures frame rate, duration, and bitrate over a sliding window.
type PerfTracker struct {
	mu            sync.Mutex
	count         int
	totalDuration time.Duration
	totalBytes    int64
	fps           float64
	avgDuration   time.Duration
	bitrateBps    float64
	lastCalc      time.Time
}

// Record adds a sample to the performance tracker.
func (t *PerfTracker) Record(duration time.Duration, bytes int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count++
	t.totalDuration += duration
	t.totalBytes += int64(bytes)

	now := time.Now()
	if t.lastCalc.IsZero() {
		t.lastCalc = now
		return
	}
	elapsed := now.Sub(t.lastCalc)
	if elapsed >= 800*time.Millisecond {
		sec := elapsed.Seconds()
		t.fps = float64(t.count) / sec
		if t.count > 0 {
			t.avgDuration = t.totalDuration / time.Duration(t.count)
		}
		t.bitrateBps = float64(t.totalBytes*8) / sec
		t.count = 0
		t.totalDuration = 0
		t.totalBytes = 0
		t.lastCalc = now
	}
}

// Snapshot returns the current estimated FPS, average duration, and bitrate.
func (t *PerfTracker) Snapshot() (fps float64, avgDuration time.Duration, bitrateBps float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fps, t.avgDuration, t.bitrateBps
}

// NetRateTracker tracks instantaneous upload and download bitrates.
type NetRateTracker struct {
	mu           sync.Mutex
	lastRecv     uint64
	lastSent     uint64
	lastTime     time.Time
	downBps      float64
	upBps        float64
	downBytesSec float64
	upBytesSec   float64
}

// Update calculates current instantaneous network rates given cumulative byte counters.
func (n *NetRateTracker) Update(currRecv, currSent uint64) (downBps, upBps, downBytesSec, upBytesSec float64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	if n.lastTime.IsZero() {
		n.lastRecv = currRecv
		n.lastSent = currSent
		n.lastTime = now
		return 0, 0, 0, 0
	}
	dt := now.Sub(n.lastTime).Seconds()
	if dt >= 0.5 {
		if currRecv >= n.lastRecv {
			deltaRecv := currRecv - n.lastRecv
			n.downBytesSec = float64(deltaRecv) / dt
			n.downBps = n.downBytesSec * 8
		}
		if currSent >= n.lastSent {
			deltaSent := currSent - n.lastSent
			n.upBytesSec = float64(deltaSent) / dt
			n.upBps = n.upBytesSec * 8
		}
		n.lastRecv = currRecv
		n.lastSent = currSent
		n.lastTime = now
	}
	return n.downBps, n.upBps, n.downBytesSec, n.upBytesSec
}

// FormatBitrate formats raw bits-per-second into human-readable rate.
func FormatBitrate(bps float64) string {
	if bps >= 1e6 {
		return fmt.Sprintf("%.2f Mbps (%.1f KB/s)", bps/1e6, bps/(8*1024))
	} else if bps >= 1e3 {
		return fmt.Sprintf("%.1f Kbps (%.1f KB/s)", bps/1e3, bps/(8*1024))
	}
	return fmt.Sprintf("%.0f bps", bps)
}

// FormatBytes formats byte counts into KB/MB/GB.
func FormatBytes(b uint64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.2f GB", float64(b)/(1<<30))
	} else if b >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	} else if b >= 1<<10 {
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}

// GetNetStats extracts telemetry from the underlying PeerConnection.
func (p *Peer) GetNetStats() NetStats {
	if p == nil || p.pc == nil {
		return NetStats{ConnectionType: "未连接", Protocol: "UDP"}
	}
	report := p.pc.GetStats()
	if report == nil {
		return NetStats{ConnectionType: "就绪", Protocol: "UDP"}
	}

	var stats NetStats
	stats.ICEState = p.pc.ICEConnectionState().String()

	var activeLocalID, activeRemoteID string
	var activePair *webrtc.ICECandidatePair
	var activePairStats *webrtc.ICECandidatePairStats
	if sctp := p.pc.SCTP(); sctp != nil {
		if dtls := sctp.Transport(); dtls != nil {
			if iceT := dtls.ICETransport(); iceT != nil {
				if ps, ok := iceT.GetSelectedCandidatePairStats(); ok {
					activeLocalID = ps.LocalCandidateID
					activeRemoteID = ps.RemoteCandidateID
					cp := ps
					activePairStats = &cp
				}
				if pair, err := iceT.GetSelectedCandidatePair(); err == nil && pair != nil {
					activePair = pair
				}
			}
		}
	}

	var selectedPair *webrtc.ICECandidatePairStats
	matchedActive := false
	bestScore := -1
	bestID := ""
	candidates := make(map[string]webrtc.ICECandidateStats)

	pairRank := func(v webrtc.ICECandidatePairStats) int {
		if v.Nominated && v.State == webrtc.StatsICECandidatePairStateSucceeded {
			return 4
		}
		if v.Nominated {
			return 3
		}
		if v.State == webrtc.StatsICECandidatePairStateSucceeded {
			return 2
		}
		if v.State == webrtc.StatsICECandidatePairStateInProgress {
			return 1
		}
		return 0
	}

	for _, s := range report {
		switch v := s.(type) {
		case webrtc.ICECandidatePairStats:
			cp := v
			if activeLocalID != "" && activeRemoteID != "" &&
				cp.LocalCandidateID == activeLocalID && cp.RemoteCandidateID == activeRemoteID {
				selectedPair = &cp
				matchedActive = true
				continue
			}
			if matchedActive {
				continue
			}
			score := pairRank(cp)
			if selectedPair == nil || score > bestScore || (score == bestScore && cp.ID < bestID) {
				selectedPair = &cp
				bestScore = score
				bestID = cp.ID
			}
		case webrtc.ICECandidateStats:
			candidates[v.ID] = v
		case webrtc.InboundRTPStreamStats:
			if v.Kind == "video" {
				stats.PacketsReceived = v.PacketsReceived
				stats.PacketsLost = v.PacketsLost
				stats.PacketsDiscarded = v.PacketsDiscarded
				if v.Jitter > 0 {
					stats.Jitter = time.Duration(v.Jitter * float64(time.Second))
				}
				if v.BytesReceived > 0 {
					stats.BytesReceived = v.BytesReceived
				}
			}
		case webrtc.TransportStats:
			if stats.BytesReceived == 0 && v.BytesReceived > 0 {
				stats.BytesReceived = v.BytesReceived
			}
			if stats.BytesSent == 0 && v.BytesSent > 0 {
				stats.BytesSent = v.BytesSent
			}
		}
	}

	if (!matchedActive || selectedPair == nil) && activePairStats != nil {
		selectedPair = activePairStats
	}

	if selectedPair != nil {
		if selectedPair.CurrentRoundTripTime > 0 {
			stats.RTT = time.Duration(selectedPair.CurrentRoundTripTime * float64(time.Second))
		}
		if selectedPair.BytesReceived > 0 {
			stats.BytesReceived = selectedPair.BytesReceived
		}
		if selectedPair.BytesSent > 0 {
			stats.BytesSent = selectedPair.BytesSent
		}

		local, hasLocal := candidates[selectedPair.LocalCandidateID]
		remote, hasRemote := candidates[selectedPair.RemoteCandidateID]
		if !hasLocal && activePair != nil && activePair.Local != nil {
			local = webrtc.ICECandidateStats{
				IP:            activePair.Local.Address,
				Port:          int32(activePair.Local.Port),
				Protocol:      activePair.Local.Protocol.String(),
				CandidateType: activePair.Local.Typ,
			}
			hasLocal = true
		}
		if !hasRemote && activePair != nil && activePair.Remote != nil {
			remote = webrtc.ICECandidateStats{
				IP:            activePair.Remote.Address,
				Port:          int32(activePair.Remote.Port),
				Protocol:      activePair.Remote.Protocol.String(),
				CandidateType: activePair.Remote.Typ,
			}
			hasRemote = true
		}

		if hasLocal {
			stats.LocalCandidate = fmt.Sprintf("%s:%d (%s/%s)", local.IP, local.Port, local.Protocol, local.CandidateType)
			stats.Protocol = strings.ToUpper(local.Protocol)
		}
		if hasRemote {
			stats.RemoteCandidate = fmt.Sprintf("%s:%d (%s/%s)", remote.IP, remote.Port, remote.Protocol, remote.CandidateType)
		}

		if hasLocal && hasRemote {
			stats.CandidatePair = fmt.Sprintf("%s:%d <-> %s:%d", local.IP, local.Port, remote.IP, remote.Port)
			isLocalRelay := local.CandidateType == webrtc.ICECandidateTypeRelay
			isRemoteRelay := remote.CandidateType == webrtc.ICECandidateTypeRelay
			isSrflx := local.CandidateType == webrtc.ICECandidateTypeSrflx || remote.CandidateType == webrtc.ICECandidateTypeSrflx ||
				local.CandidateType == webrtc.ICECandidateTypePrflx || remote.CandidateType == webrtc.ICECandidateTypePrflx

			if isLocalRelay || isRemoteRelay {
				stats.ConnectionType = "TURN 中继穿透 (Relay)"
			} else if isSrflx {
				stats.ConnectionType = "P2P 公网打洞 (NAT srflx)"
			} else {
				stats.ConnectionType = "P2P 局域网直连 (Host)"
			}
		}
	}

	stats.TransportMode = p.TransportMode()
	stats.TransportReason = p.TransportReason()
	if stats.TransportMode == TransportWebSocket {
		stats.Protocol = "WebSocket (WSS)"
		stats.ConnectionType = "WebSocket 隧道穿透 (WSS)"
	}

	if stats.ConnectionType == "" {
		stats.ConnectionType = "P2P 直连"
	}
	if stats.Protocol == "" {
		if stats.TransportMode == TransportTCP {
			stats.Protocol = "TCP"
		} else {
			stats.Protocol = "UDP"
		}
	}
	return stats
}
