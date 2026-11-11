package client

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"golang.org/x/net/websocket"
	"godeck/native/clipboard"
	"godeck/native/media"
	"godeck/native/rtc"
	"godeck/native/session"
	"godeck/native/signaling"
	"godeck/native/transfer"
)

type pendingWS struct {
	tun  *rtc.WSTunnel
	done chan struct{}
}

type Controller struct {
	mu               sync.Mutex
	identity         signaling.Identity
	host             *signaling.Host
	server           *http.Server
	listener         net.Listener
	udpProbeCancel   context.CancelFunc
	incoming         *session.Host
	outgoing         *session.Viewer
	incomingCancel   context.CancelFunc
	outgoingCancel   context.CancelFunc
	outgoingTarget   string
	sessionStart     time.Time
	rateTracker      rtc.NetRateTracker
	codec            media.Codec
	preset           media.Preset
	transport        rtc.TransportMode
	advisor          *rtc.TransportAdvisor
	lastDecision     rtc.TransportDecision
	wsPending        map[string]*pendingWS
	remoteControl    bool
	remoteFiles      bool
	allowFilesHost   bool
	incomingReserved bool
	accepting        bool
	dir              string
	shareDir         string
	hub              *hubState
	hubPending       *hubApproval
	fileMgr          *transfer.Manager
	fileMu           sync.Mutex
	clipHost         *clipboard.Sync
	clipViewer       *clipboard.Sync
	clipMu           sync.Mutex
	OnRequest        func(signaling.Request)
	OnStatus         func(string)
	OnDecoder        func(media.Codec, string)
	OnEncoder        func(string, string)
	OnFrame          func(media.Sample)
	OnFileEvent      func(transfer.Event)
	OnClipboardEvent func(ClipboardRole, clipboard.Event)
}

func New(dir string, codec media.Codec) (*Controller, error) {
	identity, err := signaling.LoadIdentity(dir)
	if err != nil {
		return nil, err
	}
	host, err := signaling.NewHost()
	if err != nil {
		return nil, err
	}
	shareDir := filepath.Join(dir, "share")
	_ = os.MkdirAll(shareDir, 0700)

	c := &Controller{
		identity:  identity,
		host:      host,
		codec:     codec,
		preset:    media.Preset720p30,
		transport: rtc.TransportAuto,
		advisor:   rtc.NewTransportAdvisor(),
		wsPending: make(map[string]*pendingWS),
		dir:       dir,
		shareDir:  shareDir,
	}
	host.OnReserve = c.ReserveIncoming
	host.OnRelease = c.ReleaseIncoming
	host.OnRequest = func(r signaling.Request) {
		if c.OnRequest != nil {
			c.OnRequest(r)
		}
	}
	host.OnOffer = c.acceptOffer
	host.OnWSConnect = func(requestID string, ws *websocket.Conn) <-chan struct{} {
		c.mu.Lock()
		pw := c.wsPending[requestID]
		c.mu.Unlock()
		if pw == nil {
			return nil
		}
		_ = pw.tun.Attach(ws)
		return pw.done
	}
	return c, nil
}

func (c *Controller) Listen(address string) error {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.server != nil {
		c.mu.Unlock()
		ln.Close()
		return errors.New("监听已经启动")
	}
	server := &http.Server{
		Handler:           c.host,
		ReadHeaderTimeout: 8 * time.Second,
		IdleTimeout:       30 * time.Second,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{c.identity.Certificate},
			MinVersion:   tls.VersionTLS13,
		},
	}
	c.server = server
	c.listener = ln
	c.accepting = true
	c.host.SetAccepting(true)
	if pc, udpErr := net.ListenPacket("udp", ln.Addr().String()); udpErr == nil {
		udpCtx, udpCancel := context.WithCancel(context.Background())
		c.udpProbeCancel = udpCancel
		go signaling.ServeUDPProbe(udpCtx, pc)
	}
	c.mu.Unlock()

	go func() {
		_ = server.ServeTLS(ln, "", "")
	}()
	return nil
}

