package desktop

import (
	"context"
	"image"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"godeck/internal/config"
	"godeck/native/client"
	"godeck/native/hubclient"
	"godeck/native/media"
	"godeck/native/rtc"
	"godeck/native/signaling"
	"godeck/native/transfer"
)

type Model struct {
	mu            sync.Mutex
	w             *app.Window
	controller    *client.Controller
	status        string
	incoming      *signaling.Request
	frame         *image.NRGBA
	frameAt       time.Time
	resolution    string
	encoder       string
	decoder       string
	sessionActive bool
	connecting    bool
	connectCancel context.CancelFunc
	pendingTab    int

	// Direct connection probe state
	probedFp  string
	probingFp bool
	probeErr  string

	// Telemetry & Statistics
	showStats     bool
	stats         rtc.SessionStats
	renderPerf    rtc.PerfTracker
	renderFPS     float64

	// Hub state
	hubLogged bool
	hubURL    string
	hubUser   string
	hubErrMsg string
	devices   []hubclient.Device

	// File Transfer state
	fileList      []transfer.File
	transferring  bool
	transferTask  string
	transferBytes int64
	transferTotal int64
	transferProg  string
	transferErr   string

	// File manager: remote directory shown, and whether the files tab was
	// rendered in the last frame (file drops are only accepted there).
	remotePath     string
	filesTabActive bool

	// Clipboard sync state of the outgoing session
	clipState string // "", "pending", "active", "denied"
	clipNote  string
}

type UI struct {
	m       *Model
	theme   *material.Theme
	cfg     *config.Config
	cfgPath string

	// Navigation tabs: 0: Home, 1: Hub, 2: Session, 3: Files, 4: Settings
	activeTab int
	tabHome   widget.Clickable
	tabHub    widget.Clickable
	tabSession widget.Clickable
	tabFiles  widget.Clickable
	tabSettings widget.Clickable

	// Global incoming approval
	approveView    widget.Clickable
	approveControl widget.Clickable
	approveFiles   widget.Bool
	approveClipboard widget.Bool
	rejectIncoming widget.Clickable

	// Home View widgets
	toggleAccepting   widget.Clickable
	toggleShowSecret  widget.Clickable
	btnCopySecret     widget.Clickable
	btnCopyAddr       widget.Clickable
	btnCopyFp         widget.Clickable
	btnToggleFpExpand widget.Clickable
	fpExpanded        bool
	btnEndIncoming    widget.Clickable
	secretVisible     bool
	tooltip           string
	tooltipTime       time.Time

	// Home View: Outgoing Direct
	homeDirectTab widget.Clickable
	homeHubTab    widget.Clickable
	directTabActive bool
	inputTargetAddr widget.Editor
	inputTargetKey  widget.Editor
	btnProbeFp      widget.Clickable
	btnDirectConnect widget.Clickable
	btnCancelConnect widget.Clickable

	// Hub Devices View widgets
	hubServerAddr   widget.Editor
	hubUsername     widget.Editor
	hubPassword     widget.Editor
	hubSetupCode    widget.Editor
	hubSearchInput  widget.Editor
	btnHubLogin     widget.Clickable
	btnHubInit      widget.Clickable
	btnHubLogout    widget.Clickable
	btnHubPair      widget.Clickable
	btnHubRefresh   widget.Clickable
	hubDeviceBtns   []widget.Clickable
	hubRenameBtns   []widget.Clickable
	hubRevokeBtns   []widget.Clickable
	hubRevokeConfirm int // device index pending revoke confirmation
	checkAutoLogin  widget.Bool

	// Session View widgets
	btnBackFromSession widget.Clickable
	btnToggleOperating widget.Clickable
	btnToggleStats     widget.Clickable
	btnCloseStats      widget.Clickable
	btnRemoteEsc       widget.Clickable
	btnToggleClipboard widget.Clickable
	btnEndSession      widget.Clickable
	operating          bool
	surface            struct{}
	screenSize         image.Point
	pump               *client.InputPump
	pressed            map[key.Name]int32
	mouseButtons       pointer.Buttons
	activeTouches      map[pointer.ID]f32.Point
	primaryTouch       pointer.ID
	hasPrimaryTouch    bool
	touchButtonDown    bool
	twoFingerActive    bool
	twoFingerMoved     bool
	twoFingerStart     time.Time
	twoFingerLastY     float32
	twoFingerCenter    f32.Point

	// Files View widgets
	fileSearchInput widget.Editor
	btnFileUpload   widget.Clickable
	btnFileRefresh  widget.Clickable
	btnFileCancel   widget.Clickable
	btnFileRetry    widget.Clickable
	fileDownloadBtns []widget.Clickable
	files            fileUI

	// Settings View widgets
	preset720pBtn          widget.Clickable
	preset1080pBtn         widget.Clickable
	codecAutoBtn           widget.Clickable
	codecHwBtn             widget.Clickable
	codecSwBtn             widget.Clickable
	transportAutoBtn       widget.Clickable
	transportUDPBtn        widget.Clickable
	transportTCPBtn        widget.Clickable
	transportWSBtn         widget.Clickable
	toggleHostFilesDefault widget.Clickable
	inputShareDir          widget.Editor
	btnToggleAutoLogin     widget.Clickable
	btnSaveConfig          widget.Clickable

	// Scroll lists for views
	homeList     widget.List
	hubList      widget.List
	filesList    widget.List
	settingsList widget.List

	// Runtime information
	name      string
	codec     media.Codec
	preset    media.Preset
	transport rtc.TransportMode
}

