// Package rtc owns a WebRTC peer and bounded video/input transport.
package rtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/rtcp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
)

// PeerOptions configures WebRTC transport strategy, ICE servers, and optional WebSocket tunnel.
type PeerOptions struct {
	Offerer         bool
	AllowInput      bool
	ICEServers      []webrtc.ICEServer
	Transport       TransportMode
	TransportReason string
	WSTunnel        *WSTunnel
}

type Input struct {
	Type string  `json:"type"`
	Code int32   `json:"code,omitempty"`
	Down bool    `json:"down,omitempty"`
	X    float64 `json:"x,omitempty"`
	Y    float64 `json:"y,omitempty"`
	Axis float64 `json:"axis,omitempty"`
}

type Peer struct {
	pc              *webrtc.PeerConnection
	track           *webrtc.TrackLocalStaticSample
	control         *webrtc.DataChannel
	pointer         *webrtc.DataChannel
	mu              sync.RWMutex
	remoteSSRC      uint32
	allowInput      bool
	allowFiles      bool
	fileHandler     func(*webrtc.DataChannel)
	clipHandler     func(*webrtc.DataChannel)
	negotiatedLevel int
	lastTelemetry   HostTelemetry
	lastPLI         time.Time
	transportMode   TransportMode
	transportReason string
	tcpMux          ice.TCPMux
	tcpListener     net.Listener
	wsTun           *WSTunnel
	connectedCh     chan struct{}
	connectedOnce   sync.Once
	failedCh        chan struct{}
	failedOnce      sync.Once
	OnInput         func(Input)
	OnSample        func([]byte, time.Duration)
	OnState         func(string)
	OnKeyframe      func()
	OnTelemetry     func(HostTelemetry)
}

func New(offerer, allowInput bool) (*Peer, error) {
	return NewWithICE(offerer, allowInput, nil)
}

// NewWithICE configures explicit MediaEngine H.264 profiles (Level 4.0 for 1080p and Level 3.1 for 720p)
// and applies STUN/TURN configuration before SDP gathering starts.
func NewWithICE(offerer, allowInput bool, servers []webrtc.ICEServer) (*Peer, error) {
	return NewWithOptions(PeerOptions{
		Offerer:    offerer,
		AllowInput: allowInput,
		ICEServers: servers,
		Transport:  TransportUDP,
	})
}

