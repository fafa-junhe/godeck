// Package main implements the GoDeck Legacy Web Compatibility Mode.
// NOTICE: This is only a fallback compatibility server using browser HTTP polling.
// The formal native client is located at cmd/godeck (or root main.go).
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

//go:embed web/*
var assets embed.FS

const maxUpload = 100 << 20

var token string
var share string
var control atomic.Bool
var started = time.Now()
var waylandSingle bool
var captureBackend string
var captureGate = make(chan struct{}, 1)
var screenSize struct {
	sync.RWMutex
	width, height int
}

type inputEvent struct {
	Type   string  `json:"type"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Button int     `json:"button"`
	Key    string  `json:"key"`
	Code   string  `json:"code"`
	Delta  int     `json:"delta"`
}

func main() {
	addr := flag.String("listen", "127.0.0.1:8765", "监听地址；局域网访问可使用 0.0.0.0:8765")
	dir := flag.String("share", "./share", "文件共享目录")
	allow := flag.Bool("control", false, "允许远程输入")
	cert := flag.String("tls-cert", "", "TLS 证书路径")
	key := flag.String("tls-key", "", "TLS 私钥路径")
	flag.StringVar(&captureBackend, "capture-backend", "auto", "截图后端：auto、spectacle 或 grim（仅 Wayland）")
	flag.BoolVar(&waylandSingle, "wayland-single-output", false, "Wayland 单显示器 100% 缩放、平面指针加速度下启用鼠标定位")
	flag.Parse()

	if captureBackend != "auto" && captureBackend != "spectacle" && captureBackend != "grim" {
		log.Fatal("截图后端必须是 auto、spectacle 或 grim")
	}
	if (*cert == "") != (*key == "") {
		log.Fatal("TLS 证书和私钥必须同时指定")
	}
	var err error
	if err = os.MkdirAll(*dir, 0700); err != nil {
		log.Fatal(err)
	}
	share, err = filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}
	share, err = filepath.EvalSymlinks(share)
	if err != nil {
		log.Fatal(err)
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		log.Fatal(err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	control.Store(*allow)

	mux := http.NewServeMux()
	web, _ := fs.Sub(assets, "web")
	mux.Handle("/", http.FileServer(http.FS(web)))
	mux.HandleFunc("/api/status", auth(status))
	mux.HandleFunc("/api/screen", auth(screen))
	mux.HandleFunc("/api/input", auth(input))
	mux.HandleFunc("/api/files", auth(files))
	mux.HandleFunc("/api/file", auth(file))

	server := &http.Server{
		Addr:              *addr,
		Handler:           security(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	fmt.Printf("[旧版兼容模式] GoDeck Web 兼容服务已启动\n")
	fmt.Printf("提示：此为基于浏览器的老旧兼容模式，正式原生桌面客户端请直接启动 godeck 或 GoDeck.exe\n")
	fmt.Printf("监听地址: %s\n访问密钥（仅显示一次）: %s\n共享目录: %s\n远程控制: %t\n", *addr, token, share, *allow)

	if *cert != "" {
		err = server.ListenAndServeTLS(*cert, *key)
	} else {
		fmt.Println("当前使用 HTTP，请仅在可信网络或 VPN 中使用。")
		err = server.ListenAndServe()
	}
	log.Fatal(err)
}

func security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob:; style-src 'self'; script-src 'self'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := r.Header.Get("X-GoDeck-Key")
		if len(t) != len(token) || subtle.ConstantTimeCompare([]byte(t), []byte(token)) != 1 {
			http.Error(w, "访问密钥无效", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func require(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		w.Header().Set("Allow", method)
		http.Error(w, "方法不允许", 405)
		return false
	}
	return true
}

func status(w http.ResponseWriter, r *http.Request) {
	if !require(w, r, "GET") {
		return
	}
	host, _ := os.Hostname()
	jsonOut(w, map[string]any{
		"hostname":        host,
		"os":              runtime.GOOS,
		"arch":            runtime.GOARCH,
		"control":         control.Load(),
		"uptime":          int(time.Since(started).Seconds()),
		"share":           filepath.Base(share),
		"platformNote":    platformNote(),
		"inputCapability": inputCapability(),
		"desktop":         desktopName(),
		"captureBackend":  selectedCaptureBackend(),
		"inputBackend":    inputBackend(),
		"inputReady":      inputReady(),
		"capabilities":    capabilities(),
		"maxUploadBytes":  maxUpload,
	})
}

func platformNote() string {
	switch runtime.GOOS {
	case "windows":
		return "需要已登录的交互式桌面；锁屏或无桌面会影响捕获。"
	case "darwin":
		return "需要为终端授予屏幕录制权限；此版本不支持 macOS 远程输入。"
	case "linux":
		return "Wayland 鼠标定位需单显示器、100% 缩放、无旋转，并关闭虚拟指针加速。截图和控制分别配置。"
	}
	return "当前系统没有受支持的桌面捕获方式。"
}

func screen(w http.ResponseWriter, r *http.Request) {
	if !require(w, r, "GET") {
		return
	}
	data, err := capture()
	if err != nil {
		http.Error(w, "无法获取桌面画面："+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(data)
}

func capture() ([]byte, error) {
	switch runtime.GOOS {
	case "windows":
		script := `Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing; $b=[System.Windows.Forms.SystemInformation]::VirtualScreen; $i=New-Object System.Drawing.Bitmap $b.Width,$b.Height; $g=[System.Drawing.Graphics]::FromImage($i); $g.CopyFromScreen($b.X,$b.Y,0,0,$b.Size); $m=New-Object System.IO.MemoryStream; $i.Save($m,[System.Drawing.Imaging.ImageFormat]::Png); [Console]::Out.Write([Convert]::ToBase64String($m.ToArray())); $g.Dispose(); $i.Dispose(); $m.Dispose()`
		out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Output()
		if err != nil {
			return nil, errors.New("请确认已登录桌面，并允许屏幕捕获")
		}
		return base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	case "darwin":
		f, err := os.CreateTemp("", "godeck-*.png")
		if err != nil {
			return nil, err
		}
		name := f.Name()
		f.Close()
		defer os.Remove(name)
		if err = exec.Command("screencapture", "-x", name).Run(); err != nil {
			return nil, errors.New("请检查屏幕录制权限")
		}
		return os.ReadFile(name)
	case "linux":
		return captureLinux()
	default:
		return nil, errors.New("不支持当前系统")
	}
}

func input(w http.ResponseWriter, r *http.Request) {
	if !require(w, r, "POST") {
		return
	}
	if !control.Load() {
		http.Error(w, "此设备以只读模式运行", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var e inputEvent
	if json.NewDecoder(r.Body).Decode(&e) != nil {
		http.Error(w, "输入数据无效", 400)
		return
	}
	if e.X < 0 || e.X > 1 || e.Y < 0 || e.Y > 1 {
		http.Error(w, "坐标无效", 400)
		return
	}
	if err := sendInput(e); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.WriteHeader(204)
}

func sendInput(e inputEvent) error {
	if runtime.GOOS == "windows" {
		return windowsInput(e)
	}
	if runtime.GOOS == "linux" && os.Getenv("WAYLAND_DISPLAY") != "" {
		return waylandInput(e)
	}
	if runtime.GOOS != "linux" {
		return errors.New("当前桌面环境不支持远程输入")
	}
	var args []string
	switch e.Type {
	case "move":
		out, err := exec.Command("xdotool", "getdisplaygeometry").Output()
		if err != nil {
			return errors.New("无法获取 X11 屏幕尺寸")
		}
		fields := strings.Fields(string(out))
		if len(fields) != 2 {
			return errors.New("无法获取 X11 屏幕尺寸")
		}
		width, err1 := strconv.Atoi(fields[0])
		height, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil || width < 1 || height < 1 {
			return errors.New("无法获取 X11 屏幕尺寸")
		}
		args = []string{"mousemove", strconv.Itoa(int(e.X * float64(width-1))), strconv.Itoa(int(e.Y * float64(height-1)))}
	case "down", "up":
		if e.Button < 0 || e.Button > 2 {
			return errors.New("鼠标按钮无效")
		}
		button := []string{"1", "2", "3"}[e.Button]
		action := "mousedown"
		if e.Type == "up" {
			action = "mouseup"
		}
		args = []string{action, button}
	case "keydown", "keyup":
		k, ok := linuxKey(e.Key)
		if !ok {
			return errors.New("不支持该按键")
		}
		action := "keydown"
		if e.Type == "keyup" {
			action = "keyup"
		}
		args = []string{action, k}
	case "wheel":
		button := "5"
		if e.Delta < 0 {
			button = "4"
		}
		args = []string{"click", button}
	default:
		return errors.New("不支持的输入事件")
	}
	if err := exec.Command("xdotool", args...).Run(); err != nil {
		return errors.New("请确认已安装 xdotool 并启用 X11")
	}
	return nil
}

func linuxKey(k string) (string, bool) {
	if len(k) == 1 && strings.Contains("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", k) {
		return k, true
	}
	m := map[string]string{
		"Enter": "Return", "Escape": "Escape", "Backspace": "BackSpace", "Tab": "Tab", " ": "space",
		"ArrowLeft": "Left", "ArrowRight": "Right", "ArrowUp": "Up", "ArrowDown": "Down",
		"Shift": "Shift_L", "Control": "Control_L", "Alt": "Alt_L", "Delete": "Delete",
		"Home": "Home", "End": "End", "PageUp": "Page_Up", "PageDown": "Page_Down",
	}
	v, ok := m[k]
	return v, ok
}

func windowsInput(e inputEvent) error {
	var action string
	switch e.Type {
	case "move":
		action = fmt.Sprintf(`[GoDeckInput]::Move(%d,%d)`, int(e.X*65535), int(e.Y*65535))
	case "down", "up":
		if e.Button < 0 || e.Button > 2 {
			return errors.New("鼠标按钮无效")
		}
		flags := []int{2, 32, 8}
		if e.Type == "up" {
			flags = []int{4, 64, 16}
		}
		action = fmt.Sprintf(`[GoDeckInput]::Mouse(%d,0)`, flags[e.Button])
	case "wheel":
		delta := -120
		if e.Delta < 0 {
			delta = 120
		}
		action = fmt.Sprintf(`[GoDeckInput]::Mouse(2048,%d)`, delta)
	case "keydown", "keyup":
		vk, ok := virtualKey(e.Key)
		if !ok {
			return errors.New("不支持该按键")
		}
		up := 0
		if e.Type == "keyup" {
			up = 2
		}
		action = fmt.Sprintf(`[GoDeckInput]::Key(%d,%d)`, vk, up)
	default:
		return errors.New("不支持的输入事件")
	}
	script := `Add-Type -TypeDefinition 'using System; using System.Runtime.InteropServices; public class GoDeckInput { [DllImport("user32.dll")] static extern void mouse_event(uint f, uint x, uint y, int d, UIntPtr e); [DllImport("user32.dll")] static extern void keybd_event(byte k, byte s, uint f, UIntPtr e); public static void Move(int x,int y) { mouse_event(0xC001,(uint)x,(uint)y,0,UIntPtr.Zero); } public static void Mouse(int f,int d) { mouse_event((uint)f,0,0,d,UIntPtr.Zero); } public static void Key(int k,int f) { keybd_event((byte)k,0,(uint)f,UIntPtr.Zero); } }'; ` + action
	if err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Run(); err != nil {
		return errors.New("无法发送输入，请确认交互式桌面可用")
	}
	return nil
}

func virtualKey(k string) (int, bool) {
	if len(k) == 1 {
		c := strings.ToUpper(k)[0]
		if c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			return int(c), true
		}
	}
	m := map[string]int{
		"Enter": 13, "Escape": 27, "Backspace": 8, "Tab": 9, " ": 32,
		"ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40,
		"Shift": 16, "Control": 17, "Alt": 18, "Delete": 46, "Home": 36,
		"End": 35, "PageUp": 33, "PageDown": 34,
	}
	v, ok := m[k]
	return v, ok
}

func validFileName(name string) bool {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\<>:"|?*`) || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	stem := strings.ToUpper(strings.TrimRight(strings.SplitN(name, ".", 2)[0], " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9", "COM¹", "COM²", "COM³", "LPT¹", "LPT²", "LPT³":
		return false
	}
	return true
}

