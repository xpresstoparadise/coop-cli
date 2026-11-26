// Package userconf keeps per-user settings: the default coop server and
// the host key for opening rooms on it. The file is
// <user config dir>/coop/config.json (mode 0600), or $COOP_CONFIG.
package userconf

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Config is the user's coop settings.
type Config struct {
	Server  string `json:"server,omitempty"`
	HostKey string `json:"host_key,omitempty"`
}

// Path is where the config lives.
func Path() (string, error) {
	if p := os.Getenv("COOP_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "coop", "config.json"), nil
}

// Load reads the config; a missing file is an empty config. Environment
// variables COOP_SERVER and COOP_HOST_KEY override the file.
func Load() (Config, error) {
	var c Config
	p, err := Path()
	if err != nil {
		return c, err
	}
	data, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &c); err != nil {
			return c, err
		}
	}
	if v := os.Getenv("COOP_SERVER"); v != "" {
		c.Server = v
	}
	if v := os.Getenv("COOP_HOST_KEY"); v != "" {
		c.HostKey = v
	}
	return c, nil
}

// Save writes the config with owner-only permissions.
func Save(c Config) (string, error) {
	p, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	return p, os.WriteFile(p, append(data, '\n'), 0o600)
}
