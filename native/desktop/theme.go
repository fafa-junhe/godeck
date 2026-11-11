package desktop

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

var (
	// Brand & Surface Palette
	ColBg           = color.NRGBA{R: 20, G: 22, B: 25, A: 255}   // #141619 深石墨底
	ColSurface      = color.NRGBA{R: 31, G: 35, B: 39, A: 255}   // #1f2327 主卡片底色
	ColSurfaceLight = color.NRGBA{R: 40, G: 45, B: 50, A: 255}   // #282d32 输入框/悬浮底色
	ColBorder       = color.NRGBA{R: 52, G: 58, B: 66, A: 255}   // #343a42 边框色
	ColDivider      = color.NRGBA{R: 36, G: 40, B: 45, A: 255}   // #24282d 分隔线色

	// Accent Colors
	ColPrimary     = color.NRGBA{R: 195, G: 233, B: 155, A: 255} // #c3e99b 柔和青柠主操作色
	ColPrimaryDark = color.NRGBA{R: 160, G: 200, B: 120, A: 255} // Hover Lime
	ColPrimaryText = color.NRGBA{R: 20, G: 25, B: 20, A: 255}    // Contrast text on lime
	ColActiveTab   = color.NRGBA{R: 45, G: 52, B: 58, A: 255}

	// Status Colors
	ColSuccess = color.NRGBA{R: 85, G: 215, B: 155, A: 255} // Green 正常/就绪
	ColWarning = color.NRGBA{R: 245, G: 190, B: 75, A: 255} // Amber 警告/连接中
	ColDanger  = color.NRGBA{R: 235, G: 85, B: 85, A: 255}  // Coral Red 危险/断开
	ColInfo    = color.NRGBA{R: 100, G: 180, B: 245, A: 255} // Light Blue 信息

	// Typography Colors
	ColTextMain      = color.NRGBA{R: 235, G: 240, B: 245, A: 255} // 标题与主文字
	ColTextSecondary = color.NRGBA{R: 165, G: 175, B: 185, A: 255} // 字段标签与次级文字
	ColTextMuted     = color.NRGBA{R: 110, G: 120, B: 130, A: 255} // 辅助文字与占位符
)

func Dp(val int) unit.Dp {
	return unit.Dp(val)
}

func Sp(val float32) unit.Sp {
	return unit.Sp(val)
}

func Label(th *material.Theme, text string, sizeSp float32, col color.NRGBA) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		lbl := material.Label(th, Sp(sizeSp), text)
		lbl.Color = col
		return lbl.Layout(gtx)
	}
}

func Heading(th *material.Theme, text string) layout.Widget {
	return Label(th, text, 18, ColTextMain)
}

func Subheading(th *material.Theme, text string) layout.Widget {
	return Label(th, text, 15, ColTextMain)
}

func Caption(th *material.Theme, text string) layout.Widget {
	return Label(th, text, 13, ColTextSecondary)
}

func Muted(th *material.Theme, text string) layout.Widget {
	return Label(th, text, 12, ColTextMuted)
}

func Card(gtx layout.Context, content layout.Widget) layout.Dimensions {
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			r := clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Min}, gtx.Dp(8))
			paint.FillShape(gtx.Ops, ColSurface, r.Op(gtx.Ops))
			return layout.Dimensions{Size: gtx.Constraints.Min}
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(Dp(20)).Layout(gtx, content)
		}),
	)
}

func CardWithBorder(gtx layout.Context, borderCol color.NRGBA, content layout.Widget) layout.Dimensions {
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			r := clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Min}, gtx.Dp(8))
			paint.FillShape(gtx.Ops, ColSurface, r.Op(gtx.Ops))
			stroke := clip.Stroke{
				Path:  r.Path(gtx.Ops),
				Width: float32(gtx.Dp(1)),
			}
			paint.FillShape(gtx.Ops, borderCol, stroke.Op())
			return layout.Dimensions{Size: gtx.Constraints.Min}
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(Dp(20)).Layout(gtx, content)
		}),
	)
}

func Badge(th *material.Theme, text string, bg, fg color.NRGBA) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx layout.Context) layout.Dimensions {
				r := clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Min}, gtx.Dp(4))
				paint.FillShape(gtx.Ops, bg, r.Op(gtx.Ops))
				return layout.Dimensions{Size: gtx.Constraints.Min}
			}),
			layout.Stacked(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{
					Top: Dp(3), Bottom: Dp(3),
					Left: Dp(8), Right: Dp(8),
				}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					lbl := material.Label(th, Sp(12), text)
					lbl.Color = fg
					return lbl.Layout(gtx)
				})
			}),
		)
	}
}

