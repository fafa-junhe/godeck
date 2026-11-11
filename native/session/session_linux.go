//go:build linux && cgo

package session

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/pion/webrtc/v4"
	"godeck/native/media"
	"godeck/native/platform/portal"
	"godeck/native/rtc"
)

type Host struct {
	Capture    *portal.Capture
	Encoder    *media.Pipeline
	AVEncoder  *media.AVEncoder
	Peer       *rtc.Peer
	cancel     context.CancelFunc
	once       sync.Once
	done       chan struct{}
	mu         sync.Mutex
	keys       map[int32]bool
	buttons    map[int32]bool
	width      float64
	height     float64
	targetW    int
	targetH    int
	stopping   bool
	first      *media.Sample
	Codec      media.Codec
	Device     string
	Preset     media.Preset
	Files      bool
	perf       rtc.PerfTracker
	sps        []byte
	pps        []byte
	needKey    bool
}

// StartHost asks the desktop for permission before producing an answer.
func StartHost(parent context.Context, offer string, control bool, codec media.Codec) (*Host, string, error) {
	return StartHostFull(parent, offer, control, false, codec, media.Preset720p30, nil, nil)
}

func StartHostWithICE(parent context.Context, offer string, control bool, codec media.Codec, servers []webrtc.ICEServer) (*Host, string, error) {
	return StartHostFull(parent, offer, control, false, codec, media.Preset720p30, servers, nil)
}

func StartHostFull(parent context.Context, offer string, control bool, files bool, codec media.Codec, preset media.Preset, servers []webrtc.ICEServer, fileHandler func(*webrtc.DataChannel)) (*Host, string, error) {
	return StartHostWithTransport(parent, offer, control, files, codec, preset, HostOptions{
		ICEServers:  servers,
		FileHandler: fileHandler,
		Transport:   rtc.TransportUDP,
	})
}

