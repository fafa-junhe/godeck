// Package clipboard synchronizes plain-text clipboard contents over a dedicated,
// explicitly authorized WebRTC DataChannel ("godeck-clipboard").
//
// Only UTF-8 text is carried. Content is sent only when the local clipboard
// changes after the channel opened; the existing clipboard content at connect
// time is never pushed automatically.
package clipboard

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pion/webrtc/v4"
)

// MaxTextBytes bounds a single clipboard item.
const MaxTextBytes = 1 << 20

const (
	partBytes  = 32 << 10
	headerSize = 1 + 4 + 2 + 2 // 'C' + seq + index + count
	maxParts   = (MaxTextBytes + partBytes - 1) / partBytes
)

// Platform is the local OS clipboard.
type Platform interface {
	// Watch calls onChange with the current text each time the local clipboard
	// changes, until ctx is done. It must not report the content present when
	// Watch starts.
	Watch(ctx context.Context, onChange func(string)) error
	// Write replaces the local clipboard with text.
	Write(text string) error
}

// Event reports channel state to the UI.
type Event struct {
	Type  string // "ready", "closed", "sent", "received", "error"
	Bytes int
	Error string
}

type Sync struct {
	dc       *webrtc.DataChannel
	platform Platform
	emit     func(Event)

	mu       sync.Mutex
	enabled  bool
	opened   bool
	closed   bool
	cancel   context.CancelFunc
	lastHash [32]byte // last text we sent or applied, to suppress echo loops
	seq      uint32

	rxSeq   uint32
	rxCount int
	rxParts [][]byte
	rxBytes int
}

// New binds a clipboard channel. Sync starts enabled; the viewer may pause it.
func New(dc *webrtc.DataChannel, platform Platform, emit func(Event)) *Sync {
	s := &Sync{dc: dc, platform: platform, emit: emit, enabled: true}
	dc.OnOpen(s.open)
	dc.OnClose(func() { s.shutdown() })
	dc.OnMessage(s.message)
	// The host side receives an already-open channel.
	if dc.ReadyState() == webrtc.DataChannelStateOpen {
		go s.open()
	}
	return s
}

func (s *Sync) event(e Event) {
	if s.emit != nil {
		s.emit(e)
	}
}

func (s *Sync) open() {
	s.mu.Lock()
	if s.opened || s.closed {
		s.mu.Unlock()
		return
	}
	s.opened = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.mu.Unlock()

	s.event(Event{Type: "ready"})
	go func() {
		if err := s.platform.Watch(ctx, s.local); err != nil && ctx.Err() == nil {
			s.event(Event{Type: "error", Error: "本机剪贴板监听不可用：" + err.Error()})
		}
	}()
}

// Opened reports whether the peer accepted the channel.
func (s *Sync) Opened() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opened && !s.closed
}

// Enabled reports whether local changes are sent and remote changes applied.
func (s *Sync) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

// SetEnabled pauses or resumes synchronization in both directions.
func (s *Sync) SetEnabled(on bool) {
	s.mu.Lock()
	s.enabled = on
	s.mu.Unlock()
}

func (s *Sync) shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	cancel := s.cancel
	s.rxParts = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.event(Event{Type: "closed"})
}

// Close stops watching and closes the channel.
func (s *Sync) Close() error {
	s.shutdown()
	return s.dc.Close()
}

func normalize(text string) string {
	return strings.ReplaceAll(text, "\x00", "")
}

func (s *Sync) local(text string) {
	text = normalize(text)
	if text == "" || !utf8.ValidString(text) {
		return
	}
	if len(text) > MaxTextBytes {
		s.event(Event{Type: "error", Error: "剪贴板内容超过 1 MiB，未同步"})
		return
	}
	h := sha256.Sum256([]byte(text))
	s.mu.Lock()
	if !s.enabled || s.closed || h == s.lastHash {
		s.mu.Unlock()
		return
	}
	s.lastHash = h
	s.seq++
	seq := s.seq
	s.mu.Unlock()

	if err := s.send(seq, []byte(text)); err != nil {
		s.event(Event{Type: "error", Error: "剪贴板发送失败：" + err.Error()})
		return
	}
	s.event(Event{Type: "sent", Bytes: len(text)})
}

func (s *Sync) send(seq uint32, data []byte) error {
	if s.dc.ReadyState() != webrtc.DataChannelStateOpen {
		return errors.New("剪贴板通道未就绪")
	}
	count := (len(data) + partBytes - 1) / partBytes
	for i := 0; i < count; i++ {
		end := (i + 1) * partBytes
		if end > len(data) {
			end = len(data)
		}
		packet := make([]byte, headerSize+end-i*partBytes)
		packet[0] = 'C'
		binary.BigEndian.PutUint32(packet[1:5], seq)
		binary.BigEndian.PutUint16(packet[5:7], uint16(i))
		binary.BigEndian.PutUint16(packet[7:9], uint16(count))
		copy(packet[headerSize:], data[i*partBytes:end])
		// Simple backpressure: clipboard items are small and rare.
		for wait := 0; s.dc.BufferedAmount() > 256<<10; wait++ {
			if wait > 200 {
				return errors.New("通道拥塞")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := s.dc.Send(packet); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sync) message(msg webrtc.DataChannelMessage) {
	d := msg.Data
	if msg.IsString || len(d) < headerSize || len(d) > headerSize+partBytes || d[0] != 'C' {
		return
	}
	seq := binary.BigEndian.Uint32(d[1:5])
	idx := int(binary.BigEndian.Uint16(d[5:7]))
	count := int(binary.BigEndian.Uint16(d[7:9]))
	if count == 0 || count > maxParts || idx >= count {
		return
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if idx == 0 {
		s.rxSeq, s.rxCount, s.rxParts, s.rxBytes = seq, count, make([][]byte, 0, count), 0
	}
	// Channel is ordered and reliable: parts must arrive in sequence.
	if seq != s.rxSeq || count != s.rxCount || idx != len(s.rxParts) || s.rxBytes+len(d)-headerSize > MaxTextBytes {
		s.rxParts = nil
		s.rxCount = 0
		s.mu.Unlock()
		return
	}
	s.rxParts = append(s.rxParts, append([]byte(nil), d[headerSize:]...))
	s.rxBytes += len(d) - headerSize
	if len(s.rxParts) < s.rxCount {
		s.mu.Unlock()
		return
	}
	buf := make([]byte, 0, s.rxBytes)
	for _, p := range s.rxParts {
		buf = append(buf, p...)
	}
	s.rxParts = nil
	s.rxCount = 0
	enabled := s.enabled
	text := normalize(string(buf))
	valid := text != "" && utf8.ValidString(text)
	if enabled && valid {
		s.lastHash = sha256.Sum256([]byte(text))
	}
	s.mu.Unlock()

	if !enabled || !valid {
		return
	}
	if err := s.platform.Write(text); err != nil {
		s.event(Event{Type: "error", Error: "写入本机剪贴板失败：" + err.Error()})
		return
	}
	s.event(Event{Type: "received", Bytes: len(text)})
}
