// Package signaling implements a pinned-certificate LAN signaling transport.
// Certificate probing is deliberately separate from sending any session secret.
package signaling

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

const maxOfferBytes = 256 << 10
const ApprovalDeadline = 60 * time.Second

type Identity struct {
	Certificate tls.Certificate
	Fingerprint string
}

func random(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(rand.Reader, b)
	return b, err
}

func NewSecret() (string, error) {
	b, err := random(32)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func Fingerprint(cert []byte) string {
	sum := sha256.Sum256(cert)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func LoadIdentity(dir string) (Identity, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Identity{}, err
	}
	certPath, keyPath := filepath.Join(dir, "identity-cert.pem"), filepath.Join(dir, "identity-key.pem")
	certPEM, errCert := os.ReadFile(certPath)
	keyPEM, errKey := os.ReadFile(keyPath)
	if os.IsNotExist(errCert) && os.IsNotExist(errKey) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return Identity{}, err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return Identity{}, err
		}
		now := time.Now()
		template := &x509.Certificate{
			SerialNumber:          serial,
			Subject:               pkix.Name{CommonName: "GoDeck local device"},
			NotBefore:             now.Add(-time.Hour),
			NotAfter:              now.AddDate(2, 0, 0),
			KeyUsage:              x509.KeyUsageDigitalSignature,
			ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			BasicConstraintsValid: true,
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			return Identity{}, err
		}
		private, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return Identity{}, err
		}
		certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})
		// O_EXCL avoids silently replacing existing or partially written identities.
		f, err := os.OpenFile(keyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return Identity{}, err
		}
		_, err = f.Write(keyPEM)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return Identity{}, err
		}
		f, err = os.OpenFile(certPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return Identity{}, err
		}
		_, err = f.Write(certPEM)
		if err == nil {
			err = f.Sync()
		}
		closeErr = f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return Identity{}, err
		}
	} else if errCert != nil || errKey != nil {
		return Identity{}, errors.New("设备证书不完整或不可读取，请检查身份目录")
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil || len(cert.Certificate) == 0 {
		return Identity{}, errors.New("设备证书或私钥无效")
	}
	return Identity{Certificate: cert, Fingerprint: Fingerprint(cert.Certificate[0])}, nil
}

// ProbeFingerprint sends no secret. It is informational until confirmed out of band.
func ProbeFingerprint(ctx context.Context, address string) (string, error) {
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 8 * time.Second},
		Config: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true,
		},
	}
	c, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return "", err
	}
	defer c.Close()
	state := c.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", errors.New("对端未提供证书")
	}
	return Fingerprint(state.PeerCertificates[0].Raw), nil
}

func pinnedTLSConfig(expected string) *tls.Config {
	expected = strings.ReplaceAll(strings.ToUpper(expected), ":", "")
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("对端没有证书")
			}
			actual := Fingerprint(state.PeerCertificates[0].Raw)
			if len(actual) != 64 || len(expected) != 64 || subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
				return errors.New("证书指纹不匹配")
			}
			return nil
		},
	}
}

func PinnedClient(expected string) *http.Client {
	return &http.Client{
		Timeout: 90 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig:   pinnedTLSConfig(expected),
		},
	}
}

type Capabilities struct {
	Transports []string `json:"transports"`
	UDPProbe   bool     `json:"udp_probe"`
}

type Offer struct {
	RequestID       string `json:"request_id"`
	Name            string `json:"name"`
	SDP             string `json:"sdp"`
	Transport       string `json:"transport,omitempty"`
	TransportReason string `json:"transport_reason,omitempty"`
}

