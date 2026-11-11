package desktop

import (
	"fmt"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"godeck/native/transfer"
)

func (u *UI) layoutFilesView(gtx layout.Context) layout.Dimensions {
	c := u.m.controller

	_ = isEditorSubmitted(&u.fileSearchInput, gtx)

	// Event handling: Refresh file list
	if u.btnFileRefresh.Clicked(gtx) {
		if fm := c.FileManager(); fm != nil {
			go func() {
				if err := fm.List(); err != nil {
					u.m.mu.Lock()
					u.m.transferErr = "刷新列表失败：" + err.Error()
					u.m.mu.Unlock()
					if u.m.w != nil {
						u.m.w.Invalidate()
					}
				}
			}()
		}
	}

	// Event handling: Cancel current transfer
	if u.btnFileCancel.Clicked(gtx) {
		if fm := c.FileManager(); fm != nil {
			go func() {
				_ = fm.Cancel()
			}()
		}
	}

	// Event handling: Upload File
	if u.btnFileUpload.Clicked(gtx) {
		go func() {
			path, err := transfer.ChooseOpenFile("选择要上传的文件")
			if err == nil && path != "" {
				if fm := c.FileManager(); fm != nil {
					if err = fm.Upload(path); err != nil {
						u.m.mu.Lock()
						u.m.transferErr = "上传失败：" + err.Error()
						u.m.mu.Unlock()
						if u.m.w != nil {
							u.m.w.Invalidate()
						}
					}
				}
			}
		}()
	}

	u.m.mu.Lock()
	files := append([]transfer.File(nil), u.m.fileList...)
	transferring := u.m.transferring
	progText := u.m.transferProg
	errText := u.m.transferErr
	sessionActive := u.m.sessionActive
	u.m.mu.Unlock()

	return layout.Inset{
		Top:    Dp(16),
		Bottom: Dp(16),
		Left:   Dp(24),
		Right:  Dp(24),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		if !sessionActive || !c.RemoteFilesAllowed() {
			return Card(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(Heading(u.theme, "文件传输通道")),
					layout.Rigid(VGap(8)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if !sessionActive {
							return Badge(u.theme, "未建立远程连接", ColTextMuted, ColTextMain)(gtx)
						}
						return Badge(u.theme, "对端未授权文件传输", ColWarning, ColPrimaryText)(gtx)
					}),
					layout.Rigid(VGap(16)),
					layout.Rigid(Caption(u.theme, "本机文件共享目录：")),
					layout.Rigid(VGap(4)),
					layout.Rigid(Subheading(u.theme, c.ShareDir())),
					layout.Rigid(VGap(16)),
					layout.Rigid(Muted(u.theme, "说明：发起连接并获得对端文件授权后，双方即可在独立通道安全互传文件，支持断点控制与背压保护。")),
				)
			})
		}

		// Active session and files permitted
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			// Header & Actions
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return Card(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(Heading(u.theme, "远端共享文件")),
						layout.Rigid(HGap(16)),
						layout.Rigid(PrimaryButton(u.theme, &u.btnFileUpload, "上传文件到对端...")),
						layout.Rigid(HGap(8)),
						layout.Rigid(SecondaryButton(u.theme, &u.btnFileRefresh, "刷新列表")),
						layout.Flexed(1, layout.Spacer{}.Layout),
						layout.Rigid(Muted(u.theme, "本机下载目录："+c.ShareDir())),
					)
				})
			}),
			layout.Rigid(VGap(12)),

			// Transfer In-Progress Card
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if transferring {
					return layout.Inset{Bottom: Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return CardWithBorder(gtx, ColPrimary, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(Badge(u.theme, "传输中", ColPrimary, ColPrimaryText)),
								layout.Rigid(HGap(12)),
								layout.Flexed(1, Subheading(u.theme, progText)),
								layout.Rigid(DangerButton(u.theme, &u.btnFileCancel, "取消当前传输")),
							)
						})
					})
				}
				if errText != "" {
					return layout.Inset{Bottom: Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return CardWithBorder(gtx, ColDanger, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(Badge(u.theme, "传输提示", ColDanger, ColTextMain)),
								layout.Rigid(HGap(12)),
								layout.Flexed(1, Label(u.theme, errText, 13, ColDanger)),
							)
						})
					})
				}
				return layout.Dimensions{}
			}),

			// Search Filter
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return TextField(u.theme, &u.fileSearchInput, "搜索对端文件列表...")(gtx)
			}),
			layout.Rigid(VGap(12)),

			// File List
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				searchQ := strings.ToLower(strings.TrimSpace(u.fileSearchInput.Text()))
				var filtered []struct {
					idx int
					f   transfer.File
				}
				for i, f := range files {
					if searchQ != "" && !strings.Contains(strings.ToLower(f.Name), searchQ) {
						continue
					}
					filtered = append(filtered, struct {
						idx int
						f   transfer.File
					}{idx: i, f: f})
				}

				if len(filtered) == 0 {
					return Card(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Center.Layout(gtx, Muted(u.theme, "远端目录暂无文件或未匹配到搜索结果。"))
					})
				}

				return material.List(u.theme, &u.filesList).Layout(gtx, len(filtered), func(gtx layout.Context, i int) layout.Dimensions {
					item := filtered[i]
					f := item.f

					for len(u.fileDownloadBtns) <= item.idx {
						u.fileDownloadBtns = append(u.fileDownloadBtns, widget.Clickable{})
					}

					if u.fileDownloadBtns[item.idx].Clicked(gtx) {
						fileName := f.Name
						go func() {
							if fm := c.FileManager(); fm != nil {
								if err := fm.Download(fileName); err != nil {
									u.m.mu.Lock()
									u.m.transferErr = "下载失败：" + err.Error()
									u.m.mu.Unlock()
									if u.m.w != nil {
										u.m.w.Invalidate()
									}
								}
							}
						}()
					}

					return layout.Inset{Bottom: Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return Card(gtx, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
								layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
									return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
										layout.Rigid(Subheading(u.theme, f.Name)),
										layout.Rigid(VGap(4)),
										layout.Rigid(Muted(u.theme, fmt.Sprintf("%s · 修改时间: %s", formatBytes(f.Size), f.Modified.Local().Format("2006-01-02 15:04:05")))),
									)
								}),
								layout.Rigid(PrimaryButton(u.theme, &u.fileDownloadBtns[item.idx], "下载")),
							)
						})
					})
				})
			}),
		)
	})
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
