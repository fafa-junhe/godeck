// Package transfer implements bounded, explicitly authorized file transfers
// inside a single shared directory tree, with a sequential job queue on the
// viewer side.
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
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
)

const MaxFileBytes int64 = 100 << 20
const chunkBytes = 16 << 10
const windowBytes int64 = 512 << 10 // sliding window in flight
const maxBuffered = 64 << 10       // DataChannel buffer limit
const maxListEntries = 2000
const maxTreeEntries = 10000
const maxQueuedJobs = 20000
const timeout = 30 * time.Second

type Role int

const (
	Viewer Role = iota
	Host
)

// File is one entry of a remote directory listing.
type File struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Dir      bool      `json:"dir,omitempty"`
}

type treeFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type Event struct {
	Type  string // "ready", "list", "progress", "complete", "error", "jobs"
	ID    string
	Name  string
	Path  string // directory of a "list" event
	Error string
	Bytes int64
	Total int64
	Files []File
}

type wire struct {
	Type   string     `json:"type"`
	ID     string     `json:"id,omitempty"`
	Name   string     `json:"name,omitempty"`
	Path   string     `json:"path,omitempty"`
	Size   int64      `json:"size,omitempty"`
	Offset int64      `json:"offset,omitempty"`
	Error  string     `json:"error,omitempty"`
	Files  []File     `json:"files,omitempty"`
	Tree   []treeFile `json:"tree,omitempty"`
	Dirs   []string   `json:"dirs,omitempty"`
}

// JobKind is the operation of a queued job.
type JobKind string

const (
	JobUpload   JobKind = "upload"
	JobDownload JobKind = "download"
	JobMkdir    JobKind = "mkdir"
)

// JobState is the lifecycle state of a queued job.
type JobState string

const (
	JobQueued   JobState = "queued"
	JobRunning  JobState = "running"
	JobDone     JobState = "done"
	JobFailed   JobState = "failed"
	JobCanceled JobState = "canceled"
)

// Job is one queued transfer, as seen by the viewer UI.
type Job struct {
	ID     int
	Kind   JobKind
	Local  string // upload: absolute local source; download: path relative to the download dir
	Remote string // wire path in the remote share
	Size   int64
	Done   int64
	State  JobState
	Error  string
	Saved  string // final name when renamed to avoid overwriting
}

