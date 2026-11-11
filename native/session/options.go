package session

import (
	"github.com/pion/webrtc/v4"
	"godeck/native/rtc"
)

// HostOptions configures ICE servers, file channels, and WebRTC transport/tunnel options for a Host session.
type HostOptions struct {
	ICEServers      []webrtc.ICEServer
	FileHandler     func(*webrtc.DataChannel)
	Transport       rtc.TransportMode
	TransportReason string
	WSTunnel        *rtc.WSTunnel
}

// ViewerOptions configures ICE servers and WebRTC transport/tunnel options for a Viewer session.
type ViewerOptions struct {
	ICEServers      []webrtc.ICEServer
	Transport       rtc.TransportMode
	TransportReason string
	WSTunnel        *rtc.WSTunnel
}