func safePath(name string, existing bool) (string, error) {
	if !validFileName(name) {
		return "", errors.New("文件名无效")
	}
	p := filepath.Join(share, name)
	if existing {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(share, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return "", errors.New("文件不在共享目录中")
		}
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() {
			return "", errors.New("只允许访问普通文件")
		}
		return resolved, nil
	}
	return p, nil
}

func fileError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

func files(w http.ResponseWriter, r *http.Request) {
	if !require(w, r, "GET") {
		return
	}
	entries, err := os.ReadDir(share)
	if err != nil {
		fileError(w, 500, "list_failed", "无法读取共享目录")
		return
	}
	type item struct {
		Name     string    `json:"name"`
		Size     int64     `json:"size"`
		Modified time.Time `json:"modified"`
	}
	list := []item{}
	for _, e := range entries {
		if !validFileName(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			list = append(list, item{e.Name(), info.Size(), info.ModTime()})
		}
	}
	jsonOut(w, list)
}

func file(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	switch r.Method {
	case "GET":
		p, err := safePath(name, true)
		if err != nil {
			fileError(w, 404, "not_found", "文件不可用或已移除")
			return
		}
		f, err := os.Open(p)
		if err != nil {
			fileError(w, 404, "not_found", "文件不可用或已移除")
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			fileError(w, 404, "not_found", "文件不可用或已移除")
			return
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		http.ServeContent(w, r, name, info.ModTime(), f)
	case "POST":
		if !validFileName(name) {
			fileError(w, 400, "invalid_name", "文件名无效；请避免路径符号、控制字符和系统保留名称")
			return
		}
		if r.ContentLength > maxUpload {
			fileError(w, 413, "too_large", "文件超过允许的大小")
			return
		}
		dir, err := os.MkdirTemp(share, ".godeck-upload-")
		if err != nil {
			fileError(w, 500, "storage_failed", "无法创建临时文件，请检查共享目录权限和空间")
			return
		}
		defer os.RemoveAll(dir)
		tmp := filepath.Join(dir, "data")
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			fileError(w, 500, "storage_failed", "无法创建临时文件，请检查共享目录权限和空间")
			return
		}
		n, copyErr := io.Copy(f, io.LimitReader(r.Body, maxUpload+1))
		closeErr := f.Close()
		if n > maxUpload {
			fileError(w, 413, "too_large", "文件超过允许的大小")
			return
		}
		if copyErr != nil || closeErr != nil || r.Context().Err() != nil || (r.ContentLength >= 0 && n != r.ContentLength) {
			fileError(w, 500, "upload_incomplete", "文件传输未完成，请刷新列表确认后重试")
			return
		}
		target := filepath.Join(share, name)
		if err = os.Link(tmp, target); err != nil {
			if os.IsExist(err) {
				fileError(w, 409, "name_conflict", "同名文件已存在，请更改本地文件名后重新选择")
				return
			}
			fileError(w, 500, "commit_failed", "无法保存文件；请检查空间、权限，以及共享目录所在文件系统是否支持硬链接")
			return
		}
		w.WriteHeader(http.StatusCreated)
	default:
		w.Header().Set("Allow", "GET, POST")
		fileError(w, 405, "method_not_allowed", "方法不允许")
	}
}

