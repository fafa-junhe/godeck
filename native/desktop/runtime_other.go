//go:build !windows

package desktop

import "sync"

var runtimeOnce sync.Once

// initRuntimeEnv 在 Linux 等非 Windows 系统上保持透明。
// 直接使用宿主系统的 PipeWire / GStreamer / FFmpeg / libva 组件，
// 不改变系统 PATH，也不覆盖 GST_PLUGIN_* 环境变量，避免破坏用户系统的插件链。
func initRuntimeEnv() error {
	runtimeOnce.Do(func() {
		// No-op on Linux / non-Windows.
	})
	return nil
}
