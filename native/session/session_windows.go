//go:build windows && amd64

package session

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"godeck/native/media"
	"godeck/native/platform/wininput"
	"godeck/native/rtc"
)

type Host struct {
	Encoder  *media.Pipeline
	Peer     *rtc.Peer
	cancel   context.CancelFunc
	once     sync.Once
	done     chan struct{}
	mu       sync.Mutex
	keys     map[int32]bool
	buttons  map[int32]bool
	control  bool
	stopping bool
	first    *media.Sample
	sps      []byte
	pps      []byte
	needKey  bool
	Codec    media.Codec
	Device   string
	Preset   media.Preset
	Files    bool
	targetW  int
	targetH  int
	perf     rtc.PerfTracker
}

func (h *Host) CanControl() bool {
	return true
}

// StartHost asks the desktop for permission before producing an answer.
func StartHost(parent context.Context, offer string, control bool, codec media.Codec) (*Host, string, error) {
	return StartHostFull(parent, offer, control, false, codec, media.Preset720p30, nil, nil)
}

func StartHostWithICE(parent context.Context, offer string, control bool, codec media.Codec, servers []webrtc.ICEServer) (*Host, string, error) {
	return StartHostFull(parent, offer, control, false, codec, media.Preset720p30, servers, nil)
}

func StartHostWithFiles(parent context.Context, offer string, control bool, codec media.Codec, servers []webrtc.ICEServer, files bool, handler func(*webrtc.DataChannel)) (*Host, string, error) {
	return StartHostFull(parent, offer, control, files, codec, media.Preset720p30, servers, handler)
}

func StartHostFull(parent context.Context, offer string, control bool, files bool, codec media.Codec, preset media.Preset, servers []webrtc.ICEServer, handler func(*webrtc.DataChannel)) (*Host, string, error) {
	return StartHostWithTransport(parent, offer, control, files, codec, preset, HostOptions{
		ICEServers:  servers,
		FileHandler: handler,
		Transport:   rtc.TransportUDP,
	})
}

