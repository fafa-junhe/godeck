package desktop

import (
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"godeck/native/transfer"
)

// fileUI groups the file manager widgets.
type fileUI struct {
	btnUploadFolder widget.Clickable
	btnUp           widget.Clickable
	btnOpenLocal    widget.Clickable
	btnCancelAll    widget.Clickable
	btnClearDone    widget.Clickable
	crumbs          [33]widget.Clickable
	openBtns        []widget.Clickable
	dirDownloadBtns []widget.Clickable
	jobsList        widget.List
}

func (u *UI) fileError(prefix string, err error) {
	u.m.mu.Lock()
	u.m.transferErr = prefix + err.Error()
	u.m.mu.Unlock()
	if u.m.w != nil {
		u.m.w.Invalidate()
	}
}

func (u *UI) listRemote(dir string) {
	if fm := u.m.controller.FileManager(); fm != nil {
		go func() {
			if err := fm.List(dir); err != nil {
				u.fileError("刷新列表失败：", err)
			}
		}()
	}
}

// handleFileDrop queues dropped local files/folders for upload into the
// remote directory currently shown. Called on the window event thread.
func (m *Model) handleFileDrop(paths []string) {
	m.mu.Lock()
	onFiles := m.filesTabActive
	dir := m.remotePath
	m.mu.Unlock()
	fm := m.controller.FileManager()
	if !onFiles || fm == nil || !m.controller.RemoteFilesAllowed() {
		m.mu.Lock()
		m.status = "请先连接对端并在“文件”页拖放（需对端授权文件传输）"
		m.mu.Unlock()
		m.w.Invalidate()
		return
	}
	go func() {
		var errs []string
		for _, p := range paths {
			if err := fm.EnqueueUpload(p, dir); err != nil {
				errs = append(errs, err.Error())
			}
		}
		m.mu.Lock()
		if len(errs) > 0 {
			m.transferErr = "部分拖放项未加入队列：" + strings.Join(errs, "；")
		} else {
			m.transferErr = ""
		}
		m.mu.Unlock()
		m.w.Invalidate()
	}()
}

func sortFiles(files []transfer.File) {
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Dir != files[j].Dir {
			return files[i].Dir
		}
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
}

func openLocalFolder(path string) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("explorer.exe", path)
	} else {
		cmd = exec.Command("xdg-open", path)
	}
	if cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}

func jobCounts(jobs []transfer.Job) (active, done, failed int) {
	for _, j := range jobs {
		switch j.State {
		case transfer.JobQueued, transfer.JobRunning:
			active++
		case transfer.JobDone:
			done++
		default:
			failed++
		}
	}
	return
}