type Answer struct {
	SDP             string `json:"sdp"`
	Control         bool   `json:"control"`
	FilesAllowed    bool   `json:"files_allowed,omitempty"`
	Encoder         string `json:"encoder,omitempty"`
	EncoderDevice   string `json:"encoder_device,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	Preset          string `json:"preset,omitempty"`
	Transport       string `json:"transport,omitempty"`
	TransportReason string `json:"transport_reason,omitempty"`
}

type Request struct {
	ID       string
	Name     string
	Remote   string
	Deadline time.Time
}

type Decision struct {
	Accept  bool
	Control bool
	Files   bool
}

type pending struct {
	request  Request
	decision chan Decision
	cancel   context.CancelFunc
}

type attempt struct {
	count int
	until time.Time
}

type Host struct {
	mu          sync.Mutex
	attempts    map[string]attempt
	secret      string
	expires     time.Time
	accepting   bool
	current     *pending
	OnReserve   func() bool
	OnRelease   func()
	OnRequest   func(Request)
	OnOffer     func(context.Context, Offer, bool, bool) (Answer, error)
	OnWSConnect func(requestID string, ws *websocket.Conn) <-chan struct{}
}

func NewHost() (*Host, error) {
	secret, err := NewSecret()
	if err != nil {
		return nil, err
	}
	return &Host{
		attempts: map[string]attempt{},
		secret:   secret,
		expires:  time.Now().Add(30 * time.Minute),
	}, nil
}

func (h *Host) Secret() (string, time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !time.Now().Before(h.expires) {
		if secret, err := NewSecret(); err == nil {
			h.secret = secret
			h.expires = time.Now().Add(30 * time.Minute)
		}
	}
	return h.secret, h.expires
}

func (h *Host) SetAccepting(v bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.accepting = v
	if !v && h.current != nil {
		h.current.cancel()
	}
}

func (h *Host) Decide(id string, d Decision) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current == nil || h.current.request.ID != id {
		return false
	}
	if time.Now().After(h.current.request.Deadline) {
		return false
	}
	select {
	case h.current.decision <- d:
		return true
	default:
		return false
	}
}

func (h *Host) End() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current != nil {
		h.current.cancel()
	}
}

func (h *Host) authenticateLocked(r *http.Request) (int, string) {
	now := time.Now()
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	for k, v := range h.attempts {
		if !now.Before(v.until) {
			delete(h.attempts, k)
		}
	}
	entry, exists := h.attempts[ip]
	if !exists {
		if len(h.attempts) >= 4096 {
			return 429, "请求过多"
		}
		entry = attempt{until: now.Add(time.Minute)}
	}
	entry.count++
	h.attempts[ip] = entry
	if entry.count > 24 {
		return 429, "请求过多，请稍后重试"
	}
	secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	ok := h.accepting && now.Before(h.expires) && len(secret) == len(h.secret) && subtle.ConstantTimeCompare([]byte(secret), []byte(h.secret)) == 1
	if !ok {
		return 401, "设备未接受连接或密钥无效"
	}
	return 0, ""
}

func (h *Host) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/ws" && r.Method == "GET" {
		h.serveWS(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if r.URL.Path == "/probe" && r.Method == "GET" {
		h.mu.Lock()
		status, msg := h.authenticateLocked(r)
		h.mu.Unlock()
		if status != 0 {
			http.Error(w, msg, status)
			return
		}
		_ = json.NewEncoder(w).Encode(Capabilities{
			Transports: []string{"udp", "tcp", "websocket"},
			UDPProbe:   true,
		})
		return
	}

	if r.Method != "POST" || r.URL.Path != "/offer" {
		http.Error(w, "请求不可用", 404)
		return
	}
	h.mu.Lock()
	if status, msg := h.authenticateLocked(r); status != 0 {
		h.mu.Unlock()
		http.Error(w, msg, status)
		return
	}
	if h.current != nil {
		h.mu.Unlock()
		http.Error(w, "设备正忙", 409)
		return
	}
	if h.OnReserve != nil && !h.OnReserve() {
		h.mu.Unlock()
		http.Error(w, "设备正忙", 409)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	requestID, err := NewSecret()
	if err != nil {
		cancel()
		if h.OnRelease != nil {
			h.OnRelease()
		}
		h.mu.Unlock()
		http.Error(w, "无法创建请求", 500)
		return
	}
	p := &pending{
		request: Request{
			ID:       requestID,
			Remote:   r.RemoteAddr,
			Deadline: time.Now().Add(ApprovalDeadline),
		},
		decision: make(chan Decision, 1),
		cancel:   cancel,
	}
	h.current = p
	h.mu.Unlock()
	go func() {
		<-ctx.Done()
		if h.OnRelease != nil {
			h.OnRelease()
		}
	}()
	stopRequestWatch := context.AfterFunc(r.Context(), cancel)
	defer stopRequestWatch()
	transferred := false
	defer func() {
		if transferred {
			return
		}
		cancel()
		h.mu.Lock()
		if h.current == p {
			h.current = nil
		}
		h.mu.Unlock()
	}()
	r.Body = http.MaxBytesReader(w, r.Body, maxOfferBytes)
	var offer Offer
	if json.NewDecoder(r.Body).Decode(&offer) != nil || len(offer.Name) > 128 || len(offer.SDP) == 0 {
		http.Error(w, "连接请求无效", 400)
		return
	}
	p.request.Name = offer.Name
	if h.OnRequest != nil {
		h.OnRequest(p.request)
	}
	timer := time.NewTimer(ApprovalDeadline)
	defer timer.Stop()
	var decision Decision
	select {
	case decision = <-p.decision:
	case <-ctx.Done():
		return
	case <-r.Context().Done():
		return
	case <-timer.C:
		http.Error(w, "等待批准超时", 408)
		return
	}
	if !decision.Accept {
		http.Error(w, "连接被拒绝", 403)
		return
	}
	if h.OnOffer == nil {
		http.Error(w, "媒体服务不可用", 503)
		return
	}
	answer, err := h.OnOffer(ctx, offer, decision.Control, decision.Files)
	if err != nil {
		http.Error(w, "无法建立媒体会话："+err.Error(), 503)
		return
	}
	if ctx.Err() != nil || r.Context().Err() != nil {
		return
	}
	if err = json.NewEncoder(w).Encode(answer); err != nil {
		return
	}
	stopRequestWatch()
	transferred = true
	go func() {
		<-ctx.Done()
		h.mu.Lock()
		if h.current == p {
			h.current = nil
		}
		h.mu.Unlock()
	}()
}

func (h *Host) serveWS(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	status, msg := h.authenticateLocked(r)
	h.mu.Unlock()
	if status != 0 {
		http.Error(w, msg, status)
		return
	}
	if r.URL.Query().Get("probe") == "1" {
		srv := websocket.Server{
			Handshake: func(cfg *websocket.Config, req *http.Request) error { return nil },
			Handler: func(ws *websocket.Conn) {
				_ = websocket.Message.Send(ws, []byte("GDKPONG"))
			},
		}
		srv.ServeHTTP(w, r)
		return
	}
	reqID := r.URL.Query().Get("request_id")
	if reqID == "" || h.OnWSConnect == nil {
		http.Error(w, "无效的 WebSocket 会话参数", 400)
		return
	}
	srv := websocket.Server{
		Handshake: func(cfg *websocket.Config, req *http.Request) error { return nil },
		Handler: func(ws *websocket.Conn) {
			done := h.OnWSConnect(reqID, ws)
			if done != nil {
				<-done
			}
		},
	}
	srv.ServeHTTP(w, r)
}

// ServeUDPProbe answers stateless GoDeck UDP ping bursts for link latency & packet loss probing.
func ServeUDPProbe(ctx context.Context, pc net.PacketConn) {
	if pc == nil {
		return
	}
	go func() {
		<-ctx.Done()
		_ = pc.Close()
	}()
	buf := make([]byte, 128)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		if n >= 8 && n <= 64 && string(buf[:8]) == "GDKPROBE" {
			_, _ = pc.WriteTo(buf[:n], addr)
		}
	}
}

// ProbeCapabilities queries supported transport protocols and UDP probe availability on a direct host.
func ProbeCapabilities(ctx context.Context, address, pin, secret string) (Capabilities, error) {
	if len(pin) != 64 {
		return Capabilities{}, errors.New("证书指纹无效")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://"+address+"/probe", nil)
	if err != nil {
		return Capabilities{}, err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := PinnedClient(pin).Do(req)
	if err != nil {
		return Capabilities{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Capabilities{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var caps Capabilities
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&caps); err != nil {
		return Capabilities{}, err
	}
	return caps, nil
}

// ProbeWS upgrades a WebSocket connection against /ws?probe=1 to measure real WSS round-trip latency.
func ProbeWS(ctx context.Context, address, pin, secret string) (bool, time.Duration) {
	ws, err := dialPinnedWS(ctx, address, pin, secret, "/ws?probe=1")
	if err != nil {
		return false, 0
	}
	defer ws.Close()
	t0 := time.Now()
	_ = ws.SetDeadline(time.Now().Add(time.Second))
	var reply []byte
	if err := websocket.Message.Receive(ws, &reply); err != nil {
		return false, 0
	}
	return true, time.Since(t0)
}

// DialWebSocket opens a pinned-TLS WebSocket tunnel for direct session media/control transport.
func DialWebSocket(ctx context.Context, address, pin, secret, requestID string) (*websocket.Conn, error) {
	return dialPinnedWS(ctx, address, pin, secret, "/ws?request_id="+requestID)
}

func dialPinnedWS(ctx context.Context, address, pin, secret, path string) (*websocket.Conn, error) {
	if len(pin) != 64 {
		return nil, errors.New("证书指纹无效")
	}
	cfg, err := websocket.NewConfig("wss://"+address+path, "https://"+address)
	if err != nil {
		return nil, err
	}
	cfg.Header.Set("Authorization", "Bearer "+secret)
	cfg.TlsConfig = pinnedTLSConfig(pin)
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 8 * time.Second},
		Config:    cfg.TlsConfig,
	}
	tlsConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	ws, err := websocket.NewClient(cfg, tlsConn)
	if err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	return ws, nil
}

func Connect(ctx context.Context, address, pin, secret string, offer Offer) (Answer, error) {
	if len(pin) != 64 {
		return Answer{}, errors.New("请先确认设备证书指纹")
	}
	data, err := json.Marshal(offer)
	if err != nil {
		return Answer{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://"+address+"/offer", strings.NewReader(string(data)))
	if err != nil {
		return Answer{}, err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := PinnedClient(pin).Do(req)
	if err != nil {
		return Answer{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return Answer{}, fmt.Errorf("连接失败：%s", strings.TrimSpace(string(b)))
	}
	var answer Answer
	err = json.NewDecoder(io.LimitReader(resp.Body, maxOfferBytes)).Decode(&answer)
	return answer, err
}