func StartHostWithTransport(parent context.Context, offer string, control bool, files bool, codec media.Codec, preset media.Preset, opts HostOptions) (*Host, string, error) {
	ctx, cancel := context.WithCancel(parent)

	// Check offer SDP compatibility and determine negotiated H.264 level before building encoder pipeline.
	maxWidth, maxHeight, fps, desiredLevel := preset.MaxDimensions()
	negotiatedLevel, downgraded, err := rtc.CheckOfferCompatibility(offer, desiredLevel)
	if err != nil {
		cancel()
		return nil, "", err
	}
	if downgraded {
		preset = media.Preset720p30
		maxWidth, maxHeight, fps, _ = preset.MaxDimensions()
	}

	// Calculate proportional target dimensions based on primary monitor.
	primW, primH := wininput.PrimaryDimensions()
	scale := math.Min(1, math.Min(float64(maxWidth)/float64(primW), float64(maxHeight)/float64(primH)))
	targetWidth := int(float64(primW)*scale) &^ 1
	targetHeight := int(float64(primH)*scale) &^ 1
	if targetWidth < 2 {
		targetWidth = 2
	}
	if targetHeight < 2 {
		targetHeight = 2
	}

	var encoder *media.Pipeline
	var first media.Sample
	selected := codec
	candidates := []media.Codec{codec}

	if codec == media.Auto || codec == media.HardwareRequired {
		candidates = []media.Codec{media.VA}
		if codec == media.Auto {
			candidates = append(candidates, media.Software)
		}
	}
	for _, candidate := range candidates {
		encoder, err = media.NewEncoderWithProfile(targetWidth, targetHeight, fps, negotiatedLevel, candidate)
		if err != nil {
			continue
		}
		probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
		first, err = encoder.Pull(probeCtx)
		probeCancel()
		if err == nil && len(first.Data) > 0 {
			selected = candidate
			break
		}
		encoder.Close()
		encoder = nil
	}
	if encoder == nil {
		cancel()
		if codec == media.HardwareRequired {
			return nil, "", errors.New("要求硬件编码，但 Media Foundation / D3D11 硬件管线不可用或未产生有效数据")
		}
		return nil, "", errors.New("无法启动 H.264 编码；请检查 GStreamer 插件与显卡驱动")
	}

	peer, err := rtc.NewWithOptions(rtc.PeerOptions{
		Offerer:         false,
		AllowInput:      control,
		ICEServers:      opts.ICEServers,
		Transport:       opts.Transport,
		TransportReason: opts.TransportReason,
		WSTunnel:        opts.WSTunnel,
	})
	if err != nil {
		encoder.Close()
		cancel()
		return nil, "", err
	}
	peer.SetFileHandler(files, opts.FileHandler)
	peer.SetClipboardHandler(opts.ClipboardHandler)

	// Dimensions reported in metadata come from actual encoder output if available, otherwise target dimensions.
	actualW, actualH := targetWidth, targetHeight
	if first.Width > 0 && first.Height > 0 {
		actualW, actualH = first.Width, first.Height
	}

	initSPS, initPPS, _, hasInitSPS, hasInitPPS := parseH264NALs(first.Data)
	host := &Host{
		control:  control,
		Encoder:  encoder,
		Peer:     peer,
		cancel:   cancel,
		done:     make(chan struct{}),
		keys:     map[int32]bool{},
		buttons:  map[int32]bool{},
		first:    &first,
		Codec:    selected,
		Preset:   preset,
		Files:    files,
		targetW:  actualW,
		targetH:  actualH,
	}
	if hasInitSPS {
		host.sps = initSPS
	}
	if hasInitPPS {
		host.pps = initPPS
	}
	peer.OnInput = host.input
	peer.OnKeyframe = func() {
		host.mu.Lock()
		defer host.mu.Unlock()
		if !host.stopping {
			host.needKey = true
			encoder.ForceKey()
		}
	}
	peer.OnState = func(state string) {
		if state == "failed" || state == "closed" || state == "disconnected" {
			cancel()
		}
	}
	if err = peer.AddVideoWithProfile(negotiatedLevel); err != nil {
		peer.Close()
		encoder.Close()
		cancel()
		return nil, "", err
	}
	answer, err := peer.Answer(ctx, offer)
	if err != nil {
		peer.Close()
		encoder.Close()
		cancel()
		return nil, "", err
	}
	go host.pump(ctx)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-host.done:
				return
			case <-ticker.C:
				fps, dur, bps := host.perf.Snapshot()
				_ = host.Peer.SendTelemetry(rtc.HostTelemetry{
					EncodeFPS:     fps,
					EncodeMs:      float64(dur.Microseconds()) / 1000.0,
					EncodeBitrate: bps,
					Encoder:       host.Codec.String(),
					Width:         host.targetW,
					Height:        host.targetH,
				})
			}
		}
	}()
	go func() {
		<-ctx.Done()
		host.Close()
	}()
	return host, answer, nil
}

func (h *Host) TargetDimensions() (int, int) {
	return h.targetW, h.targetH
}

func (h *Host) pump(ctx context.Context) {
	defer close(h.done)
	var previous time.Duration
	for {
		var sample media.Sample
		var err error
		t0 := time.Now()
		if h.first != nil {
			sample = *h.first
			h.first = nil
		} else {
			sample, err = h.Encoder.Pull(ctx)
		}
		if err != nil {
			return
		}

		sps, pps, hasIDR, hasSPS, hasPPS := parseH264NALs(sample.Data)
		h.mu.Lock()
		if hasSPS && len(sps) > 0 {
			h.sps = sps
		}
		if hasPPS && len(pps) > 0 {
			h.pps = pps
		}
		if (hasIDR || h.needKey) && (!hasSPS || !hasPPS) && len(h.sps) > 0 && len(h.pps) > 0 {
			sample.Data = append(append(append([]byte{}, h.sps...), h.pps...), sample.Data...)
		}
		if hasIDR {
			h.needKey = false
		}
		h.mu.Unlock()

		h.perf.Record(time.Since(t0), len(sample.Data))
		duration := sample.Timestamp - previous
		previous = sample.Timestamp
		if duration <= 0 || duration > time.Second {
			duration = time.Second / 30
		}
		if err = h.Peer.WriteVideo(sample.Data, duration); err != nil {
			return
		}
	}
}