// NewWithOptions initializes a PeerConnection with explicit transport (UDP / TCP / WebSocket)
// and optional fallback configuration.
func NewWithOptions(opts PeerOptions) (*Peer, error) {
	m := &webrtc.MediaEngine{}
	feedback := []webrtc.RTCPFeedback{
		{Type: "goog-remb", Parameter: ""},
		{Type: "ccm", Parameter: "fir"},
		{Type: "nack", Parameter: ""},
		{Type: "nack", Parameter: "pli"},
	}

	// Register H.264 Level 4.0 (1080p30, profile-level-id=42e028)
	_ = m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeH264,
			ClockRate:    90000,
			Channels:     0,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e028",
			RTCPFeedback: feedback,
		},
		PayloadType: 102,
	}, webrtc.RTPCodecTypeVideo)

	// Register H.264 Level 3.1 (720p30, profile-level-id=42e01f)
	_ = m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeH264,
			ClockRate:    90000,
			Channels:     0,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
			RTCPFeedback: feedback,
		},
		PayloadType: 104,
	}, webrtc.RTPCodecTypeVideo)

	// Register H.264 Level 3.1 compatible variant (profile-level-id=42001f)
	_ = m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeH264,
			ClockRate:    90000,
			Channels:     0,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
			RTCPFeedback: feedback,
		},
		PayloadType: 106,
	}, webrtc.RTPCodecTypeVideo)

	mode := opts.Transport
	if mode == "" || mode == TransportAuto {
		mode = TransportUDP
	}

	se := webrtc.SettingEngine{}
	servers := FilterICEServers(opts.ICEServers, mode)
	var tcpMux ice.TCPMux
	var tcpListener net.Listener
	wsTun := opts.WSTunnel

	switch mode {
	case TransportTCP:
		se.SetNetworkTypes([]webrtc.NetworkType{
			webrtc.NetworkTypeTCP4,
			webrtc.NetworkTypeTCP6,
		})
		l, err := net.ListenTCP("tcp", &net.TCPAddr{Port: 0})
		if err == nil {
			tcpListener = l
			tcpMux = ice.NewTCPMuxDefault(ice.TCPMuxParams{
				Listener:        l,
				ReadBufferSize:  4 << 20,
				WriteBufferSize: 4 << 20,
			})
			se.SetICETCPMux(tcpMux)
		}
	case TransportWebSocket:
		if wsTun == nil {
			wsTun = NewWSTunnel(opts.Offerer)
		}
		se.SetNet(wsTun.Net())
		se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
		se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
		servers = nil
	default:
		mode = TransportUDP
		se.SetNetworkTypes([]webrtc.NetworkType{
			webrtc.NetworkTypeUDP4,
			webrtc.NetworkTypeUDP6,
		})
	}

	cleanupTransport := func() {
		if tcpMux != nil {
			_ = tcpMux.Close()
		}
		if tcpListener != nil {
			_ = tcpListener.Close()
		}
		if wsTun != nil {
			_ = wsTun.Close()
		}
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: servers})
	if err != nil {
		cleanupTransport()
		return nil, err
	}

	p := &Peer{
		pc:              pc,
		allowInput:      opts.AllowInput,
		negotiatedLevel: 31,
		transportMode:   mode,
		transportReason: opts.TransportReason,
		tcpMux:          tcpMux,
		tcpListener:     tcpListener,
		wsTun:           wsTun,
		connectedCh:     make(chan struct{}),
		failedCh:        make(chan struct{}),
	}

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		switch s {
		case webrtc.PeerConnectionStateConnected:
			p.connectedOnce.Do(func() { close(p.connectedCh) })
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			p.failedOnce.Do(func() { close(p.failedCh) })
		}
		if p.OnState != nil {
			p.OnState(s.String())
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		p.attach(dc)
	})
	pc.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if !strings.EqualFold(t.Codec().MimeType, webrtc.MimeTypeH264) {
			return
		}
		go p.receive(t)
	})
	if opts.Offerer {
		if _, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			_ = p.Close()
			return nil, err
		}
		ordered := true
		p.control, err = pc.CreateDataChannel("control", &webrtc.DataChannelInit{Ordered: &ordered})
		if err != nil {
			_ = p.Close()
			return nil, err
		}
		p.attach(p.control)
		ordered = false
		zero := uint16(0)
		p.pointer, err = pc.CreateDataChannel("pointer", &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &zero})
		if err != nil {
			_ = p.Close()
			return nil, err
		}
		p.attach(p.pointer)
	}
	return p, nil
}