func (c *Controller) Address() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listener == nil {
		return ""
	}
	return c.listener.Addr().String()
}

func (c *Controller) Fingerprint() string {
	return c.identity.Fingerprint
}

func (c *Controller) Secret() (string, time.Time) {
	return c.host.Secret()
}

func (c *Controller) AllowIncoming(allow bool) {
	c.mu.Lock()
	c.accepting = allow
	c.mu.Unlock()
	c.host.SetAccepting(allow)
	if !allow {
		c.EndIncoming()
	}
}

func (c *Controller) Accepting() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.accepting
}

// IncomingActive returns true if an incoming host session is currently connected.
func (c *Controller) IncomingActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.incoming != nil
}

func (c *Controller) Decide(requestID string, allow bool, control bool, files bool, clipboard bool) error {
	d := signaling.Decision{Accept: allow, Control: control, Files: files, Clipboard: clipboard}
	c.mu.Lock()
	p := c.hubPending
	c.mu.Unlock()
	if p != nil && p.id == requestID {
		select {
		case p.decision <- d:
			return nil
		default:
			return errors.New("决策已提交或审批已超时")
		}
	}
	if !c.host.Decide(requestID, d) {
		return errors.New("决策提交失败或审批已超时")
	}
	return nil
}

func (c *Controller) SetCodec(codec media.Codec) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.codec = codec
}

func (c *Controller) Codec() media.Codec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.codec
}

func (c *Controller) SetPreset(preset media.Preset) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.preset = preset
}

func (c *Controller) Preset() media.Preset {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.preset
}

func (c *Controller) SetTransport(mode rtc.TransportMode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transport = rtc.ParseTransport(string(mode))
}

func (c *Controller) Transport() rtc.TransportMode {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transport == "" {
		return rtc.TransportAuto
	}
	return c.transport
}

func (c *Controller) LastTransportDecision() rtc.TransportDecision {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastDecision
}

func (c *Controller) SetShareDir(dir string) {
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	c.shareDir = dir
}

func (c *Controller) ShareDir() string {
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	return c.shareDir
}

func (c *Controller) SetAllowFilesHost(allow bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.allowFilesHost = allow
}

func (c *Controller) AllowFilesHost() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.allowFilesHost
}

func (c *Controller) downloadDir() string {
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	if c.shareDir != "" {
		return c.shareDir
	}
	return c.dir
}

func (c *Controller) codecChoice() media.Codec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.codec
}

func (c *Controller) presetChoice() media.Preset {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.preset
}

