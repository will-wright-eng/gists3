package gists3

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Config is the opt-in file configuration for CLI use and quick scripts. It
// enters a program only through the explicitly named ...FromConfig
// constructors — New never touches the filesystem.
type Config struct {
	// DefaultUser is informational in v1: the token alone determines API
	// identity. It lets tooling label output and reserves room for
	// multi-profile support without a schema break.
	DefaultUser string `json:"default_user"`

	// Token is a GitHub personal access token with the gist scope.
	Token string `json:"token,omitempty"`

	// BaseURL overrides the API endpoint; empty means
	// https://api.github.com.
	BaseURL string `json:"base_url,omitempty"`

	// Warnings collects non-fatal findings from LoadConfig, e.g. a config
	// file readable by other users. Never persisted.
	Warnings []string `json:"-"`
}

// configPath resolves the per-OS config file location via os.UserConfigDir:
// $XDG_CONFIG_HOME or ~/.config on Linux, ~/Library/Application Support on
// macOS, %AppData% on Windows.
func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("gists3: resolve user config dir: %w", err)
	}
	return filepath.Join(dir, "gists3", "config.json"), nil
}

// LoadConfig reads and validates the config file at the default per-OS path
// (<user config dir>/gists3/config.json). A missing or token-less file is an
// error. Because the token sits in the file in plaintext, a group- or
// world-readable file appends a warning to Config.Warnings (the load still
// succeeds); create the file with mode 0600.
func LoadConfig() (*Config, error) {
	p, err := configPath()
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
	if cfg.Token == "" {
		return nil, fmt.Errorf("gists3: config %s: token is required", p)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(p); err == nil && fi.Mode().Perm()&0o077 != 0 {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf(
				"config file %s is readable by other users (mode %#o) and holds a plaintext token; chmod 600 recommended",
				p, fi.Mode().Perm()))
		}
	}
	return &cfg, nil
}

// NewFromConfig constructs a Client from an explicit Config. Precedence:
// functional options beat config fields (a WithBaseURL option overrides
// cfg.BaseURL), and config fields beat built-in defaults.
func NewFromConfig(cfg *Config, opts ...Option) *Client {
	all := make([]Option, 0, len(opts)+1)
	if cfg.BaseURL != "" {
		all = append(all, WithBaseURL(cfg.BaseURL))
	}
	all = append(all, opts...)
	return New(cfg.Token, all...)
}

// NewFromDefaultConfig is LoadConfig followed by NewFromConfig — the one
// constructor that reads ambient state, named so call sites show it.
func NewFromDefaultConfig(opts ...Option) (*Client, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	return NewFromConfig(cfg, opts...), nil
}