func (h *Host) input(e rtc.Input) {
	h.mu.Lock()
	if !h.control || h.stopping {
		h.mu.Unlock()
		return
	}
	switch e.Type {
	case "move":
		h.mu.Unlock()
		if math.IsNaN(e.X) || math.IsNaN(e.Y) || e.X < 0 || e.X > 1 || e.Y < 0 || e.Y > 1 {
			return
		}
		_ = wininput.MovePrimary(e.X, e.Y)
	case "key":
		vk := evdevVK(e.Code)
		if vk == 0 {
			h.mu.Unlock()
			return
		}
		if e.Down {
			h.keys[e.Code] = true
		} else {
			delete(h.keys, e.Code)
		}
		h.mu.Unlock()
		_ = wininput.Key(vk, e.Down)
	case "button":
		b := -1
		switch e.Code {
		case 272:
			b = 0
		case 273:
			b = 1
		case 274:
			b = 2
		}
		if b < 0 {
			h.mu.Unlock()
			return
		}
		if e.Down {
			h.buttons[e.Code] = true
		} else {
			delete(h.buttons, e.Code)
		}
		h.mu.Unlock()
		_ = wininput.Button(b, e.Down)
	case "wheel":
		h.mu.Unlock()
		if math.IsNaN(e.Axis) {
			return
		}
		// Windows WHEEL_DELTA is 120 per notch.
		delta := int32(-e.Axis * 120)
		if delta > 1200 {
			delta = 1200
		} else if delta < -1200 {
			delta = -1200
		}
		if delta != 0 {
			_ = wininput.Wheel(delta)
		}
	case "release":
		var batchKeys []int32
		var batchButtons []int32
		for key := range h.keys {
			batchKeys = append(batchKeys, key)
			delete(h.keys, key)
		}
		for button := range h.buttons {
			batchButtons = append(batchButtons, button)
			delete(h.buttons, button)
		}
		h.mu.Unlock()
		for _, key := range batchKeys {
			if vk := evdevVK(key); vk != 0 {
				_ = wininput.Key(vk, false)
			}
		}
		for _, button := range batchButtons {
			b := 0
			if button == 273 {
				b = 1
			} else if button == 274 {
				b = 2
			}
			_ = wininput.Button(b, false)
		}
	default:
		h.mu.Unlock()
	}
}

func evdevVK(code int32) uint16 {
	return map[int32]uint16{
		1: 27, 2: 49, 3: 50, 4: 51, 5: 52, 6: 53, 7: 54, 8: 55, 9: 56, 10: 57, 11: 48,
		12: 189, 13: 187, 14: 8, 15: 9, 16: 81, 17: 87, 18: 69, 19: 82, 20: 84, 21: 89, 22: 85, 23: 73,
		24: 79, 25: 80, 26: 219, 27: 221, 28: 13, 29: 17, 30: 65, 31: 83, 32: 68, 33: 70, 34: 71, 35: 72,
		36: 74, 37: 75, 38: 76, 39: 186, 40: 222, 41: 192, 42: 16, 43: 220, 44: 90, 45: 88, 46: 67, 47: 86, 48: 66, 49: 78,
		50: 77, 51: 188, 52: 190, 53: 191, 54: 16, 55: 106, 56: 18, 57: 32, 58: 20, 59: 112, 60: 113, 61: 114, 62: 115, 63: 116, 64: 117,
		65: 118, 66: 119, 67: 120, 68: 121, 87: 122, 88: 123, 97: 17, 100: 18, 102: 36, 103: 38, 104: 33,
		105: 37, 106: 39, 107: 35, 108: 40, 109: 34, 110: 45, 111: 46, 125: 91, 126: 92,
	}[code]
}

func (h *Host) Done() <-chan struct{} {
	return h.done
}

func (h *Host) Close() {
	h.once.Do(func() {
		h.mu.Lock()
		h.stopping = true
		h.mu.Unlock()
		h.cancel()
		h.Peer.Close()
		h.Encoder.Interrupt()
		<-h.done
		h.mu.Lock()
		for key := range h.keys {
			_ = wininput.Key(evdevVK(key), false)
		}
		for button := range h.buttons {
			b := 0
			if button == 273 {
				b = 1
			} else if button == 274 {
				b = 2
			}
			_ = wininput.Button(b, false)
		}
		h.mu.Unlock()
		h.Encoder.Close()
	})
}

type Viewer struct {
	Peer       *rtc.Peer
	Decoder    *media.Pipeline
	Frames     chan media.Sample
	OnDecoder  func(media.Codec, string)
	OnFailure  func(error)
	cancel     context.CancelFunc
	once       sync.Once
	done       chan struct{}
	pts        time.Duration
	mu         sync.Mutex
	stopping   bool
	choices    []media.Codec
	active     media.Codec
	started    chan struct{}
	startOnce  sync.Once
	pipelineMu sync.Mutex
	perf       rtc.PerfTracker
	sps        []byte
	pps        []byte
	hasDecoded bool
}