type task struct {
	mu      sync.Mutex
	id      string
	name    string // target file name
	dir     string // target directory (receiver) for temp + commit
	total   int64
	offset  int64
	file    *os.File
	temp    string
	sender  bool
	job     *Job
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

type pendingTree struct {
	remote string // requested remote directory
	local  string // local base path relative to the download dir
}

type Manager struct {
	mu          sync.Mutex
	pumpMu      sync.Mutex // serializes job selection + start
	dc          *webrtc.DataChannel
	role        Role
	root        string
	allowed     func() bool
	emit        func(Event)
	current     *task
	closed      bool
	pendingList string
	listPath    string
	trees       map[string]pendingTree
	jobs        []*Job
	nextJob     int
}

func New(dc *webrtc.DataChannel, role Role, root string, allowed func() bool, emit func(Event)) *Manager {
	m := &Manager{
		dc:      dc,
		role:    role,
		root:    root,
		allowed: allowed,
		emit:    emit,
		trees:   make(map[string]pendingTree),
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

func errOf(s string) error {
	if s == "" {
		return nil
	}
	return errors.New(s)
}

// wirePath accepts the new "path" field and falls back to the legacy "name".
func wirePath(x wire) string {
	if x.Path != "" {
		return x.Path
	}
	return x.Name
}

// ---------------------------------------------------------------------------
// Viewer API

// List requests the entries of a remote directory ("" is the share root).
// A newer request supersedes an older pending one.
func (m *Manager) List(dir string) error {
	if m.role != Viewer {
		return errors.New("仅查看方可请求列表")
	}
	if _, ok := splitPath(dir); !ok {
		return errors.New("目录路径无效")
	}
	req := id()
	if req == "" {
		return errors.New("无法创建请求")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("文件通道已关闭")
	}
	m.pendingList = req
	m.listPath = dir
	m.mu.Unlock()

	if e := m.send(wire{Type: "list", ID: req, Path: dir}); e != nil {
		m.mu.Lock()
		if m.pendingList == req {
			m.pendingList = ""
		}
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

// Upload queues a single local file or a whole local folder into the remote share root.
func (m *Manager) Upload(path string) error { return m.EnqueueUpload(path, "") }

// Download queues a remote file from the share root.
func (m *Manager) Download(name string) error { return m.EnqueueDownload(name, false) }

func (m *Manager) addJobs(jobs []*Job) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("文件通道已关闭")
	}
	queued := 0
	for _, j := range m.jobs {
		if j.State == JobQueued || j.State == JobRunning {
			queued++
		}
	}
	if queued+len(jobs) > maxQueuedJobs {
		m.mu.Unlock()
		return errors.New("排队任务过多，请等待当前任务完成")
	}
	for _, j := range jobs {
		m.nextJob++
		j.ID = m.nextJob
		if j.State == "" {
			j.State = JobQueued
		}
		m.jobs = append(m.jobs, j)
	}
	m.mu.Unlock()
	m.event(Event{Type: "jobs"})
	go m.pump()
	return nil
}

// EnqueueUpload queues a local file, or every regular file under a local
// folder (preserving structure), for upload into remoteDir. Symlinks and
// special files are skipped; names the peer could not store are rejected.
func (m *Manager) EnqueueUpload(local, remoteDir string) error {
	if m.role != Viewer {
		return errors.New("仅查看方可上传")
	}
	if _, ok := splitPath(remoteDir); !ok {
		return errors.New("目标目录无效")
	}
	info, err := os.Lstat(local)
	if err != nil {
		return err
	}
	base := filepath.Base(local)
	if !validName(base) {
		return errors.New("名称不受支持：" + base)
	}
	if info.Mode().IsRegular() {
		j := &Job{Kind: JobUpload, Local: local, Remote: JoinPath(remoteDir, base), Size: info.Size()}
		if info.Size() > MaxFileBytes {
			j.State, j.Error = JobFailed, "超过 100 MiB 上限"
		}
		return m.addJobs([]*Job{j})
	}
	if !isPlainDir(info) {
		return errors.New("仅支持普通文件或文件夹：" + base)
	}

	var jobs []*Job
	skipped := 0
	err = filepath.WalkDir(local, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			skipped++
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, e := filepath.Rel(filepath.Dir(local), p)
		if e != nil {
			return e
		}
		remote := JoinPath(remoteDir, filepath.ToSlash(rel))
		if _, ok := splitPath(remote); !ok {
			skipped++
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		fi, e := d.Info()
		if e != nil {
			skipped++
			return nil
		}
		switch {
		case isPlainDir(fi):
			jobs = append(jobs, &Job{Kind: JobMkdir, Local: p, Remote: remote})
		case fi.Mode().IsRegular():
			j := &Job{Kind: JobUpload, Local: p, Remote: remote, Size: fi.Size()}
			if fi.Size() > MaxFileBytes {
				j.State, j.Error = JobFailed, "超过 100 MiB 上限"
			}
			jobs = append(jobs, j)
		default:
			skipped++ // symlinks, devices, junctions
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		if len(jobs) > maxTreeEntries {
			return errors.New("文件夹内条目超过 10000 个")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err = m.addJobs(jobs); err != nil {
		return err
	}
	if skipped > 0 {
		m.event(Event{Type: "error", Error: "已跳过 " + itoa(skipped) + " 个链接、特殊文件或名称不受支持的条目"})
	}
	return nil
}

// EnqueueDownload queues a remote file, or (dir=true) every file under a
// remote folder, for download into the local download directory.
func (m *Manager) EnqueueDownload(remote string, dir bool) error {
	if m.role != Viewer {
		return errors.New("仅查看方可下载")
	}
	parts, ok := splitPath(remote)
	if !ok || len(parts) == 0 {
		return errors.New("文件路径无效")
	}
	if !dir {
		return m.addJobs([]*Job{{Kind: JobDownload, Remote: remote, Local: BaseName(remote)}})
	}
	req := id()
	if req == "" {
		return errors.New("无法创建请求")
	}
	m.mu.Lock()
	m.trees[req] = pendingTree{remote: remote, local: BaseName(remote)}
	m.mu.Unlock()
	if e := m.send(wire{Type: "tree", ID: req, Path: remote}); e != nil {
		m.mu.Lock()
		delete(m.trees, req)
		m.mu.Unlock()
		return e
	}
	go func() {
		time.Sleep(timeout)
		m.mu.Lock()
		_, waiting := m.trees[req]
		delete(m.trees, req)
		m.mu.Unlock()
		if waiting {
			m.event(Event{Type: "error", Error: "读取远端文件夹超时"})
		}
	}()
	return nil
}

// Jobs returns a snapshot of the queue, oldest first.
func (m *Manager) Jobs() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, len(m.jobs))
	for i, j := range m.jobs {
		out[i] = *j
	}
	return out
}

// Cancel aborts the running job; queued jobs continue.
func (m *Manager) Cancel() error {
	m.mu.Lock()
	t := m.current
	m.mu.Unlock()
	if t != nil {
		_ = m.send(wire{Type: "cancel", ID: t.id})
		m.finishState(t, errors.New("已取消"), JobCanceled)
	}
	return nil
}

// CancelAll drops every queued job and aborts the running one.
func (m *Manager) CancelAll() {
	m.mu.Lock()
	for _, j := range m.jobs {
		if j.State == JobQueued {
			j.State = JobCanceled
			j.Error = "已取消"
		}
	}
	m.mu.Unlock()
	_ = m.Cancel()
	m.event(Event{Type: "jobs"})
}

// RetryFailed re-queues failed and canceled jobs, except oversize files.
func (m *Manager) RetryFailed() {
	m.mu.Lock()
	for _, j := range m.jobs {
		if (j.State == JobFailed || j.State == JobCanceled) && j.Size <= MaxFileBytes {
			j.State, j.Error, j.Done, j.Saved = JobQueued, "", 0, ""
		}
	}
	m.mu.Unlock()
	m.event(Event{Type: "jobs"})
	go m.pump()
}

// ClearFinished removes completed, failed and canceled jobs from the list.
func (m *Manager) ClearFinished() {
	m.mu.Lock()
	kept := m.jobs[:0]
	for _, j := range m.jobs {
		if j.State == JobQueued || j.State == JobRunning {
			kept = append(kept, j)
		}
	}
	for i := len(kept); i < len(m.jobs); i++ {
		m.jobs[i] = nil
	}
	m.jobs = kept
	m.mu.Unlock()
	m.event(Event{Type: "jobs"})
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
	for _, j := range m.jobs {
		if j.State == JobQueued || j.State == JobRunning {
			j.State, j.Error = JobCanceled, "连接已断开"
		}
	}
	m.mu.Unlock()
	if t != nil {
		t.close()
	}
	m.event(Event{Type: "jobs"})
	return nil
}

// pump starts the next queued job when the channel is idle.
func (m *Manager) pump() {
	m.pumpMu.Lock()
	defer m.pumpMu.Unlock()
	for {
		m.mu.Lock()
		if m.closed || m.current != nil || m.role != Viewer {
			m.mu.Unlock()
			return
		}
		var j *Job
		for _, x := range m.jobs {
			if x.State == JobQueued {
				j = x
				break
			}
		}
		if j == nil {
			m.mu.Unlock()
			return
		}
		j.State = JobRunning
		j.Done = 0
		m.mu.Unlock()
		m.event(Event{Type: "jobs"})

		var err error
		switch j.Kind {
		case JobUpload:
			err = m.startUpload(j)
		case JobDownload:
			err = m.startDownload(j)
		case JobMkdir:
			err = m.startMkdir(j)
		}
		if err == nil {
			return // finish() will pump again
		}
		m.mu.Lock()
		if j.State == JobRunning {
			j.State, j.Error = JobFailed, err.Error()
		}
		m.mu.Unlock()
		m.event(Event{Type: "jobs"})
	}
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

func newTask(taskID, name string, job *Job) *task {
	return &task{
		id:      taskID,
		name:    name,
		job:     job,
		signals: make(chan wire, 64),
		stop:    make(chan struct{}),
	}
}

func (m *Manager) startUpload(j *Job) error {
	f, e := openRegular(j.Local)
	if e != nil {
		return errors.New("无法读取本地文件")
	}
	info, e := f.Stat()
	if e != nil || info.Size() > MaxFileBytes {
		f.Close()
		return errors.New("文件不可用或超过 100 MiB")
	}
	t := newTask(id(), BaseName(j.Remote), j)
	t.total = info.Size()
	t.file = f
	t.sender = true
	if t.id == "" {
		t.close()
		return errors.New("无法创建任务")
	}
	if e = m.start(t); e != nil {
		t.close()
		return e
	}
	m.mu.Lock()
	j.Size = t.total
	m.mu.Unlock()
	if e = m.send(wire{Type: "upload", ID: t.id, Name: t.name, Path: j.Remote, Size: t.total}); e != nil {
		m.finish(t, e)
		return nil
	}
	go m.sendFile(t)
	return nil
}

func (m *Manager) startDownload(j *Job) error {
	localParts, ok := splitPath(j.Local)
	if !ok || len(localParts) == 0 {
		return errors.New("本地路径无效")
	}
	dir, e := resolveDir(m.root, localParts[:len(localParts)-1], true)
	if e != nil {
		return e
	}
	t := newTask(id(), localParts[len(localParts)-1], j)
	t.dir = dir
	if t.id == "" {
		return errors.New("无法创建任务")
	}
	if e = m.start(t); e != nil {
		return e
	}
	if e = m.send(wire{Type: "download", ID: t.id, Name: BaseName(j.Remote), Path: j.Remote}); e != nil {
		m.finish(t, e)
		return nil
	}
	go m.deadline(t)
	return nil
}

func (m *Manager) startMkdir(j *Job) error {
	t := newTask(id(), BaseName(j.Remote), j)
	if t.id == "" {
		return errors.New("无法创建任务")
	}
	if e := m.start(t); e != nil {
		return e
	}
	if e := m.send(wire{Type: "mkdir", ID: t.id, Path: j.Remote}); e != nil {
		m.finish(t, e)
		return nil
	}
	go m.deadline(t)
	return nil
}

func (m *Manager) finish(t *task, e error) {
	state := JobDone
	if e != nil {
		state = JobFailed
	}
	m.finishState(t, e, state)
}

func (m *Manager) finishState(t *task, e error, state JobState) {
	m.mu.Lock()
	if m.current != t {
		m.mu.Unlock()
		return
	}
	m.current = nil
	t.mu.Lock()
	offset, total := t.offset, t.total
	t.mu.Unlock()
	if j := t.job; j != nil {
		j.State = state
		j.Done = offset
		if e != nil {
			j.Error = e.Error()
		}
	}
	m.mu.Unlock()
	t.close()

	event := Event{ID: t.id, Name: t.name, Bytes: offset, Total: total}
	if e != nil {
		event.Type = "error"
		event.Error = t.name + "：" + e.Error()
	} else {
		event.Type = "complete"
	}
	m.event(event)
	if t.job != nil {
		m.event(Event{Type: "jobs"})
		go m.pump()
	}
}

func (m *Manager) progress(t *task, done int64) {
	m.mu.Lock()
	if t.job != nil {
		t.job.Done = done
	}
	m.mu.Unlock()
	m.event(Event{Type: "progress", ID: t.id, Name: t.name, Bytes: done, Total: t.total})
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
				m.finish(t, errors.New("等待对端响应超时，结果未确认；请刷新列表"))
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

	// handle applies one peer signal; it returns false when the task ended.
	handle := func(sig wire) bool {
		if sig.Type == "ack" && sig.Offset > ackedOffset {
			ackedOffset = sig.Offset
			t.mu.Lock()
			t.offset = ackedOffset
			t.mu.Unlock()
			m.progress(t, ackedOffset)
		} else if sig.Type == "result" && sig.Error != "" {
			m.finish(t, errors.New(sig.Error))
			return false
		}
		return true
	}

	for sentOffset < t.total {
		if !m.permitted() {
			m.finish(t, errors.New("文件权限已撤回"))
			return
		}

		// Sliding window backpressure: wait while too many bytes are in flight.
		for sentOffset-ackedOffset >= windowBytes {
			sig, err := m.signal(t)
			if err != nil {
				m.finish(t, err)
				return
			}
			if !handle(sig) {
				return
			}
		}

		// WebRTC DataChannel buffer backpressure.
		for m.dc.BufferedAmount() > maxBuffered {
			select {
			case <-t.stop:
				return
			case sig := <-t.signals:
				if !handle(sig) {
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

		// Drain available ACKs non-blockingly.
	drain:
		for {
			select {
			case sig := <-t.signals:
				if !handle(sig) {
					return
				}
			default:
				break drain
			}
		}
	}

	// Wait until every sent byte is acknowledged.
	for ackedOffset < t.total {
		sig, err := m.signal(t)
		if err != nil {
			m.finish(t, err)
			return
		}
		if !handle(sig) {
			return
		}
	}

	if e = m.send(wire{Type: "done", ID: t.id, Offset: t.total}); e != nil {
		m.finish(t, e)
		return
	}
	for {
		result, e := m.signal(t)
		if e != nil {
			m.finish(t, e)
			return
		}
		if result.Type == "ack" {
			continue
		}
		if result.Type != "result" || result.Error != "" {
			m.finish(t, errors.New(result.Error))
			return
		}
		if t.job != nil && result.Name != "" && result.Name != t.name {
			m.mu.Lock()
			t.job.Saved = result.Name
			m.mu.Unlock()
		}
		m.finish(t, nil)
		return
	}
}

// receiveFile prepares a private temp file inside the target directory so the
// final commit is a same-filesystem link.
func (m *Manager) receiveFile(t *task, size int64) error {
	if size < 0 || size > MaxFileBytes {
		return errors.New("文件大小无效")
	}
	dir, err := os.MkdirTemp(t.dir, tempPrefix)
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

// ---------------------------------------------------------------------------
// Host handlers

func (m *Manager) hostList(x wire) {
	parts, ok := splitPath(x.Path)
	if !ok {
		_ = m.send(wire{Type: "list_result", ID: x.ID, Path: x.Path, Error: "目录路径无效"})
		return
	}
	dir, err := resolveDir(m.root, parts, false)
	if err != nil {
		_ = m.send(wire{Type: "list_result", ID: x.ID, Path: x.Path, Error: err.Error()})
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		_ = m.send(wire{Type: "list_result", ID: x.ID, Path: x.Path, Error: "无法读取共享目录"})
		return
	}
	files := make([]File, 0)
	for _, entry := range entries {
		if len(files) >= maxListEntries {
			break
		}
		if !validName(entry.Name()) {
			continue
		}
		info, e := entry.Info()
		if e != nil {
			continue
		}
		if isPlainDir(info) {
			files = append(files, File{Name: entry.Name(), Modified: info.ModTime(), Dir: true})
		} else if info.Mode().IsRegular() {
			files = append(files, File{Name: entry.Name(), Size: info.Size(), Modified: info.ModTime()})
		}
	}
	_ = m.send(wire{Type: "list_result", ID: x.ID, Path: x.Path, Files: files})
}

func (m *Manager) hostTree(x wire) {
	parts, ok := splitPath(x.Path)
	if !ok || len(parts) == 0 {
		_ = m.send(wire{Type: "tree_result", ID: x.ID, Error: "目录路径无效"})
		return
	}
	top, err := resolveDir(m.root, parts, false)
	if err != nil {
		_ = m.send(wire{Type: "tree_result", ID: x.ID, Error: err.Error()})
		return
	}
	var files []treeFile
	var dirs []string
	count := 0
	var walk func(abs, rel string, depth int) error
	walk = func(abs, rel string, depth int) error {
		if depth > maxPathDepth {
			return errors.New("文件夹层级过深")
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			return errors.New("无法读取文件夹")
		}
		dirs = append(dirs, rel)
		for _, entry := range entries {
			if !validName(entry.Name()) {
				continue
			}
			count++
			if count > maxTreeEntries {
				return errors.New("文件夹内条目超过 10000 个")
			}
			info, e := entry.Info()
			if e != nil {
				continue
			}
			childRel := JoinPath(rel, entry.Name())
			if isPlainDir(info) {
				if err := walk(filepath.Join(abs, entry.Name()), childRel, depth+1); err != nil {
					return err
				}
			} else if info.Mode().IsRegular() {
				files = append(files, treeFile{Path: childRel, Size: info.Size()})
			}
		}
		return nil
	}
	if err = walk(top, x.Path, len(parts)); err != nil {
		_ = m.send(wire{Type: "tree_result", ID: x.ID, Error: err.Error()})
		return
	}
	if files == nil {
		files = []treeFile{}
	}
	_ = m.send(wire{Type: "tree_result", ID: x.ID, Path: x.Path, Tree: files, Dirs: dirs})
}

func (m *Manager) hostUpload(x wire) {
	parts, ok := splitPath(wirePath(x))
	if !ok || len(parts) == 0 {
		_ = m.send(wire{Type: "result", ID: x.ID, Error: "文件名不受支持"})
		return
	}
	t := newTask(x.ID, parts[len(parts)-1], nil)
	if e := m.start(t); e != nil {
		_ = m.send(wire{Type: "result", ID: x.ID, Error: e.Error()})
		return
	}
	dir, e := resolveDir(m.root, parts[:len(parts)-1], true)
	if e == nil {
		t.dir = dir
		e = m.receiveFile(t, x.Size)
	}
	if e != nil {
		_ = m.send(wire{Type: "result", ID: x.ID, Error: e.Error()})
		m.finish(t, e)
		return
	}
	_ = m.send(wire{Type: "ready", ID: x.ID})
	go m.deadline(t)
}

func (m *Manager) hostMkdir(x wire) {
	parts, ok := splitPath(x.Path)
	if !ok || len(parts) == 0 {
		_ = m.send(wire{Type: "result", ID: x.ID, Error: "目录名不受支持"})
		return
	}
	if _, e := resolveDir(m.root, parts, true); e != nil {
		_ = m.send(wire{Type: "result", ID: x.ID, Error: e.Error()})
		return
	}
	_ = m.send(wire{Type: "result", ID: x.ID, Name: parts[len(parts)-1]})
}

func (m *Manager) hostDownload(x wire) {
	parts, ok := splitPath(wirePath(x))
	if !ok || len(parts) == 0 {
		_ = m.send(wire{Type: "result", ID: x.ID, Error: "文件名无效"})
		return
	}
	dir, e := resolveDir(m.root, parts[:len(parts)-1], false)
	if e != nil {
		_ = m.send(wire{Type: "result", ID: x.ID, Error: e.Error()})
		return
	}
	f, e := openRegular(filepath.Join(dir, parts[len(parts)-1]))
	if e != nil {
		_ = m.send(wire{Type: "result", ID: x.ID, Error: "文件不可用"})
		return
	}
	info, e := f.Stat()
	if e != nil || info.Size() > MaxFileBytes {
		f.Close()
		_ = m.send(wire{Type: "result", ID: x.ID, Error: "文件超过 100 MiB 限制或不可用"})
		return
	}
	t := newTask(x.ID, parts[len(parts)-1], nil)
	t.total = info.Size()
	t.file = f
	t.sender = true
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
}

// viewerTree turns a remote folder listing into download jobs and creates
// empty local folders right away.
func (m *Manager) viewerTree(x wire) {
	m.mu.Lock()
	req, ok := m.trees[x.ID]
	delete(m.trees, x.ID)
	m.mu.Unlock()
	if !ok || m.role != Viewer {
		return
	}
	if x.Error != "" {
		m.event(Event{Type: "error", Error: "读取远端文件夹失败：" + x.Error})
		return
	}
	prefix := req.remote + "/"
	localPath := func(remote string) (string, bool) {
		if remote == req.remote {
			return req.local, true
		}
		if len(remote) <= len(prefix) || remote[:len(prefix)] != prefix {
			return "", false
		}
		local := JoinPath(req.local, remote[len(prefix):])
		_, valid := splitPath(local)
		return local, valid
	}
	for _, d := range x.Dirs {
		if local, ok := localPath(d); ok {
			if parts, ok := splitPath(local); ok {
				_, _ = resolveDir(m.root, parts, true)
			}
		}
	}
	jobs := make([]*Job, 0, len(x.Tree))
	for _, f := range x.Tree {
		local, ok := localPath(f.Path)
		if !ok {
			continue
		}
		if _, ok = splitPath(f.Path); !ok {
			continue
		}
		j := &Job{Kind: JobDownload, Remote: f.Path, Local: local, Size: f.Size}
		if f.Size > MaxFileBytes {
			j.State, j.Error = JobFailed, "超过 100 MiB 上限"
		}
		jobs = append(jobs, j)
	}
	if len(jobs) == 0 {
		m.event(Event{Type: "error", Error: "远端文件夹为空，已在本机创建目录"})
		return
	}
	if err := m.addJobs(jobs); err != nil {
		m.event(Event{Type: "error", Error: err.Error()})
	}
}

// ---------------------------------------------------------------------------
// Message dispatch

func (m *Manager) message(msg webrtc.DataChannelMessage) {
	if !m.permitted() {
		_ = m.Cancel()
		return
	}
	if !msg.IsString {
		m.chunk(msg.Data)
		return
	}
	if len(msg.Data) > 4<<20 {
		return
	}
	var x wire
	if json.Unmarshal(msg.Data, &x) != nil || !validID(x.ID) {
		return
	}
	switch x.Type {
	case "list":
		if m.role == Host {
			m.hostList(x)
		}

	case "tree":
		if m.role == Host {
			m.hostTree(x)
		}

	case "upload":
		if m.role == Host {
			m.hostUpload(x)
		}

	case "mkdir":
		if m.role == Host {
			m.hostMkdir(x)
		}

	case "download":
		if m.role == Host {
			m.hostDownload(x)
		}

	case "list_result":
		m.mu.Lock()
		ok := m.role == Viewer && m.pendingList == x.ID
		path := m.listPath
		if ok {
			m.pendingList = ""
		}
		m.mu.Unlock()
		if ok {
			m.event(Event{Type: "list", Path: path, Files: x.Files, Error: x.Error})
		}

	case "tree_result":
		m.viewerTree(x)

	case "begin":
		m.mu.Lock()
		t := m.current
		m.mu.Unlock()
		if m.role != Viewer || t == nil || t.id != x.ID || t.sender || t.job == nil || t.job.Kind != JobDownload {
			return
		}
		if e := m.receiveFile(t, x.Size); e != nil {
			_ = m.send(wire{Type: "result", ID: x.ID, Error: e.Error()})
			m.finish(t, e)
			return
		}
		m.mu.Lock()
		t.job.Size = x.Size
		m.mu.Unlock()
		_ = m.send(wire{Type: "ready", ID: x.ID})

	case "ready", "ack", "result":
		m.mu.Lock()
		t := m.current
		m.mu.Unlock()
		if t == nil || t.id != x.ID {
			return
		}
		if !t.sender && x.Type == "result" {
			// mkdir success, or a download/upload refused by the peer.
			m.finish(t, errOf(x.Error))
			return
		}
		select {
		case t.signals <- x:
		default:
			m.finish(t, errors.New("传输确认队列溢出"))
		}

	case "done":
		m.mu.Lock()
		t := m.current
		m.mu.Unlock()
		if t == nil || t.id != x.ID || t.sender {
			return
		}
		m.commit(t, x)

	case "cancel":
		m.mu.Lock()
		t := m.current
		m.mu.Unlock()
		if t != nil && t.id == x.ID {
			m.finish(t, errors.New("对方取消了传输"))
		}
	}
}

func (m *Manager) commit(t *task, x wire) {
	t.mu.Lock()
	if t.file == nil || t.offset != t.total || x.Offset != t.total {
		t.mu.Unlock()
		_ = m.send(wire{Type: "result", ID: x.ID, Error: "文件长度不符"})
		m.finish(t, errors.New("文件长度不符"))
		return
	}
	e := t.file.Sync()
	if cerr := t.file.Close(); e == nil {
		e = cerr
	}
	t.file = nil
	saved := ""
	if e == nil && m.permitted() {
		saved, e = commitUnique(filepath.Join(t.temp, "data"), t.dir, t.name)
	} else if e == nil {
		e = errors.New("文件权限已撤回")
	}
	t.mu.Unlock()
	if e != nil {
		_ = m.send(wire{Type: "result", ID: x.ID, Error: e.Error()})
		m.finish(t, e)
		return
	}
	if t.job != nil && saved != t.name {
		m.mu.Lock()
		t.job.Saved = saved
		m.mu.Unlock()
	}
	_ = m.send(wire{Type: "result", ID: x.ID, Name: saved})
	m.finish(t, nil)
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
	now := t.offset
	t.mu.Unlock()
	t.last.Store(time.Now().UnixNano())

	_ = m.send(wire{Type: "ack", ID: id, Offset: now})
	m.progress(t, now)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
