package desktop

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"godeck/native/rtc"
)

func (u *UI) layoutSessionView(gtx layout.Context) layout.Dimensions {
	c := u.m.controller

	// Event handling: Back to Home
	if u.btnBackFromSession.Clicked(gtx) {
		u.activeTab = TabHome
	}

	// Event handling: Toggle Stats Overlay
	if u.btnToggleStats.Clicked(gtx) || u.btnCloseStats.Clicked(gtx) {
		u.m.mu.Lock()
		u.m.showStats = !u.m.showStats
		newStats := u.m.showStats
		u.m.mu.Unlock()
		if u.cfg != nil {
			u.cfg.ShowStats = newStats
			u.saveConfig()
		}
	}

	// Event handling: Operating toggle
	if u.btnToggleOperating.Clicked(gtx) {
		if u.operating {
			u.stopInput()
		} else if c.RemoteControlAllowed() {
			u.startInput()
			gtx.Execute(key.FocusCmd{Tag: &u.surface})
		}
	}

	// Event handling: Remote Esc
	if u.btnRemoteEsc.Clicked(gtx) && u.operating && u.pump != nil {
		_ = u.pump.Send(rtc.Input{Type: "key", Code: 1, Down: true})
		_ = u.pump.Send(rtc.Input{Type: "key", Code: 1, Down: false})
	}

	// Event handling: Disconnect
	if u.btnEndSession.Clicked(gtx) {
		u.stopInput()
		end := c.OutgoingEnder()
		go end()

		u.m.mu.Lock()
		u.m.sessionActive = false
		u.m.frame = nil
		u.m.showStats = false
		u.m.status = "已断开连接"
		u.m.mu.Unlock()

		u.activeTab = TabHome
	}

	u.m.mu.Lock()
	frame := u.m.frame
	frameAt := u.m.frameAt
	res := u.m.resolution
	enc := u.m.encoder
	dec := u.m.decoder
	stats := u.m.stats
	showStats := u.m.showStats
	statusText := u.m.status
	sessionActive := u.m.sessionActive
	u.m.mu.Unlock()

	if u.operating && (!sessionActive || !c.RemoteControlAllowed()) {
		u.stopInput()
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		// Top Toolbar
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return Card(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(GhostButton(u.theme, &u.btnBackFromSession, "← 返回主页")),
					layout.Rigid(HGap(12)),

					// Media pipeline specs
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						info := "画面就绪"
						if res != "" {
							info = res
						}
						if enc != "" || dec != "" {
							info += fmt.Sprintf(" (编: %s | 解: %s)", enc, dec)
						}
						return Badge(u.theme, info, ColSurfaceLight, ColPrimary)(gtx)
					}),

					layout.Rigid(HGap(8)),

					// Transport mode badge
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						modeText := stats.TransportMode
						if modeText == "" {
							modeText = c.Transport().String()
						}
						badgeCol := ColPrimary
						if strings.Contains(modeText, "WebSocket") {
							badgeCol = ColWarning
						}
						return Badge(u.theme, "通道: "+modeText, ColSurfaceLight, badgeCol)(gtx)
					}),

					layout.Rigid(HGap(8)),

					// Stats Toggle Button
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if showStats {
							return PrimaryButton(u.theme, &u.btnToggleStats, "监控中 (开启)")(gtx)
						}
						return GhostButton(u.theme, &u.btnToggleStats, "实时统计 (F2)")(gtx)
					}),

					layout.Flexed(1, layout.Spacer{}.Layout),

					// Remote Control Controls
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if !c.RemoteControlAllowed() {
							return Badge(u.theme, "只读桌面 (未授权操作)", ColSurfaceLight, ColTextMuted)(gtx)
						}

						if u.operating {
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(PrimaryButton(u.theme, &u.btnToggleOperating, "正在控制 (点击或Esc释放)")),
								layout.Rigid(HGap(8)),
								layout.Rigid(GhostButton(u.theme, &u.btnRemoteEsc, "发送远端 Esc")),
							)
						}
						return SecondaryButton(u.theme, &u.btnToggleOperating, "启用桌面控制 (点击画面)")(gtx)
					}),

					layout.Rigid(HGap(16)),
					layout.Rigid(DangerButton(u.theme, &u.btnEndSession, "断开连接")),
				)
			})
		}),

		layout.Rigid(VGap(8)),

		// Center Desktop Canvas with optional floating statistics overlay
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			u.screenSize = gtx.Constraints.Max
			u.handleInputEvents(gtx)

			defer clip.Rect{Max: u.screenSize}.Push(gtx.Ops).Pop()
			event.Op(gtx.Ops, &u.surface)

			return layout.Stack{}.Layout(gtx,
				// Layer 1: Desktop Frame or Waiting state
				layout.Expanded(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = gtx.Constraints.Max
					if frame == nil {
						return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return Card(gtx, func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(Subheading(u.theme, "等待对端画面传输...")),
									layout.Rigid(VGap(8)),
									layout.Rigid(Muted(u.theme, "当前状态："+statusText)),
								)
							})
						})
					}

					// Render desktop frame
					imgDim := (widget.Image{
						Src:      paint.NewImageOp(frame),
						Fit:      widget.Contain,
						Position: layout.Center,
					}).Layout(gtx)

					// Check for stale frame (>2.5 seconds without update)
					stale := time.Since(frameAt) > 2500*time.Millisecond
					if stale {
						return layout.Stack{Alignment: layout.S}.Layout(gtx,
							layout.Expanded(func(gtx layout.Context) layout.Dimensions {
								return layout.Dimensions{Size: imgDim.Size}
							}),
							layout.Stacked(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Bottom: Dp(20)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return Badge(u.theme, "画面暂停 · 正在等待新关键帧与画面更新...", ColWarning, ColPrimaryText)(gtx)
								})
							}),
						)
					}
					return imgDim
				}),

				// Layer 2: Floating Real-time Telemetry & Statistics Card
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					if !showStats {
						return layout.Dimensions{}
					}
					gtx.Constraints.Min = gtx.Constraints.Max
					return layout.NE.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{
							Top:   Dp(12),
							Right: Dp(16),
						}.Layout(gtx, u.layoutStatsOverlay)
					})
				}),
			)
		}),
	)
}