func inputCapability() string {
	if !control.Load() {
		return "只读（启动时未启用控制）"
	}
	if runtime.GOOS == "darwin" {
		return "当前平台不支持输入"
	}
	if runtime.GOOS == "linux" && os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, err := exec.LookPath("ydotool"); err != nil {
			return "画面可查看；请安装 ydotool 1.x 配置远程控制"
		}
		socket := os.Getenv("YDOTOOL_SOCKET")
		if socket == "" {
			if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
				socket = filepath.Join(dir, ".ydotool_socket")
			} else {
				socket = "/tmp/.ydotool_socket"
			}
		}
		conn, err := net.DialTimeout("unixgram", socket, time.Second)
		if err != nil {
			return "画面可查看；请启动 ydotoold 并检查套接字权限"
		}
		conn.Close()
		if !waylandSingle {
			return "键盘可用；鼠标定位需要单屏配置和 -wayland-single-output"
		}
		return "ydotool 已连接；鼠标定位要求单屏、100% 缩放和禁用指针加速"
	}
	return "已启用；输入能力取决于桌面会话"
}

func waylandInput(e inputEvent) error {
	var args []string
	switch e.Type {
	case "move":
		if !waylandSingle {
			return errors.New("Wayland 鼠标定位需单显示器、100% 缩放并禁用指针加速，然后用 -wayland-single-output 启动")
		}
		screenSize.RLock()
		width, height := screenSize.width, screenSize.height
		screenSize.RUnlock()
		if width < 1 || height < 1 {
			return errors.New("请先等待 Wayland 截图成功")
		}
		args = []string{"mousemove", "--absolute", "-x", strconv.Itoa(int(e.X * float64(width-1))), "-y", strconv.Itoa(int(e.Y * float64(height-1)))}
	case "down", "up":
		if !waylandSingle {
			return errors.New("请先启用 Wayland 单屏鼠标定位")
		}
		if e.Button < 0 || e.Button > 2 {
			return errors.New("鼠标按钮无效")
		}
		button := []int{0, 2, 1}[e.Button]
		mask := 64
		if e.Type == "up" {
			mask = 128
		}
		args = []string{"click", fmt.Sprintf("0x%x", mask|button)}
	case "wheel":
		amount := -1
		if e.Delta < 0 {
			amount = 1
		}
		args = []string{"mousemove", "--wheel", "--", "0", strconv.Itoa(amount)}
	case "keydown", "keyup":
		code, ok := evdevKey(e.Code)
		if !ok {
			return errors.New("此按键暂不支持；请使用英文键盘和常用控制键")
		}
		down := 1
		if e.Type == "keyup" {
			down = 0
		}
		args = []string{"key", fmt.Sprintf("%d:%d", code, down)}
	default:
		return errors.New("不支持的输入事件")
	}
	if err := exec.Command("ydotool", args...).Run(); err != nil {
		return errors.New("ydotool 输入失败：请安装兼容的 1.x 版本、启动 ydotoold 并检查套接字权限")
	}
	return nil
}