func (c *Controller) acceptOffer(ctx context.Context, offer signaling.Offer, d signaling.Decision) (signaling.Answer, error) {
	control, files := d.Control, d.Files
	sessionCtx, cancel := context.WithCancel(ctx)
	clipHandler := c.hostClipboardHandler(d.Clipboard)
	filesAllowed := files && c.AllowFilesHost()
	var fileHandler func(*webrtc.DataChannel)

	if filesAllowed {
		fileHandler = func(dc *webrtc.DataChannel) {
			c.fileMu.Lock()
			c.fileMgr = transfer.New(dc, transfer.Host, c.ShareDir(), func() bool {
				return c.AllowFilesHost()
			}, func(e transfer.Event) {
				if c.OnFileEvent != nil {
					c.OnFileEvent(e)
				}
			})
			c.fileMu.Unlock()
		}
	}

	mode := rtc.TransportUDP
	if offer.Transport != "" {
		parsed := rtc.ParseTransport(offer.Transport)
		if parsed != rtc.TransportAuto {
			mode = parsed
		}
	}

	var wsTun *rtc.WSTunnel
	var wsDone chan struct{}
	if mode == rtc.TransportWebSocket && offer.RequestID != "" {
		wsTun = rtc.NewWSTunnel(false)
		wsDone = make(chan struct{})
		c.mu.Lock()
		c.wsPending[offer.RequestID] = &pendingWS{tun: wsTun, done: wsDone}
		c.mu.Unlock()
	}

	cleanupWS := func() {
		if offer.RequestID != "" && wsDone != nil {
			c.mu.Lock()
			delete(c.wsPending, offer.RequestID)
			c.mu.Unlock()
			close(wsDone)
			wsDone = nil
		}
	}

	h, answer, err := session.StartHostWithTransport(sessionCtx, offer.SDP, control, filesAllowed, c.codecChoice(), c.presetChoice(), session.HostOptions{
		FileHandler:      fileHandler,
		ClipboardHandler: clipHandler,
		Transport:       mode,
		TransportReason: offer.TransportReason,
		WSTunnel:        wsTun,
	})
	if err != nil {
		cleanupWS()
		cancel()
		return signaling.Answer{}, err
	}
	c.mu.Lock()
	if sessionCtx.Err() != nil {
		c.mu.Unlock()
		cleanupWS()
		h.Close()
		cancel()
		return signaling.Answer{}, errors.New("来访已取消")
	}
	c.incoming = h
	c.incomingCancel = cancel
	c.sessionStart = time.Now()
	c.mu.Unlock()

	targetW, targetH := h.TargetDimensions()
	go func() {
		select {
		case <-sessionCtx.Done():
		case <-h.Done():
		}
		c.host.End()
		cancel()
		cleanupWS()
		h.Close()
		c.fileMu.Lock()
		if c.fileMgr != nil {
			_ = c.fileMgr.Close()
			c.fileMgr = nil
		}
		c.fileMu.Unlock()
		c.closeHostClipboard()
		c.mu.Lock()
		if c.incoming == h {
			c.incoming = nil
			c.incomingCancel = nil
			c.sessionStart = time.Time{}
		}
		c.mu.Unlock()
	}()

	return signaling.Answer{
		SDP:             answer,
		Control:         control && h.CanControl(),
		FilesAllowed:    filesAllowed,
		Encoder:         h.Codec.String(),
		EncoderDevice:   h.Device,
		Width:           targetW,
		Height:          targetH,
		Preset:          h.Preset.String(),
		Transport:       mode.ConfigString(),
		TransportReason: offer.TransportReason,
	}, nil
}

func (c *Controller) EndIncoming() {
	c.host.End()
	c.mu.Lock()
	cancel := c.incomingCancel
	p := c.hubPending
	c.mu.Unlock()
	if p != nil {
		select {
		case p.decision <- signaling.Decision{}:
		default:
		}
	}
	if cancel != nil {
		cancel()
	}
}

func (c *Controller) Probe(ctx context.Context, address string) (string, error) {
	return signaling.ProbeFingerprint(ctx, address)
}

func (c *Controller) decideDirectTransport(ctx context.Context, address, pin, secret string) (rtc.TransportDecision, error) {
	req := c.Transport()
	probeCtx, probeCancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer probeCancel()

	caps, err := signaling.ProbeCapabilities(probeCtx, address, pin, secret)
	if err != nil {
		if req != rtc.TransportAuto && req != rtc.TransportUDP {
			return rtc.TransportDecision{}, errors.New("对端暂不支持 " + req.String() + " 传输模式")
		}
		return rtc.TransportDecision{
			Requested: req,
			Selected:  rtc.TransportUDP,
			Ranked:    []rtc.TransportMode{rtc.TransportUDP},
			Reason:    "标准 UDP 连接",
		}, nil
	}

	if req != rtc.TransportAuto {
		return c.advisor.Decide(address, req, rtc.ProbeMetrics{WSDirect: true}), nil
	}

	var (
		wg      sync.WaitGroup
		metrics = rtc.ProbeMetrics{WSDirect: true}
	)
	if caps.UDPProbe {
		wg.Add(1)
		go func() {
			defer wg.Done()
			metrics.UDPAttempted = true
			metrics.UDPReachable, metrics.UDPRTT, metrics.UDPLoss, metrics.UDPJitter = rtc.ProbeUDPBurst(probeCtx, address, false)
		}()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		metrics.TCPAttempted = true
		metrics.TCPReachable, metrics.TCPRTT = rtc.ProbeTCPConnect(probeCtx, address)
	}()
	go func() {
		defer wg.Done()
		metrics.WSAttempted = true
		metrics.WSReachable, metrics.WSRTT = signaling.ProbeWS(probeCtx, address, pin, secret)
	}()
	wg.Wait()

	return c.advisor.Decide(address, req, metrics), nil
}