func StartHostWithTransport(parent context.Context, offer string, control bool, files bool, codec media.Codec, preset media.Preset, opts HostOptions) (*Host, string, error) {
	ctx, cancel := context.WithCancel(parent)
	capture, err := portal.Open(ctx, control)
	if err != nil {
		cancel()
		return nil, "", err
	}

	maxWidth, maxHeight, fps, desiredLevel := preset.MaxDimensions()
	negotiatedLevel, downgraded, err := rtc.CheckOfferCompatibility(offer, desiredLevel)
	if err != nil {
		capture.Close()
		cancel()
		return nil, "", err
	}
	if downgraded {
		preset = media.Preset720p30
		maxWidth, maxHeight, fps, _ = preset.MaxDimensions()
	}
	level := negotiatedLevel

	targetWidth, targetHeight := maxWidth, maxHeight
	if v, ok := capture.Stream.Properties["size"]; ok {
		var dims [2]int32
		if dbus.Store([]any{v.Value()}, &dims) == nil && dims[0] > 0 && dims[1] > 0 {
			scale := math.Min(1, math.Min(float64(maxWidth)/float64(dims[0]), float64(maxHeight)/float64(dims[1])))
			targetWidth = int(float64(dims[0])*scale) &^ 1
			targetHeight = int(float64(dims[1])*scale) &^ 1
			if targetWidth < 2 {
				targetWidth = 2
			}
			if targetHeight < 2 {
				targetHeight = 2
			}
		}
	}

	var encoder *media.Pipeline
	var avEncoder *media.AVEncoder
	var first media.Sample
	selected := codec
	candidates := []media.Codec{codec}

	if codec == media.Auto || codec == media.HardwareRequired {
		capability := media.ProbeVAEncode()
		if capability.Encode && capability.Device != "" {
			encoder, err = media.NewRawCapture(int(capture.FD.Fd()), capture.Stream.Node, targetWidth, targetHeight)
			if err == nil {
				avEncoder, err = media.NewAVEncoderWithProfile(targetWidth, targetHeight, fps, level, capability.Device)
				if err == nil {
					probeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
					for probeCtx.Err() == nil {
						raw, e := encoder.Pull(probeCtx)
						if e != nil {
							err = e
							break
						}
						packet, e := avEncoder.EncodeNV12(raw.Data, raw.Timestamp)
						if e == media.ErrAVAgain {
							continue
						}
						if e != nil {
							err = e
							break
						}
						first = media.Sample{Data: packet.Data, Timestamp: packet.PTS, Width: targetWidth, Height: targetHeight}
						selected = media.FFmpegVA
						err = nil
						break
					}
					stop()
					if len(first.Data) == 0 {
						err = errors.New("VA 硬编码没有产生首帧")
					}
				}
				if err != nil {
					if avEncoder != nil {
						avEncoder.Close()
						avEncoder = nil
					}
					encoder.Close()
					encoder = nil
				}
			}
		}
	}

	if codec == media.Auto || codec == media.HardwareRequired {
		candidates = []media.Codec{media.VA, media.LegacyVA}
		if codec == media.Auto {
			candidates = append(candidates, media.Software)
		}
	}

	for _, candidate := range candidates {
		if encoder != nil {
			break
		}
		encoder, err = media.NewEncoder(int(capture.FD.Fd()), capture.Stream.Node, candidate)
		if err != nil {
			continue
		}
		probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
		first, err = encoder.Pull(probeCtx)
		probeCancel()
		if err == nil && candidate != media.Software {
			path := encoder.DevicePath()
			if path == "" || !media.ProbeVAPath(path).Encode {
				err = errors.New("无法确认编码器使用的 VA 设备")
			}
		}
		if err == nil {
			selected = candidate
			break
		}
		encoder.Close()
		encoder = nil
	}

	if encoder == nil {
		capture.Close()
		cancel()
		return nil, "", errors.New("无法启动 H.264 编码；请检查 VA-API、GStreamer 插件与屏幕共享权限")
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
		if avEncoder != nil {
			avEncoder.Close()
		}
		encoder.Close()
		capture.Close()
		cancel()
		return nil, "", err
	}
	peer.SetFileHandler(files, opts.FileHandler)

	actualW, actualH := targetWidth, targetHeight
	if first.Width > 0 && first.Height > 0 {
		actualW, actualH = first.Width, first.Height
	}

	var initSPS, initPPS []byte
	var hasInitSPS, hasInitPPS bool
	if len(first.Data) > 0 {
		initSPS, initPPS, _, hasInitSPS, hasInitPPS = parseH264NALs(first.Data)
	}

	h := &Host{
		Capture:   capture,
		Encoder:   encoder,
		AVEncoder: avEncoder,
		Peer:      peer,
		cancel:    cancel,
		done:      make(chan struct{}),
		keys:      map[int32]bool{},
		buttons:   map[int32]bool{},
		first:     &first,
		Codec:     selected,
		Device:    encoder.DevicePath(),
		Preset:    preset,
		Files:     files,
		targetW:   actualW,
		targetH:   actualH,
	}
	if hasInitSPS {
		h.sps = initSPS
	}
	if hasInitPPS {
		h.pps = initPPS
	}
	if avEncoder != nil {
		h.Device = avEncoder.DevicePath()
	}
	if v, ok := capture.Stream.Properties["size"]; ok {
		var dims [2]int32
		if dbus.Store([]any{v.Value()}, &dims) == nil {
			h.width = float64(dims[0])
			h.height = float64(dims[1])
		}
	}
	peer.OnInput = h.input
	peer.OnKeyframe = func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.stopping {
			h.needKey = true
			if h.AVEncoder != nil {
				h.AVEncoder.ForceKey()
			} else {
				encoder.ForceKey()
			}
		}
	}
	peer.OnState = func(state string) {
		if state == "failed" || state == "closed" || state == "disconnected" {
			cancel()
		}
	}
	if err = peer.AddVideoWithProfile(level); err != nil {
		peer.Close()
		if avEncoder != nil {
			avEncoder.Close()
		}
		encoder.Close()
		capture.Close()
		cancel()
		return nil, "", err
	}
	answer, err := peer.Answer(ctx, offer)
	if err != nil {
		peer.Close()
		if avEncoder != nil {
			avEncoder.Close()
		}
		encoder.Close()
		capture.Close()
		cancel()
		return nil, "", err
	}
	go h.pump(ctx)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-h.done:
				return
			case <-ticker.C:
				fps, dur, bps := h.perf.Snapshot()
				_ = h.Peer.SendTelemetry(rtc.HostTelemetry{
					EncodeFPS:     fps,
					EncodeMs:      float64(dur.Microseconds()) / 1000.0,
					EncodeBitrate: bps,
					Encoder:       h.Codec.String(),
					Width:         h.targetW,
					Height:        h.targetH,
				})
			}
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
		case <-capture.Closed():
		}
		h.Close()
	}()
	return h, answer, nil
}

