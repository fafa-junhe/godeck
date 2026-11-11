// Package transfer implements bounded, explicitly authorized, single-file transfers.
package transfer

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/pion/webrtc/v4"
)

const MaxFileBytes int64 = 100 << 20
const chunkBytes = 16 << 10
const windowBytes int64 = 512 << 10 // sliding window in flight
const maxBuffered = 64 << 10       // DataChannel buffer limit
const maxFiles = 512
const timeout = 30 * time.Second

type Role int

const (
	Viewer Role = iota
	Host
)

type File struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type Event struct {
	Type  string // "ready", "list", "progress", "complete", "error"
	ID    string
	Name  string
	Error string
	Bytes int64
	Total int64
	Files []File
}

type wire struct {
	Type   string `json:"type"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Offset int64  `json:"offset,omitempty"`
	Error  string `json:"error,omitempty"`
	Files  []File `json:"files,omitempty"`
}

type task struct {
	mu      sync.Mutex
	id      string
	name    string
	total   int64
	offset  int64
	file    *os.File
	temp    string
	sender  bool
	signals chan wire
	stop    chan struct{}
	once    sync.Once
	last    atomic.Int64
}

func (t *task) close() {
	t.once.Do(func() {
		close(t.stop)
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.file != nil {
			_ = t.file.Close()
			t.file = nil
		}
		if t.temp != "" {
			_ = os.RemoveAll(t.temp)
			t.temp = ""
		}
	})
}

type Manager struct {
	mu          sync.Mutex
	dc          *webrtc.DataChannel
	role        Role
	root        string
	allowed     func() bool
	emit        func(Event)
	current     *task
	closed      bool
	pendingList string
}

func New(dc *webrtc.DataChannel, role Role, root string, allowed func() bool, emit func(Event)) *Manager {
	m := &Manager{
		dc:      dc,
		role:    role,
		root:    root,
		allowed: allowed,
		emit:    emit,
	}
	dc.OnMessage(m.message)
	dc.OnClose(func() { _ = m.Close() })
	dc.OnOpen(func() { m.event(Event{Type: "ready"}) })
	return m
}

func (m *Manager) event(e Event) {
	if m.emit != nil {
		m.emit(e)
	}
}

func (m *Manager) permitted() bool {
	return m.allowed != nil && m.allowed()
}

func (m *Manager) send(w wire) error {
	if !m.permitted() {
		return errors.New("文件访问未授权")
	}
	if m.dc.ReadyState() != webrtc.DataChannelStateOpen {
		return errors.New("文件通道未就绪或对端不支持")
	}
	b, e := json.Marshal(w)
	if e != nil {
		return e
	}
	return m.dc.SendText(string(b))
}

func id() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

func validID(v string) bool {
	if len(v) != 32 {
		return false
	}
	_, e := hex.DecodeString(v)
	return e == nil
}

func validName(name string) bool {
	if name == "" || len(name) > 240 || name == "." || name == ".." || filepath.Base(name) != name ||
		strings.ContainsAny(name, `/\<>:"|?*`+"\x00") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	stem := strings.ToUpper(strings.TrimRight(strings.SplitN(name, ".", 2)[0], " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
		"COM¹", "COM²", "COM³", "LPT¹", "LPT²", "LPT³":
		return false
	}
	return true
}

func (m *Manager) List() error {
	if m.role != Viewer {
		return errors.New("仅查看方可请求列表")
	}
	req := id()
	if req == "" {
		return errors.New("无法创建请求")
	}
	m.mu.Lock()
	if m.closed || m.pendingList != "" {
		m.mu.Unlock()
		return errors.New("列表请求进行中")
	}
	m.pendingList = req
	m.mu.Unlock()

	if e := m.send(wire{Type: "list", ID: req}); e != nil {
		m.mu.Lock()
		m.pendingList = ""
		m.mu.Unlock()
		return e
	}
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		<-timer.C
		m.mu.Lock()
		waiting := m.pendingList == req
		if waiting {
			m.pendingList = ""
		}
		m.mu.Unlock()
		if waiting {
			m.event(Event{Type: "error", Error: "列表请求超时，对端可能不支持文件传输"})
		}
	}()
	return nil
}

