//go:build windows && (amd64 || arm64)

// Package wininput injects native Windows events after per-session approval.
package wininput

import (
	"errors"
	"math"
	"syscall"
	"unsafe"
)

var user32 = syscall.NewLazyDLL("user32.dll")
var sendInput = user32.NewProc("SendInput")
var systemMetric = user32.NewProc("GetSystemMetrics")

const (
	inputMouse          = 0
	inputKeyboard       = 1
	mouseMove           = 0x0001
	mouseAbsolute       = 0x8000
	mouseVirtualDesktop = 0x4000
	keyExtended         = 0x0001
	keyUp               = 0x0002
)

type mouseInput struct {
	Dx        int32
	Dy        int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	Extra     uintptr
}

type keyInput struct {
	VK    uint16
	Scan  uint16
	Flags uint32
	Time  uint32
	Extra uintptr
}

// INPUT uses a 32-byte union aligned to pointer width on Windows amd64/arm64.
type nativeInput struct {
	Type uint32
	_    uint32
	Data [32]byte
}

func send(i *nativeInput) error {
	r, _, e := sendInput.Call(1, uintptr(unsafe.Pointer(i)), unsafe.Sizeof(*i))
	if r != 1 {
		if e != syscall.Errno(0) {
			return e
		}
		return errors.New("Windows 未接受输入事件；请检查目标窗口和权限")
	}
	return nil
}

func metric(index int32) int32 {
	v, _, _ := systemMetric.Call(uintptr(index))
	return int32(v)
}

// PrimaryDimensions returns the primary monitor resolution in pixels.
func PrimaryDimensions() (int, int) {
	w := int(metric(0))
	h := int(metric(1))
	if w <= 0 {
		w = 1920
	}
	if h <= 0 {
		h = 1080
	}
	return w, h
}

// Move accepts normalized coordinates over the Windows virtual desktop.
func Move(x, y float64) error {
	if math.IsNaN(x) || math.IsNaN(y) || x < 0 || x > 1 || y < 0 || y > 1 {
		return errors.New("鼠标坐标无效")
	}
	if metric(78) < 1 || metric(79) < 1 {
		return errors.New("虚拟桌面不可用")
	}
	var in nativeInput
	in.Type = inputMouse
	m := (*mouseInput)(unsafe.Pointer(&in.Data[0]))
	m.Dx = int32(x * 65535)
	m.Dy = int32(y * 65535)
	m.Flags = mouseMove | mouseAbsolute | mouseVirtualDesktop
	return send(&in)
}

func Button(button int, down bool) error {
	var flags uint32
	switch button {
	case 0: // Left
		if down {
			flags = 0x0002 // MOUSEEVENTF_LEFTDOWN
		} else {
			flags = 0x0004 // MOUSEEVENTF_LEFTUP
		}
	case 1: // Right
		if down {
			flags = 0x0008 // MOUSEEVENTF_RIGHTDOWN
		} else {
			flags = 0x0010 // MOUSEEVENTF_RIGHTUP
		}
	case 2: // Middle
		if down {
			flags = 0x0020 // MOUSEEVENTF_MIDDLEDOWN
		} else {
			flags = 0x0040 // MOUSEEVENTF_MIDDLEUP
		}
	default:
		return errors.New("鼠标按钮无效")
	}
	var in nativeInput
	in.Type = inputMouse
	m := (*mouseInput)(unsafe.Pointer(&in.Data[0]))
	m.Flags = flags
	return send(&in)
}

func Wheel(delta int32) error {
	if delta == 0 || delta > 1200 || delta < -1200 {
		return errors.New("滚轮参数无效")
	}
	var in nativeInput
	in.Type = inputMouse
	m := (*mouseInput)(unsafe.Pointer(&in.Data[0]))
	m.Flags = 0x0800
	m.MouseData = uint32(delta)
	return send(&in)
}

func isExtendedKey(vk uint16) bool {
	switch vk {
	case 33, 34, 35, 36, 37, 38, 39, 40, 45, 46, // PageUp, PageDown, End, Home, Left, Up, Right, Down, Insert, Delete
		91, 92, 93: // Left Win, Right Win, Apps
		return true
	default:
		return false
	}
}

func Key(vk uint16, down bool) error {
	if vk == 0 {
		return errors.New("按键无效")
	}
	var in nativeInput
	in.Type = inputKeyboard
	k := (*keyInput)(unsafe.Pointer(&in.Data[0]))
	k.VK = vk
	if isExtendedKey(vk) {
		k.Flags |= keyExtended
	}
	if !down {
		k.Flags |= keyUp
	}
	return send(&in)
}

// MovePrimary targets the primary monitor captured by d3d11screencapturesrc.
func MovePrimary(x, y float64) error {
	if math.IsNaN(x) || math.IsNaN(y) || x < 0 || x > 1 || y < 0 || y > 1 {
		return errors.New("鼠标坐标无效")
	}
	if metric(0) < 1 || metric(1) < 1 {
		return errors.New("主显示器不可用")
	}
	var in nativeInput
	in.Type = inputMouse
	m := (*mouseInput)(unsafe.Pointer(&in.Data[0]))
	m.Dx = int32(x * 65535)
	m.Dy = int32(y * 65535)
	m.Flags = mouseMove | mouseAbsolute
	return send(&in)
}
