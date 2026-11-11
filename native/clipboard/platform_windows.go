//go:build windows

package clipboard

import (
	"context"
	"errors"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procEmptyClipboard             = user32.NewProc("EmptyClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procSetClipboardData           = user32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
	procGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	procGlobalFree                 = kernel32.NewProc("GlobalFree")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procGlobalSize                 = kernel32.NewProc("GlobalSize")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

type winClipboard struct{}

// System returns the Win32 clipboard. Change detection polls
// GetClipboardSequenceNumber, which needs no window and works unfocused.
func System() Platform { return winClipboard{} }

func sequence() uintptr {
	r, _, _ := procGetClipboardSequenceNumber.Call()
	return r
}

func openClipboard() error {
	for i := 0; i < 10; i++ {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("剪贴板被其他程序占用")
}

func readText() (string, bool) {
	if r, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); r == 0 {
		return "", false
	}
	if openClipboard() != nil {
		return "", false
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", false
	}
	size, _, _ := procGlobalSize.Call(h)
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", false
	}
	defer procGlobalUnlock.Call(h)
	n := int(size / 2)
	if n > MaxTextBytes {
		n = MaxTextBytes + 1 // let the caller reject oversize content
	}
	units := unsafe.Slice((*uint16)(unsafe.Pointer(p)), n)
	for i, u := range units {
		if u == 0 {
			units = units[:i]
			break
		}
	}
	return string(utf16.Decode(units)), true
}

func (winClipboard) Watch(ctx context.Context, onChange func(string)) error {
	last := sequence()
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			cur := sequence()
			if cur == last {
				continue
			}
			last = cur
			if text, ok := readText(); ok {
				onChange(text)
			}
		}
	}
}

func (winClipboard) Write(text string) error {
	units := utf16.Encode([]rune(text))
	units = append(units, 0)
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(units)*2))
	if h == 0 {
		return errors.New("内存分配失败")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errors.New("内存锁定失败")
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(units)), units)
	procGlobalUnlock.Call(h)

	if err := openClipboard(); err != nil {
		procGlobalFree.Call(h)
		return err
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h) // ownership transfers only on success
		return errors.New("SetClipboardData 失败")
	}
	return nil
}