func evdevKey(code string) (int, bool) {
	keys := map[string]int{
		"Escape": 1, "Digit1": 2, "Digit2": 3, "Digit3": 4, "Digit4": 5, "Digit5": 6, "Digit6": 7, "Digit7": 8, "Digit8": 9, "Digit9": 10, "Digit0": 11, "Minus": 12, "Equal": 13, "Backspace": 14, "Tab": 15,
		"KeyQ": 16, "KeyW": 17, "KeyE": 18, "KeyR": 19, "KeyT": 20, "KeyY": 21, "KeyU": 22, "KeyI": 23, "KeyO": 24, "KeyP": 25, "BracketLeft": 26, "BracketRight": 27, "Enter": 28, "ControlLeft": 29,
		"KeyA": 30, "KeyS": 31, "KeyD": 32, "KeyF": 33, "KeyG": 34, "KeyH": 35, "KeyJ": 36, "KeyK": 37, "KeyL": 38, "Semicolon": 39, "Quote": 40, "Backquote": 41, "ShiftLeft": 42, "Backslash": 43,
		"KeyZ": 44, "KeyX": 45, "KeyC": 46, "KeyV": 47, "KeyB": 48, "KeyN": 49, "KeyM": 50, "Comma": 51, "Period": 52, "Slash": 53, "ShiftRight": 54, "AltLeft": 56, "Space": 57, "CapsLock": 58,
		"F1": 59, "F2": 60, "F3": 61, "F4": 62, "F5": 63, "F6": 64, "F7": 65, "F8": 66, "F9": 67, "F10": 68, "F11": 87, "F12": 88, "ControlRight": 97, "AltRight": 100, "Home": 102, "ArrowUp": 103, "PageUp": 104, "ArrowLeft": 105, "ArrowRight": 106, "End": 107, "ArrowDown": 108, "PageDown": 109, "Insert": 110, "Delete": 111, "MetaLeft": 125, "MetaRight": 126,
	}
	v, ok := keys[code]
	return v, ok
}

