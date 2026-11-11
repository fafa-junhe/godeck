package desktop

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"godeck/native/media"
)

const (
	TabHome     = 0
	TabHub      = 1
	TabSession  = 2
	TabFiles    = 3
	TabSettings = 4
)

func (u *UI) layoutNavbar(gtx layout.Context) layout.Dimensions {
	if u.tabHome.Clicked(gtx) {
		u.activeTab = TabHome
	}
	if u.tabHub.Clicked(gtx) {
		u.activeTab = TabHub
	}
	if u.tabSession.Clicked(gtx) {
		u.activeTab = TabSession
	}
	if u.tabFiles.Clicked(gtx) {
		u.activeTab = TabFiles
	}
	if u.tabSettings.Clicked(gtx) {
		u.activeTab = TabSettings
	}

	u.m.mu.Lock()
	sessionActive := u.m.sessionActive
	transferring := u.m.transferring
	statusText := u.m.status
	u.m.mu.Unlock()

	// 限制状态文字长度，防止挤压导航入口
	shortStatus := statusText
	if len([]rune(shortStatus)) > 14 {
		shortStatus = string([]rune(shortStatus)[:12]) + "..."
	}

	return layout.Stack{}.Layout(gtx,
		// 背景使用明确几何约束的 FillShape，严禁使用全屏 paint.Fill
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			r := clip.Rect{Max: gtx.Constraints.Min}
			paint.FillShape(gtx.Ops, ColSurface, r.Op())
			// 底部 1dp 细分隔线
			borderLine := clip.Rect{
				Min: image.Pt(0, gtx.Constraints.Min.Y-gtx.Dp(Dp(1))),
				Max: gtx.Constraints.Min,
			}
			paint.FillShape(gtx.Ops, ColBorder, borderLine.Op())
			return layout.Dimensions{Size: gtx.Constraints.Min}
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			isNarrow := gtx.Constraints.Max.X < gtx.Dp(Dp(780))

			if isNarrow {
				// 窄屏布局：上行品牌与状态，下行完整 5 个导航项
				return layout.Inset{
					Top:    Dp(8),
					Bottom: Dp(8),
					Left:   Dp(16),
					Right:  Dp(16),
				}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									lbl := material.Label(u.theme, Sp(17), "GoDeck")
									lbl.Color = ColPrimary
									return lbl.Layout(gtx)
								}),
								layout.Flexed(1, layout.Spacer{}.Layout),
								layout.Rigid(u.layoutStatusPill(sessionActive, shortStatus)),
							)
						}),
						layout.Rigid(VGap(6)),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(u.navItem("连接", &u.tabHome, u.activeTab == TabHome, false)),
								layout.Rigid(HGap(4)),
								layout.Rigid(u.navItem("设备", &u.tabHub, u.activeTab == TabHub, false)),
								layout.Rigid(HGap(4)),
								layout.Rigid(u.navItem("远程桌面", &u.tabSession, u.activeTab == TabSession, sessionActive)),
								layout.Rigid(HGap(4)),
								layout.Rigid(u.navItem("文件", &u.tabFiles, u.activeTab == TabFiles, transferring)),
								layout.Rigid(HGap(4)),
								layout.Rigid(u.navItem("设置", &u.tabSettings, u.activeTab == TabSettings, false)),
							)
						}),
					)
				})
			}

			// 宽屏标准布局（单行固定约 60dp）
			return layout.Inset{
				Top:    Dp(12),
				Bottom: Dp(12),
				Left:   Dp(24),
				Right:  Dp(24),
			}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{
					Axis:      layout.Horizontal,
					Alignment: layout.Middle,
				}.Layout(gtx,
					// Logo & App Name
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								lbl := material.Label(u.theme, Sp(18), "GoDeck")
								lbl.Color = ColPrimary
								return lbl.Layout(gtx)
							}),
							layout.Rigid(HGap(8)),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								sz := gtx.Dp(Dp(4))
								r := clip.UniformRRect(image.Rectangle{Max: image.Pt(sz, sz)}, sz/2)
								paint.FillShape(gtx.Ops, ColTextMuted, r.Op(gtx.Ops))
								return layout.Dimensions{Size: image.Pt(sz, sz)}
							}),
						)
					}),

					layout.Rigid(HGap(24)),

					// 5 个导航标签（连接、设备、远程桌面、文件、设置）
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(u.navItem("连接", &u.tabHome, u.activeTab == TabHome, false)),
							layout.Rigid(HGap(6)),
							layout.Rigid(u.navItem("设备", &u.tabHub, u.activeTab == TabHub, false)),
							layout.Rigid(HGap(6)),
							layout.Rigid(u.navItem("远程桌面", &u.tabSession, u.activeTab == TabSession, sessionActive)),
							layout.Rigid(HGap(6)),
							layout.Rigid(u.navItem("文件", &u.tabFiles, u.activeTab == TabFiles, transferring)),
							layout.Rigid(HGap(6)),
							layout.Rigid(u.navItem("设置", &u.tabSettings, u.activeTab == TabSettings, false)),
						)
					}),

					// 右侧状态指示器
					layout.Rigid(u.layoutStatusPill(sessionActive, shortStatus)),
				)
			})
		}),
	)
}

func (u *UI) layoutStatusPill(sessionActive bool, shortStatus string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		if media.IsPreview {
			return Badge(u.theme, "UI 预览模式", ColWarning, ColPrimaryText)(gtx)
		}
		dotCol := ColSuccess
		if !sessionActive && (shortStatus == "未连接" || shortStatus == "") {
			dotCol = ColTextMuted
			if shortStatus == "" {
				shortStatus = "空闲就绪"
			}
		} else if !sessionActive {
			dotCol = ColWarning
		}
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				sz := gtx.Dp(Dp(8))
				r := clip.UniformRRect(image.Rectangle{Max: image.Pt(sz, sz)}, sz/2)
				paint.FillShape(gtx.Ops, dotCol, r.Op(gtx.Ops))
				return layout.Dimensions{Size: image.Pt(sz, sz)}
			}),
			layout.Rigid(HGap(6)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(u.theme, Sp(12), shortStatus)
				lbl.Color = ColTextSecondary
				return lbl.Layout(gtx)
			}),
		)
	}
}

func (u *UI) navItem(title string, btn *widget.Clickable, active bool, dot bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return btn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Stack{}.Layout(gtx,
				layout.Expanded(func(gtx layout.Context) layout.Dimensions {
					if active {
						r := clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Min}, gtx.Dp(6))
						paint.FillShape(gtx.Ops, ColActiveTab, r.Op(gtx.Ops))
					}
					return layout.Dimensions{Size: gtx.Constraints.Min}
				}),
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{
						Top:    Dp(7),
						Bottom: Dp(7),
						Left:   Dp(14),
						Right:  Dp(14),
					}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								col := ColTextSecondary
								if active {
									col = ColPrimary
								}
								lbl := material.Label(u.theme, Sp(14), title)
								lbl.Color = col
								return lbl.Layout(gtx)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								if !dot {
									return layout.Dimensions{}
								}
								sz := gtx.Dp(Dp(6))
								r := clip.UniformRRect(image.Rectangle{Max: image.Pt(sz, sz)}, sz/2)
								paint.FillShape(gtx.Ops, ColPrimary, r.Op(gtx.Ops))
								return layout.Inset{Left: Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return layout.Dimensions{Size: image.Pt(sz, sz)}
								})
							}),
						)
					})
				}),
			)
		})
	}
}