func (u *UI) layoutFilesView(gtx layout.Context) layout.Dimensions {
	c := u.m.controller
	fu := &u.files
	fm := c.FileManager()

	_ = isEditorSubmitted(&u.fileSearchInput, gtx)

	u.m.mu.Lock()
	u.m.filesTabActive = true
	files := append([]transfer.File(nil), u.m.fileList...)
	remotePath := u.m.remotePath
	errText := u.m.transferErr
	sessionActive := u.m.sessionActive
	u.m.mu.Unlock()

	// --- Event handling -------------------------------------------------
	if u.btnFileRefresh.Clicked(gtx) {
		u.listRemote(remotePath)
	}
	if fu.btnUp.Clicked(gtx) && remotePath != "" {
		u.listRemote(transfer.ParentPath(remotePath))
	}
	crumbParts := []string{}
	if remotePath != "" {
		crumbParts = strings.Split(remotePath, "/")
	}
	for i := 0; i <= len(crumbParts) && i < len(fu.crumbs); i++ {
		if fu.crumbs[i].Clicked(gtx) {
			u.listRemote(strings.Join(crumbParts[:i], "/"))
		}
	}
	if u.btnFileUpload.Clicked(gtx) && fm != nil {
		go func() {
			path, err := transfer.ChooseOpenFile("选择要上传的文件")
			if err == nil && path != "" {
				if err = fm.EnqueueUpload(path, remotePath); err != nil {
					u.fileError("上传失败：", err)
				}
			}
		}()
	}
	if fu.btnUploadFolder.Clicked(gtx) && fm != nil {
		go func() {
			path, err := transfer.ChooseFolder("选择要上传的文件夹")
			if err == nil && path != "" {
				if err = fm.EnqueueUpload(path, remotePath); err != nil {
					u.fileError("上传文件夹失败：", err)
				}
			}
		}()
	}
	if fu.btnOpenLocal.Clicked(gtx) {
		openLocalFolder(c.ShareDir())
	}
	if u.btnFileCancel.Clicked(gtx) && fm != nil {
		go func() { _ = fm.Cancel() }()
	}
	if fu.btnCancelAll.Clicked(gtx) && fm != nil {
		go fm.CancelAll()
	}
	if u.btnFileRetry.Clicked(gtx) && fm != nil {
		go fm.RetryFailed()
	}
	if fu.btnClearDone.Clicked(gtx) && fm != nil {
		go fm.ClearFinished()
	}

	var jobs []transfer.Job
	if fm != nil {
		jobs = fm.Jobs()
	}

	return layout.Inset{Top: Dp(16), Bottom: Dp(16), Left: Dp(24), Right: Dp(24)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		if !sessionActive || !c.RemoteFilesAllowed() || fm == nil {
			return Card(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(Heading(u.theme, "文件管理")),
					layout.Rigid(VGap(8)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if !sessionActive {
							return Badge(u.theme, "未建立远程连接", ColTextMuted, ColTextMain)(gtx)
						}
						return Badge(u.theme, "对端未授权文件传输", ColWarning, ColPrimaryText)(gtx)
					}),
					layout.Rigid(VGap(16)),
					layout.Rigid(Caption(u.theme, "本机共享/下载目录：")),
					layout.Rigid(VGap(4)),
					layout.Rigid(Subheading(u.theme, c.ShareDir())),
					layout.Rigid(VGap(16)),
					layout.Rigid(Muted(u.theme, "连接对端并获得文件授权后，可在此浏览对端共享目录、下载文件或整个文件夹，也可把本机文件/文件夹直接拖进窗口上传。")),
				)
			})
		}

		narrow := gtx.Constraints.Max.X < gtx.Dp(Dp(960))
		browser := func(gtx layout.Context) layout.Dimensions {
			return u.layoutRemoteBrowser(gtx, fm, files, remotePath, crumbParts)
		}
		queue := func(gtx layout.Context) layout.Dimensions {
			return u.layoutJobQueue(gtx, jobs)
		}

		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			// Header & actions
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return Card(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(Heading(u.theme, "文件管理")),
						layout.Rigid(HGap(16)),
						layout.Rigid(PrimaryButton(u.theme, &u.btnFileUpload, "上传文件…")),
						layout.Rigid(HGap(8)),
						layout.Rigid(PrimaryButton(u.theme, &fu.btnUploadFolder, "上传文件夹…")),
						layout.Rigid(HGap(8)),
						layout.Rigid(SecondaryButton(u.theme, &u.btnFileRefresh, "刷新")),
						layout.Flexed(1, layout.Spacer{}.Layout),
						layout.Rigid(GhostButton(u.theme, &fu.btnOpenLocal, "打开本机下载目录")),
					)
				})
			}),
			layout.Rigid(VGap(8)),

			// Drop hint
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				target := "/" + remotePath
				return CardWithBorder(gtx, ColBorder, func(gtx layout.Context) layout.Dimensions {
					return layout.Center.Layout(gtx, Label(u.theme,
						"把文件或文件夹从资源管理器 / Dolphin 拖进此窗口，即上传到对端当前目录 "+target+"（单文件上限 100 MiB，同名自动改名，不覆盖）",
						13, ColTextSecondary))
				})
			}),
			layout.Rigid(VGap(8)),

			// Error banner
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if errText == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Bottom: Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return CardWithBorder(gtx, ColDanger, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(Badge(u.theme, "提示", ColDanger, ColTextMain)),
							layout.Rigid(HGap(12)),
							layout.Flexed(1, Label(u.theme, errText, 13, ColDanger)),
						)
					})
				})
			}),

			// Browser + queue
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				if narrow {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Flexed(3, browser),
						layout.Rigid(VGap(8)),
						layout.Flexed(2, queue),
					)
				}
				return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
					layout.Flexed(3, browser),
					layout.Rigid(HGap(12)),
					layout.Flexed(2, queue),
				)
			}),
		)
	})
}

