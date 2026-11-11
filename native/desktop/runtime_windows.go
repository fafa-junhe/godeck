//go:build windows

package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var runtimeOnce sync.Once
var runtimeErr error

// initRuntimeEnv 在 Windows 上按自包含便携包方式初始化 GStreamer 运行环境。
// 仅执行一次，避免重复配置；若发生致命错误，返回明确错误信息。
func initRuntimeEnv() error {
	runtimeOnce.Do(func() {
		runtimeErr = setupWindowsRuntime()
	})
	return runtimeErr
}

func setupWindowsRuntime() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("无法获取宿主可执行文件绝对路径: %w", err)
	}
	exeDir := filepath.Dir(exe)

	// 1. GStreamer 插件目录 (GST_PLUGIN_PATH_1_0 与 GST_PLUGIN_SYSTEM_PATH_1_0)
	// 设置 SYSTEM_PATH 避免扫描宿主未知路径造成符号冲突
	pluginDir := filepath.Join(exeDir, "gstreamer-1.0")
	if fi, err := os.Stat(pluginDir); err == nil && fi.IsDir() {
		_ = os.Setenv("GST_PLUGIN_PATH_1_0", pluginDir)
		_ = os.Setenv("GST_PLUGIN_SYSTEM_PATH_1_0", pluginDir)
	}

	// 2. GStreamer 插件扫描器 (GST_PLUGIN_SCANNER_1_0 与 GST_PLUGIN_SCANNER)
	// 优先使用同级根目录 gst-plugin-scanner.exe（与其依赖 DLL 位于同一应用目录），
	// 次选标准子目录 libexec/gstreamer-1.0/gst-plugin-scanner.exe。
	scannerCandidates := []string{
		filepath.Join(exeDir, "gst-plugin-scanner.exe"),
		filepath.Join(exeDir, "libexec", "gstreamer-1.0", "gst-plugin-scanner.exe"),
	}
	var foundScanner string
	for _, cand := range scannerCandidates {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			foundScanner = cand
			break
		}
	}
	if foundScanner != "" {
		_ = os.Setenv("GST_PLUGIN_SCANNER_1_0", foundScanner)
		_ = os.Setenv("GST_PLUGIN_SCANNER", foundScanner)
	}

	// 3. 用户可写缓存目录 (GST_REGISTRY_1_0 与 GST_REGISTRY)
	// 便携包可能放置于只读介质或无管理员写入权限目录中，
	// 强制将 GStreamer 插件缓存导向当前用户的标准缓存目录 (%LocalAppData%\godeck\gstreamer-1.0\registry.bin)。
	cacheDir, err := os.UserCacheDir()
	if err == nil && cacheDir != "" {
		regDir := filepath.Join(cacheDir, "godeck", "gstreamer-1.0")
		if err := os.MkdirAll(regDir, 0755); err == nil {
			regFile := filepath.Join(regDir, "registry.bin")
			_ = os.Setenv("GST_REGISTRY_1_0", regFile)
			_ = os.Setenv("GST_REGISTRY", regFile)
		}
	}

	// 4. 将应用目录加入当前进程 PATH，确保外部插件扫描器子进程能直接解析同级 DLL
	curPath := os.Getenv("PATH")
	if !strings.Contains(curPath, exeDir) {
		_ = os.Setenv("PATH", exeDir+string(os.PathListSeparator)+curPath)
	}

	return nil
}
