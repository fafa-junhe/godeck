//go:build cgo && windows && amd64 && !godeck_preview

package media

/*
#cgo pkg-config: gstreamer-1.0 gstreamer-app-1.0 gstreamer-video-1.0
#include "bridge.h"
*/
import "C"
import (
	"context"
	"errors"
	"time"
	"unsafe"
)

type Pipeline struct {
	ptr *C.godeck_media
}

type Sample struct {
	Data      []byte
	Timestamp time.Duration
	Width     int
	Height    int
}

type Codec int

const (
	Software Codec = iota
	VA
	LegacyVA
	Auto
	HardwareRequired
	FFmpegVA
)

const IsPreview = false

func NewEncoderWithProfile(width, height, fps, level int, codec Codec) (*Pipeline, error) {
	if codec < Software || codec > LegacyVA {
		return nil, errors.New("不支持的编码器")
	}
	var e *C.char
	p := C.godeck_encoder_with_profile(C.int(width), C.int(height), C.int(fps), C.int(level), C.int(codec), &e)
	if p == nil {
		return nil, readError(e)
	}
	return &Pipeline{p}, nil
}

func NewEncoder(fd int, node uint32, codec Codec) (*Pipeline, error) {
	return NewEncoderWithProfile(1280, 720, 30, 31, codec)
}

func NewDecoder(codec Codec) (*Pipeline, error) {
	if codec < Software || codec > LegacyVA {
		return nil, errors.New("不支持的解码器")
	}
	var e *C.char
	p := C.godeck_decoder(C.int(codec), &e)
	if p == nil {
		return nil, readError(e)
	}
	return &Pipeline{p}, nil
}

func readError(e *C.char) error {
	if e == nil {
		return errors.New("媒体管线不可用")
	}
	defer C.godeck_free_error(e)
	return errors.New(C.GoString(e))
}

func (p *Pipeline) Pull(ctx context.Context) (Sample, error) {
	for {
		select {
		case <-ctx.Done():
			return Sample{}, ctx.Err()
		default:
		}
		var data *C.uchar
		var size C.size_t
		var ts C.uint64_t
		var w, h C.int
		v := C.godeck_pull(p.ptr, &data, &size, &ts, &w, &h)
		if v < 0 {
			return Sample{}, errors.New("媒体管线已停止")
		}
		if v == 0 {
			continue
		}
		if size > 64<<20 {
			C.godeck_free_buffer(unsafe.Pointer(data))
			return Sample{}, errors.New("媒体帧过大")
		}
		b := C.GoBytes(unsafe.Pointer(data), C.int(size))
		C.godeck_free_buffer(unsafe.Pointer(data))
		return Sample{Data: b, Timestamp: time.Duration(ts), Width: int(w), Height: int(h)}, nil
	}
}

func (p *Pipeline) Push(data []byte, pts time.Duration) error {
	if len(data) == 0 || len(data) > 32<<20 {
		return errors.New("无效的媒体帧")
	}
	if C.godeck_push(p.ptr, (*C.uchar)(unsafe.Pointer(&data[0])), C.size_t(len(data)), C.uint64_t(pts)) != 0 {
		return errors.New("解码管线已停止")
	}
	return nil
}

func (p *Pipeline) Interrupt() {
	if p.ptr != nil {
		C.godeck_interrupt(p.ptr)
	}
}

func (p *Pipeline) ForceKey() {
	if p.ptr != nil {
		C.godeck_force_key(p.ptr)
	}
}

func (p *Pipeline) Close() {
	if p.ptr != nil {
		C.godeck_stop(p.ptr)
		p.ptr = nil
	}
}

func (c Codec) String() string {
	switch c {
	case VA:
		return "硬件 · Media Foundation / D3D11"
	case Software:
		return "软件 · x264 / libav"
	case HardwareRequired:
		return "要求硬件"
	default:
		return "自动"
	}
}