// Connect must be called only after the user confirms the fingerprint returned by Probe.
func (c *Controller) Connect(ctx context.Context, address, pin, key, name string) error {
	c.mu.Lock()
	if c.outgoing != nil {
		c.mu.Unlock()
		return errors.New("已有主动连接")
	}
	c.mu.Unlock()

	if c.OnStatus != nil {
		c.OnStatus("正在评估最佳传输通道...")
	}
	decision, err := c.decideDirectTransport(ctx, address, pin, key)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.lastDecision = decision
	c.mu.Unlock()

	selected := decision.Selected
	sessionCtx, cancel := context.WithCancel(context.Background())
	stopPending := context.AfterFunc(ctx, cancel)
	defer stopPending()

	var wsTun *rtc.WSTunnel
	if selected == rtc.TransportWebSocket {
		wsTun = rtc.NewWSTunnel(true)
	}

	v, err := session.NewViewerWithOptions(sessionCtx, c.codecChoice(), session.ViewerOptions{
		Transport:       selected,
		TransportReason: decision.Reason,
		WSTunnel:        wsTun,
	})
	if err != nil {
		cancel()
		return err
	}
	v.OnDecoder = func(codec media.Codec, dev string) {
		if c.OnDecoder != nil {
			c.OnDecoder(codec, dev)
		}
	}
	v.OnFailure = func(err error) {
		if c.OnStatus != nil {
			c.OnStatus(err.Error())
		}
	}
	previousState := v.Peer.OnState
	v.Peer.OnState = func(state string) {
		if previousState != nil {
			previousState(state)
		}
		c.mu.Lock()
		current := c.outgoing == v
		c.mu.Unlock()
		if current && c.OnStatus != nil {
			c.OnStatus(state)
		}
	}

	committed := false
	defer func() {
		if !committed {
			cancel()
			v.Close()
		}
	}()

	gatherCtx, done := context.WithTimeout(ctx, 20*time.Second)
	offer, err := v.Offer(gatherCtx)
	done()
	if err != nil {
		return err
	}
	requestID, err := signaling.NewSecret()
	if err != nil {
		return err
	}
	answer, err := signaling.Connect(ctx, address, pin, key, signaling.Offer{
		RequestID:       requestID,
		Name:            name,
		SDP:             offer,
		Transport:       selected.ConfigString(),
		TransportReason: decision.Reason,
	})
	if err != nil {
		if c.Transport() == rtc.TransportAuto {
			c.advisor.RecordResult(address, selected, 0, 1.0, true)
		}
		return err
	}
	if selected == rtc.TransportWebSocket && wsTun != nil {
		ws, wsErr := signaling.DialWebSocket(ctx, address, pin, key, requestID)
		if wsErr != nil {
			c.advisor.RecordResult(address, selected, 0, 1.0, true)
			return wsErr
		}
		if err = wsTun.Attach(ws); err != nil {
			return err
		}
	}
	if err = v.Answer(answer.SDP); err != nil {
		return err
	}
	if c.OnEncoder != nil {
		c.OnEncoder(answer.Encoder, answer.EncoderDevice)
	}

	c.mu.Lock()
	if ctx.Err() != nil {
		c.mu.Unlock()
		return ctx.Err()
	}
	if c.outgoing != nil {
		c.mu.Unlock()
		return errors.New("已有主动连接")
	}
	c.outgoing = v
	c.outgoingCancel = cancel
	c.outgoingTarget = address
	c.sessionStart = time.Now()
	c.remoteControl = answer.Control
	c.remoteFiles = answer.FilesAllowed
	stopPending()
	c.mu.Unlock()

	committed = true

	c.openViewerClipboard(v)

	if answer.FilesAllowed {
		if dc, e := v.OpenFiles(); e == nil {
			c.fileMu.Lock()
			c.fileMgr = transfer.New(dc, transfer.Viewer, c.downloadDir(), func() bool {
				return c.RemoteFilesAllowed()
			}, func(ev transfer.Event) {
				if c.OnFileEvent != nil {
					c.OnFileEvent(ev)
				}
			})
			c.fileMu.Unlock()
		}
	}

	go func() {
		<-sessionCtx.Done()
		c.mu.Lock()
		current := c.outgoing == v
		c.mu.Unlock()
		if current {
			c.endOutgoing(v)
		}
	}()
	go func() {
		for {
			select {
			case <-sessionCtx.Done():
				return
			case f := <-v.Frames:
				c.mu.Lock()
				current := c.outgoing == v
				c.mu.Unlock()
				if current && c.OnFrame != nil {
					c.OnFrame(f)
				}
			}
		}
	}()
	return nil
}

