package client

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"godeck/native/hubclient"
	"godeck/native/media"
	"godeck/native/rtc"
	"godeck/native/session"
	"godeck/native/signaling"
	"godeck/native/transfer"
)

type hubState struct {
	client         *hubclient.Client
	cancel         context.CancelFunc
	name           string
	url            string
	user           string
	deviceID       string
	outgoingCancel context.CancelFunc
}

type hubApproval struct {
	id       string
	decision chan signaling.Decision
}

func hubICE(servers []hubclient.ICEServer) []webrtc.ICEServer {
	out := make([]webrtc.ICEServer, 0, len(servers))
	for _, s := range servers {
		out = append(out, webrtc.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	return out
}

func (c *Controller) decideHubTransport(ctx context.Context, h *hubclient.Client, device string, servers []webrtc.ICEServer) (rtc.TransportDecision, bool, error) {
	req := c.Transport()
	probeCtx, probeCancel := context.WithTimeout(ctx, 1100*time.Millisecond)
	defer probeCancel()

	supportsWS := false
	if status, err := h.Status(probeCtx); err == nil {
		for _, t := range status.Transports {
			if t == "websocket" {
				supportsWS = true
				break
			}
		}
	}

	targetKey := "hub:" + device
	udpTarget, tcpTarget := rtc.ExtractProbeTargets(servers)

	if !supportsWS {
		if req == rtc.TransportWebSocket {
			return rtc.TransportDecision{}, false, errors.New("中心服务器暂不支持 WebSocket 隧道模式")
		}
		if req != rtc.TransportAuto {
			return c.advisor.Decide(targetKey, req, rtc.ProbeMetrics{}), false, nil
		}
		metrics := rtc.ProbeMetrics{
			WSAttempted: true,
			WSReachable: false,
		}
		var wg sync.WaitGroup
		if udpTarget != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				metrics.UDPAttempted = true
				metrics.UDPReachable, metrics.UDPRTT, metrics.UDPLoss, metrics.UDPJitter = rtc.ProbeUDPBurst(probeCtx, udpTarget, true)
			}()
		}
		if tcpTarget != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				metrics.TCPAttempted = true
				metrics.TCPReachable, metrics.TCPRTT = rtc.ProbeTCPConnect(probeCtx, tcpTarget)
			}()
		}
		wg.Wait()
		d := c.advisor.Decide(targetKey, req, metrics)
		if d.Selected == rtc.TransportWebSocket {
			d.Selected = rtc.TransportUDP
			d.Reason = "标准 UDP/ICE 连接"
		}
		return d, false, nil
	}

	if req != rtc.TransportAuto {
		return c.advisor.Decide(targetKey, req, rtc.ProbeMetrics{WSDirect: false}), true, nil
	}

	var (
		wg      sync.WaitGroup
		metrics = rtc.ProbeMetrics{WSDirect: false}
	)
	if udpTarget != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			metrics.UDPAttempted = true
			metrics.UDPReachable, metrics.UDPRTT, metrics.UDPLoss, metrics.UDPJitter = rtc.ProbeUDPBurst(probeCtx, udpTarget, true)
		}()
	}
	if tcpTarget != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			metrics.TCPAttempted = true
			metrics.TCPReachable, metrics.TCPRTT = rtc.ProbeTCPConnect(probeCtx, tcpTarget)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		metrics.WSAttempted = true
		metrics.WSReachable, metrics.WSRTT = h.ProbeWS(probeCtx)
	}()
	wg.Wait()

	return c.advisor.Decide(targetKey, req, metrics), true, nil
}