func (p *Peer) attach(dc *webrtc.DataChannel) {
	if dc.Label() == "godeck-files" {
		p.mu.RLock()
		allowed, handler := p.allowFiles, p.fileHandler
		p.mu.RUnlock()
		if !allowed || handler == nil {
			dc.Close()
			return
		}
		handler(dc)
		return
	}
	if dc.Label() == ClipboardLabel {
		p.mu.RLock()
		handler := p.clipHandler
		p.mu.RUnlock()
		if handler == nil {
			dc.Close()
			return
		}
		handler(dc)
		return
	}
	if dc.Label() != "control" && dc.Label() != "pointer" {
		dc.Close()
		return
	}
	p.mu.Lock()
	if dc.Label() == "control" {
		p.control = dc
	} else {
		p.pointer = dc
	}
	p.mu.Unlock()
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if len(msg.Data) > 4096 {
			return
		}
		if strings.Contains(string(msg.Data), `"telemetry"`) {
			var telem HostTelemetry
			if json.Unmarshal(msg.Data, &telem) == nil && telem.Type == "telemetry" {
				p.mu.Lock()
				p.lastTelemetry = telem
				p.mu.Unlock()
				if p.OnTelemetry != nil {
					p.OnTelemetry(telem)
				}
				return
			}
		}
		if !p.allowInput {
			return
		}
		var input Input
		if json.Unmarshal(msg.Data, &input) != nil {
			return
		}
		if math.IsNaN(input.X) || math.IsInf(input.X, 0) || math.IsNaN(input.Y) || math.IsInf(input.Y, 0) || math.IsNaN(input.Axis) || math.IsInf(input.Axis, 0) {
			return
		}
		if dc.Label() == "pointer" && input.Type != "move" {
			return
		}
		if dc.Label() == "control" && input.Type == "move" {
			return
		}
		switch input.Type {
		case "move", "key", "button", "wheel", "release":
			if p.OnInput != nil {
				p.OnInput(input)
			}
		}
	})
}

// NegotiateLevel checks remote offer SDP for compatible H.264 profile-level-id and downgrades if remote lacks 1080p level 4.0.
func CheckOfferCompatibility(offerSDP string, desiredLevel int) (negotiatedLevel int, downgraded bool, err error) {
	lower := strings.ToLower(offerSDP)
	has40 := strings.Contains(lower, "42e028")
	has31 := strings.Contains(lower, "42e01f") || strings.Contains(lower, "42001f")

	if !has40 && !has31 {
		return 0, false, errors.New("对端未提供兼容的 H.264 编解码能力 (需 Level 3.1 或 Level 4.0)")
	}

	if desiredLevel == 40 {
		if has40 {
			return 40, false, nil
		}
		// Remote does not support Level 4.0, downgrade to Level 3.1
		return 31, true, nil
	}
	// Desired is 31 (720p)
	return 31, false, nil
}

func (p *Peer) AddVideo() error {
	return p.AddVideoWithProfile(31)
}

func (p *Peer) AddVideoWithProfile(level int) error {
	p.mu.Lock()
	p.negotiatedLevel = level
	p.mu.Unlock()

	profileLevelID := "42e01f"
	if level == 40 {
		profileLevelID = "42e028"
	} else if level == 42 {
		profileLevelID = "42e02a"
	}
	fmtp := fmt.Sprintf("level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=%s", profileLevelID)
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: fmtp,
	}, "screen", "godeck")
	if err != nil {
		return err
	}
	sender, err := p.pc.AddTrack(track)
	if err != nil {
		return err
	}
	p.track = track
	go func() {
		buf := make([]byte, 1500)
		for {
			n, _, err := sender.Read(buf)
			if err != nil {
				return
			}
			packets, err := rtcp.Unmarshal(buf[:n])
			if err != nil {
				continue
			}
			for _, pkt := range packets {
				switch pkt.(type) {
				case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
					if p.OnKeyframe != nil {
						p.OnKeyframe()
					}
				}
			}
		}
	}()
	return nil
}

func (p *Peer) NegotiatedLevel() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.negotiatedLevel
}

func (p *Peer) WriteVideo(data []byte, duration time.Duration) error {
	if p.track == nil {
		return errors.New("视频轨道不可用")
	}
	if duration <= 0 {
		duration = time.Second / 30
	}
	return p.track.WriteSample(media.Sample{Data: data, Duration: duration})
}

func (p *Peer) RequestKeyframe() {
	p.mu.Lock()
	pc := p.pc
	ssrc := p.remoteSSRC
	if pc == nil || ssrc == 0 || time.Since(p.lastPLI) < 250*time.Millisecond {
		p.mu.Unlock()
		return
	}
	p.lastPLI = time.Now()
	p.mu.Unlock()
	_ = pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}})
}