func NewViewer(parent context.Context, codec media.Codec) (*Viewer, error) {
	return NewViewerWithICE(parent, codec, nil)
}

func NewViewerWithICE(parent context.Context, codec media.Codec, servers []webrtc.ICEServer) (*Viewer, error) {
	return NewViewerWithOptions(parent, codec, ViewerOptions{
		ICEServers: servers,
		Transport:  rtc.TransportUDP,
	})
}

func NewViewerWithOptions(parent context.Context, codec media.Codec, opts ViewerOptions) (*Viewer, error) {
	ctx, cancel := context.WithCancel(parent)
	choices := []media.Codec{codec}
	if codec == media.Auto || codec == media.HardwareRequired {
		choices = []media.Codec{media.VA}
		if codec == media.Auto {
			choices = append(choices, media.Software)
		}
	}
	var decoder *media.Pipeline
	var err error
	var selected media.Codec
	for len(choices) > 0 {
		selected = choices[0]
		choices = choices[1:]
		decoder, err = media.NewDecoder(selected)
		if err == nil {
			break
		}
	}
	if decoder == nil {
		cancel()
		return nil, errors.New("无法启动 H.264 解码，请检查 显卡驱动或 GStreamer 插件")
	}
	peer, err := rtc.NewWithOptions(rtc.PeerOptions{
		Offerer:         true,
		AllowInput:      false,
		ICEServers:      opts.ICEServers,
		Transport:       opts.Transport,
		TransportReason: opts.TransportReason,
		WSTunnel:        opts.WSTunnel,
	})
	if err != nil {
		decoder.Close()
		cancel()
		return nil, err
	}
	v := &Viewer{
		Peer:     peer,
		Decoder:  decoder,
		Frames:   make(chan media.Sample, 1),
		cancel:   cancel,
		done:     make(chan struct{}),
		choices:  choices,
		active:   selected,
		started:  make(chan struct{}),
	}
	peer.OnSample = func(data []byte, duration time.Duration) {
		v.startOnce.Do(func() { close(v.started) })
		v.mu.Lock()
		defer v.mu.Unlock()
		if v.stopping || v.Decoder == nil {
			return
		}

		sps, pps, hasIDR, hasSPS, hasPPS := parseH264NALs(data)
		if hasSPS && len(sps) > 0 {
			v.sps = sps
		}
		if hasPPS && len(pps) > 0 {
			v.pps = pps
		}
		if hasIDR && (!hasSPS || !hasPPS) && len(v.sps) > 0 && len(v.pps) > 0 {
			data = append(append(append([]byte{}, v.sps...), v.pps...), data...)
			hasSPS = true
			hasPPS = true
		}

		if !v.hasDecoded && (!hasIDR || !hasSPS || !hasPPS) {
			if v.Peer != nil {
				v.Peer.RequestKeyframe()
			}
			return
		}

		v.pts += duration
		if v.Decoder.Push(data, v.pts) == nil {
			v.hasDecoded = true
		}
	}
	peer.OnState = func(state string) {
		if state == "failed" || state == "closed" || state == "disconnected" {
			cancel()
		}
	}
	go v.receiveFrames(ctx)
	return v, nil
}