// ConnectHub creates the lease before gathering ICE, then waits for local approval.
func (c *Controller) ConnectHub(ctx context.Context, h *hubclient.Client, device string) error {
	c.mu.Lock()
	busy := c.outgoing != nil
	c.mu.Unlock()
	if busy {
		return errors.New("已有主动连接")
	}

	lease, err := h.CreateSession(ctx, device)
	if err != nil {
		return err
	}
	transferred := false
	defer func() {
		if !transferred {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = h.EndSession(cleanup, lease.ID, false)
		}
	}()

	if c.OnStatus != nil {
		c.OnStatus("正在评估中心节点最佳传输通道...")
	}
	iceServers := hubICE(lease.ICEServers)
	decision, supportsTransport, err := c.decideHubTransport(ctx, h, device, iceServers)
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
		ICEServers:      iceServers,
		Transport:       selected,
		TransportReason: decision.Reason,
		WSTunnel:        wsTun,
	})
	if err != nil {
		cancel()
		return err
	}
	committed := false
	defer func() {
		if !committed {
			cancel()
			v.Close()
		}
	}()

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
	previous := v.Peer.OnState
	v.Peer.OnState = func(state string) {
		if previous != nil {
			previous(state)
		}
		c.mu.Lock()
		current := c.outgoing == v
		c.mu.Unlock()
		if current && c.OnStatus != nil {
			c.OnStatus(state)
		}
	}

	offerCtx, done := context.WithTimeout(ctx, 25*time.Second)
	offer, err := v.Offer(offerCtx)
	done()
	if err != nil {
		return err
	}
	trParam, trReason := "", ""
	if supportsTransport {
		trParam = selected.ConfigString()
		trReason = decision.Reason
	}
	if err = h.SubmitOfferWithTransport(ctx, lease.ID, offer, trParam, trReason); err != nil {
		return err
	}

	for {
		lease, err = h.GetSession(ctx, lease.ID)
		if err != nil {
			return err
		}
		switch lease.State {
		case "ready":
			goto accepted
		case "rejected", "ended", "expired":
			return errors.New("对方拒绝或会话已结束")
		case "pending", "preparing":
		default:
			return errors.New("中心会话状态不可用")
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

accepted:
	targetKey := "hub:" + device
	if selected == rtc.TransportWebSocket && wsTun != nil {
		ws, wsErr := h.DialSessionWS(ctx, lease.ID, false)
		if wsErr != nil {
			c.advisor.RecordResult(targetKey, selected, 0, 1.0, true)
			return wsErr
		}
		if err = wsTun.Attach(ws); err != nil {
			return err
		}
	}
	if err = v.Answer(lease.Answer); err != nil {
		c.advisor.RecordResult(targetKey, selected, 0, 1.0, true)
		return err
	}
	c.mu.Lock()
	if ctx.Err() != nil || c.outgoing != nil {
		c.mu.Unlock()
		return errors.New("连接已取消或已有主动连接")
	}
	c.outgoing = v
	c.outgoingCancel = cancel
	c.outgoingTarget = targetKey
	c.sessionStart = time.Now()
	c.remoteControl = lease.Control
	c.remoteFiles = lease.FilesAllowed
	stopPending()
	c.mu.Unlock()

	committed = true
	transferred = true

	if c.OnEncoder != nil {
		c.OnEncoder(lease.Media.Encoder, "")
	}

	if lease.FilesAllowed {
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
		defer func() {
			c.endOutgoing(v)
			cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
			defer done()
			_ = h.EndSession(cleanup, lease.ID, false)
		}()
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-sessionCtx.Done():
				return
			case <-ticker.C:
				poll, done := context.WithTimeout(sessionCtx, 5*time.Second)
				state, e := h.GetSession(poll, lease.ID)
				done()
				if e == nil && (state.State == "ended" || state.State == "expired" || state.State == "rejected") {
					return
				}
			}
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

// StartHubHost is called only after this device's user explicitly approves.
func (c *Controller) StartHubHost(parent context.Context, h *hubclient.Client, request hubclient.Session, control bool, files bool) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer c.ReleaseIncoming()

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
	if request.Transport != "" {
		parsed := rtc.ParseTransport(request.Transport)
		if parsed != rtc.TransportAuto {
			mode = parsed
		}
	}
	var wsTun *rtc.WSTunnel
	if mode == rtc.TransportWebSocket {
		wsTun = rtc.NewWSTunnel(false)
	}

	host, answer, err := session.StartHostWithTransport(ctx, request.Offer, control, filesAllowed, c.codecChoice(), c.presetChoice(), session.HostOptions{
		ICEServers:      hubICE(request.ICEServers),
		FileHandler:     fileHandler,
		Transport:       mode,
		TransportReason: request.TransportReason,
		WSTunnel:        wsTun,
	})
	if err != nil {
		if wsTun != nil {
			_ = wsTun.Close()
		}
		return err
	}
	defer host.Close()

	c.mu.Lock()
	if ctx.Err() != nil {
		c.mu.Unlock()
		return ctx.Err()
	}
	c.incoming = host
	c.incomingCancel = cancel
	c.sessionStart = time.Now()
	c.mu.Unlock()

	defer func() {
		c.fileMu.Lock()
		if c.fileMgr != nil {
			_ = c.fileMgr.Close()
			c.fileMgr = nil
		}
		c.fileMu.Unlock()
		c.mu.Lock()
		if c.incoming == host {
			c.incoming = nil
			c.incomingCancel = nil
			c.sessionStart = time.Time{}
		}
		c.mu.Unlock()
	}()

	targetW, targetH := host.TargetDimensions()
	info := hubclient.MediaInfo{
		Width:    targetW,
		Height:   targetH,
		Encoder:  host.Codec.String(),
		Hardware: host.Codec == media.VA || host.Codec == media.LegacyVA || host.Codec == media.FFmpegVA,
	}
	if err = h.AnswerSession(ctx, request.ID, request.Nonce, answer, true, control && host.CanControl(), filesAllowed, info); err != nil {
		return err
	}
	if mode == rtc.TransportWebSocket && wsTun != nil {
		go func() {
			dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
			ws, wsErr := h.DialSessionWS(dialCtx, request.ID, true)
			dialCancel()
			if wsErr != nil {
				cancel()
				return
			}
			if attachErr := wsTun.Attach(ws); attachErr != nil {
				cancel()
			}
		}()
	}

	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = h.EndSession(cleanup, request.ID, true)
	}()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-host.Done():
			return nil
		case <-ticker.C:
			poll, done := context.WithTimeout(ctx, 5*time.Second)
			state, e := h.GetDeviceSession(poll, request.ID)
			done()
			if e == nil && (state.State == "ended" || state.State == "expired" || state.State == "rejected") {
				return nil
			}
		}
	}
}