func NewUI(m *Model, th *material.Theme, cfg *config.Config, cfgPath string) *UI {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	preset := media.ParsePreset(cfg.Preset)
	codec := media.ParseCodec(cfg.Codec)
	transport := rtc.ParseTransport(cfg.Transport)

	u := &UI{
		m:                m,
		theme:            th,
		cfg:              cfg,
		cfgPath:          cfgPath,
		name:             cfg.DeviceName,
		codec:            codec,
		preset:           preset,
		transport:        transport,
		directTabActive:  true,
		pressed:          map[key.Name]int32{},
		hubDeviceBtns:    make([]widget.Clickable, 256),
		hubRenameBtns:    make([]widget.Clickable, 256),
		hubRevokeBtns:    make([]widget.Clickable, 256),
		fileDownloadBtns: make([]widget.Clickable, 256),
		hubRevokeConfirm: -1,
	}

	u.homeList.Axis = layout.Vertical
	u.hubList.Axis = layout.Vertical
	u.filesList.Axis = layout.Vertical
	u.settingsList.Axis = layout.Vertical

	u.inputTargetAddr.SingleLine = true
	u.inputTargetAddr.Submit = true
	u.inputTargetKey.SingleLine = true
	u.inputTargetKey.Submit = true
	u.inputTargetKey.Mask = '•'
	u.hubServerAddr.SingleLine = true
	u.hubServerAddr.Submit = true
	u.hubUsername.SingleLine = true
	u.hubUsername.Submit = true
	u.hubPassword.SingleLine = true
	u.hubPassword.Submit = true
	u.hubPassword.Mask = '•'
	u.hubSetupCode.SingleLine = true
	u.hubSetupCode.Submit = true
	u.hubSearchInput.SingleLine = true
	u.hubSearchInput.Submit = true
	u.fileSearchInput.SingleLine = true
	u.fileSearchInput.Submit = true
	u.inputShareDir.SingleLine = true
	u.inputShareDir.Submit = true

	// Initialize fields from persistent configuration
	u.hubServerAddr.SetText(cfg.HubURL)
	u.hubUsername.SetText(cfg.HubUser)
	u.hubPassword.SetText(cfg.HubPassword)
	u.checkAutoLogin.Value = cfg.AutoLogin
	u.approveFiles.Value = cfg.AllowFilesHost
	m.mu.Lock()
	m.pendingTab = -1
	m.mu.Unlock()
	if cfg.LastTargetAddr != "" {
		u.inputTargetAddr.SetText(cfg.LastTargetAddr)
	}
	if cfg.ShareDir != "" {
		u.inputShareDir.SetText(cfg.ShareDir)
	} else {
		u.inputShareDir.SetText(m.controller.ShareDir())
	}
	m.showStats = cfg.ShowStats

	return u
}

func (u *UI) saveConfig() {
	if u.cfg == nil || u.cfgPath == "" {
		return
	}
	_ = config.Save(u.cfg, u.cfgPath)
}

func (u *UI) triggerAutoLogin() {
	if u.cfg == nil || !u.cfg.AutoLogin {
		return
	}
	url := strings.TrimSpace(u.cfg.HubURL)
	user := strings.TrimSpace(u.cfg.HubUser)
	pass := u.cfg.HubPassword
	if url == "" || user == "" || pass == "" {
		return
	}

	u.m.mu.Lock()
	u.m.hubErrMsg = ""
	u.m.status = "正在自动登录中心..."
	u.m.mu.Unlock()
	if u.m.w != nil {
		u.m.w.Invalidate()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	devices, err := u.m.controller.HubLogin(ctx, url, user, pass, u.name)
	u.m.mu.Lock()
	defer u.m.mu.Unlock()
	if err != nil {
		u.m.hubErrMsg = "自动登录失败：" + err.Error()
		u.m.status = "中心自动登录失败"
	} else {
		u.m.hubLogged = true
		u.m.hubURL = url
		u.m.hubUser = user
		u.m.devices = devices
		u.m.status = "中心已自动登录"

		// Auto pair if device is not paired yet
		if u.cfg.AutoPair && u.m.controller.HubLocalID() == "" {
			go func() {
				pairCtx, pairCancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer pairCancel()
				if pairErr := u.m.controller.HubPair(pairCtx); pairErr == nil {
					if refreshed, _ := u.m.controller.HubDevices(pairCtx); len(refreshed) > 0 {
						u.m.mu.Lock()
						u.m.devices = refreshed
						u.m.status = "中心已自动登录并绑定本机"
						u.m.mu.Unlock()
						if u.m.w != nil {
							u.m.w.Invalidate()
						}
					}
				}
			}()
		}
	}
	if u.m.w != nil {
		u.m.w.Invalidate()
	}
}

func (u *UI) SetTooltip(msg string) {
	u.tooltip = msg
	u.tooltipTime = time.Now()
}

func (m *Model) recordRenderFrame() {
	m.renderPerf.Record(0, 0)
	fps, _, _ := m.renderPerf.Snapshot()
	if fps > 0 {
		m.renderFPS = fps
	}
}