// A hardware candidate is not considered active until it produces a decoded frame.
func (v *Viewer) receiveFrames(ctx context.Context) {
	defer close(v.done)
	timer := time.NewTimer(95 * time.Second)
	defer timer.Stop()
	select {
	case <-v.started:
	case <-ctx.Done():
		return
	case <-timer.C:
		if v.OnFailure != nil {
			v.OnFailure(errors.New("等待远端视频超时"))
		}
		v.cancel()
		return
	}
	first := true
	for {
		v.mu.Lock()
		decoder := v.Decoder
		codec := v.active
		v.mu.Unlock()
		if decoder == nil {
			return
		}
		pullCtx := ctx
		cancel := func() {}
		if first {
			pullCtx, cancel = context.WithTimeout(ctx, 8*time.Second)
		}
		t0 := time.Now()
		frame, err := decoder.Pull(pullCtx)
		pullDur := time.Since(t0)
		cancel()
		if err == nil {
			v.perf.Record(pullDur, len(frame.Data))
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			decoder.Interrupt()
			v.mu.Lock()
			if v.stopping {
				v.mu.Unlock()
				return
			}
			v.pipelineMu.Lock()
			decoder.Close()
			v.Decoder = nil
			v.hasDecoded = false
			if v.Peer != nil {
				v.Peer.RequestKeyframe()
			}
			for len(v.choices) > 0 {
				next := v.choices[0]
				v.choices = v.choices[1:]
				p, e := media.NewDecoder(next)
				if e == nil {
					v.Decoder = p
					v.active = next
					break
				}
			}
			ready := v.Decoder != nil
			v.pipelineMu.Unlock()
			v.mu.Unlock()
			if !ready {
				if v.OnFailure != nil {
					v.OnFailure(errors.New("H.264 解码失败，未收到画面"))
				}
				v.cancel()
				return
			}
			first = true
			continue
		}
		if first {
			first = false
			if v.OnDecoder != nil {
				v.OnDecoder(codec, "")
			}
		}
		select {
		case v.Frames <- frame:
		default:
			select {
			case <-v.Frames:
			default:
			}
			select {
			case v.Frames <- frame:
			default:
			}
		}
	}
}

func (v *Viewer) Offer(ctx context.Context) (string, error) {
	return v.Peer.Offer(ctx)
}

func (v *Viewer) Answer(sdp string) error {
	return v.Peer.ApplyAnswer(sdp)
}

func (v *Viewer) Input(e rtc.Input) error {
	if v.Peer == nil {
		return errors.New("会话未连接")
	}
	return v.Peer.SendInput(e)
}

func (v *Viewer) OpenFiles() (*webrtc.DataChannel, error) {
	if v.Peer == nil {
		return nil, errors.New("会话未连接")
	}
	return v.Peer.OpenFiles()
}

func (v *Viewer) OpenClipboard() (*webrtc.DataChannel, error) {
	if v.Peer == nil {
		return nil, errors.New("会话未连接")
	}
	return v.Peer.OpenClipboard()
}

func (v *Viewer) Close() {
	v.once.Do(func() {
		v.cancel()
		v.Peer.Close()
		v.pipelineMu.Lock()
		if v.Decoder != nil {
			v.Decoder.Interrupt()
		}
		v.pipelineMu.Unlock()
		v.mu.Lock()
		v.stopping = true
		v.mu.Unlock()
		<-v.done
		v.mu.Lock()
		v.pipelineMu.Lock()
		if v.Decoder != nil {
			v.Decoder.Close()
			v.Decoder = nil
		}
		v.pipelineMu.Unlock()
		v.mu.Unlock()
	})
}

// GetEncodeStats returns real-time encoding FPS, average latency, and bitrate.
func (h *Host) GetEncodeStats() (float64, time.Duration, float64) {
	return h.perf.Snapshot()
}

// GetDecodeStats returns real-time decoding FPS and average latency.
func (v *Viewer) GetDecodeStats() (float64, time.Duration) {
	fps, dur, _ := v.perf.Snapshot()
	return fps, dur
}

func parseH264NALs(b []byte) (sps, pps []byte, hasIDR, hasSPS, hasPPS bool) {
	var starts []int
	for i := 0; i+3 < len(b); {
		n := 0
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			n = 3
		} else if i+4 < len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 0 && b[i+3] == 1 {
			n = 4
		}
		if n > 0 {
			starts = append(starts, i+n)
			i += n
		} else {
			i++
		}
	}
	for idx, start := range starts {
		end := len(b)
		if idx+1 < len(starts) {
			end = starts[idx+1]
			for end > start && b[end-1] == 0 {
				end--
			}
			if end > 0 && b[end-1] == 1 {
				end--
			}
			for end > start && b[end-1] == 0 {
				end--
			}
		}
		nal := b[start:end]
		if len(nal) == 0 {
			continue
		}
		switch nal[0] & 0x1F {
		case 7:
			hasSPS = true
			if len(sps) == 0 {
				sps = append([]byte{0, 0, 0, 1}, nal...)
			}
		case 8:
			hasPPS = true
			if len(pps) == 0 {
				pps = append([]byte{0, 0, 0, 1}, nal...)
			}
		case 5:
			hasIDR = true
		}
	}
	return
}