func (c *Controller) HubStatus(ctx context.Context, url string) (bool, error) {
	h, e := hubclient.New(url, hubclient.Options{})
	if e != nil {
		return false, e
	}
	s, e := h.Status(ctx)
	return s.SetupRequired, e
}

func (c *Controller) HubLogin(ctx context.Context, url, user, password, name string) ([]hubclient.Device, error) {
	return c.hubAuthenticate(ctx, url, user, password, name, "")
}

func (c *Controller) HubInitialize(ctx context.Context, url, user, password, name, setup string) ([]hubclient.Device, error) {
	if setup == "" {
		return nil, errors.New("请输入初始化码")
	}
	return c.hubAuthenticate(ctx, url, user, password, name, setup)
}

func (c *Controller) hubAuthenticate(ctx context.Context, url, user, password, name, setup string) ([]hubclient.Device, error) {
	h, e := hubclient.New(url, hubclient.Options{})
	if e != nil {
		return nil, e
	}
	if setup != "" {
		e = h.Initialize(ctx, setup, user, password)
	} else {
		e = h.Login(ctx, user, password)
	}
	if e != nil {
		return nil, e
	}
	devices, e := h.ListDevices(ctx)
	if e != nil {
		return nil, e
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	run, cancel := context.WithCancel(context.Background())
	state := &hubState{
		client: h,
		cancel: cancel,
		name:   name,
		url:    url,
		user:   user,
	}

	// Auto-reuse saved device binding if present
	if d, e := c.loadHubDevice(url, user); e == nil {
		state.deviceID = d.ID
		h.DeviceToken = d.Token
	}

	c.mu.Lock()
	old := c.hub
	c.hub = state
	c.mu.Unlock()

	if old != nil {
		old.cancel()
	}
	if h.DeviceToken != "" {
		go c.hubHeartbeat(run, state)
		go c.hubInvitations(run, state)
	}
	return devices, nil
}

func (c *Controller) HubPair(ctx context.Context) error {
	c.mu.Lock()
	h := c.hub
	c.mu.Unlock()
	if h == nil {
		return errors.New("请先登录中心")
	}
	if h.client.DeviceToken != "" {
		return errors.New("本机已经绑定")
	}
	code, e := h.client.CreatePairCode(ctx)
	if e != nil {
		return e
	}
	pair, e := h.client.Pair(ctx, hubclient.PairRequest{
		Code:     code.Code,
		Name:     h.name,
		Platform: runtime.GOOS,
	})
	if e != nil {
		return e
	}
	if e = c.saveHubDevice(savedHubDevice{
		URL:   h.url,
		User:  h.user,
		ID:    pair.DeviceID,
		Token: pair.DeviceToken,
	}); e != nil {
		h.client.DeviceToken = ""
		return e
	}
	run, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	h.deviceID = pair.DeviceID
	old := h.cancel
	h.cancel = cancel
	c.mu.Unlock()
	if old != nil {
		old()
	}
	go c.hubHeartbeat(run, h)
	go c.hubInvitations(run, h)
	return nil
}

func (c *Controller) HubDevices(ctx context.Context) ([]hubclient.Device, error) {
	c.mu.Lock()
	h := c.hub
	c.mu.Unlock()
	if h == nil {
		return nil, errors.New("请先登录中心")
	}
	return h.client.ListDevices(ctx)
}

func (c *Controller) HubLocalID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hub == nil {
		return ""
	}
	return c.hub.deviceID
}

func (c *Controller) HubRename(ctx context.Context, id, name string) error {
	c.mu.Lock()
	h := c.hub
	c.mu.Unlock()
	if h == nil {
		return errors.New("请先登录中心")
	}
	return h.client.RenameDevice(ctx, id, name)
}

