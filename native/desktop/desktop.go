package desktop

import (
	"context"
	_ "embed"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/font/opentype"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget/material"

	"godeck/internal/config"
	"godeck/native/client"
	"godeck/native/media"
	"godeck/native/rtc"
	"godeck/native/signaling"
	"godeck/native/transfer"
)

//go:embed fonts/NotoSansCJKsc-Regular.otf
var chineseFont []byte

// Main is the standard entry point for running the desktop GUI application.
func Main() {
	if err := initRuntimeEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "GoDeck 运行时环境警告:", err)
	}
	go func() {
		if err := Run(); err != nil {
			fmt.Fprintln(os.Stderr, "GoDeck 运行时错误:", err)
		}
		os.Exit(0)
	}()
	app.Main()
}

// Run creates the main window and runs the GUI event loop.
func Run() error {
	if err := initRuntimeEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "GoDeck 运行时环境警告:", err)
	}

	var customConfig string
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "-config" || arg == "--config" {
			if i+1 < len(os.Args) {
				customConfig = os.Args[i+1]
				i++
			}
		} else if strings.HasPrefix(arg, "-config=") {
			customConfig = strings.TrimPrefix(arg, "-config=")
		} else if strings.HasPrefix(arg, "--config=") {
			customConfig = strings.TrimPrefix(arg, "--config=")
		}
	}

	cfg, cfgPath, err := config.Load(customConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "GoDeck 加载配置文件警告 (%s): %v\n", cfgPath, err)
		cfg = config.DefaultConfig()
	}

	w := new(app.Window)
	w.Option(
		app.Title("GoDeck"),
		app.Size(Dp(1120), Dp(780)),
		app.MinSize(Dp(680), Dp(520)),
	)

	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	dataDir := filepath.Join(dir, "godeck")

	codec := media.ParseCodec(cfg.Codec)
	preset := media.ParsePreset(cfg.Preset)

	controller, err := client.New(dataDir, codec)
	if err != nil {
		return err
	}
	defer controller.Close()

	controller.SetPreset(preset)
	controller.SetTransport(rtc.ParseTransport(cfg.Transport))
	controller.SetAllowFilesHost(cfg.AllowFilesHost)
	if cfg.ShareDir != "" {
		controller.SetShareDir(cfg.ShareDir)
	}

	listenAddr := cfg.ListenAddr
	if listenAddr == "" {
		listenAddr = "0.0.0.0:8766"
	}
	if err = controller.Listen(listenAddr); err != nil {
		fmt.Fprintf(os.Stderr, "GoDeck 启动本地监听警告 (%s): %v\n", listenAddr, err)
	}
	controller.AllowIncoming(cfg.AcceptIncoming)

	m := &Model{
		w:          w,
		controller: controller,
		status:     "就绪",
		pendingTab: -1,
	}

	// Register Controller callbacks
	controller.OnStatus = func(state string) {
		m.mu.Lock()
		if state == "failed" || state == "closed" || state == "disconnected" || strings.Contains(state, "解码失败") || strings.Contains(state, "超时") {
			m.status = "连接中断：" + state
			m.frame = nil
			m.sessionActive = false
		} else {
			m.status = state
		}
		m.mu.Unlock()
		w.Invalidate()
	}

	controller.OnDecoder = func(codec media.Codec, path string) {
		m.mu.Lock()
		m.decoder = codec.String()
		if path != "" {
			m.decoder += " · " + path
		}
		m.mu.Unlock()
		w.Invalidate()
	}

	controller.OnEncoder = func(codec, path string) {
		m.mu.Lock()
		m.encoder = codec
		if path != "" {
			m.encoder += " · " + path
		}
		m.mu.Unlock()
		w.Invalidate()
	}

	controller.OnRequest = func(r signaling.Request) {
		m.mu.Lock()
		m.incoming = &r
		m.mu.Unlock()
		w.Invalidate()
	}

	controller.OnFrame = func(f media.Sample) {
		if f.Width <= 0 || f.Height <= 0 || len(f.Data) < f.Width*f.Height*4 {
			return
		}
		img := image.NewNRGBA(image.Rect(0, 0, f.Width, f.Height))
		copy(img.Pix, f.Data[:f.Width*f.Height*4])
		m.mu.Lock()
		m.frame = img
		m.frameAt = time.Now()
		m.resolution = fmt.Sprintf("%d×%d", f.Width, f.Height)
		m.recordRenderFrame()
		m.status = "画面就绪"
		m.mu.Unlock()
		w.Invalidate()
	}

	controller.OnFileEvent = func(e transfer.Event) {
		var needRefresh bool
		m.mu.Lock()
		switch e.Type {
		case "ready":
			m.transferErr = ""
			needRefresh = true
		case "list":
			m.fileList = e.Files
			if e.Error != "" {
				m.transferErr = e.Error
			}
		case "progress":
			m.transferring = true
			m.transferTask = e.Name
			m.transferBytes = e.Bytes
			m.transferTotal = e.Total
			pct := 0
			if e.Total > 0 {
				pct = int(e.Bytes * 100 / e.Total)
			}
			m.transferProg = fmt.Sprintf("%s (%d%% · %s/%s)", e.Name, pct, formatBytes(e.Bytes), formatBytes(e.Total))
		case "complete":
			m.transferring = false
			m.transferProg = "传输完成：" + e.Name
			needRefresh = true
		case "error":
			m.transferring = false
			m.transferErr = e.Error
			m.transferProg = ""
		}
		m.mu.Unlock()
		if needRefresh {
			if fm := controller.FileManager(); fm != nil {
				go func() {
					_ = fm.List()
				}()
			}
		}
		w.Invalidate()
	}

	// Initialize Typography & Theme
	theme := material.NewTheme()
	faces, err := opentype.ParseCollection(chineseFont)
	if err == nil && len(faces) > 0 {
		theme.Shaper = text.NewShaper(text.WithCollection(faces))
		theme.Face = faces[0].Font.Typeface
	}
	theme.Palette = material.Palette{
		Bg:         ColBg,
		Fg:         ColTextMain,
		ContrastBg: ColPrimary,
		ContrastFg: ColPrimaryText,
	}

	u := NewUI(m, theme, cfg, cfgPath)
	defer u.stopInput()

	// 启动时自动登录中心账号 (若已在配置中开启)
	if cfg.AutoLogin && cfg.HubURL != "" && cfg.HubUser != "" && cfg.HubPassword != "" {
		go func() {
			time.Sleep(150 * time.Millisecond)
			u.triggerAutoLogin()
		}()
	}

	// Periodic Stale Frame & Telemetry Monitor
	tickCtx, tickCancel := context.WithCancel(context.Background())
	defer tickCancel()
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-tickCtx.Done():
				return
			case <-ticker.C:
				m.mu.Lock()
				active := m.sessionActive
				if active && m.frame != nil && time.Since(m.frameAt) > 6*time.Second {
					m.frame = nil
					m.status = "画面暂停，等待新画面"
				}
				m.mu.Unlock()

				if active {
					// Query live session stats outside m.mu to avoid cross-lock ordering with controller
					st := controller.GetSessionStats()
					m.mu.Lock()
					if m.sessionActive {
						st.DecoderName = m.decoder
						st.EncoderName = m.encoder
						if st.Resolution == "" {
							st.Resolution = m.resolution
						}
						st.RenderFPS = m.renderFPS
						m.stats = st
					}
					m.mu.Unlock()
				}
				w.Invalidate()
			}
		}
	}()

	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			paint.Fill(gtx.Ops, ColBg)

			m.mu.Lock()
			if m.pendingTab >= 0 {
				u.activeTab = m.pendingTab
				m.pendingTab = -1
			}
			m.mu.Unlock()

			layout.Stack{Alignment: layout.S}.Layout(gtx,
				layout.Expanded(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = gtx.Constraints.Max
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						// Global Navbar
						layout.Rigid(u.layoutNavbar),

						// Global Incoming Connection Approval Banner (if present)
						layout.Rigid(u.layoutApprovalCard),

						// View Content based on active tab
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							if u.activeTab != TabSession && u.operating {
								u.stopInput()
							}
							switch u.activeTab {
							case TabHome:
								return u.layoutHomeView(gtx)
							case TabHub:
								return u.layoutDevicesView(gtx)
							case TabSession:
								return u.layoutSessionView(gtx)
							case TabFiles:
								return u.layoutFilesView(gtx)
							case TabSettings:
								return u.layoutSettingsView(gtx)
							default:
								return u.layoutHomeView(gtx)
							}
						}),
					)
				}),

				// Floating Tooltip / Toast overlay (does not shift main layout or remote desktop coordinates)
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					if u.tooltip != "" && time.Since(u.tooltipTime) < 2500*time.Millisecond {
						return layout.Inset{Bottom: Dp(16)}.Layout(gtx, Badge(u.theme, u.tooltip, ColPrimary, ColPrimaryText))
					}
					return layout.Dimensions{}
				}),
			)

			e.Frame(gtx.Ops)
		}
	}
}
