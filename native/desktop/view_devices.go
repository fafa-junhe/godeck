package desktop

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"godeck/native/hubclient"
)

func (u *UI) layoutDevicesView(gtx layout.Context) layout.Dimensions {
	c := u.m.controller

	submitLogin := isEditorSubmitted(&u.hubServerAddr, gtx) || isEditorSubmitted(&u.hubUsername, gtx) || isEditorSubmitted(&u.hubPassword, gtx)
	submitInit := isEditorSubmitted(&u.hubSetupCode, gtx)
	_ = isEditorSubmitted(&u.hubSearchInput, gtx)

	// Event handling: Hub Login
	if u.btnHubLogin.Clicked(gtx) || submitLogin {
		url := strings.TrimSpace(u.hubServerAddr.Text())
		user := strings.TrimSpace(u.hubUsername.Text())
		pass := u.hubPassword.Text()
		autoLogin := u.checkAutoLogin.Value

		u.m.mu.Lock()
		u.m.hubErrMsg = ""
		u.m.status = "正在登录中心..."
		u.m.mu.Unlock()

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			devices, err := c.HubLogin(ctx, url, user, pass, u.name)
			u.m.mu.Lock()
			defer u.m.mu.Unlock()
			if err != nil {
				u.m.hubErrMsg = "登录失败：" + err.Error()
				u.m.status = "中心登录失败"
			} else {
				u.m.hubLogged = true
				u.m.hubURL = url
				u.m.hubUser = user
				u.m.devices = devices
				u.m.status = "中心已登录"

				if u.cfg != nil {
					u.cfg.HubURL = url
					u.cfg.HubUser = user
					u.cfg.AutoLogin = autoLogin
					if autoLogin {
						u.cfg.HubPassword = pass
					} else {
						u.cfg.HubPassword = ""
					}
					u.saveConfig()
				}
			}
			if u.m.w != nil {
				u.m.w.Invalidate()
			}
		}()
	}

	// Event handling: Hub Init
	if u.btnHubInit.Clicked(gtx) || submitInit {
		url := strings.TrimSpace(u.hubServerAddr.Text())
		user := strings.TrimSpace(u.hubUsername.Text())
		pass := u.hubPassword.Text()
		code := strings.TrimSpace(u.hubSetupCode.Text())
		autoLogin := u.checkAutoLogin.Value
		u.hubSetupCode.SetText("")

		u.m.mu.Lock()
		u.m.hubErrMsg = ""
		u.m.status = "正在初始化中心账号..."
		u.m.mu.Unlock()

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			devices, err := c.HubInitialize(ctx, url, user, pass, u.name, code)
			u.m.mu.Lock()
			defer u.m.mu.Unlock()
			if err != nil {
				u.m.hubErrMsg = "初始化失败：" + err.Error()
				u.m.status = "中心初始化失败"
			} else {
				u.m.hubLogged = true
				u.m.hubURL = url
				u.m.hubUser = user
				u.m.devices = devices
				u.m.status = "中心初始化成功"

				if u.cfg != nil {
					u.cfg.HubURL = url
					u.cfg.HubUser = user
					u.cfg.AutoLogin = autoLogin
					if autoLogin {
						u.cfg.HubPassword = pass
					} else {
						u.cfg.HubPassword = ""
					}
					u.saveConfig()
				}
			}
			if u.m.w != nil {
				u.m.w.Invalidate()
			}
		}()
	}

	// Event handling: Hub Logout
	if u.btnHubLogout.Clicked(gtx) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = c.HubLogout(ctx)
			u.m.mu.Lock()
			u.m.hubLogged = false
			u.m.devices = nil
			u.m.hubErrMsg = ""
			u.m.status = "已退出中心"
			if u.cfg != nil {
				u.cfg.AutoLogin = false
				u.saveConfig()
			}
			u.m.mu.Unlock()
			if u.m.w != nil {
				u.m.w.Invalidate()
			}
		}()
	}

	// Event handling: Hub Pair
	if u.btnHubPair.Clicked(gtx) {
		u.m.mu.Lock()
		u.m.hubErrMsg = ""
		u.m.mu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := c.HubPair(ctx)
			devices, _ := c.HubDevices(ctx)
			u.m.mu.Lock()
			if err != nil {
				u.m.hubErrMsg = "绑定失败：" + err.Error()
			} else {
				u.m.devices = devices
				u.m.status = "本机已成功绑定到中心"
			}
			u.m.mu.Unlock()
			if u.m.w != nil {
				u.m.w.Invalidate()
			}
		}()
	}

	// Event handling: Hub Refresh
	if u.btnHubRefresh.Clicked(gtx) {
		u.m.mu.Lock()
		u.m.hubErrMsg = ""
		u.m.mu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			devices, err := c.HubDevices(ctx)
			u.m.mu.Lock()
			if err != nil {
				u.m.hubErrMsg = "获取设备列表失败：" + err.Error()
			} else {
				u.m.devices = devices
			}
			u.m.mu.Unlock()
			if u.m.w != nil {
				u.m.w.Invalidate()
			}
		}()
	}

	u.m.mu.Lock()
	hubLogged := u.m.hubLogged
	devices := append([]hubclient.Device(nil), u.m.devices...)
	hubUser := u.m.hubUser
	hubURL := u.m.hubURL
	hubErrMsg := u.m.hubErrMsg
	u.m.mu.Unlock()

	return layout.Inset{
		Top:    Dp(16),
		Bottom: Dp(16),
		Left:   Dp(24),
		Right:  Dp(24),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		if !hubLogged {
			return u.layoutHubLoginForm(gtx, hubErrMsg)
		}
		return u.layoutHubDeviceList(gtx, devices, hubUser, hubURL, hubErrMsg)
	})
}

