//go:build linux

package clipboard

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// System returns the desktop clipboard.
//
// Wayland uses wl-clipboard (wl-paste --watch / wl-copy), which relies on the
// compositor's data-control protocol so it works while GoDeck is unfocused
// (KDE Plasma supports it). X11 falls back to polling xclip.
func System() Platform {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return wayland{}
	}
	return x11{}
}

const readTimeout = 3 * time.Second

func run(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, errors.New("未找到 " + name + "，请安装 " + pkgFor(name))
	}
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout = &limitWriter{w: &out, n: MaxTextBytes + 1}
	err := cmd.Run()
	return out.Bytes(), err
}

func pkgFor(name string) string {
	if strings.HasPrefix(name, "wl-") {
		return "wl-clipboard"
	}
	return name
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	total := len(p)
	if len(p) > l.n {
		p = p[:l.n]
	}
	l.n -= len(p)
	if _, err := l.w.Write(p); err != nil {
		return 0, err
	}
	return total, nil
}

type wayland struct{}

func (wayland) read(ctx context.Context) (string, bool) {
	out, err := run(ctx, nil, "wl-paste", "--no-newline", "--type", "text")
	if err != nil {
		return "", false
	}
	return string(out), true
}

func (w wayland) Watch(ctx context.Context, onChange func(string)) error {
	if _, err := exec.LookPath("wl-paste"); err != nil {
		return errors.New("未找到 wl-paste，请安装 wl-clipboard")
	}
	// wl-paste runs the command once immediately and again on every change;
	// the command only prints a marker, content is fetched separately.
	cmd := exec.CommandContext(ctx, "wl-paste", "--type", "text", "--watch", "sh", "-c", "cat >/dev/null; echo changed")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	started := time.Now()
	sc := bufio.NewScanner(stdout)
	first := true
	for sc.Scan() {
		if first {
			first = false // initial content at connect time is not pushed
			continue
		}
		if text, ok := w.read(ctx); ok {
			onChange(text)
		}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	if time.Since(started) < 2*time.Second {
		return errors.New("wl-paste --watch 退出，合成器可能不支持 data-control 协议")
	}
	return err
}

func (wayland) Write(text string) error {
	if _, err := exec.LookPath("wl-copy"); err != nil {
		return errors.New("未找到 wl-copy，请安装 wl-clipboard")
	}
	// wl-copy forks a background server that owns the selection.
	cmd := exec.Command("wl-copy", "--type", "text/plain;charset=utf-8")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

type x11 struct{}

func (x11) read(ctx context.Context) (string, bool) {
	out, err := run(ctx, nil, "xclip", "-selection", "clipboard", "-o", "-t", "UTF8_STRING")
	if err != nil {
		return "", false
	}
	return string(out), true
}

func (x x11) Watch(ctx context.Context, onChange func(string)) error {
	if _, err := exec.LookPath("xclip"); err != nil {
		return errors.New("未找到 xclip")
	}
	last, _ := x.read(ctx)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if text, ok := x.read(ctx); ok && text != last {
				last = text
				onChange(text)
			}
		}
	}
}

func (x11) Write(text string) error {
	if _, err := exec.LookPath("xclip"); err != nil {
		return errors.New("未找到 xclip")
	}
	cmd := exec.Command("xclip", "-selection", "clipboard", "-i")
	cmd.Stdin = strings.NewReader(text)
	// xclip keeps serving the selection until another client takes ownership.
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