func (c *Controller) endOutgoing(v *session.Viewer) {
	c.mu.Lock()
	if v == nil || c.outgoing != v {
		c.mu.Unlock()
		return
	}
	cancel := c.outgoingCancel
	target := c.outgoingTarget
	c.outgoing = nil
	c.outgoingCancel = nil
	c.outgoingTarget = ""
	c.sessionStart = time.Time{}
	c.remoteControl = false
	c.remoteFiles = false
	c.mu.Unlock()

	if v.Peer != nil && target != "" {
		ns := v.Peer.GetNetStats()
		var loss float64
		if ns.PacketsReceived > 0 && ns.PacketsLost > 0 {
			loss = float64(ns.PacketsLost) / float64(uint64(ns.PacketsReceived)+uint64(ns.PacketsLost))
		}
		failed := ns.ICEState == "failed" || (ns.PacketsReceived == 0 && ns.BytesReceived == 0 && ns.TransportMode != rtc.TransportWebSocket)
		c.advisor.RecordResult(target, ns.TransportMode, ns.RTT, loss, failed)
	}

	c.fileMu.Lock()
	if c.fileMgr != nil {
		_ = c.fileMgr.Close()
		c.fileMgr = nil
	}
	c.fileMu.Unlock()
	c.closeViewerClipboard()

	if cancel != nil {
		cancel()
	}
	v.Close()
}

func (c *Controller) OutgoingEnder() func() {
	c.mu.Lock()
	v := c.outgoing
	c.mu.Unlock()
	return func() {
		c.endOutgoing(v)
	}
}

func (c *Controller) EndOutgoing() {
	c.OutgoingEnder()()
}

func (c *Controller) Close() {
	c.stopHub()
	c.AllowIncoming(false)
	c.EndOutgoing()
	c.mu.Lock()
	srv := c.server
	c.server = nil
	udpCancel := c.udpProbeCancel
	c.udpProbeCancel = nil
	c.mu.Unlock()
	if udpCancel != nil {
		udpCancel()
	}
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

func (c *Controller) RemoteControlAllowed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remoteControl
}

func (c *Controller) RemoteFilesAllowed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remoteFiles
}

func (c *Controller) FileManager() *transfer.Manager {
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	return c.fileMgr
}

func (c *Controller) Input(e rtc.Input) error {
	c.mu.Lock()
	v, allowed := c.outgoing, c.remoteControl
	c.mu.Unlock()
	if v == nil || !allowed {
		return errors.New("对端未授权操作")
	}
	return v.Input(e)
}