func (u *UI) layoutRemoteBrowser(gtx layout.Context, fm *transfer.Manager, files []transfer.File, remotePath string, crumbParts []string) layout.Dimensions {
	fu := &u.files
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		// Breadcrumbs
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			children := []layout.FlexChild{
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if remotePath == "" {
						return DisabledButton(u.theme, "上一级")(gtx)
					}
					return SecondaryButton(u.theme, &fu.btnUp, "上一级")(gtx)
				}),
				layout.Rigid(HGap(8)),
				layout.Rigid(GhostButton(u.theme, &fu.crumbs[0], "共享根目录")),
			}
			for i, part := range crumbParts {
				if i+1 >= len(fu.crumbs) {
					break
				}
				children = append(children,
					layout.Rigid(Muted(u.theme, " / ")),
					layout.Rigid(GhostButton(u.theme, &fu.crumbs[i+1], part)),
				)
			}
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
		}),
		layout.Rigid(VGap(8)),
		layout.Rigid(TextField(u.theme, &u.fileSearchInput, "筛选当前目录…")),
		layout.Rigid(VGap(8)),

		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			q := strings.ToLower(strings.TrimSpace(u.fileSearchInput.Text()))
			idx := make([]int, 0, len(files))
			for i, f := range files {
				if q == "" || strings.Contains(strings.ToLower(f.Name), q) {
					idx = append(idx, i)
				}
			}
			if len(idx) == 0 {
				return Card(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Center.Layout(gtx, Muted(u.theme, "此目录为空或无匹配项。"))
				})
			}
			for len(u.fileDownloadBtns) < len(files) {
				u.fileDownloadBtns = append(u.fileDownloadBtns, widget.Clickable{})
			}
			for len(fu.openBtns) < len(files) {
				fu.openBtns = append(fu.openBtns, widget.Clickable{})
			}
			for len(fu.dirDownloadBtns) < len(files) {
				fu.dirDownloadBtns = append(fu.dirDownloadBtns, widget.Clickable{})
			}

			return material.List(u.theme, &u.filesList).Layout(gtx, len(idx), func(gtx layout.Context, k int) layout.Dimensions {
				i := idx[k]
				f := files[i]
				full := transfer.JoinPath(remotePath, f.Name)

				if f.Dir && fu.openBtns[i].Clicked(gtx) {
					u.listRemote(full)
				}
				if f.Dir && fu.dirDownloadBtns[i].Clicked(gtx) {
					go func() {
						if err := fm.EnqueueDownload(full, true); err != nil {
							u.fileError("下载文件夹失败：", err)
						}
					}()
				}
				if !f.Dir && u.fileDownloadBtns[i].Clicked(gtx) {
					go func() {
						if err := fm.EnqueueDownload(full, false); err != nil {
							u.fileError("下载失败：", err)
						}
					}()
				}

				return layout.Inset{Bottom: Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return Card(gtx, func(gtx layout.Context) layout.Dimensions {
						info := fmt.Sprintf("%s · %s", formatBytes(f.Size), f.Modified.Local().Format("2006-01-02 15:04"))
						kind := Badge(u.theme, "文件", ColSurfaceLight, ColTextSecondary)
						if f.Dir {
							info = "文件夹 · " + f.Modified.Local().Format("2006-01-02 15:04")
							kind = Badge(u.theme, "文件夹", ColSurfaceLight, ColPrimary)
						}
						actions := []layout.FlexChild{}
						if f.Dir {
							actions = append(actions,
								layout.Rigid(SecondaryButton(u.theme, &fu.openBtns[i], "打开")),
								layout.Rigid(HGap(8)),
								layout.Rigid(PrimaryButton(u.theme, &fu.dirDownloadBtns[i], "下载文件夹")),
							)
						} else {
							actions = append(actions, layout.Rigid(PrimaryButton(u.theme, &u.fileDownloadBtns[i], "下载")))
						}
						row := []layout.FlexChild{
							layout.Rigid(kind),
							layout.Rigid(HGap(10)),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(Subheading(u.theme, f.Name)),
									layout.Rigid(VGap(2)),
									layout.Rigid(Muted(u.theme, info)),
								)
							}),
						}
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, append(row, actions...)...)
					})
				})
			})
		}),
	)
}