func (c *Controller) HubRevoke(ctx context.Context, id string) error {
	c.mu.Lock()
	h := c.hub
	c.mu.Unlock()
	if h == nil {
		return errors.New("请先登录中心")
	}
	if e := h.client.RevokeDevice(ctx, id); e != nil {
		return e
	}
	c.mu.Lock()
	match := h.deviceID == id
	var revokeCancel context.CancelFunc
	if match {
		revokeCancel = h.cancel
		h.client.DeviceToken = ""
		h.deviceID = ""
	}
	c.mu.Unlock()
	if match {
		if revokeCancel != nil {
			revokeCancel()
		}
		c.deleteHubDevice()
	}
	return nil
}

func (c *Controller) HubLogout(ctx context.Context) error {
	c.mu.Lock()
	h := c.hub
	c.hub = nil
	var cancelFn, outCancel context.CancelFunc
	if h != nil {
		cancelFn = h.cancel
		outCancel = h.outgoingCancel
	}
	c.mu.Unlock()
	if h == nil {
		return nil
	}
	if cancelFn != nil {
		cancelFn()
	}
	if outCancel != nil {
		outCancel()
	}
	return h.client.Logout(ctx)
}

func (c *Controller) stopHub() {
	c.mu.Lock()
	h := c.hub
	c.hub = nil
	var cancelFn, outCancel context.CancelFunc
	if h != nil {
		cancelFn = h.cancel
		outCancel = h.outgoingCancel
	}
	c.mu.Unlock()
	if cancelFn != nil {
		cancelFn()
	}
	if outCancel != nil {
		outCancel()
	}
}

func (c *Controller) HubConnect(ctx context.Context, id string) error {
	c.mu.Lock()
	h := c.hub
	if h == nil {
		c.mu.Unlock()
		return errors.New("请先登录中心")
	}
	connect, cancel := context.WithCancel(ctx)
	h.outgoingCancel = cancel
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		h.outgoingCancel = nil
		c.mu.Unlock()
	}()
	return c.ConnectHub(connect, h.client, id)
}

func (c *Controller) hubHeartbeat(ctx context.Context, h *hubState) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		c.mu.Lock()
		accept := c.accepting
		c.mu.Unlock()
		_ = h.client.Heartbeat(ctx, hubclient.HeartbeatRequest{
			Name:         h.name,
			Platform:     runtime.GOOS,
			Accepting:    accept,
			Capabilities: []string{"h264", "control", "files"},
		})
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Controller) hubInvitations(ctx context.Context, h *hubState) {
	for {
		inv, e := h.client.WaitInvitations(ctx)
		if e != nil {
			if ctx.Err() != nil {
				return
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		c.hubReceive(ctx, h, inv)
	}
}

func (c *Controller) hubReceive(ctx context.Context, h *hubState, inv hubclient.Session) {
	c.mu.Lock()
	accept := c.accepting && c.hub == h
	c.mu.Unlock()

	if !accept || !c.ReserveIncoming() {
		_ = h.client.AnswerSession(ctx, inv.ID, inv.Nonce, "", false, false, false, hubclient.MediaInfo{})
		return
	}

	p := &hubApproval{
		id:       inv.ID,
		decision: make(chan signaling.Decision, 1),
	}
	c.mu.Lock()
	c.hubPending = p
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if c.hubPending == p {
			c.hubPending = nil
		}
		c.mu.Unlock()
	}()

	if c.OnRequest != nil {
		c.OnRequest(signaling.Request{
			ID:       inv.ID,
			Name:     "中心账号设备",
			Remote:   "Hub",
			Deadline: inv.Expires,
		})
	}

	timer := time.NewTimer(time.Until(inv.Expires))
	defer timer.Stop()

	var d signaling.Decision
	select {
	case d = <-p.decision:
	case <-ctx.Done():
		c.ReleaseIncoming()
		return
	case <-timer.C:
		c.ReleaseIncoming()
		_ = h.client.AnswerSession(ctx, inv.ID, inv.Nonce, "", false, false, false, hubclient.MediaInfo{})
		return
	}

	if !d.Accept {
		c.ReleaseIncoming()
		_ = h.client.AnswerSession(ctx, inv.ID, inv.Nonce, "", false, false, false, hubclient.MediaInfo{})
		return
	}

	if e := c.StartHubHost(ctx, h.client, inv, d.Control, d.Files); e != nil {
		_ = h.client.AnswerSession(ctx, inv.ID, inv.Nonce, "", false, false, false, hubclient.MediaInfo{})
	}
}
