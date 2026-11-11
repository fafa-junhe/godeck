package rtc

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/pion/transport/v3"
	"golang.org/x/net/websocket"
)

// WSTunnel adapts a binary WebSocket stream into a virtual transport.Net packet link
// so WebRTC performs full ICE + DTLS + SRTP + SCTP natively inside HTTPS/WSS.
type WSTunnel struct {
	mu           sync.Mutex
	localIP      net.IP
	peerIP       net.IP
	localAddr    *net.UDPAddr
	remoteAddr   *net.UDPAddr
	iface        *transport.Interface
	inbox        chan []byte
	outbox       chan []byte
	ws           *websocket.Conn
	closed       bool
	done         chan struct{}
	once         sync.Once
	readDeadline time.Time
	deadlineCh   chan struct{}
}

// NewWSTunnel allocates a virtual WebSocket transport endpoint for either Offerer or Answerer.
func NewWSTunnel(offerer bool) *WSTunnel {
	localIP := net.IPv4(10, 255, 0, 2)
	peerIP := net.IPv4(10, 255, 0, 1)
	if offerer {
		localIP = net.IPv4(10, 255, 0, 1)
		peerIP = net.IPv4(10, 255, 0, 2)
	}
	lAddr := &net.UDPAddr{IP: localIP, Port: 50000}
	rAddr := &net.UDPAddr{IP: peerIP, Port: 50000}

	ifc := transport.NewInterface(net.Interface{
		Index: 1,
		MTU:   1400,
		Name:  "ws0",
		Flags: net.FlagUp,
	})
	ifc.AddAddress(&net.IPNet{
		IP:   localIP,
		Mask: net.CIDRMask(24, 32),
	})

	return &WSTunnel{
		localIP:    localIP,
		peerIP:     peerIP,
		localAddr:  lAddr,
		remoteAddr: rAddr,
		iface:      ifc,
		inbox:      make(chan []byte, 512),
		outbox:     make(chan []byte, 512),
		done:       make(chan struct{}),
		deadlineCh: make(chan struct{}, 1),
	}
}

// Net returns the transport.Net adapter for webrtc.SettingEngine.SetNet.
func (t *WSTunnel) Net() transport.Net {
	return &wsNet{tun: t}
}

// Attach binds a live WebSocket connection to start bidirectional packet forwarding.
func (t *WSTunnel) Attach(ws *websocket.Conn) error {
	if ws == nil {
		return errors.New("WebSocket 连接为空")
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		_ = ws.Close()
		return net.ErrClosed
	}
	t.ws = ws
	t.mu.Unlock()

	ws.MaxPayloadBytes = 64 << 10
	_ = ws.SetDeadline(time.Time{})

	// Dedicated writer loop ensures single writer concurrency on *websocket.Conn.
	go func() {
		for {
			select {
			case <-t.done:
				return
			case pkt := <-t.outbox:
				if err := websocket.Message.Send(ws, pkt); err != nil {
					_ = t.Close()
					return
				}
			}
		}
	}()

	// Dedicated reader loop receives discrete binary frames into non-blocking inbox.
	go func() {
		for {
			var pkt []byte
			if err := websocket.Message.Receive(ws, &pkt); err != nil {
				_ = t.Close()
				return
			}
			if len(pkt) == 0 {
				continue
			}
			select {
			case <-t.done:
				return
			case t.inbox <- pkt:
			default:
				select {
				case <-t.inbox:
				default:
				}
				select {
				case t.inbox <- pkt:
				default:
				}
			}
		}
	}()

	return nil
}

// Close closes the WebSocket tunnel and wakes any blocked readers.
func (t *WSTunnel) Close() error {
	t.once.Do(func() {
		t.mu.Lock()
		t.closed = true
		ws := t.ws
		t.ws = nil
		t.mu.Unlock()
		close(t.done)
		if ws != nil {
			_ = ws.Close()
		}
	})
	return nil
}

type wsNet struct {
	tun *WSTunnel
}

var _ transport.Net = (*wsNet)(nil)

func (n *wsNet) ListenPacket(network, address string) (net.PacketConn, error) {
	return n.ListenUDP(network, n.tun.localAddr)
}

func (n *wsNet) ListenUDP(network string, locAddr *net.UDPAddr) (transport.UDPConn, error) {
	if locAddr != nil && locAddr.IP != nil && locAddr.IP.To4() == nil && !locAddr.IP.IsUnspecified() {
		return nil, transport.ErrNotSupported
	}
	return &wsUDPConn{tun: n.tun}, nil
}

func (n *wsNet) ListenTCP(network string, laddr *net.TCPAddr) (transport.TCPListener, error) {
	return nil, transport.ErrNotSupported
}

func (n *wsNet) Dial(network, address string) (net.Conn, error) {
	return nil, transport.ErrNotSupported
}

func (n *wsNet) DialUDP(network string, laddr, raddr *net.UDPAddr) (transport.UDPConn, error) {
	return &wsUDPConn{tun: n.tun}, nil
}