// inputFor prevents an old pump from sending into a later connection.
func (c *Controller) inputFor(v *session.Viewer, e rtc.Input) error {
	c.mu.Lock()
	allowed := v != nil && c.outgoing == v && c.remoteControl
	c.mu.Unlock()
	if !allowed {
		return errors.New("会话已结束")
	}
	return v.Input(e)
}

// ReserveIncoming coordinates direct and hub requests before local approval.
func (c *Controller) ReserveIncoming() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.incomingReserved {
		return false
	}
	c.incomingReserved = true
	return true
}

func (c *Controller) ReleaseIncoming() {
	c.mu.Lock()
	c.incomingReserved = false
	c.mu.Unlock()
}

func fillNetStats(stats *rtc.SessionStats, net rtc.NetStats) {
	stats.RTT = net.RTT
	stats.Jitter = net.Jitter
	stats.TotalBytesRecv = net.BytesReceived
	stats.TotalBytesSent = net.BytesSent
	stats.PacketsReceived = net.PacketsReceived
	stats.PacketsLost = net.PacketsLost
	stats.PacketsDiscarded = net.PacketsDiscarded
	stats.ConnectionType = net.ConnectionType
	stats.Protocol = net.Protocol
	stats.TransportMode = net.TransportMode.String()
	stats.TransportReason = net.TransportReason
	stats.LocalCandidate = net.LocalCandidate
	stats.RemoteCandidate = net.RemoteCandidate
	stats.CandidatePair = net.CandidatePair
	stats.ICEState = net.ICEState
}

// GetSessionStats aggregates real-time session telemetry for the viewer or host.
func (c *Controller) GetSessionStats() rtc.SessionStats {
	c.mu.Lock()
	v := c.outgoing
	h := c.incoming
	start := c.sessionStart
	c.mu.Unlock()

	var stats rtc.SessionStats
	if v == nil && h == nil {
		return stats
	}
	stats.Active = true
	if !start.IsZero() {
		stats.Duration = time.Since(start)
	}

	if v != nil {
		net := v.Peer.GetNetStats()
		fillNetStats(&stats, net)

		stats.DecodeFPS, stats.DecodeDuration = v.GetDecodeStats()

		hostTelem := v.Peer.LastTelemetry()
		if hostTelem.EncodeFPS > 0 {
			stats.EncodeFPS = hostTelem.EncodeFPS
			stats.EncodeDuration = time.Duration(hostTelem.EncodeMs * float64(time.Millisecond))
			stats.EncodeBitrate = hostTelem.EncodeBitrate
		}

		stats.DownBitrateBps, stats.UpBitrateBps, stats.DownBytesPerSec, stats.UpBytesPerSec = c.rateTracker.Update(net.BytesReceived, net.BytesSent)

		totalExpected := uint64(net.PacketsReceived)
		if net.PacketsLost > 0 {
			totalExpected += uint64(net.PacketsLost)
		}
		if totalExpected > 0 && net.PacketsLost > 0 {
			stats.PacketLossRate = float64(net.PacketsLost) / float64(totalExpected) * 100.0
		}
		if net.PacketsReceived > 0 && net.PacketsDiscarded > 0 {
			stats.DiscardRate = float64(net.PacketsDiscarded) / float64(net.PacketsReceived) * 100.0
		}
		return stats
	}

	if h != nil {
		net := h.Peer.GetNetStats()
		fillNetStats(&stats, net)

		stats.EncodeFPS, stats.EncodeDuration, stats.EncodeBitrate = h.GetEncodeStats()
		stats.DownBitrateBps, stats.UpBitrateBps, stats.DownBytesPerSec, stats.UpBytesPerSec = c.rateTracker.Update(net.BytesReceived, net.BytesSent)
		return stats
	}

	return stats
}
