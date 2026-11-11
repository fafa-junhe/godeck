package desktop

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/widget/material"
	"godeck/native/client"
	"godeck/native/media"
)

// getConnectableAddresses 枚举本机非回环局域网 IPv4 地址与监听端口，避免将 0.0.0.0 显示给对端。
func getConnectableAddresses(bindAddr string) (primary string, candidates []string, isWildcard bool) {
	host, port, err := net.SplitHostPort(bindAddr)
	if err != nil {
		port = "8766"
		host = bindAddr
	}
	isWildcard = host == "" || host == "0.0.0.0" || host == "::" || host == "[::]"

	if !isWildcard && host != "" {
		return bindAddr, nil, false
	}

	// 局域网网卡枚举
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ip4 := ipnet.IP.To4(); ip4 != nil {
					candidates = append(candidates, net.JoinHostPort(ip4.String(), port))
				}
			}
		}
	}

	if len(candidates) > 0 {
		primary = candidates[0]
		if len(candidates) > 1 {
			return primary, candidates[1:], true
		}
		return primary, nil, true
	}

	// 回退至原始地址
	if bindAddr != "" {
		return bindAddr, nil, true
	}
	return "0.0.0.0:" + port, nil, true
}

func (u *UI) layoutHomeView(gtx layout.Context) layout.Dimensions {
	c := u.m.controller

	// 事件处理
	if u.toggleAccepting.Clicked(gtx) {
		if media.IsPreview {
			u.SetTooltip("无媒体预览版无法开启桌面来访服务")
		} else {
			accepting := !c.Accepting()
			c.AllowIncoming(accepting)
			if accepting {
				u.SetTooltip("已开启来访服务，等待对方连接")
			} else {
				u.SetTooltip("已暂停来访服务")
			}
		}
	}

	if u.toggleShowSecret.Clicked(gtx) {
		u.secretVisible = !u.secretVisible
	}

	if u.btnCopySecret.Clicked(gtx) {
		key, _ := c.Secret()
		gtx.Execute(clipboard.WriteCmd{Type: "text/plain", Data: io.NopCloser(strings.NewReader(key))})
		u.SetTooltip("临时会话密钥已复制到剪贴板")
	}

	if u.btnCopyFp.Clicked(gtx) {
		fp := c.Fingerprint()
		gtx.Execute(clipboard.WriteCmd{Type: "text/plain", Data: io.NopCloser(strings.NewReader(fp))})
		u.SetTooltip("证书指纹已复制")
	}

	if u.btnToggleFpExpand.Clicked(gtx) {
		u.fpExpanded = !u.fpExpanded
	}

	if u.btnEndIncoming.Clicked(gtx) {
		c.EndIncoming()
		u.SetTooltip("已断开当前来访连接")
	}

	if u.homeDirectTab.Clicked(gtx) {
		u.directTabActive = true
	}
	if u.homeHubTab.Clicked(gtx) {
		u.directTabActive = false
	}

	submitTarget := isEditorSubmitted(&u.inputTargetAddr, gtx) || isEditorSubmitted(&u.inputTargetKey, gtx)

	u.m.mu.Lock()
	curProbedFp := u.m.probedFp
	u.m.mu.Unlock()

	// 局域网直连：获取指纹并核对
	if u.btnProbeFp.Clicked(gtx) || (submitTarget && curProbedFp == "") {
		if media.IsPreview {
			u.m.mu.Lock()
			u.m.probeErr = "当前为无媒体 UI 预览版，不支持建立桌面远控连接"
			u.m.mu.Unlock()
			return layout.Dimensions{}
		}
		addr := strings.TrimSpace(u.inputTargetAddr.Text())
		if addr != "" {
			u.m.mu.Lock()
			u.m.connecting = true
			u.m.probingFp = true
			u.m.probeErr = ""
			u.m.probedFp = ""
			u.m.status = "正在获取设备指纹..."
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			u.m.connectCancel = cancel
			u.m.mu.Unlock()

			go func() {
				pin, err := c.Probe(ctx, addr)
				u.m.mu.Lock()
				defer u.m.mu.Unlock()
				u.m.probingFp = false
				u.m.connecting = false
				if err != nil {
					u.m.probeErr = "无法连接目标设备：" + err.Error()
					u.m.status = "指纹核对失败"
				} else {
					u.m.probedFp = pin
					u.m.status = "请核对设备指纹后确认连接"
				}
				if u.m.w != nil {
					u.m.w.Invalidate()
				}
			}()
		}
	}

	// 局域网直连：确认指纹并连接
	if u.btnDirectConnect.Clicked(gtx) || (submitTarget && curProbedFp != "") {
		if media.IsPreview {
			u.SetTooltip("无媒体预览版不支持主动远控连接")
			return layout.Dimensions{}
		}
		addr := strings.TrimSpace(u.inputTargetAddr.Text())
		key := strings.TrimSpace(u.inputTargetKey.Text())
		pin := curProbedFp

		if u.cfg != nil {
			u.cfg.LastTargetAddr = addr
			u.saveConfig()
		}

		u.m.mu.Lock()
		u.m.connecting = true
		u.m.status = "等待对方授权屏幕与媒体..."
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		u.m.connectCancel = cancel
		u.m.mu.Unlock()

		go func() {
			err := c.Connect(ctx, addr, pin, key, u.name)
			u.m.mu.Lock()
			defer u.m.mu.Unlock()
			u.m.connecting = false
			if err != nil {
				u.m.probeErr = "连接失败：" + err.Error()
				u.m.status = "连接失败"
			} else {
				u.m.sessionActive = true
				u.m.pendingTab = TabSession
				if u.m.frame == nil {
					u.m.status = "连接建立，等待对端画面..."
				}
			}
			if u.m.w != nil {
				u.m.w.Invalidate()
			}
		}()
	}

	// 取消连接
	if u.btnCancelConnect.Clicked(gtx) {
		u.m.mu.Lock()
		if u.m.connectCancel != nil {
			u.m.connectCancel()
		}
		u.m.connecting = false
		u.m.probingFp = false
		u.m.probedFp = ""
		u.m.status = "已取消连接"
		u.m.mu.Unlock()
		end := c.OutgoingEnder()
		go end()
	}

	secret, expiry := c.Secret()
	shownSecret := "••••••••••••"
	if u.secretVisible {
		shownSecret = secret
	}
	toggleSecretText := "显示"
	if u.secretVisible {
		toggleSecretText = "隐藏"
	}

	fullFp := c.Fingerprint()
	displayFp := fullFp
	if !u.fpExpanded && len(fullFp) > 24 {
		displayFp = fullFp[:12] + "..." + fullFp[len(fullFp)-8:]
	}

	primaryAddr, candidateAddrs, isWildcard := getConnectableAddresses(c.Address())
	if u.btnCopyAddr.Clicked(gtx) {
		gtx.Execute(clipboard.WriteCmd{Type: "text/plain", Data: io.NopCloser(strings.NewReader(primaryAddr))})
		u.SetTooltip("本机连接地址已复制到剪贴板")
	}

	u.m.mu.Lock()
	connecting := u.m.connecting
	statusMsg := u.m.status
	probingFp := u.m.probingFp
	probedFp := u.m.probedFp
	probeErr := u.m.probeErr
	u.m.mu.Unlock()

	// 首页全局可滚动列表（避免在小窗口或高DPI下内容被裁切）
	return material.List(u.theme, &u.homeList).Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
		maxContentW := gtx.Dp(Dp(1120))
		windowW := gtx.Constraints.Max.X
		horizontalPadding := Dp(24)
		if windowW < gtx.Dp(Dp(780)) {
			horizontalPadding = Dp(16)
		}

		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			if gtx.Constraints.Max.X > maxContentW {
				gtx.Constraints.Max.X = maxContentW
			}

			return layout.Inset{
				Top:    Dp(24),
				Bottom: Dp(32),
				Left:   horizontalPadding,
				Right:  horizontalPadding,
			}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					// 预览版警告横幅（若有）
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if media.IsPreview {
							return layout.Inset{Bottom: Dp(16)}.Layout(gtx,
								Badge(u.theme, "当前为无媒体 UI 预览版 (godeck_preview)：远程画面捕获与编解码不可用，不可作为正式远程客户端使用。", ColWarning, ColPrimaryText),
							)
						}
						return layout.Dimensions{}
					}),

					// 页面大标题与辅助说明
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(Label(u.theme, "远程连接", 26, ColTextMain)),
							layout.Rigid(VGap(6)),
							layout.Rigid(Label(u.theme, "连接其他设备，或允许他人连接这台电脑", 14, ColTextSecondary)),
						)
					}),

					layout.Rigid(VGap(24)),

					// 核心双卡片布局：宽屏水平并排（顶端对齐，自然高度），窄屏垂直单列
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						isSingleColumn := gtx.Constraints.Max.X < gtx.Dp(Dp(900))

						if isSingleColumn {
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(u.layoutThisDeviceCard(c, primaryAddr, candidateAddrs, isWildcard, shownSecret, toggleSecretText, expiry, displayFp, fullFp)),
								layout.Rigid(VGap(20)),
								layout.Rigid(u.layoutConnectDeviceCard(connecting, statusMsg, probedFp, probingFp, probeErr)),
							)
						}

						// 宽屏等宽双卡
						return layout.Flex{
							Axis:      layout.Horizontal,
							Alignment: layout.Start,
						}.Layout(gtx,
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return u.layoutThisDeviceCard(c, primaryAddr, candidateAddrs, isWildcard, shownSecret, toggleSecretText, expiry, displayFp, fullFp)(gtx)
							}),
							layout.Rigid(HGap(24)),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return u.layoutConnectDeviceCard(connecting, statusMsg, probedFp, probingFp, probeErr)(gtx)
							}),
						)
					}),
				)
			})
		})
	})
}