func desktopName() string {
	if runtime.GOOS != "linux" {
		return runtime.GOOS
	}
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return "Linux X11"
	}
	if isKDE() {
		return "KDE Plasma / Wayland"
	}
	return "Linux Wayland"
}

func isKDE() bool {
	desktop := strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP") + ":" + os.Getenv("XDG_SESSION_DESKTOP") + ":" + os.Getenv("DESKTOP_SESSION"))
	for _, part := range strings.FieldsFunc(desktop, func(r rune) bool { return r == ':' || r == ';' }) {
		if strings.Contains(part, "kde") || strings.Contains(part, "plasma") {
			return true
		}
	}
	return os.Getenv("KDE_FULL_SESSION") == "true"
}

func selectedCaptureBackend() string {
	if runtime.GOOS != "linux" {
		return runtime.GOOS
	}
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return "ImageMagick import"
	}
	if captureBackend == "spectacle" {
		return "Spectacle"
	}
	if captureBackend == "grim" {
		return "grim"
	}
	if isKDE() {
		return "Spectacle"
	}
	return "grim"
}

func inputBackend() string {
	if runtime.GOOS == "linux" {
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return "ydotool"
		}
		return "xdotool"
	}
	if runtime.GOOS == "windows" {
		return "Windows"
	}
	return "不可用"
}

