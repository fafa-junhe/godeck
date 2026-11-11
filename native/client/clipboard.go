package client

import (
	"github.com/pion/webrtc/v4"
	"godeck/native/clipboard"
	"godeck/native/session"
)

// ClipboardRole distinguishes the incoming (host) and outgoing (viewer) session.
type ClipboardRole int

const (
	ClipboardHost ClipboardRole = iota
	ClipboardViewer
)

func (c *Controller) clipboardEmit(role ClipboardRole) func(clipboard.Event) {
	return func(e clipboard.Event) {
		if c.OnClipboardEvent != nil {
			c.OnClipboardEvent(role, e)
		}
	}
}

// hostClipboardHandler returns nil unless the local user approved clipboard
// sync for this incoming session; a nil handler makes the peer refuse the channel.
func (c *Controller) hostClipboardHandler(approved bool) func(*webrtc.DataChannel) {
	if !approved {
		return nil
	}
	return func(dc *webrtc.DataChannel) {
		s := clipboard.New(dc, clipboard.System(), c.clipboardEmit(ClipboardHost))
		c.clipMu.Lock()
		old := c.clipHost
		c.clipHost = s
		c.clipMu.Unlock()
		if old != nil {
			_ = old.Close()
		}
	}
}

func (c *Controller) closeHostClipboard() {
	c.clipMu.Lock()
	s := c.clipHost
	c.clipHost = nil
	c.clipMu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

// openViewerClipboard asks the host for a clipboard channel. The host closes it
// immediately when clipboard sync was not approved, which the UI reports.
func (c *Controller) openViewerClipboard(v *session.Viewer) {
	dc, err := v.OpenClipboard()
	if err != nil {
		return
	}
	s := clipboard.New(dc, clipboard.System(), c.clipboardEmit(ClipboardViewer))
	c.clipMu.Lock()
	old := c.clipViewer
	c.clipViewer = s
	c.clipMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

func (c *Controller) closeViewerClipboard() {
	c.clipMu.Lock()
	s := c.clipViewer
	c.clipViewer = nil
	c.clipMu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

// ViewerClipboard returns the clipboard sync of the outgoing session, if any.
func (c *Controller) ViewerClipboard() *clipboard.Sync {
	c.clipMu.Lock()
	defer c.clipMu.Unlock()
	return c.clipViewer
}

// HostClipboard returns the clipboard sync of the incoming session, if any.
func (c *Controller) HostClipboard() *clipboard.Sync {
	c.clipMu.Lock()
	defer c.clipMu.Unlock()
	return c.clipHost
}
