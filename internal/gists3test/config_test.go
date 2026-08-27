package gists3test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

// writeConfig points os.UserConfigDir at a temp dir (covering the Linux,
// macOS, and Windows lookup paths) and writes a config file there.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))
	t.Setenv("AppData", filepath.Join(tmp, "appdata"))
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Join(dir, "gists3")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(cfgDir, "config.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfig(t *testing.T) {
	writeConfig(t, `{"default_user":"octocat","base_url":"https://ghe.example/api/v3"}`)
	cfg, err := gists3.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultUser != "octocat" || cfg.BaseURL != "https://ghe.example/api/v3" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadConfigIgnoresLegacyToken(t *testing.T) {
	// The inversion of the pre-004 contract: a config without a token used
	// to be fatal (`token is required`). The field is gone from the schema,
	// so a stale token in an old file is ignored, never read as identity.
	writeConfig(t, `{"default_user":"octocat","token":"ghp_stale"}`)
	cfg, err := gists3.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultUser != "octocat" {
		t.Errorf("DefaultUser = %q, want octocat", cfg.DefaultUser)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	// cmd/g3 treats absence as "no config" via errors.Is, so the wrapped
	// fs.ErrNotExist is contract, not implementation detail.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))
	t.Setenv("AppData", filepath.Join(tmp, "appdata"))
	_, err := gists3.LoadConfig()
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want one wrapping fs.ErrNotExist", err)
	}
}

func TestLoadConfigBadJSON(t *testing.T) {
	writeConfig(t, `{not json`)
	if _, err := gists3.LoadConfig(); err == nil {
		t.Fatal("want error for malformed config")
	}
}
