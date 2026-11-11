// SPDX-License-Identifier: Unlicense OR MIT

// GoDeck local patch: WM_DROPFILES support.

package app

import (
	"syscall"
	"unsafe"
)

var (
	dropShell32                    = syscall.NewLazyDLL("shell32.dll")
	dropUser32                     = syscall.NewLazyDLL("user32.dll")
	procDragAcceptFiles            = dropShell32.NewProc("DragAcceptFiles")
	procDragQueryFileW             = dropShell32.NewProc("DragQueryFileW")
	procDragFinish                 = dropShell32.NewProc("DragFinish")
	procChangeWindowMessageFilterEx = dropUser32.NewProc("ChangeWindowMessageFilterEx")
)

const (
	wmDropFiles   = 0x0233
	wmCopyData    = 0x004A
	wmCopyGlobal  = 0x0049
	msgfltAllow   = 1
)

// enableFileDrop lets Explorer drop files on hwnd, also when GoDeck runs
// elevated (UIPI would otherwise filter the drop messages).
func enableFileDrop(hwnd uintptr) {
	for _, m := range []uintptr{wmDropFiles, wmCopyData, wmCopyGlobal} {
		procChangeWindowMessageFilterEx.Call(hwnd, m, msgfltAllow, 0)
	}
	procDragAcceptFiles.Call(hwnd, 1)
}

// dropPaths extracts the dropped paths and releases the HDROP.
func dropPaths(hdrop uintptr) []string {
	defer procDragFinish.Call(hdrop)
	count, _, _ := procDragQueryFileW.Call(hdrop, 0xFFFFFFFF, 0, 0)
	paths := make([]string, 0, count)
	for i := uintptr(0); i < count; i++ {
		n, _, _ := procDragQueryFileW.Call(hdrop, i, 0, 0)
		if n == 0 {
			continue
		}
		buf := make([]uint16, n+1)
		procDragQueryFileW.Call(hdrop, i, uintptr(unsafe.Pointer(&buf[0])), n+1)
		paths = append(paths, syscall.UTF16ToString(buf))
	}
	return paths
}
