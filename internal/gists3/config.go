package gists3

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the opt-in file configuration for CLI use and quick scripts. It
// carries no credentials — identity is resolved by the caller (cmd/g3 uses
// GIST_TOKEN, then the gh CLI) — so the file is safe to share, e.g. in a
// dotfiles repo.
type Config struct {
	// DefaultUser is informational in v1: the token alone determines API
	// identity. It lets tooling label output and reserves room for
	// multi-profile support without a schema break.
	DefaultUser string `json:"default_user"`

	// BaseURL overrides the API endpoint; empty means
	// https://api.github.com.
	BaseURL string `json:"base_url,omitempty"`

	// Links maps link names to declarations; the CLI's link commands
	// maintain the table (docs/004-linked-paths.md §4.1).
	Links map[string]Link `json:"links,omitempty"`
}

// Link declares that a local path is the working copy of a remote key
// (docs/004-linked-paths.md). Path is stored unexpanded, exactly as the
// user typed it, so the config file stays portable across machines.
type Link struct {
	URI  string `json:"uri"`
	Path string `json:"path"`
}

// ConfigPath resolves the per-OS config file location via os.UserConfigDir:
// $XDG_CONFIG_HOME or ~/.config on Linux, ~/Library/Application Support on
// macOS, %AppData% on Windows.
func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("gists3: resolve user config dir: %w", err)
	}
	return filepath.Join(dir, "gists3", "config.json"), nil
}

// LoadConfig reads the config file at the default per-OS path
// (<user config dir>/gists3/config.json). A missing file is an error
// wrapping fs.ErrNotExist, so callers can treat absence as "no config".
// Unknown keys — including a stale token field from a pre-v0.2 file — are
// ignored.
func LoadConfig() (*Config, error) {
	p, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("gists3: read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("gists3: parse config %s: %w", p, err)
	}
	return &cfg, nil
}