func (m *Manager) start(t *task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || !m.permitted() {
		return errors.New("文件访问未授权或已关闭")
	}
	if m.current != nil {
		return errors.New("已有文件传输进行中")
	}
	t.last.Store(time.Now().UnixNano())
	m.current = t
	return nil
}

func (m *Manager) Upload(path string) error {
	if m.role != Viewer {
		return errors.New("仅查看方可上传")
	}
	name := filepath.Base(path)
	if !validName(name) {
		return errors.New("文件名不受支持")
	}
	f, e := openRegular(path)
	if e != nil {
		return e
	}
	info, e := f.Stat()
	if e != nil || info.Size() > MaxFileBytes {
		f.Close()
		return errors.New("文件不可用或超过 100 MiB")
	}
	t := &task{
		id:      id(),
		name:    name,
		total:   info.Size(),
		file:    f,
		sender:  true,
		signals: make(chan wire, 64),
		stop:    make(chan struct{}),
	}
	if t.id == "" {
		t.close()
		return errors.New("无法创建任务")
	}
	if e = m.start(t); e != nil {
		t.close()
		return e
	}
	if e = m.send(wire{Type: "upload", ID: t.id, Name: name, Size: t.total}); e != nil {
		m.finish(t, e)
		return e
	}
	go m.sendFile(t)
	return nil
}

func (m *Manager) Download(name string) error {
	if m.role != Viewer || !validName(name) {
		return errors.New("文件名无效")
	}
	t := &task{
		id:      id(),
		name:    name,
		signals: make(chan wire, 64),
		stop:    make(chan struct{}),
	}
	if t.id == "" {
		return errors.New("无法创建任务")
	}
	if e := m.start(t); e != nil {
		return e
	}
	if e := m.send(wire{Type: "download", ID: t.id, Name: name}); e != nil {
		m.finish(t, e)
		return e
	}
	go m.deadline(t)
	return nil
}

func (m *Manager) Cancel() error {
	m.mu.Lock()
	t := m.current
	m.mu.Unlock()
	if t != nil {
		_ = m.send(wire{Type: "cancel", ID: t.id})
		m.finish(t, errors.New("已取消"))
	}
	return nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	t := m.current
	m.current = nil
	m.pendingList = ""
	m.mu.Unlock()
	if t != nil {
		t.close()
	}
	return nil
}

func (m *Manager) finish(t *task, e error) {
	m.mu.Lock()
	if m.current != t {
		m.mu.Unlock()
		return
	}
	m.current = nil
	m.mu.Unlock()

	t.mu.Lock()
	offset, total := t.offset, t.total
	t.mu.Unlock()
	t.close()

	event := Event{ID: t.id, Name: t.name, Bytes: offset, Total: total}
	if e != nil {
		event.Type = "error"
		event.Error = e.Error()
	} else {
		event.Type = "complete"
	}
	m.event(event)
}

func (m *Manager) deadline(t *task) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-ticker.C:
			if time.Since(time.Unix(0, t.last.Load())) >= timeout {
				_ = m.send(wire{Type: "cancel", ID: t.id})
				m.finish(t, errors.New("等待文件数据超时，结果未确认；请刷新列表"))
				return
			}
		}
	}
}

func (m *Manager) signal(t *task) (wire, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case x := <-t.signals:
		return x, nil
	case <-t.stop:
		return wire{}, errors.New("传输已关闭")
	case <-timer.C:
		return wire{}, errors.New("等待确认超时，结果未确认；请刷新列表")
	}
}