func (u *UI) layoutStatsOverlay(gtx layout.Context) layout.Dimensions {
	u.m.mu.Lock()
	st := u.m.stats
	res := u.m.resolution
	enc := u.m.encoder
	dec := u.m.decoder
	renderFPS := u.m.renderFPS
	u.m.mu.Unlock()

	// Fill fallbacks if not yet measured
	if st.Resolution == "" {
		st.Resolution = res
	}
	if st.EncoderName == "" {
		st.EncoderName = enc
	}
	if st.DecoderName == "" {
		st.DecoderName = dec
	}
	if st.RenderFPS == 0 {
		st.RenderFPS = renderFPS
	}

	maxW := gtx.Dp(Dp(380))
	if gtx.Constraints.Max.X > maxW {
		gtx.Constraints.Max.X = maxW
	}
	gtx.Constraints.Min.X = gtx.Dp(Dp(320))

	return CardWithBorder(gtx, ColBorder, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			// Title Header
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(Label(u.theme, "实时连接与媒体监控", 14, ColPrimary)),
					layout.Flexed(1, layout.Spacer{}.Layout),
					layout.Rigid(Badge(u.theme, "F2 切换", ColSurfaceLight, ColTextMuted)),
					layout.Rigid(HGap(6)),
					layout.Rigid(GhostButton(u.theme, &u.btnCloseStats, "关闭")),
				)
			}),
			layout.Rigid(VGap(6)),
			layout.Rigid(Divider()),
			layout.Rigid(VGap(6)),

			// Section 1: 网络传输速率与质量 (Network & Internet)
			layout.Rigid(Label(u.theme, "实时互联网与传输质量", 13, ColTextMain)),
			layout.Rigid(VGap(3)),
			layout.Rigid(statItem(u.theme, "当前下载速率", rtc.FormatBitrate(st.DownBitrateBps), ColTextMain)),
			layout.Rigid(statItem(u.theme, "当前上传速率", rtc.FormatBitrate(st.UpBitrateBps), ColTextMain)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				rttMs := st.RTT.Milliseconds()
				rttText := fmt.Sprintf("%d ms", rttMs)
				if rttMs == 0 && st.RTT > 0 {
					rttText = fmt.Sprintf("%.1f ms", float64(st.RTT.Microseconds())/1000.0)
				} else if rttMs == 0 {
					rttText = "< 1 ms"
				}
				badgeCol := ColSuccess
				if rttMs > 100 {
					badgeCol = ColDanger
				} else if rttMs > 50 {
					badgeCol = ColWarning
				}
				return statItemWithBadge(u.theme, "网络往返延迟 (RTT)", rttText, ColSurfaceLight, badgeCol)(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				jitterText := fmt.Sprintf("%.1f ms", float64(st.Jitter.Microseconds())/1000.0)
				return statItem(u.theme, "网络抖动 (Jitter)", jitterText, ColTextSecondary)(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lossText := fmt.Sprintf("%.2f%% (%d 丢包)", st.PacketLossRate, st.PacketsLost)
				lossCol := ColSuccess
				if st.PacketLossRate > 1.0 {
					lossCol = ColDanger
				} else if st.PacketLossRate > 0.1 {
					lossCol = ColWarning
				}
				return statItem(u.theme, "丢包率 / 丢包数", lossText, lossCol)(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				discardText := fmt.Sprintf("%.2f%% (%d 错过/丢弃)", st.DiscardRate, st.PacketsDiscarded)
				return statItem(u.theme, "错过/丢弃帧率", discardText, ColTextSecondary)(gtx)
			}),
			layout.Rigid(statItem(u.theme, "累计传输流量", fmt.Sprintf("下行 %s · 上行 %s", rtc.FormatBytes(st.TotalBytesRecv), rtc.FormatBytes(st.TotalBytesSent)), ColTextMuted)),

			layout.Rigid(VGap(6)),
			layout.Rigid(Divider()),
			layout.Rigid(VGap(6)),

			// Section 2: 链路拓扑与协议 (Topology)
			layout.Rigid(Label(u.theme, "连接拓扑与传输协议", 13, ColTextMain)),
			layout.Rigid(VGap(3)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				connType := st.ConnectionType
				if connType == "" {
					connType = "P2P 直连"
				}
				badgeFg := ColPrimary
				if strings.Contains(connType, "中继") || strings.Contains(connType, "Relay") {
					badgeFg = ColWarning
				}
				return statItemWithBadge(u.theme, "连接方式", connType, ColSurfaceLight, badgeFg)(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				modeText := st.TransportMode
				if modeText == "" {
					modeText = u.m.controller.Transport().String()
				}
				return statItemWithBadge(u.theme, "传输模式", modeText, ColSurfaceLight, ColPrimary)(gtx)
			}),
			layout.Rigid(statItem(u.theme, "传输协议", st.Protocol, ColTextMain)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				reason := st.TransportReason
				if reason == "" {
					reason = "默认直连策略"
				}
				return statItem(u.theme, "决策依据", reason, ColTextSecondary)(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				pair := st.CandidatePair
				if pair == "" {
					pair = "正在协商候选对..."
				}
				return statItem(u.theme, "活跃候选地址对", pair, ColTextSecondary)(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				iceState := st.ICEState
				if iceState == "" {
					iceState = "connected"
				}
				return statItemWithBadge(u.theme, "ICE 链路状态", iceState, ColSurfaceLight, ColSuccess)(gtx)
			}),

			layout.Rigid(VGap(6)),
			layout.Rigid(Divider()),
			layout.Rigid(VGap(6)),

			// Section 3: 媒体与编解码性能 (Media & Codec)
			layout.Rigid(Label(u.theme, "画面与硬件编解码性能", 13, ColTextMain)),
			layout.Rigid(VGap(3)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				resFps := st.Resolution
				if resFps == "" {
					resFps = "自适应"
				}
				if st.RenderFPS > 0 {
					resFps += fmt.Sprintf(" @ %.1f FPS", st.RenderFPS)
				}
				return statItem(u.theme, "当前分辨率与帧率", resFps, ColPrimary)(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				encName := st.EncoderName
				if encName == "" {
					encName = "协商中"
				}
				return statItem(u.theme, "远端编码器", encName, ColTextMain)(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				encodeRate := "统计采集中..."
				if st.EncodeFPS > 0 {
					encodeRate = fmt.Sprintf("%.1f fps", st.EncodeFPS)
					if st.EncodeDuration > 0 {
						encodeRate += fmt.Sprintf(" (耗时 %.1fms)", float64(st.EncodeDuration.Microseconds())/1000.0)
					}
					if st.EncodeBitrate > 0 {
						encodeRate += fmt.Sprintf(" · %s", rtc.FormatBitrate(st.EncodeBitrate))
					}
				} else if st.DownBitrateBps > 0 {
					encodeRate = fmt.Sprintf("码率 %s", rtc.FormatBitrate(st.DownBitrateBps))
				}
				return statItem(u.theme, "当前编码速率", encodeRate, ColTextSecondary)(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				decName := st.DecoderName
				if decName == "" {
					decName = "初始化中"
				}
				return statItem(u.theme, "本端解码器", decName, ColTextMain)(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				decodeRate := "统计采集中..."
				if st.DecodeFPS > 0 {
					decodeRate = fmt.Sprintf("%.1f fps", st.DecodeFPS)
					if st.DecodeDuration > 0 {
						decodeRate += fmt.Sprintf(" (耗时 %.1fms)", float64(st.DecodeDuration.Microseconds())/1000.0)
					}
				} else if st.RenderFPS > 0 {
					decodeRate = fmt.Sprintf("%.1f fps", st.RenderFPS)
				}
				return statItem(u.theme, "当前解码速率", decodeRate, ColTextSecondary)(gtx)
			}),

			layout.Rigid(VGap(6)),
			layout.Rigid(Divider()),
			layout.Rigid(VGap(4)),

			// Footer: 会话时长
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				durSec := int(st.Duration.Seconds())
				durText := fmt.Sprintf("会话持续时长：%02d:%02d", durSec/60, durSec%60)
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(Muted(u.theme, durText)),
					layout.Flexed(1, layout.Spacer{}.Layout),
					layout.Rigid(Muted(u.theme, "按 F2 可随时切换")),
				)
			}),
		)
	})
}

func statItem(th *material.Theme, title, value string, valColor color.NRGBA) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: Dp(1), Bottom: Dp(1)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(Label(th, title, 12, ColTextSecondary)),
				layout.Flexed(1, layout.Spacer{}.Layout),
				layout.Rigid(Label(th, value, 12, valColor)),
			)
		})
	}
}

func statItemWithBadge(th *material.Theme, title, badgeText string, bg, fg color.NRGBA) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: Dp(1), Bottom: Dp(1)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(Label(th, title, 12, ColTextSecondary)),
				layout.Flexed(1, layout.Spacer{}.Layout),
				layout.Rigid(Badge(th, badgeText, bg, fg)),
			)
		})
	}
}
