//go:build windows && amd64

package client

import (
	"os"
)

type savedHubDevice = savedDevice

func (c *Controller) loadHubDevice(url, user string) (savedHubDevice, error) {
	return loadDevice(url, user)
}

func (c *Controller) saveHubDevice(d savedHubDevice) error {
	return saveDevice(d)
}

func (c *Controller) deleteHubDevice() {
	if p, e := deviceFile(); e == nil {
		_ = os.Remove(p)
	}
}