func PrimaryButton(th *material.Theme, btn *widget.Clickable, text string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		b := material.Button(th, btn, text)
		b.Background = ColPrimary
		b.Color = ColPrimaryText
		b.CornerRadius = Dp(6)
		b.TextSize = Sp(14)
		b.Inset = layout.Inset{Top: Dp(10), Bottom: Dp(10), Left: Dp(18), Right: Dp(18)}
		return b.Layout(gtx)
	}
}

func SecondaryButton(th *material.Theme, btn *widget.Clickable, text string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		b := material.Button(th, btn, text)
		b.Background = ColSurfaceLight
		b.Color = ColTextMain
		b.CornerRadius = Dp(6)
		b.TextSize = Sp(14)
		b.Inset = layout.Inset{Top: Dp(10), Bottom: Dp(10), Left: Dp(16), Right: Dp(16)}
		return b.Layout(gtx)
	}
}

func DangerButton(th *material.Theme, btn *widget.Clickable, text string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		b := material.Button(th, btn, text)
		b.Background = ColDanger
		b.Color = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
		b.CornerRadius = Dp(6)
		b.TextSize = Sp(14)
		b.Inset = layout.Inset{Top: Dp(10), Bottom: Dp(10), Left: Dp(16), Right: Dp(16)}
		return b.Layout(gtx)
	}
}

func GhostButton(th *material.Theme, btn *widget.Clickable, text string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		b := material.Button(th, btn, text)
		b.Background = color.NRGBA{A: 0}
		b.Color = ColPrimary
		b.CornerRadius = Dp(6)
		b.TextSize = Sp(13)
		b.Inset = layout.Inset{Top: Dp(6), Bottom: Dp(6), Left: Dp(10), Right: Dp(10)}
		return b.Layout(gtx)
	}
}

// DisabledButton 使用纯样式容器渲染不可交互状态，避免每帧分配 Clickable 对象。
func DisabledButton(th *material.Theme, text string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx layout.Context) layout.Dimensions {
				r := clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Min}, gtx.Dp(6))
				paint.FillShape(gtx.Ops, ColSurfaceLight, r.Op(gtx.Ops))
				return layout.Dimensions{Size: gtx.Constraints.Min}
			}),
			layout.Stacked(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{
					Top: Dp(10), Bottom: Dp(10),
					Left: Dp(18), Right: Dp(18),
				}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					lbl := material.Label(th, Sp(14), text)
					lbl.Color = ColTextMuted
					return lbl.Layout(gtx)
				})
			}),
		)
	}
}

// TextField 撑满可用宽度，最小高度 44dp，聚焦高亮青柠色边框。
func TextField(th *material.Theme, ed *widget.Editor, hint string) layout.Widget {
	ed.SingleLine = true
	ed.Submit = true
	return func(gtx layout.Context) layout.Dimensions {
		minH := gtx.Dp(Dp(44))
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx layout.Context) layout.Dimensions {
				sz := gtx.Constraints.Min
				if sz.Y < minH {
					sz.Y = minH
				}
				r := clip.UniformRRect(image.Rectangle{Max: sz}, gtx.Dp(8))
				paint.FillShape(gtx.Ops, ColSurfaceLight, r.Op(gtx.Ops))
				borderCol := ColBorder
				if gtx.Focused(ed) {
					borderCol = ColPrimary
				}
				stroke := clip.Stroke{Path: r.Path(gtx.Ops), Width: float32(gtx.Dp(1))}
				paint.FillShape(gtx.Ops, borderCol, stroke.Op())
				return layout.Dimensions{Size: sz}
			}),
			layout.Stacked(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{
					Top:    Dp(11),
					Bottom: Dp(11),
					Left:   Dp(14),
					Right:  Dp(14),
				}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					e := material.Editor(th, ed, hint)
					e.TextSize = Sp(15)
					e.Color = ColTextMain
					e.HintColor = ColTextMuted
					return e.Layout(gtx)
				})
			}),
		)
	}
}

func Gap(dp int) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(gtx.Dp(Dp(dp)), gtx.Dp(Dp(dp)))}
	}
}

func HGap(dp int) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(gtx.Dp(Dp(dp)), 1)}
	}
}

func VGap(dp int) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(1, gtx.Dp(Dp(dp)))}
	}
}

// Divider 采用精准局部裁切的矩形绘制 1dp 分隔线，严禁使用全屏 paint.Fill。
func Divider() layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(Dp(1))
		if h < 1 {
			h = 1
		}
		w := gtx.Constraints.Max.X
		if w <= 0 {
			w = gtx.Constraints.Min.X
		}
		sz := image.Pt(w, h)
		r := clip.Rect{Max: sz}
		paint.FillShape(gtx.Ops, ColDivider, r.Op())
		return layout.Dimensions{Size: sz}
	}
}
