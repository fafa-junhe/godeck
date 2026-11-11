//go:build linux

package transfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	portalDest   = "org.freedesktop.portal.Desktop"
	portalPath   = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalFC     = "org.freedesktop.portal.FileChooser"
	portalReq    = "org.freedesktop.portal.Request"
)

func randomToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "godeck" + hex.EncodeToString(b[:])
}

// ChooseOpenFile opens a native file selection dialog (KDE FileChooser portal preferred, falling back to kdialog/zenity).
func ChooseOpenFile(title string) (string, error) {
	path, err := portalFileChooser("OpenFile", title, false, "", false)
	if err == nil && path != "" {
		return path, nil
	}
	// Fallback to CLI dialogs if portal is not active
	if out, err := exec.Command("kdialog", "--title", title, "--getopenfilename").Output(); err == nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			return s, nil
		}
	}
	if out, err := exec.Command("zenity", "--file-selection", "--title="+title).Output(); err == nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			return s, nil
		}
	}
	if err != nil {
		return "", err
	}
	return "", errors.New("已取消选择文件")
}

// ChooseSaveFile opens a native save destination dialog (KDE FileChooser portal preferred).
func ChooseSaveFile(title, defaultName string) (string, error) {
	path, err := portalFileChooser("SaveFile", title, true, defaultName, false)
	if err == nil && path != "" {
		return path, nil
	}
	if out, err := exec.Command("kdialog", "--title", title, "--getsavefilename", defaultName).Output(); err == nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			return s, nil
		}
	}
	if out, err := exec.Command("zenity", "--file-selection", "--save", "--confirm-overwrite", "--title="+title, "--filename="+defaultName).Output(); err == nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			return s, nil
		}
	}
	if err != nil {
		return "", err
	}
	return "", errors.New("已取消保存路径选择")
}

func portalFileChooser(method, title string, save bool, currentName string, directory bool) (string, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return "", err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	handleToken := randomToken()
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(handleToken),
		"multiple":     dbus.MakeVariant(false),
	}
	if directory {
		options["directory"] = dbus.MakeVariant(true)
	}
	if save && currentName != "" {
		options["current_name"] = dbus.MakeVariant(currentName)
	}

	signal := make(chan *dbus.Signal, 16)
	conn.Signal(signal)
	defer conn.RemoveSignal(signal)

	if err = conn.AddMatchSignal(dbus.WithMatchInterface(portalReq), dbus.WithMatchMember("Response")); err != nil {
		return "", err
	}
	defer conn.RemoveMatchSignal(dbus.WithMatchInterface(portalReq), dbus.WithMatchMember("Response"))

	var reqPath dbus.ObjectPath
	call := conn.Object(portalDest, portalPath).CallWithContext(ctx, portalFC+"."+method, 0, "", title, options)
	if call.Err != nil {
		return "", call.Err
	}
	if err = call.Store(&reqPath); err != nil {
		return "", err
	}

	for {
		select {
		case <-ctx.Done():
			closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
			_ = conn.Object(portalDest, reqPath).CallWithContext(closeCtx, portalReq+".Close", 0).Err
			closeCancel()
			return "", ctx.Err()
		case sig, ok := <-signal:
			if !ok {
				return "", errors.New("portal disconnected")
			}
			if sig.Path != reqPath || sig.Name != portalReq+".Response" || len(sig.Body) != 2 {
				continue
			}
			var response uint32
			var results map[string]dbus.Variant
			if err = dbus.Store(sig.Body, &response, &results); err != nil {
				return "", err
			}
			if response != 0 {
				return "", errors.New("已取消操作")
			}
			v, ok := results["uris"]
			if !ok {
				return "", errors.New("未选择文件")
			}
			var uris []string
			if err = dbus.Store([]any{v.Value()}, &uris); err != nil || len(uris) == 0 {
				return "", errors.New("未能解析文件路径")
			}
			u, err := url.Parse(uris[0])
			if err != nil {
				return "", err
			}
			return u.Path, nil
		}
	}
}

// ChooseFolder opens a native folder selection dialog (portal first, then kdialog/zenity).
func ChooseFolder(title string) (string, error) {
	path, err := portalFileChooser("OpenFile", title, false, "", true)
	if err == nil && path != "" {
		return path, nil
	}
	if out, e := exec.Command("kdialog", "--title", title, "--getexistingdirectory").Output(); e == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s, nil
		}
	}
	if out, e := exec.Command("zenity", "--file-selection", "--directory", "--title="+title).Output(); e == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s, nil
		}
	}
	if err != nil {
		return "", err
	}
	return "", errors.New("已取消选择文件夹")
}