func (n *wsNet) DialTCP(network string, laddr, raddr *net.TCPAddr) (transport.TCPConn, error) {
	return nil, transport.ErrNotSupported
}

func (n *wsNet) ResolveIPAddr(network, address string) (*net.IPAddr, error) {
	return net.ResolveIPAddr(network, address)
}

func (n *wsNet) ResolveUDPAddr(network, address string) (*net.UDPAddr, error) {
	return net.ResolveUDPAddr(network, address)
}

func (n *wsNet) ResolveTCPAddr(network, address string) (*net.TCPAddr, error) {
	return net.ResolveTCPAddr(network, address)
}

func (n *wsNet) Interfaces() ([]*transport.Interface, error) {
	return []*transport.Interface{n.tun.iface}, nil
}

func (n *wsNet) InterfaceByIndex(index int) (*transport.Interface, error) {
	if index == 1 {
		return n.tun.iface, nil
	}
	return nil, transport.ErrInterfaceNotFound
}

func (n *wsNet) InterfaceByName(name string) (*transport.Interface, error) {
	if name == "ws0" {
		return n.tun.iface, nil
	}
	return nil, transport.ErrInterfaceNotFound
}

func (n *wsNet) CreateDialer(dialer *net.Dialer) transport.Dialer {
	return dialer
}

type wsUDPConn struct {
	tun *WSTunnel
}

var _ transport.UDPConn = (*wsUDPConn)(nil)

func (c *wsUDPConn) Close() error {
	// WSTunnel owns the underlying WebSocket lifecycle; closing individual sockets is a no-op until Peer.Close().
	return nil
}

func (c *wsUDPConn) LocalAddr() net.Addr  { return c.tun.localAddr }
func (c *wsUDPConn) RemoteAddr() net.Addr { return c.tun.remoteAddr }

func (c *wsUDPConn) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}

func (c *wsUDPConn) SetReadDeadline(t time.Time) error {
	c.tun.mu.Lock()
	c.tun.readDeadline = t
	c.tun.mu.Unlock()
	select {
	case c.tun.deadlineCh <- struct{}{}:
	default:
	}
	return nil
}

func (c *wsUDPConn) SetWriteDeadline(t time.Time) error { return nil }
func (c *wsUDPConn) SetReadBuffer(bytes int) error      { return nil }
func (c *wsUDPConn) SetWriteBuffer(bytes int) error     { return nil }

func (c *wsUDPConn) Read(b []byte) (int, error) {
	n, _, err := c.ReadFrom(b)
	return n, err
}

func (c *wsUDPConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		c.tun.mu.Lock()
		dl := c.tun.readDeadline
		c.tun.mu.Unlock()

		var timer *time.Timer
		var timerC <-chan time.Time
		if !dl.IsZero() {
			rem := time.Until(dl)
			if rem <= 0 {
				return 0, nil, &net.OpError{Op: "read", Net: "udp", Err: timeoutErr{}}
			}
			timer = time.NewTimer(rem)
			timerC = timer.C
		}

		select {
		case <-c.tun.done:
			if timer != nil {
				timer.Stop()
			}
			return 0, nil, net.ErrClosed
		case pkt := <-c.tun.inbox:
			if timer != nil {
				timer.Stop()
			}
			n := copy(p, pkt)
			if n < len(pkt) {
				return n, c.tun.remoteAddr, io.ErrShortBuffer
			}
			return n, c.tun.remoteAddr, nil
		case <-timerC:
			return 0, nil, &net.OpError{Op: "read", Net: "udp", Err: timeoutErr{}}
		case <-c.tun.deadlineCh:
			if timer != nil {
				timer.Stop()
			}
		}
	}
}

func (c *wsUDPConn) ReadFromUDP(b []byte) (int, *net.UDPAddr, error) {
	n, _, err := c.ReadFrom(b)
	return n, c.tun.remoteAddr, err
}

func (c *wsUDPConn) ReadMsgUDP(b, oob []byte) (int, int, int, *net.UDPAddr, error) {
	n, _, err := c.ReadFrom(b)
	return n, 0, 0, c.tun.remoteAddr, err
}

func (c *wsUDPConn) Write(b []byte) (int, error) {
	return c.WriteTo(b, c.tun.remoteAddr)
}

func (c *wsUDPConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	select {
	case <-c.tun.done:
		return 0, net.ErrClosed
	default:
	}
	cp := make([]byte, len(p))
	copy(cp, p)
	select {
	case <-c.tun.done:
		return 0, net.ErrClosed
	case c.tun.outbox <- cp:
		return len(p), nil
	default:
		// Non-blocking drop-oldest preserves real-time freshness inside tunnel.
		select {
		case <-c.tun.outbox:
		default:
		}
		select {
		case c.tun.outbox <- cp:
			return len(p), nil
		default:
			return len(p), nil
		}
	}
}

func (c *wsUDPConn) WriteToUDP(b []byte, addr *net.UDPAddr) (int, error) {
	return c.WriteTo(b, addr)
}

func (c *wsUDPConn) WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (int, int, error) {
	n, err := c.WriteTo(b, addr)
	return n, 0, err
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }
