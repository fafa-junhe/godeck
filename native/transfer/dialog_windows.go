//go:build windows

package transfer

import (
	"errors"
	"runtime"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	comdlg32        = syscall.NewLazyDLL("comdlg32.dll")
	getOpenFileName = comdlg32.NewProc("GetOpenFileNameW")
	getSaveFileName = comdlg32.NewProc("GetSaveFileNameW")
)

type openFileNameW struct {
	structSize      uint32
	hwndOwner       uintptr
	instance        uintptr
	filter          *uint16
	customFilter    *uint16
	maxCustFilter   uint32
	filterIndex     uint32
	file            *uint16
	maxFile         uint32
	fileTitle       *uint16
	maxFileTitle    uint32
	initialDir      *uint16
	title           *uint16
	flags           uint32
	fileOffset      uint16
	fileExtension   uint16
	defExt          *uint16
	custData        uintptr
	fnHook          uintptr
	templateName    *uint16
	pvReserved      uintptr
	dwReserved      uint32
	flagsEx         uint32
}

const (
	ofnFileMustExist   = 0x00001000
	ofnPathMustExist   = 0x00000800
	ofnOverwritePrompt = 0x00000002
	ofnNoChangeDir     = 0x00000008
)

// ChooseOpenFile opens a native Windows file open dialog.
func ChooseOpenFile(title string) (string, error) {
	buf := make([]uint16, 1024)
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	filterSlice := utf16.Encode([]rune("所有文件 (*.*)\x00*.*\x00\x00"))

	var ofn openFileNameW
	ofn.structSize = uint32(unsafe.Sizeof(ofn))
	ofn.title = titlePtr
	ofn.filter = &filterSlice[0]
	ofn.file = &buf[0]
	ofn.maxFile = uint32(len(buf))
	ofn.flags = ofnFileMustExist | ofnPathMustExist | ofnNoChangeDir

	r, _, _ := getOpenFileName.Call(uintptr(unsafe.Pointer(&ofn)))
	runtime.KeepAlive(titlePtr)
	runtime.KeepAlive(filterSlice)
	runtime.KeepAlive(buf)
	if r == 0 {
		return "", errors.New("已取消选择文件")
	}
	return syscall.UTF16ToString(buf), nil
}

// ChooseSaveFile opens a native Windows save file dialog.
func ChooseSaveFile(title, defaultName string) (string, error) {
	buf := make([]uint16, 1024)
	if len(defaultName) > 0 {
		defSlice, _ := syscall.UTF16FromString(defaultName)
		copy(buf, defSlice)
	}
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	filterSlice := utf16.Encode([]rune("所有文件 (*.*)\x00*.*\x00\x00"))

	var ofn openFileNameW
	ofn.structSize = uint32(unsafe.Sizeof(ofn))
	ofn.title = titlePtr
	ofn.filter = &filterSlice[0]
	ofn.file = &buf[0]
	ofn.maxFile = uint32(len(buf))
	ofn.flags = ofnOverwritePrompt | ofnPathMustExist | ofnNoChangeDir

	r, _, _ := getSaveFileName.Call(uintptr(unsafe.Pointer(&ofn)))
	runtime.KeepAlive(titlePtr)
	runtime.KeepAlive(filterSlice)
	runtime.KeepAlive(buf)
	if r == 0 {
		return "", errors.New("已取消保存路径选择")
	}
	return syscall.UTF16ToString(buf), nil
}