func (m *Manager) sendFile(t *task) {
	defer func() {
		if p := recover(); p != nil {
			m.finish(t, errors.New("传输中断"))
		}
	}()

	first, e := m.signal(t)
	if e != nil {
		m.finish(t, e)
		return
	}
	if first.Type != "ready" {
		m.finish(t, errors.New(first.Error))
		return
	}

	buf := make([]byte, chunkBytes)
	sentOffset := int64(0)
	ackedOffset := int64(0)

	for sentOffset < t.total {
		if !m.permitted() {
			m.finish(t, errors.New("文件权限已撤回"))
			return
		}

		// Sliding window backpressure: wait for window if too many bytes are in-flight
		for sentOffset-ackedOffset >= windowBytes {
			sig, err := m.signal(t)
			if err != nil {
				m.finish(t, err)
				return
			}
			if sig.Type == "ack" {
				if sig.Offset > ackedOffset {
					ackedOffset = sig.Offset
					t.mu.Lock()
					t.offset = ackedOffset
					t.mu.Unlock()
					m.event(Event{Type: "progress", ID: t.id, Name: t.name, Bytes: ackedOffset, Total: t.total})
				}
			} else if sig.Type == "result" && sig.Error != "" {
				m.finish(t, errors.New(sig.Error))
				return
			}
		}

		// WebRTC DataChannel buffer backpressure
		for m.dc.BufferedAmount() > maxBuffered {
			select {
			case <-t.stop:
				return
			case sig := <-t.signals:
				if sig.Type == "ack" && sig.Offset > ackedOffset {
					ackedOffset = sig.Offset
					t.mu.Lock()
					t.offset = ackedOffset
					t.mu.Unlock()
					m.event(Event{Type: "progress", ID: t.id, Name: t.name, Bytes: ackedOffset, Total: t.total})
				} else if sig.Type == "result" && sig.Error != "" {
					m.finish(t, errors.New(sig.Error))
					return
				}
			case <-time.After(5 * time.Millisecond):
			}
		}

		t.mu.Lock()
		if t.file == nil {
			t.mu.Unlock()
			return
		}
		n, e := t.file.Read(buf)
		t.mu.Unlock()
		if e != nil && e != io.EOF {
			m.finish(t, e)
			return
		}
		if n == 0 {
			m.finish(t, errors.New("源文件提前结束"))
			return
		}
		if int64(n) > t.total-sentOffset {
			n = int(t.total - sentOffset)
		}

		packet := make([]byte, 1+32+8+n)
		packet[0] = 'D'
		copy(packet[1:33], t.id)
		binary.BigEndian.PutUint64(packet[33:41], uint64(sentOffset))
		copy(packet[41:], buf[:n])

		if e = m.dc.Send(packet); e != nil {
			m.finish(t, e)
			return
		}
		sentOffset += int64(n)

		// Drain available ACKs non-blockingly
		for {
			select {
			case sig := <-t.signals:
				if sig.Type == "ack" && sig.Offset > ackedOffset {
					ackedOffset = sig.Offset
					t.mu.Lock()
					t.offset = ackedOffset
					t.mu.Unlock()
					m.event(Event{Type: "progress", ID: t.id, Name: t.name, Bytes: ackedOffset, Total: t.total})
				} else if sig.Type == "result" && sig.Error != "" {
					m.finish(t, errors.New(sig.Error))
					return
				}
			default:
				goto drained
			}
		}
	drained:
	}

	// Drain remaining ACKs until all sent bytes are acknowledged
	for ackedOffset < t.total {
		sig, err := m.signal(t)
		if err != nil {
			m.finish(t, err)
			return
		}
		if sig.Type == "ack" && sig.Offset > ackedOffset {
			ackedOffset = sig.Offset
			t.mu.Lock()
			t.offset = ackedOffset
			t.mu.Unlock()
			m.event(Event{Type: "progress", ID: t.id, Name: t.name, Bytes: ackedOffset, Total: t.total})
		} else if sig.Type == "result" && sig.Error != "" {
			m.finish(t, errors.New(sig.Error))
			return
		}
	}

	if e = m.send(wire{Type: "done", ID: t.id, Offset: t.total}); e != nil {
		m.finish(t, e)
		return
	}
	result, e := m.signal(t)
	if e != nil {
		m.finish(t, e)
		return
	}
	if result.Type != "result" || result.Error != "" {
		m.finish(t, errors.New(result.Error))
		return
	}
	m.finish(t, nil)
}