type strictH264Depacketizer struct {
	pkt       codecs.H264Packet
	inFUA     bool
	onCorrupt func()
}

func (d *strictH264Depacketizer) Unmarshal(packet []byte) ([]byte, error) {
	if len(packet) > 0 {
		naluType := packet[0] & 0x1F
		if naluType == 28 {
			if len(packet) < 2 {
				d.pkt = codecs.H264Packet{}
				d.inFUA = false
				if d.onCorrupt != nil {
					d.onCorrupt()
				}
				return nil, errors.New("short FU-A packet")
			}
			isStart := (packet[1] & 0x80) != 0
			isEnd := (packet[1] & 0x40) != 0
			if isStart {
				d.pkt = codecs.H264Packet{}
				d.inFUA = true
			} else if !d.inFUA {
				d.pkt = codecs.H264Packet{}
				if d.onCorrupt != nil {
					d.onCorrupt()
				}
				return nil, errors.New("orphan FU-A continuation packet")
			}
			out, err := d.pkt.Unmarshal(packet)
			if isEnd || err != nil {
				d.inFUA = false
				if err != nil {
					d.pkt = codecs.H264Packet{}
					if d.onCorrupt != nil {
						d.onCorrupt()
					}
				}
			}
			return out, err
		}
		if d.inFUA {
			d.pkt = codecs.H264Packet{}
			d.inFUA = false
		}
	}
	return d.pkt.Unmarshal(packet)
}

func (d *strictH264Depacketizer) IsPartitionHead(payload []byte) bool {
	return d.pkt.IsPartitionHead(payload)
}

func (d *strictH264Depacketizer) IsPartitionTail(marker bool, payload []byte) bool {
	return d.pkt.IsPartitionTail(marker, payload)
}

func (p *Peer) receive(t *webrtc.TrackRemote) {
	p.mu.Lock()
	p.remoteSSRC = uint32(t.SSRC())
	p.lastPLI = time.Now()
	p.mu.Unlock()

	builder := samplebuilder.New(
		512,
		&strictH264Depacketizer{onCorrupt: p.RequestKeyframe},
		90000,
		samplebuilder.WithMaxTimeDelay(600*time.Millisecond),
	)
	ticker := time.NewTicker(500 * time.Millisecond)
	done := make(chan struct{})
	defer close(done)

	// Send initial PLI immediately upon track detection so host generates an IDR frame
	_ = p.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(t.SSRC())}})

	go func() {
		defer ticker.Stop()
		timeout := time.NewTimer(5 * time.Second)
		defer timeout.Stop()
		for {
			select {
			case <-done:
				return
			case <-timeout.C:
				return
			case <-ticker.C:
				_ = p.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(t.SSRC())}})
			}
		}
	}()
	for {
		packet, _, err := t.ReadRTP()
		if err != nil {
			return
		}
		builder.Push(packet)
		for {
			sample := builder.Pop()
			if sample == nil {
				break
			}
			if sample.PrevDroppedPackets > 0 {
				p.RequestKeyframe()
			}
			if p.OnSample != nil {
				p.OnSample(sample.Data, sample.Duration)
			}
		}
	}
}

func (p *Peer) SendInput(input Input) error {
	if math.IsNaN(input.X) || math.IsInf(input.X, 0) || math.IsNaN(input.Y) || math.IsInf(input.Y, 0) || math.IsNaN(input.Axis) || math.IsInf(input.Axis, 0) {
		return errors.New("输入参数包含非有效数值")
	}
	p.mu.RLock()
	dc := p.control
	if input.Type == "move" {
		dc = p.pointer
	}
	p.mu.RUnlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return errors.New("输入通道尚未就绪")
	}
	if dc.BufferedAmount() > 64<<10 {
		return errors.New("输入通道繁忙")
	}
	b, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return dc.Send(b)
}