func (h *Host) TargetDimensions() (int, int) {
	return h.targetW, h.targetH
}

func (h *Host) CanControl() bool {
	return h.Capture != nil && h.Capture.Devices != 0
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
			if err == nil && h.AVEncoder != nil {
				packet, e := h.AVEncoder.EncodeNV12(sample.Data, sample.Timestamp)
				if e == media.ErrAVAgain {
					continue
				}
				if e != nil {
					return
				}
				sample.Data = packet.Data
				sample.Timestamp = packet.PTS
			}
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
	var (
		notifyMethod string
		notifyArgs   []any
		batchKeys    []int32
		batchButtons []int32
	)

	h.mu.Lock()
	if h.stopping || h.Capture.Devices == 0 {
		h.mu.Unlock()
		return
	}

	switch e.Type {
	case "move":
		if h.width <= 0 || h.height <= 0 || math.IsNaN(e.X) || math.IsNaN(e.Y) || e.X < 0 || e.X > 1 || e.Y < 0 || e.Y > 1 {
			h.mu.Unlock()
			return
		}
		notifyMethod = "NotifyPointerMotionAbsolute"
		notifyArgs = []any{h.Capture.Stream.Node, e.X * h.width, e.Y * h.height}
		h.mu.Unlock()

	case "key":
		if e.Code < 1 || e.Code > 255 {
			h.mu.Unlock()
			return
		}
		state := uint32(0)
		if e.Down {
			state = 1
			h.keys[e.Code] = true
		} else {
			delete(h.keys, e.Code)
		}
		notifyMethod = "NotifyKeyboardKeycode"
		notifyArgs = []any{e.Code, state}
		h.mu.Unlock()

	case "button":
		if e.Code != 272 && e.Code != 273 && e.Code != 274 {
			h.mu.Unlock()
			return
		}
		state := uint32(0)
		if e.Down {
			state = 1
			h.buttons[e.Code] = true
		} else {
			delete(h.buttons, e.Code)
		}
		notifyMethod = "NotifyPointerButton"
		notifyArgs = []any{e.Code, state}
		h.mu.Unlock()

	case "release":
		for key := range h.keys {
			batchKeys = append(batchKeys, key)
			delete(h.keys, key)
		}
		for button := range h.buttons {
			batchButtons = append(batchButtons, button)
			delete(h.buttons, button)
		}
		h.mu.Unlock()

	case "wheel":
		h.mu.Unlock()
		if math.IsNaN(e.Axis) || math.Abs(e.Axis) > 100 {
			return
		}
		notifyMethod = "NotifyPointerAxis"
		notifyArgs = []any{float64(0), e.Axis}

	default:
		h.mu.Unlock()
		return
	}

	if notifyMethod != "" {
		_ = h.Capture.Notify(notifyMethod, notifyArgs...)
	}
	for _, key := range batchKeys {
		_ = h.Capture.Notify("NotifyKeyboardKeycode", key, uint32(0))
	}
	for _, btn := range batchButtons {
		_ = h.Capture.Notify("NotifyPointerButton", btn, uint32(0))
	}
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
			_ = h.Capture.Notify("NotifyKeyboardKeycode", key, uint32(0))
		}
		for button := range h.buttons {
			_ = h.Capture.Notify("NotifyPointerButton", button, uint32(0))
		}
		h.mu.Unlock()
		if h.AVEncoder != nil {
			h.AVEncoder.Close()
		}
		h.Encoder.Close()
		h.Capture.Close()
	})
}

