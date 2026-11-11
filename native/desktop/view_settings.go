package desktop

import (
	"runtime"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"godeck/native/media"
	"godeck/native/rtc"
)

func (u *UI) layoutSettingsView(gtx layout.Context) layout.Dimensions {
	c := u.m.controller

	// Preset selection
	if u.preset720pBtn.Clicked(gtx) {
		u.preset = media.Preset720p30
		c.SetPreset(u.preset)
		if u.cfg != nil {
			u.cfg.Preset = u.preset.ConfigString()
			u.saveConfig()
		}
	}
	if u.preset1080pBtn.Clicked(gtx) {
		u.preset = media.Preset1080p30
		c.SetPreset(u.preset)
		if u.cfg != nil {
			u.cfg.Preset = u.preset.ConfigString()
			u.saveConfig()
		}
	}

	// Codec selection
	if u.codecAutoBtn.Clicked(gtx) {
		u.codec = media.Auto
		c.SetCodec(u.codec)
		if u.cfg != nil {
			u.cfg.Codec = u.codec.ConfigString()
			u.saveConfig()
		}
	}
	if u.codecHwBtn.Clicked(gtx) {
		u.codec = media.HardwareRequired
		c.SetCodec(u.codec)
		if u.cfg != nil {
			u.cfg.Codec = u.codec.ConfigString()
			u.saveConfig()
		}
	}
	if u.codecSwBtn.Clicked(gtx) {
		u.codec = media.Software
		c.SetCodec(u.codec)
		if u.cfg != nil {
			u.cfg.Codec = u.codec.ConfigString()
			u.saveConfig()
		}
	}

	// Transport mode selection
	transportOptions := []struct {
		mode rtc.TransportMode
		btn  *widget.Clickable
		lbl  string
	}{
		{rtc.TransportAuto, &u.transportAutoBtn, "自动优选 (延迟/丢包/效率)"},
		{rtc.TransportUDP, &u.transportUDPBtn, "UDP 打洞"},
		{rtc.TransportTCP, &u.transportTCPBtn, "TCP 打洞"},
		{rtc.TransportWebSocket, &u.transportWSBtn, "WebSocket 隧道"},
	}
	for _, opt := range transportOptions {
		if opt.btn.Clicked(gtx) {
			u.transport = opt.mode
			c.SetTransport(u.transport)
			if u.cfg != nil {
				u.cfg.Transport = u.transport.ConfigString()
				u.saveConfig()
			}
			u.SetTooltip("传输模式已切换为: " + opt.lbl)
		}
	}

	// File sharing toggle
	if u.toggleHostFilesDefault.Clicked(gtx) {
		allow := !c.AllowFilesHost()
		c.SetAllowFilesHost(allow)
		if u.cfg != nil {
			u.cfg.AllowFilesHost = allow
			u.saveConfig()
		}
	}

	// Toggle auto-login
	if u.btnToggleAutoLogin.Clicked(gtx) {
		if u.cfg != nil {
			u.cfg.AutoLogin = !u.cfg.AutoLogin
			u.checkAutoLogin.Value = u.cfg.AutoLogin
			u.saveConfig()
			if u.cfg.AutoLogin {
				u.SetTooltip("已开启启动时自动登录中心")
			} else {
				u.SetTooltip("已关闭启动时自动登录中心")
			}
		}
	}

	// Save all config
	if u.btnSaveConfig.Clicked(gtx) {
		u.saveConfig()
		u.SetTooltip("配置已保存至: " + u.cfgPath)
	}

	return material.List(u.theme, &u.settingsList).Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
		return layout.Inset{
			Top:    Dp(16),
			Bottom: Dp(24),
			Left:   Dp(24),
			Right:  Dp(24),
		}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return Card(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(Heading(u.theme, "系统与工作台偏好设置")),
				layout.Rigid(VGap(6)),
				layout.Rigid(Caption(u.theme, "管理硬件编解码偏好、传出画面质量与文件传输共享策略。")),
				layout.Rigid(VGap(20)),

				// Section 1: Quality Preset
				layout.Rigid(Subheading(u.theme, "传出画质预设")),
				layout.Rigid(VGap(6)),
				layout.Rigid(Caption(u.theme, "选择作为被控端时对外发送画面的最大分辨率与帧率：")),
				layout.Rigid(VGap(8)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if u.preset == media.Preset720p30 {
								return PrimaryButton(u.theme, &u.preset720pBtn, "● 720p 30fps (均衡低带宽)")(gtx)
							}
							return SecondaryButton(u.theme, &u.preset720pBtn, "720p 30fps (均衡低带宽)")(gtx)
						}),
						layout.Rigid(HGap(12)),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if u.preset == media.Preset1080p30 {
								return PrimaryButton(u.theme, &u.preset1080pBtn, "● 1080p 30fps (清晰优先)")(gtx)
							}
							return SecondaryButton(u.theme, &u.preset1080pBtn, "1080p 30fps (清晰优先)")(gtx)
						}),
					)
				}),
				layout.Rigid(VGap(20)),
				layout.Rigid(Divider()),
				layout.Rigid(VGap(16)),

				// Section 2: Codec Engine Preference
				layout.Rigid(Subheading(u.theme, "编解码引擎策略")),
				layout.Rigid(VGap(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if media.IsPreview {
						return Label(u.theme, "当前为无媒体 UI 预览构建 (godeck_preview)：编解码均未启用，远程画面传输不可用。", 12, ColWarning)(gtx)
					}
					return Caption(u.theme, "支持 Intel Haswell VA-API / GStreamer / FFmpeg NV12 与跨平台回退：")(gtx)
				}),
				layout.Rigid(VGap(8)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if u.codec == media.Auto {
								return PrimaryButton(u.theme, &u.codecAutoBtn, "● 自动模式 (优先硬件)")(gtx)
							}
							return SecondaryButton(u.theme, &u.codecAutoBtn, "自动模式 (优先硬件)")(gtx)
						}),
						layout.Rigid(HGap(10)),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if u.codec == media.HardwareRequired {
								return PrimaryButton(u.theme, &u.codecHwBtn, "● 仅硬件编码 (无软件回退)")(gtx)
							}
							return SecondaryButton(u.theme, &u.codecHwBtn, "仅硬件编码 (无软件回退)")(gtx)
						}),
						layout.Rigid(HGap(10)),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if u.codec == media.Software {
								return PrimaryButton(u.theme, &u.codecSwBtn, "● 软件编码 (OpenH264)")(gtx)
							}
							return SecondaryButton(u.theme, &u.codecSwBtn, "软件编码 (OpenH264)")(gtx)
						}),
					)
				}),
				layout.Rigid(VGap(20)),
				layout.Rigid(Divider()),
				layout.Rigid(VGap(16)),

				// Section 2.5: Transport Mode & NAT Hole Punching
				layout.Rigid(Subheading(u.theme, "传输协议与 NAT 穿透模式")),
				layout.Rigid(VGap(6)),
				layout.Rigid(Caption(u.theme, "自动模式会并发探测延迟、丢包率、抖动与通道效率并动态优选；UDP/TCP 受限环境可指定 WebSocket 隧道：")),
				layout.Rigid(VGap(8)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					children := make([]layout.FlexChild, 0, len(transportOptions)*2-1)
					for i, opt := range transportOptions {
						if i > 0 {
							children = append(children, layout.Rigid(HGap(8)))
						}
						opt := opt
						children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if u.transport == opt.mode {
								return PrimaryButton(u.theme, opt.btn, "● "+opt.lbl)(gtx)
							}
							return SecondaryButton(u.theme, opt.btn, opt.lbl)(gtx)
						}))
					}
					return layout.Flex{Axis: layout.Horizontal}.Layout(gtx, children...)
				}),
				layout.Rigid(VGap(20)),
				layout.Rigid(Divider()),
				layout.Rigid(VGap(16)),

				// Section 3: File Transfer Permissions
				layout.Rigid(Subheading(u.theme, "本机文件共享安全")),
				layout.Rigid(VGap(6)),
				layout.Rigid(Caption(u.theme, "作为被控端时，默认是否允许连接者访问本机共享文件：")),
				layout.Rigid(VGap(8)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					toggleText := "开启文件共享服务"
					if c.AllowFilesHost() {
						toggleText = "已开启文件共享服务 (点击关闭)"
						return PrimaryButton(u.theme, &u.toggleHostFilesDefault, "● "+toggleText)(gtx)
					}
					return SecondaryButton(u.theme, &u.toggleHostFilesDefault, toggleText)(gtx)
				}),
				layout.Rigid(VGap(12)),
				layout.Rigid(Caption(u.theme, "共享与下载存储根目录：")),
				layout.Rigid(VGap(4)),
				layout.Rigid(Subheading(u.theme, c.ShareDir())),
				layout.Rigid(VGap(20)),
				layout.Rigid(Divider()),
				layout.Rigid(VGap(16)),

				// Section 4: Configuration File & Auto-Login
				layout.Rigid(Subheading(u.theme, "配置文件与自动登录")),
				layout.Rigid(VGap(6)),
				layout.Rigid(Caption(u.theme, "管理本地配置文件 (config.json) 与启动时自动登录行为：")),
				layout.Rigid(VGap(8)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(Muted(u.theme, "配置文件路径："+u.cfgPath)),
						layout.Rigid(VGap(8)),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									autoOn := u.cfg != nil && u.cfg.AutoLogin
									if autoOn {
										return PrimaryButton(u.theme, &u.btnToggleAutoLogin, "● 启动自动登录：已开启 (点击关闭)")(gtx)
									}
									return SecondaryButton(u.theme, &u.btnToggleAutoLogin, "启动自动登录：未开启 (点击开启)")(gtx)
								}),
								layout.Rigid(HGap(12)),
								layout.Rigid(SecondaryButton(u.theme, &u.btnSaveConfig, "保存当前配置")),
							)
						}),
					)
				}),
				layout.Rigid(VGap(20)),
				layout.Rigid(Divider()),
				layout.Rigid(VGap(16)),

				// Section 5: System Information
				layout.Rigid(Subheading(u.theme, "运行环境与组件版本")),
				layout.Rigid(VGap(8)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					previewNotice := ""
					if media.IsPreview {
						previewNotice = " [无媒体 UI 预览构建]"
					}
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(Muted(u.theme, "平台："+runtime.GOOS+" / "+runtime.GOARCH+previewNotice)),
						layout.Rigid(VGap(4)),
						layout.Rigid(Muted(u.theme, "UI 框架：Gio v0.9.0 ( novulkan / OpenGL / EGL )")),
						layout.Rigid(VGap(4)),
						layout.Rigid(Muted(u.theme, "媒体传输：Pion WebRTC v4.1.6")),
					)
				}),
			)
			})
		})
	})
}