func (m *Manager) receiveFile(t *task, size int64) error {
	if size < 0 || size > MaxFileBytes {
		return errors.New("文件大小无效")
	}
	if err := os.MkdirAll(m.root, 0700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(m.root, ".godeck-upload-")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "data"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	t.mu.Lock()
	select {
	case <-t.stop:
		t.mu.Unlock()
		f.Close()
		os.RemoveAll(dir)
		return errors.New("传输已关闭")
	default:
	}
	t.temp = dir
	t.file = f
	t.total = size
	t.mu.Unlock()
	t.last.Store(time.Now().UnixNano())
	return nil
}

func (m *Manager) message(msg webrtc.DataChannelMessage) {
	if !m.permitted() {
		_ = m.Cancel()
		return
	}
	if !msg.IsString {
		m.chunk(msg.Data)
		return
	}
	if len(msg.Data) > 256<<10 {
		return
	}
	var x wire
	if json.Unmarshal(msg.Data, &x) != nil || !validID(x.ID) {
		return
	}
	switch x.Type {
	case "list":
		if m.role != Host {
			return
		}
		entries, e := os.ReadDir(m.root)
		if e != nil {
			_ = m.send(wire{Type: "list_result", ID: x.ID, Error: "无法读取共享目录"})
			return
		}
		files := make([]File, 0)
		for _, entry := range entries {
			if len(files) >= maxFiles {
				break
			}
			if !validName(entry.Name()) || strings.HasPrefix(entry.Name(), ".godeck-upload-") {
				continue
			}
			info, e := entry.Info()
			if e == nil && info.Mode().IsRegular() {
				files = append(files, File{Name: entry.Name(), Size: info.Size(), Modified: info.ModTime()})
			}
		}
		_ = m.send(wire{Type: "list_result", ID: x.ID, Files: files})

	case "list_result":
		m.mu.Lock()
		ok := m.role == Viewer && m.pendingList == x.ID
		if ok {
			m.pendingList = ""
		}
		m.mu.Unlock()
		if ok {
			m.event(Event{Type: "list", Files: x.Files, Error: x.Error})
		}

	case "upload":
		if m.role != Host || !validName(x.Name) {
			return
		}
		t := &task{
			id:      x.ID,
			name:    x.Name,
			signals: make(chan wire, 64),
			stop:    make(chan struct{}),
		}
		if e := m.start(t); e != nil {
			_ = m.send(wire{Type: "result", ID: x.ID, Error: e.Error()})
			return
		}
		if e := m.receiveFile(t, x.Size); e != nil {
			_ = m.send(wire{Type: "result", ID: x.ID, Error: e.Error()})
			m.finish(t, e)
			return
		}
		_ = m.send(wire{Type: "ready", ID: x.ID})
		go m.deadline(t)

	case "download":
		if m.role != Host || !validName(x.Name) {
			return
		}
		f, e := openRegular(filepath.Join(m.root, x.Name))
		if e != nil {
			_ = m.send(wire{Type: "result", ID: x.ID, Error: "文件不可用"})
			return
		}
		info, e := f.Stat()
		if e != nil || info.Size() > MaxFileBytes {
			f.Close()
			_ = m.send(wire{Type: "result", ID: x.ID, Error: "文件超过限制或不可用"})
			return
		}
		t := &task{
			id:      x.ID,
			name:    x.Name,
			total:   info.Size(),
			file:    f,
			sender:  true,
			signals: make(chan wire, 64),
			stop:    make(chan struct{}),
		}
		if e = m.start(t); e != nil {
			t.close()
			_ = m.send(wire{Type: "result", ID: x.ID, Error: e.Error()})
			return
		}
		if e = m.send(wire{Type: "begin", ID: t.id, Name: t.name, Size: t.total}); e != nil {
			m.finish(t, e)
			return
		}
		go m.sendFile(t)

	case "begin":
		m.mu.Lock()
		t := m.current
		m.mu.Unlock()
		if m.role != Viewer || t == nil || t.id != x.ID || t.sender || t.name != x.Name {
			return
		}
		if e := m.receiveFile(t, x.Size); e != nil {
			_ = m.send(wire{Type: "result", ID: x.ID, Error: e.Error()})
			m.finish(t, e)
			return
		}
		_ = m.send(wire{Type: "ready", ID: x.ID})

	case "ready", "ack", "result":
		m.mu.Lock()
		t := m.current
		m.mu.Unlock()
		if t == nil || t.id != x.ID {
			return
		}
		if !t.sender && x.Type == "result" {
			m.finish(t, errors.New(x.Error))
			return
		}
		select {
		case t.signals <- x:
		default:
			// Bounded signals queue overflow
			m.finish(t, errors.New("传输确认队列溢出"))
		}

	case "done":
		m.mu.Lock()
		t := m.current
		m.mu.Unlock()
		if t == nil || t.id != x.ID || t.sender {
			return
		}
		t.mu.Lock()
		if t.file == nil || t.offset != t.total || x.Offset != t.total {
			t.mu.Unlock()
			_ = m.send(wire{Type: "result", ID: x.ID, Error: "文件长度不符"})
			m.finish(t, errors.New("文件长度不符"))
			return
		}
		e := t.file.Sync()
		if e == nil {
			e = t.file.Close()
		}
		t.file = nil
		if e == nil && m.permitted() {
			src := filepath.Join(t.temp, "data")
			dst := filepath.Join(m.root, t.name)
			if e = os.Link(src, dst); e != nil {
				if _, statErr := os.Lstat(dst); statErr == nil {
					e = errors.New("无法保存：同名文件已存在")
				} else if os.IsNotExist(statErr) {
					if renErr := os.Rename(src, dst); renErr == nil {
						e = nil
					} else {
						e = errors.New("无法保存文件：" + renErr.Error())
					}
				} else {
					e = errors.New("无法保存文件：" + e.Error())
				}
			}
		} else if e == nil {
			e = errors.New("文件权限已撤回")
		}
		t.mu.Unlock()
		if e != nil {
			_ = m.send(wire{Type: "result", ID: x.ID, Error: e.Error()})
			m.finish(t, e)
			return
		}
		_ = m.send(wire{Type: "result", ID: x.ID})
		m.finish(t, nil)

	case "cancel":
		m.mu.Lock()
		t := m.current
		m.mu.Unlock()
		if t != nil && t.id == x.ID {
			m.finish(t, errors.New("对方取消了传输"))
		}
	}
}

func (m *Manager) chunk(data []byte) {
	if len(data) < 41 || len(data) > 41+chunkBytes || data[0] != 'D' {
		return
	}
	id := string(data[1:33])
	offset := binary.BigEndian.Uint64(data[33:41])

	m.mu.Lock()
	t := m.current
	m.mu.Unlock()
	if t == nil || t.id != id || t.sender {
		return
	}

	t.mu.Lock()
	if t.file == nil || offset != uint64(t.offset) || int64(len(data)-41) > t.total-t.offset {
		t.mu.Unlock()
		m.finish(t, errors.New("数据偏移或长度无效"))
		return
	}
	n, e := t.file.Write(data[41:])
	if e != nil || n != len(data)-41 {
		t.mu.Unlock()
		m.finish(t, errors.New("写入文件失败"))
		return
	}
	t.offset += int64(n)
	now, total := t.offset, t.total
	t.mu.Unlock()
	t.last.Store(time.Now().UnixNano())

	_ = m.send(wire{Type: "ack", ID: id, Offset: now})
	m.event(Event{Type: "progress", ID: id, Name: t.name, Bytes: now, Total: total})
}