func (u *UI) layoutHubLoginForm(gtx layout.Context, hubErrMsg string) layout.Dimensions {
	return Card(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(Heading(u.theme, "中心账号登录与接入")),
			layout.Rigid(VGap(6)),
			layout.Rigid(Caption(u.theme, "登录 GoDeck Hub 服务以实现远程设备集中发现、信令穿透与权限管理。")),
			layout.Rigid(VGap(16)),

			layout.Rigid(Caption(u.theme, "中心服务 HTTPS 地址：")),
			layout.Rigid(VGap(4)),
			layout.Rigid(TextField(u.theme, &u.hubServerAddr, "https://hub.example.com:8787")),
			layout.Rigid(VGap(12)),

			layout.Rigid(Caption(u.theme, "用户名：")),
			layout.Rigid(VGap(4)),
			layout.Rigid(TextField(u.theme, &u.hubUsername, "例如 admin")),
			layout.Rigid(VGap(12)),

			layout.Rigid(Caption(u.theme, "密码：")),
			layout.Rigid(VGap(4)),
			layout.Rigid(TextField(u.theme, &u.hubPassword, "登录密码")),
			layout.Rigid(VGap(10)),

			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return material.CheckBox(u.theme, &u.checkAutoLogin, "记住密码并在应用启动时自动登录").Layout(gtx)
			}),
			layout.Rigid(VGap(14)),

			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if hubErrMsg != "" {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(Label(u.theme, hubErrMsg, 12, ColDanger)),
						layout.Rigid(VGap(8)),
					)
				}
				return layout.Dimensions{}
			}),

			layout.Rigid(PrimaryButton(u.theme, &u.btnHubLogin, "登录中心账号")),
			layout.Rigid(VGap(20)),
			layout.Rigid(Divider()),
			layout.Rigid(VGap(16)),

			layout.Rigid(Subheading(u.theme, "首次部署初始化 (Setup Code)")),
			layout.Rigid(VGap(6)),
			layout.Rigid(Caption(u.theme, "如果是刚部署的新中心，使用 Hub 启动时生成的 Setup Code 创建管理员主人账号。")),
			layout.Rigid(VGap(8)),
			layout.Rigid(TextField(u.theme, &u.hubSetupCode, "首次初始化 Setup Code")),
			layout.Rigid(VGap(12)),
			layout.Rigid(SecondaryButton(u.theme, &u.btnHubInit, "初始化主人账号并登录")),
		)
	})
}

