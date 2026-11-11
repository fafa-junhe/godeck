//go:build windows

package transfer

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	shell32                  = syscall.NewLazyDLL("shell32.dll")
	ole32                    = syscall.NewLazyDLL("ole32.dll")
	procSHBrowseForFolder    = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDList  = shell32.NewProc("SHGetPathFromIDListW")
	procCoInitializeEx       = ole32.NewProc("CoInitializeEx")
	procCoUninitialize       = ole32.NewProc("CoUninitialize")
	procCoTaskMemFree        = ole32.NewProc("CoTaskMemFree")
)

type browseInfoW struct {
	owner       uintptr
	root        uintptr
	displayName *uint16
	title       *uint16
	flags       uint32
	callback    uintptr
	lparam      uintptr
	image       int32
}

const (
	bifReturnOnlyFSDirs = 0x00000001
	bifNewDialogStyle   = 0x00000040
	bifNoNewFolderBtn   = 0x00000200
	coinitApartment     = 0x2
)

// ChooseFolder opens the native Windows folder picker.
func ChooseFolder(title string) (string, error) {
	// The new-style dialog needs a single-threaded COM apartment on this thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, _ := procCoInitializeEx.Call(0, coinitApartment); int32(r) >= 0 {
		defer procCoUninitialize.Call()
	}

	display := make([]uint16, syscall.MAX_PATH)
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	bi := browseInfoW{
		displayName: &display[0],
		title:       titlePtr,
		flags:       bifReturnOnlyFSDirs | bifNewDialogStyle | bifNoNewFolderBtn,
	}
	pidl, _, _ := procSHBrowseForFolder.Call(uintptr(unsafe.Pointer(&bi)))
	runtime.KeepAlive(titlePtr)
	runtime.KeepAlive(display)
	if pidl == 0 {
		return "", errors.New("已取消选择文件夹")
	}
	defer procCoTaskMemFree.Call(pidl)
	buf := make([]uint16, 32768)
	if r, _, _ := procSHGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&buf[0]))); r == 0 {
		return "", errors.New("所选位置不是文件系统目录")
	}
	return syscall.UTF16ToString(buf), nil
}