// layoutThisDeviceCard 渲染左卡“这台设备”
func (u *UI) layoutThisDeviceCard(
	c *client.Controller,
	primaryAddr string,
	candidateAddrs []string,
	isWildcard bool,
	shownSecret, toggleSecretText string,
	expiry time.Time,
	displayFp, fullFp string,
) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return Card(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				// 卡片标题栏与状态指示
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(Heading(u.theme, "这台设备")),
						layout.Flexed(1, layout.Spacer{}.Layout),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if media.IsPreview {
								return Badge(u.theme, "预览模式", ColWarning, ColPrimaryText)(gtx)
							}
							if c.IncomingActive() {
								return Badge(u.theme, "正在被连接", ColWarning, ColPrimaryText)(gtx)
							}
							if c.Accepting() {
								return Badge(u.theme, "允许来访", ColSuccess, ColPrimaryText)(gtx)
							}
							return Badge(u.theme, "已暂停来访", ColSurfaceLight, ColTextMuted)(gtx)
						}),
					)
				}),

				layout.Rigid(VGap(16)),

				// 设备名称
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(Caption(u.theme, "设备名称：")),
						layout.Rigid(HGap(6)),
						layout.Rigid(Label(u.theme, u.name, 15, ColTextMain)),
					)
				}),

				layout.Rigid(VGap(14)),
				layout.Rigid(Divider()),
				layout.Rigid(VGap(14)),

				// 对方连接地址
				layout.Rigid(Caption(u.theme, "连接地址（提供给对方）：")),
				layout.Rigid(VGap(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(Label(u.theme, primaryAddr, 16, ColPrimary)),
						layout.Rigid(HGap(12)),
						layout.Rigid(GhostButton(u.theme, &u.btnCopyAddr, "复制地址")),
					)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if isWildcard && len(candidateAddrs) > 0 {
						return layout.Inset{Top: Dp(4)}.Layout(gtx,
							Muted(u.theme, "其它局域网候选: "+strings.Join(candidateAddrs, ", ")),
						)
					}
					if isWildcard {
						return layout.Inset{Top: Dp(4)}.Layout(gtx,
							Muted(u.theme, "服务监听所有网卡，请提供本机的实际局域网 IP 给对方"),
						)
					}
					return layout.Dimensions{}
				}),

				layout.Rigid(VGap(14)),
				layout.Rigid(Divider()),
				layout.Rigid(VGap(14)),

				// 临时会话密钥
				layout.Rigid(Caption(u.theme, "临时安全密钥（用于连接核验）：")),
				layout.Rigid(VGap(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(Label(u.theme, shownSecret, 16, ColTextMain)),
						layout.Rigid(HGap(12)),
						layout.Rigid(GhostButton(u.theme, &u.toggleShowSecret, toggleSecretText)),
						layout.Rigid(HGap(6)),
						layout.Rigid(GhostButton(u.theme, &u.btnCopySecret, "复制密钥")),
					)
				}),
				layout.Rigid(VGap(4)),
				layout.Rigid(Muted(u.theme, fmt.Sprintf("有效至 %s（每小时自动刷新）", expiry.Local().Format("15:04")))),

				layout.Rigid(VGap(14)),
				layout.Rigid(Divider()),
				layout.Rigid(VGap(14)),

				// 证书指纹 (折叠区)
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(Caption(u.theme, "设备指纹：")),
						layout.Rigid(HGap(6)),
						layout.Rigid(Muted(u.theme, displayFp)),
						layout.Rigid(HGap(8)),
						layout.Rigid(GhostButton(u.theme, &u.btnToggleFpExpand, func() string {
							if u.fpExpanded {
								return "折叠"
							}
							return "展开"
						}())),
						layout.Rigid(HGap(4)),
						layout.Rigid(GhostButton(u.theme, &u.btnCopyFp, "复制")),
					)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if u.fpExpanded {
						return layout.Inset{Top: Dp(8)}.Layout(gtx,
							Label(u.theme, fullFp, 11, ColTextMuted),
						)
					}
					return layout.Dimensions{}
				}),

				layout.Rigid(VGap(20)),

				// 底部操作区：开启/暂停来访服务，仅当真实存在入站连接时显示红色断开按钮
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					listenBtnText := "开启来访服务"
					if c.Accepting() {
						listenBtnText = "暂停来访服务"
					}

					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if media.IsPreview {
								return DisabledButton(u.theme, "预览版不可开启服务")(gtx)
							}
							if c.Accepting() {
								return SecondaryButton(u.theme, &u.toggleAccepting, listenBtnText)(gtx)
							}
							return PrimaryButton(u.theme, &u.toggleAccepting, listenBtnText)(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							// 仅在真实建立入站连接时显示红色“断开当前来访连接”，不常驻空闲首页
							if c.IncomingActive() {
								return layout.Inset{Left: Dp(12)}.Layout(gtx,
									DangerButton(u.theme, &u.btnEndIncoming, "断开当前来访连接"),
								)
							}
							return layout.Dimensions{}
						}),
					)
				}),
			)
		})
	}
}