func (u *UI) layoutHubDeviceList(gtx layout.Context, devices []hubclient.Device, hubUser, hubURL, hubErrMsg string) layout.Dimensions {
	c := u.m.controller
	localID := c.HubLocalID()

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		// Header Bar
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return Card(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(Subheading(u.theme, "账号："+hubUser)),
							layout.Rigid(VGap(4)),
							layout.Rigid(Muted(u.theme, "中心："+hubURL)),
						)
					}),
					layout.Rigid(HGap(20)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if localID != "" {
							return Badge(u.theme, "本机已绑定 (ID: "+localID[:min(8, len(localID))]+")", ColSuccess, ColPrimaryText)(gtx)
						}
						return Badge(u.theme, "本机未绑定", ColWarning, ColPrimaryText)(gtx)
					}),
					layout.Rigid(HGap(8)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						autoText := "自动登录：关"
						bg := ColTextSecondary
						if u.cfg != nil && u.cfg.AutoLogin {
							autoText = "自动登录：开"
							bg = ColSuccess
						}
						return Badge(u.theme, autoText, bg, ColPrimaryText)(gtx)
					}),
					layout.Flexed(1, layout.Spacer{}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if localID == "" {
							return PrimaryButton(u.theme, &u.btnHubPair, "绑定本机到中心")(gtx)
						}
						return layout.Dimensions{}
					}),
					layout.Rigid(HGap(8)),
					layout.Rigid(SecondaryButton(u.theme, &u.btnHubRefresh, "刷新设备")),
					layout.Rigid(HGap(8)),
					layout.Rigid(DangerButton(u.theme, &u.btnHubLogout, "退出账号")),
				)
			})
		}),
		layout.Rigid(VGap(12)),

		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if hubErrMsg != "" {
				return layout.Inset{Bottom: Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return CardWithBorder(gtx, ColDanger, func(gtx layout.Context) layout.Dimensions {
						return Label(u.theme, hubErrMsg, 13, ColDanger)(gtx)
					})
				})
			}
			return layout.Dimensions{}
		}),

		// Search Filter
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return TextField(u.theme, &u.hubSearchInput, "搜索设备名称或设备 ID...")(gtx)
		}),
		layout.Rigid(VGap(12)),

		// Device Cards List
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			searchQuery := strings.ToLower(strings.TrimSpace(u.hubSearchInput.Text()))
			var filtered []struct {
				idx int
				dev hubclient.Device
			}
			for i, d := range devices {
				if searchQuery != "" && !strings.Contains(strings.ToLower(d.Name+" "+d.ID), searchQuery) {
					continue
				}
				filtered = append(filtered, struct {
					idx int
					dev hubclient.Device
				}{idx: i, dev: d})
			}

			if len(filtered) == 0 {
				return Card(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Center.Layout(gtx, Muted(u.theme, "暂无匹配的设备。请在其他机器上运行 GoDeck 并登录绑定。"))
				})
			}

			return material.List(u.theme, &u.hubList).Layout(gtx, len(filtered), func(gtx layout.Context, i int) layout.Dimensions {
				item := filtered[i]
				dev := item.dev
				isLocal := (dev.ID == localID)

				// Ensure slices capacity
				for len(u.hubDeviceBtns) <= item.idx {
					u.hubDeviceBtns = append(u.hubDeviceBtns, widget.Clickable{})
					u.hubRevokeBtns = append(u.hubRevokeBtns, widget.Clickable{})
				}

				// Check connect click
				if !isLocal && u.hubDeviceBtns[item.idx].Clicked(gtx) {
					targetID := dev.ID
					u.m.mu.Lock()
					u.m.hubErrMsg = ""
					u.m.connecting = true
					u.m.status = "正在通过中心发起连接..."
					ctx, cancel := context.WithCancel(context.Background())
					u.m.connectCancel = cancel
					u.m.mu.Unlock()

					go func() {
						err := c.HubConnect(ctx, targetID)
						u.m.mu.Lock()
						defer u.m.mu.Unlock()
						u.m.connecting = false
						if err != nil {
							u.m.hubErrMsg = "中心连接失败：" + err.Error()
							u.m.status = "中心连接失败：" + err.Error()
						} else {
							u.m.sessionActive = true
							u.m.pendingTab = TabSession
							if u.m.frame == nil {
								u.m.status = "等待画面..."
							}
						}
						if u.m.w != nil {
							u.m.w.Invalidate()
						}
					}()
				}

				// Check revoke click
				if u.hubRevokeBtns[item.idx].Clicked(gtx) {
					if u.hubRevokeConfirm == item.idx {
						// Confirmed revoke
						u.hubRevokeConfirm = -1
						targetID := dev.ID
						go func() {
							ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
							defer cancel()
							_ = c.HubRevoke(ctx, targetID)
							devs, _ := c.HubDevices(ctx)
							u.m.mu.Lock()
							u.m.devices = devs
							u.m.mu.Unlock()
							if u.m.w != nil {
								u.m.w.Invalidate()
							}
						}()
					} else {
						u.hubRevokeConfirm = item.idx
					}
				}

				return layout.Inset{Bottom: Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return Card(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							// Device Name and ID
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
											layout.Rigid(Subheading(u.theme, dev.Name)),
											layout.Rigid(HGap(10)),
											layout.Rigid(func(gtx layout.Context) layout.Dimensions {
												if isLocal {
													return Badge(u.theme, "本机", ColPrimary, ColPrimaryText)(gtx)
												}
												if dev.Online {
													if dev.Accepting {
														return Badge(u.theme, "在线 · 允许来访", ColSuccess, ColPrimaryText)(gtx)
													}
													return Badge(u.theme, "在线 · 暂停来访", ColWarning, ColPrimaryText)(gtx)
												}
												return Badge(u.theme, "离线", ColTextMuted, ColTextMain)(gtx)
											}),
										)
									}),
									layout.Rigid(VGap(4)),
									layout.Rigid(Muted(u.theme, fmt.Sprintf("设备 ID: %s | 平台: %s", dev.ID, dev.Platform))),
								)
							}),

							// Action Buttons
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								if isLocal {
									return Muted(u.theme, "（当前正在使用的设备）")(gtx)
								}

								return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										if dev.Online && dev.Accepting {
											return PrimaryButton(u.theme, &u.hubDeviceBtns[item.idx], "发起远控连接")(gtx)
										}
										return layout.Dimensions{}
									}),
									layout.Rigid(HGap(10)),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										if u.hubRevokeConfirm == item.idx {
											return DangerButton(u.theme, &u.hubRevokeBtns[item.idx], "确认解绑？")(gtx)
										}
										return GhostButton(u.theme, &u.hubRevokeBtns[item.idx], "解绑")(gtx)
									}),
								)
							}),
						)
					})
				})
			})
		}),
	)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func isEditorSubmitted(ed *widget.Editor, gtx layout.Context) bool {
	if ed == nil {
		return false
	}
	submitted := false
	for {
		ev, ok := ed.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			submitted = true
		}
	}
	return submitted
}