func jobStateBadge(th *material.Theme, j transfer.Job) layout.Widget {
	switch j.State {
	case transfer.JobQueued:
		return Badge(th, "等待", ColSurfaceLight, ColTextSecondary)
	case transfer.JobRunning:
		return Badge(th, "进行中", ColPrimary, ColPrimaryText)
	case transfer.JobDone:
		return Badge(th, "完成", ColSurfaceLight, ColSuccess)
	case transfer.JobCanceled:
		return Badge(th, "已取消", ColSurfaceLight, ColTextMuted)
	default:
		return Badge(th, "失败", ColDanger, ColTextMain)
	}
}

func (u *UI) layoutJobQueue(gtx layout.Context, jobs []transfer.Job) layout.Dimensions {
	fu := &u.files
	fu.jobsList.Axis = layout.Vertical
	active, done, failed := jobCounts(jobs)
	return Card(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(Subheading(u.theme, "传输队列")),
					layout.Rigid(HGap(10)),
					layout.Flexed(1, Muted(u.theme, fmt.Sprintf("进行/等待 %d · 完成 %d · 失败/取消 %d", active, done, failed))),
				)
			}),
			layout.Rigid(VGap(8)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(GhostButton(u.theme, &u.btnFileCancel, "取消当前")),
					layout.Rigid(HGap(6)),
					layout.Rigid(GhostButton(u.theme, &fu.btnCancelAll, "全部取消")),
					layout.Rigid(HGap(6)),
					layout.Rigid(GhostButton(u.theme, &u.btnFileRetry, "重试失败")),
					layout.Rigid(HGap(6)),
					layout.Rigid(GhostButton(u.theme, &fu.btnClearDone, "清除已结束")),
				)
			}),
			layout.Rigid(VGap(8)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				if len(jobs) == 0 {
					return layout.Center.Layout(gtx, Muted(u.theme, "暂无传输任务。"))
				}
				return material.List(u.theme, &fu.jobsList).Layout(gtx, len(jobs), func(gtx layout.Context, i int) layout.Dimensions {
					j := jobs[i]
					arrow := "↑ "
					switch j.Kind {
					case transfer.JobDownload:
						arrow = "↓ "
					case transfer.JobMkdir:
						arrow = "+ "
					}
					name := arrow + "/" + j.Remote
					detail := ""
					switch {
					case j.Kind == transfer.JobMkdir:
						detail = "创建目录"
					case j.Size > 0:
						detail = fmt.Sprintf("%s / %s", formatBytes(j.Done), formatBytes(j.Size))
					}
					if j.Saved != "" {
						detail += " · 已另存为 " + j.Saved
					}
					return layout.Inset{Bottom: Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(jobStateBadge(u.theme, j)),
									layout.Rigid(HGap(8)),
									layout.Flexed(1, Label(u.theme, name, 13, ColTextMain)),
								)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								if j.State != transfer.JobRunning || j.Size <= 0 {
									return layout.Dimensions{}
								}
								return layout.Inset{Top: Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									pb := material.ProgressBar(u.theme, float32(j.Done)/float32(j.Size))
									pb.Color = ColPrimary
									pb.TrackColor = ColSurfaceLight
									return pb.Layout(gtx)
								})
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								text, col := detail, ColTextMuted
								if j.Error != "" && j.State != transfer.JobDone {
									text, col = j.Error, ColDanger
								}
								if text == "" {
									return layout.Dimensions{}
								}
								return layout.Inset{Top: Dp(2)}.Layout(gtx, Label(u.theme, text, 12, col))
							}),
						)
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