// layoutConnectDeviceCard 渲染右卡“连接设备”
func (u *UI) layoutConnectDeviceCard(connecting bool, statusMsg string, probedFp string, probingFp bool, probeErr string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return Card(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				// 卡片标题
				layout.Rigid(Heading(u.theme, "连接设备")),

				layout.Rigid(VGap(14)),

				// 独立一行的分段选项：局域网直连 / 中心账号 (Hub)
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if u.directTabActive {
								return PrimaryButton(u.theme, &u.homeDirectTab, "局域网直连")(gtx)
							}
							return SecondaryButton(u.theme, &u.homeDirectTab, "局域网直连")(gtx)
						}),
						layout.Rigid(HGap(8)),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if !u.directTabActive {
								return PrimaryButton(u.theme, &u.homeHubTab, "中心账号 (Hub)")(gtx)
							}
							return SecondaryButton(u.theme, &u.homeHubTab, "中心账号 (Hub)")(gtx)
						}),
					)
				}),

				layout.Rigid(VGap(16)),

				// 分支 1：中心账号设备引导
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if !u.directTabActive {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(Label(u.theme, "使用中心账号（Hub）可跨公网管理绑定的设备，并一键发起端到端安全连接。", 14, ColTextSecondary)),
							layout.Rigid(VGap(16)),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return PrimaryButton(u.theme, &u.tabHub, "前往「设备」页面管理与连接")(gtx)
							}),
						)
					}

					// 分支 2：局域网直连表单
					targetAddr := strings.TrimSpace(u.inputTargetAddr.Text())
					canConnect := targetAddr != "" && !connecting && !probingFp

					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(Caption(u.theme, "目标设备 IP 与端口：")),
						layout.Rigid(VGap(6)),
						layout.Rigid(TextField(u.theme, &u.inputTargetAddr, "例如 192.168.1.100:8766")),

						layout.Rigid(VGap(14)),

						layout.Rigid(Caption(u.theme, "目标设备临时会话密钥：")),
						layout.Rigid(VGap(6)),
						layout.Rigid(TextField(u.theme, &u.inputTargetKey, "输入对方显示的 12 位密钥")),

						layout.Rigid(VGap(16)),

						// 错误信息显示（紧贴表单，可换行）
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if probeErr != "" {
								return layout.Inset{Bottom: Dp(12)}.Layout(gtx,
									Label(u.theme, probeErr, 13, ColDanger),
								)
							}
							return layout.Dimensions{}
						}),

						// 操作流程：探测中/连接中/核对指纹/就绪
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if connecting || probingFp {
								return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(Label(u.theme, statusMsg, 13, ColWarning)),
									layout.Rigid(HGap(12)),
									layout.Rigid(DangerButton(u.theme, &u.btnCancelConnect, "取消连接")),
								)
							}

							if probedFp != "" {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(Badge(u.theme, "核对证书指纹："+probedFp, ColWarning, ColPrimaryText)),
									layout.Rigid(VGap(10)),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
											layout.Rigid(PrimaryButton(u.theme, &u.btnDirectConnect, "确认指纹并连接")),
											layout.Rigid(HGap(10)),
											layout.Rigid(SecondaryButton(u.theme, &u.btnCancelConnect, "取消")),
										)
									}),
								)
							}

							if media.IsPreview {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(DisabledButton(u.theme, "预览版不可发起连接")),
								)
							}

							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									if canConnect {
										return PrimaryButton(u.theme, &u.btnProbeFp, "连接设备")(gtx)
									}
									return DisabledButton(u.theme, "连接设备")(gtx)
								}),
								layout.Rigid(VGap(6)),
								layout.Rigid(Muted(u.theme, "点击连接后将先与目标设备安全核对证书指纹")),
							)
						}),
					)
				}),
			)
		})
	}
}