func (p *Peer) Offer(ctx context.Context) (string, error) {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return "", err
	}
	return p.local(ctx, offer)
}

func (p *Peer) Answer(ctx context.Context, offer string) (string, error) {
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return "", err
	}
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	return p.local(ctx, answer)
}

func (p *Peer) ApplyAnswer(sdp string) error {
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}); err != nil {
		return err
	}
	p.mu.Lock()
	if strings.Contains(strings.ToLower(sdp), "42e028") {
		p.negotiatedLevel = 40
	} else {
		p.negotiatedLevel = 31
	}
	p.mu.Unlock()
	return nil
}

func (p *Peer) local(ctx context.Context, description webrtc.SessionDescription) (string, error) {
	done := webrtc.GatheringCompletePromise(p.pc)
	if err := p.pc.SetLocalDescription(description); err != nil {
		return "", err
	}
	select {
	case <-done:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	local := p.pc.LocalDescription()
	if local == nil {
		return "", errors.New("连接信息不可用")
	}
	return local.SDP, nil
}

func (p *Peer) TransportMode() TransportMode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.transportMode
}

func (p *Peer) TransportReason() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.transportReason
}

func (p *Peer) SetTransportInfo(mode TransportMode, reason string) {
	p.mu.Lock()
	if mode != "" {
		p.transportMode = mode
	}
	p.transportReason = reason
	p.mu.Unlock()
}

func (p *Peer) WaitConnected(ctx context.Context) error {
	select {
	case <-p.connectedCh:
		return nil
	case <-p.failedCh:
		return errors.New("ICE/传输连接建立失败")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Peer) Close() error {
	var err error
	if p.pc != nil {
		err = p.pc.Close()
	}
	if p.tcpMux != nil {
		_ = p.tcpMux.Close()
	}
	if p.tcpListener != nil {
		_ = p.tcpListener.Close()
	}
	if p.wsTun != nil {
		_ = p.wsTun.Close()
	}
	return err
}

// SetFileHandler authorizes a separate reliable file channel for this session.
// Set it before applying remote SDP; authorization must be granted locally.
func (p *Peer) SetFileHandler(allowed bool, handler func(*webrtc.DataChannel)) {
	p.mu.Lock()
	p.allowFiles = allowed
	p.fileHandler = handler
	p.mu.Unlock()
}

// ClipboardLabel names the reliable, ordered clipboard text channel.
const ClipboardLabel = "godeck-clipboard"

// SetClipboardHandler authorizes the clipboard channel for this session. A nil
// handler (the default) means the host closes any clipboard channel the viewer opens.
func (p *Peer) SetClipboardHandler(handler func(*webrtc.DataChannel)) {
	p.mu.Lock()
	p.clipHandler = handler
	p.mu.Unlock()
}

// OpenClipboard opens the clipboard channel from the viewer side. The host
// accepts it only when its user approved clipboard sync for this session.
func (p *Peer) OpenClipboard() (*webrtc.DataChannel, error) {
	ordered := true
	return p.pc.CreateDataChannel(ClipboardLabel, &webrtc.DataChannelInit{Ordered: &ordered})
}

// OpenFiles opens the ordered reliable channel after the host grants permission.
func (p *Peer) OpenFiles() (*webrtc.DataChannel, error) {
	ordered := true
	return p.pc.CreateDataChannel("godeck-files", &webrtc.DataChannelInit{Ordered: &ordered})
}

// LastTelemetry returns the latest host telemetry received by this peer.
func (p *Peer) LastTelemetry() HostTelemetry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastTelemetry
}

// SendTelemetry sends a telemetry update over the control data channel.
func (p *Peer) SendTelemetry(telem HostTelemetry) error {
	p.mu.RLock()
	dc := p.control
	p.mu.RUnlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return nil
	}
	telem.Type = "telemetry"
	b, err := json.Marshal(telem)
	if err != nil {
		return err
	}
	return dc.Send(b)
}
