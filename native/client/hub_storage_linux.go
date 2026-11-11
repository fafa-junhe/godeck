//go:build linux && cgo

package client

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type savedHubDevice struct {
	URL   string `json:"url"`
	User  string `json:"user"`
	ID    string `json:"id"`
	Token string `json:"token"`
}

func (c *Controller) loadHubDevice(url, user string) (savedHubDevice, error) {
	var d savedHubDevice
	b, e := os.ReadFile(filepath.Join(c.dir, "hub-device.json"))
	if e != nil {
		return d, e
	}
	if e = json.Unmarshal(b, &d); e != nil {
		return d, e
	}
	if d.URL != url || d.User != user || d.Token == "" {
		return savedHubDevice{}, errors.New("设备属于另一个中心账号")
	}
	return d, nil
}

func (c *Controller) saveHubDevice(d savedHubDevice) error {
	if e := os.MkdirAll(c.dir, 0700); e != nil {
		return e
	}
	b, e := json.Marshal(d)
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(c.dir, "hub-device.json"), b, 0600)
}

func (c *Controller) deleteHubDevice() {
	_ = os.Remove(filepath.Join(c.dir, "hub-device.json"))
}
