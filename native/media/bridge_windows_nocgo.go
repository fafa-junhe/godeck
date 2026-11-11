//go:build godeck_preview && windows && amd64

package media

import (
	"context"
	"errors"
	"time"
)

const IsPreview = true

type Pipeline struct{}

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

var errNoCgoMedia = errors.New("当前为无媒体 UI 预览版（godeck_preview）；远程画面捕获与硬件编解码不可用")

func NewEncoderWithProfile(width, height, fps, level int, codec Codec) (*Pipeline, error) {
	return nil, errNoCgoMedia
}

func NewEncoder(fd int, node uint32, codec Codec) (*Pipeline, error) {
	return nil, errNoCgoMedia
}

func NewDecoder(codec Codec) (*Pipeline, error) {
	return nil, errNoCgoMedia
}

func (p *Pipeline) Pull(ctx context.Context) (Sample, error) {
	<-ctx.Done()
	return Sample{}, ctx.Err()
}

func (p *Pipeline) Push(data []byte, pts time.Duration) error {
	return errNoCgoMedia
}

func (p *Pipeline) Interrupt() {}

func (p *Pipeline) ForceKey() {}

func (p *Pipeline) Close() {}

func (p *Pipeline) DevicePath() string {
	return ""
}

func (c Codec) String() string {
	return "无媒体预览版（不可用）"
}