func captureLinux() ([]byte, error) {
	select {
	case captureGate <- struct{}{}:
		defer func() { <-captureGate }()
	default:
		return nil, errors.New("截图正在进行，请稍后重试")
	}
	backend := selectedCaptureBackend()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var data []byte
	var err error
	switch backend {
	case "Spectacle":
		if _, err = exec.LookPath("spectacle"); err != nil {
			return nil, errors.New("未找到 Spectacle，请在 KDE Plasma 中安装发行版提供的 Spectacle")
		}
		var dir string
		dir, err = os.MkdirTemp("", "godeck-screen-*")
		if err != nil {
			return nil, errors.New("无法创建临时截图目录")
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "screen.png")
		cmd := exec.CommandContext(ctx, "spectacle", "--new-instance", "--background", "--nonotify", "--fullscreen", "--output", path)
		if err = cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return nil, errors.New("Spectacle 截图超时，请检查 KDE 桌面会话与截图权限")
			}
			return nil, errors.New("Spectacle 截图失败；请检查图形会话、锁屏和 KWin 授权，并确认 Spectacle 支持后台、无通知和独立实例选项；旧版本请升级")
		}
		var f *os.File
		f, err = os.Open(path)
		if err != nil {
			return nil, errors.New("Spectacle 未生成截图，请检查截图权限与桌面会话")
		}
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(f, (32<<20)+1))
		if err != nil || len(data) > 32<<20 {
			return nil, errors.New("截图读取失败或超过 32 MB")
		}
	case "grim":
		if _, err = exec.LookPath("grim"); err != nil {
			return nil, errors.New("未找到 grim，请安装 grim 并确认合成器支持 wlr-screencopy")
		}
		data, err = exec.CommandContext(ctx, "grim", "-s", "1", "-").Output()
		if err != nil {
			if ctx.Err() != nil {
				return nil, errors.New("grim 截图超时")
			}
			return nil, errors.New("grim 截图失败，请检查图形会话与 wlr-screencopy 支持")
		}
	case "ImageMagick import":
		data, err = exec.CommandContext(ctx, "import", "-window", "root", "png:-").Output()
		if err != nil {
			if ctx.Err() != nil {
				return nil, errors.New("X11 截图超时")
			}
			return nil, errors.New("X11 截图失败，请安装 ImageMagick 并检查图形会话")
		}
	}
	if len(data) > 32<<20 {
		return nil, errors.New("截图超过 32 MB")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 {
		return nil, errors.New("截图数据不可用，请检查桌面会话及截图权限")
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		screenSize.Lock()
		screenSize.width = cfg.Width
		screenSize.height = cfg.Height
		screenSize.Unlock()
	}
	return data, nil
}

func inputReady() bool {
	if !control.Load() || runtime.GOOS == "darwin" {
		return false
	}
	if runtime.GOOS == "linux" && os.Getenv("WAYLAND_DISPLAY") != "" {
		s := inputCapability()
		return !strings.HasPrefix(s, "画面可查看")
	}
	if runtime.GOOS == "linux" {
		_, err := exec.LookPath("xdotool")
		return err == nil
	}
	return runtime.GOOS == "windows"
}

func capabilities() map[string]any {
	ready := inputReady()
	pointer := ready
	reason := ""
	if !control.Load() {
		reason = "被控端以只读模式启动"
	} else if !ready {
		reason = inputCapability()
	}
	if runtime.GOOS == "linux" && os.Getenv("WAYLAND_DISPLAY") != "" && ready && !waylandSingle {
		pointer = false
		reason = "鼠标定位需要确认单屏配置并启用对应参数"
	}
	return map[string]any{"keyboard": ready, "pointer": pointer, "wheel": ready, "reason": reason, "maxUploadBytes": maxUpload}
}
