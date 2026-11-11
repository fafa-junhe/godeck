package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Config represents the persistent settings of GoDeck client.
type Config struct {
	// Hub configuration
	HubURL      string `json:"hub_url"`
	HubUser     string `json:"hub_user"`
	HubPassword string `json:"hub_password,omitempty"`
	AutoLogin   bool   `json:"auto_login"`
	AutoPair    bool   `json:"auto_pair"`
	DeviceName  string `json:"device_name,omitempty"`

	// Network & Hosting
	ListenAddr     string `json:"listen_addr"`
	AcceptIncoming bool   `json:"accept_incoming"`
	AllowFilesHost bool   `json:"allow_files_host"`
	ShareDir       string `json:"share_dir,omitempty"`

	// Media & Video Quality
	Preset    string `json:"preset"`    // "720p30", "1080p30"
	Codec     string `json:"codec"`     // "auto", "hardware", "software"
	Transport string `json:"transport"` // "auto", "udp", "tcp", "websocket"
	ShowStats bool   `json:"show_stats"` // show real-time performance overlay

	// Direct Connect History
	LastTargetAddr string `json:"last_target_addr,omitempty"`

	mu sync.Mutex `json:"-"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "GoDeck-Device"
	}
	return &Config{
		HubURL:         "https://godeck.junher.top:8443",
		ListenAddr:     "0.0.0.0:8766",
		AcceptIncoming: true,
		AllowFilesHost: false,
		Preset:         "720p30",
		Codec:          "auto",
		Transport:      "auto",
		DeviceName:     hostname,
		AutoLogin:      false,
		AutoPair:       true,
		ShowStats:      false,
	}
}

// ConfigPath resolves the path to the configuration file.
// If custom is not empty, it returns custom directly.
// Otherwise it checks for local ./godeck.json or ./config.json,
// and finally defaults to $XDG_CONFIG_HOME/godeck/config.json (or %APPDATA%\godeck\config.json).
func ConfigPath(custom string) string {
	if custom != "" {
		return custom
	}
	for _, local := range []string{"godeck.json", "config.json"} {
		if fi, err := os.Stat(local); err == nil && !fi.IsDir() {
			abs, err := filepath.Abs(local)
			if err == nil {
				return abs
			}
			return local
		}
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "godeck", "config.json")
}

// Load reads and parses the configuration file.
// If the file does not exist, it returns DefaultConfig() and the resolved path without error.
func Load(custom string) (*Config, string, error) {
	path := ConfigPath(custom)
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, path, nil
		}
		return cfg, path, err
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return cfg, path, err
	}

	// Apply normalization and fallbacks for empty fields
	cfg.HubURL = strings.TrimSpace(cfg.HubURL)
	if cfg.HubURL == "" {
		cfg.HubURL = "https://godeck.junher.top:8443"
	}
	cfg.HubUser = strings.TrimSpace(cfg.HubUser)
	cfg.ListenAddr = strings.TrimSpace(cfg.ListenAddr)
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "0.0.0.0:8766"
	}
	if cfg.Preset == "" {
		cfg.Preset = "720p30"
	}
	if cfg.Codec == "" {
		cfg.Codec = "auto"
	}
	if cfg.Transport == "" {
		cfg.Transport = "auto"
	}
	if cfg.DeviceName == "" {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "GoDeck-Device"
		}
		cfg.DeviceName = hostname
	}

	return cfg, path, nil
}

// Save writes the configuration to disk with secure file permissions (0600).
func Save(cfg *Config, path string) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	cfg.mu.Lock()
	defer cfg.mu.Unlock()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// Write to temporary file first and atomic rename to prevent corruption
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		// On Windows rename over existing file might fail; fallback to replace
		_ = os.Remove(path)
		return os.Rename(tmpPath, path)
	}
	return nil
}
