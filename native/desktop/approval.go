package desktop

import (
	"fmt"
	"time"

	"gioui.org/layout"
	"gioui.org/widget/material"
)

// layoutApprovalCard renders a prominent incoming request approval banner/card
// when a remote device attempts to connect to this machine.
func (u *UI) layoutApprovalCard(gtx layout.Context) layout.Dimensions {
	u.m.mu.Lock()
	req := u.m.incoming
	u.m.mu.Unlock()

	if req == nil {
		return layout.Dimensions{}
	}

	remaining := time.Until(req.Deadline).Round(time.Second)
	if remaining <= 0 {
		u.m.mu.Lock()
		u.m.incoming = nil
		u.m.mu.Unlock()
		return layout.Dimensions{}
	}

	// Handle button clicks
	if u.approveView.Clicked(gtx) {
		u.m.mu.Lock()
		r := u.m.incoming
		u.m.incoming = nil
		u.m.mu.Unlock()
		if r != nil {
			u.m.controller.Decide(r.ID, true, false, u.approveFiles.Value, u.approveClipboard.Value)
		}
	}
	if u.approveControl.Clicked(gtx) {
		u.m.mu.Lock()
		r := u.m.incoming
		u.m.incoming = nil
		u.m.mu.Unlock()
		if r != nil {
			u.m.controller.Decide(r.ID, true, true, u.approveFiles.Value, u.approveClipboard.Value)
		}
	}
	if u.rejectIncoming.Clicked(gtx) {
		u.m.mu.Lock()
		r := u.m.incoming
		u.m.incoming = nil
		u.m.mu.Unlock()
		if r != nil {
			u.m.controller.Decide(r.ID, false, false, false, false)
		}
	}

	return layout.Inset{
		Top:    Dp(12),
		Bottom: Dp(12),
		Left:   Dp(20),
		Right:  Dp(20),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return CardWithBorder(gtx, ColWarning, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(Badge(u.theme, "来访连接请求", ColWarning, ColPrimaryText)),
						layout.Rigid(HGap(12)),
						layout.Flexed(1, Subheading(u.theme, fmt.Sprintf("%s (%s)", req.Name, req.Remote))),
						layout.Rigid(Muted(u.theme, fmt.Sprintf("剩余审批时间：%v", remaining))),
					)
				}),
				layout.Rigid(VGap(10)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							label := "允许该来访者使用文件传输"
							if !u.m.controller.AllowFilesHost() {
								label = "允许文件传输 (全局共享未开启)"
							}
							return material.CheckBox(u.theme, &u.approveFiles, label).Layout(gtx)
						}),
						layout.Rigid(HGap(12)),
						layout.Rigid(material.CheckBox(u.theme, &u.approveClipboard, "允许双向剪贴板同步（文本）").Layout),
						layout.Flexed(1, layout.Spacer{}.Layout),
						layout.Rigid(PrimaryButton(u.theme, &u.approveControl, "允许控制与查看")),
						layout.Rigid(HGap(10)),
						layout.Rigid(SecondaryButton(u.theme, &u.approveView, "仅允许查看")),
						layout.Rigid(HGap(10)),
						layout.Rigid(DangerButton(u.theme, &u.rejectIncoming, "拒绝")),
					)
				}),
			)
		})
	})
}