type Viewer struct {
	Peer       *rtc.Peer
	Decoder    *media.Pipeline
	AVDecoder  *media.AVDecoder
	avFrames   chan media.Sample
	avErrors   chan struct{}
	device     string
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
	waitingKey bool
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
		choices = []media.Codec{media.VA, media.LegacyVA}
		if codec == media.Auto {
			choices = append(choices, media.Software)
		}
	}
	var decoder *media.Pipeline
	var avDecoder *media.AVDecoder
	var device string
	var err error
	var selected media.Codec

	if codec == media.Auto || codec == media.HardwareRequired {
		caps := media.ProbeVADecode()
		if caps.Decode && caps.Device != "" {
			avDecoder, err = media.NewAVDecoder(caps.Device)
			if err == nil {
				selected = media.FFmpegVA
				device = caps.Device
			}
		}
	}
	for avDecoder == nil && len(choices) > 0 {
		selected = choices[0]
		choices = choices[1:]
		decoder, err = media.NewDecoder(selected)
		if err == nil {
			break
		}
	}
	if decoder == nil && avDecoder == nil {
		cancel()
		return nil, errors.New("无法启动 H.264 解码，请检查 VA 驱动或 GStreamer 插件")
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
		if decoder != nil {
			decoder.Close()
		}
		if avDecoder != nil {
			avDecoder.Close()
		}
		cancel()
		return nil, err
	}
	v := &Viewer{
		Peer:      peer,
		Decoder:   decoder,
		AVDecoder: avDecoder,
		avFrames:  make(chan media.Sample, 1),
		avErrors:  make(chan struct{}, 1),
		device:    device,
		Frames:    make(chan media.Sample, 1),
		cancel:    cancel,
		done:      make(chan struct{}),
		choices:   choices,
		active:    selected,
		started:   make(chan struct{}),
	}
	peer.OnSample = func(data []byte, duration time.Duration) {
		v.startOnce.Do(func() { close(v.started) })
		v.mu.Lock()
		defer v.mu.Unlock()
		if v.stopping {
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

		if (!v.hasDecoded || v.waitingKey) && (!hasIDR || !hasSPS || !hasPPS) {
			if v.Peer != nil {
				v.Peer.RequestKeyframe()
			}
			return
		}

		v.pts += duration
		if v.AVDecoder != nil {
			t0 := time.Now()
			frame, err := v.AVDecoder.Decode(data, v.pts)
			decodeDur := time.Since(t0)
			if err == media.ErrAVAgain {
				return
			}
			if err == media.ErrAVCorrupt {
				v.waitingKey = true
				if v.Peer != nil {
					v.Peer.RequestKeyframe()
				}
				return
			}
			if err != nil {
				if !v.hasDecoded {
					if v.Peer != nil {
						v.Peer.RequestKeyframe()
					}
					return
				}
				select {
				case v.avErrors <- struct{}{}:
				default:
				}
				return
			}
			v.hasDecoded = true
			v.waitingKey = false
			v.perf.Record(decodeDur, len(frame.Data))
			sample := media.Sample{Data: frame.Data, Width: frame.Width, Height: frame.Height, Timestamp: frame.PTS}
			select {
			case v.avFrames <- sample:
			default:
				select {
				case <-v.avFrames:
				default:
				}
				select {
				case v.avFrames <- sample:
				default:
				}
			}
			return
		}
		if v.Decoder != nil {
			if v.Decoder.Push(data, v.pts) == nil {
				v.hasDecoded = true
				v.waitingKey = false
			}
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

// A VA candidate is not considered active until it produces a decoded frame.
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
		av := v.AVDecoder
		v.mu.Unlock()

		if av != nil {
			wait := time.NewTimer(8 * time.Second)
			var frame media.Sample
			var ok bool
			select {
			case frame = <-v.avFrames:
				ok = true
			case <-v.avErrors:
			case <-wait.C:
			case <-ctx.Done():
				wait.Stop()
				return
			}
			wait.Stop()
			if ok {
				if first {
					first = false
					if v.OnDecoder != nil {
						v.OnDecoder(media.FFmpegVA, v.device)
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
				continue
			}
			v.mu.Lock()
			if v.stopping {
				v.mu.Unlock()
				return
			}
			v.AVDecoder.Close()
			v.AVDecoder = nil
			v.device = ""
			v.hasDecoded = false
			v.waitingKey = true
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
		if err == nil && first && codec != media.Software {
			path := decoder.DevicePath()
			if path == "" || !media.ProbeVAPath(path).Decode {
				err = errors.New("无法确认解码器使用的 VA 设备")
			}
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
			v.waitingKey = true
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
			v.mu.Lock()
			v.device = decoder.DevicePath()
			v.mu.Unlock()
			if v.OnDecoder != nil {
				v.OnDecoder(codec, v.device)
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

func (v *Viewer) DecoderDevice() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.device
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
		if v.AVDecoder != nil {
			v.AVDecoder.Close()
			v.AVDecoder = nil
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
