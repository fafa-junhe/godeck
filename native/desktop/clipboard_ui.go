package desktop

import (
	"fmt"

	"gioui.org/layout"
	"godeck/native/client"
	"godeck/native/clipboard"
)

// handleClipboardEvent updates the outgoing-session clipboard badge. Host-side
// events only surface errors, since the host has no session toolbar.
func (m *Model) handleClipboardEvent(role client.ClipboardRole, e clipboard.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if role == client.ClipboardHost {
		if e.Type == "error" {
			m.status = e.Error
		}
		return
	}
	switch e.Type {
	case "ready":
		m.clipState = "active"
		m.clipNote = ""
	case "closed":
		if m.clipState == "active" {
			m.clipState = ""
		} else {
			// Closed before it ever opened: the host did not approve.
			m.clipState = "denied"
		}
		m.clipNote = ""
	case "sent":
		m.clipNote = fmt.Sprintf("已发送 %s", formatBytes(int64(e.Bytes)))
	case "received":
		m.clipNote = fmt.Sprintf("已接收 %s", formatBytes(int64(e.Bytes)))
	case "error":
		m.clipNote = e.Error
	}
}

func (u *UI) layoutClipboardToggle(gtx layout.Context, state, note string) layout.Dimensions {
	switch state {
	case "denied":
		return Badge(u.theme, "剪贴板：对端未授权", ColSurfaceLight, ColTextMuted)(gtx)
	case "active":
	default:
		return layout.Dimensions{}
	}
	s := u.m.controller.ViewerClipboard()
	if s == nil {
		return layout.Dimensions{}
	}
	label := "剪贴板同步：开"
	btn := PrimaryButton(u.theme, &u.btnToggleClipboard, label)
	if !s.Enabled() {
		btn = GhostButton(u.theme, &u.btnToggleClipboard, "剪贴板同步：已暂停")
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(btn),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if note == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Left: Dp(6)}.Layout(gtx, Muted(u.theme, note))
		}),
	)
}
