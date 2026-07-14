package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setConfigDir points os.UserConfigDir at a temp dir across the Linux,
// macOS, and Windows lookup paths (the writeConfig pattern from
// internal/gists3test), and neutralizes GIST_TOKEN so a developer's real
// token cannot flip precedence results.
func setConfigDir(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))
	t.Setenv("AppData", filepath.Join(tmp, "appdata"))
	t.Setenv("GIST_TOKEN", "")
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeConfig writes a config file at the temp config path. The mode is
// applied with Chmod so the test is immune to umask.
func writeConfig(t *testing.T, content string, mode os.FileMode) {
	t.Helper()
	dir := filepath.Join(setConfigDir(t), "gists3")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

// stubGH replaces the gh fallback for the test's duration.
func stubGH(t *testing.T, token string, err error) {
	t.Helper()
	orig := ghAuthToken
	ghAuthToken = func() (string, error) { return token, err }
	t.Cleanup(func() { ghAuthToken = orig })
}

func TestResolveConfigEnvWins(t *testing.T) {
	writeConfig(t, `{"token":"cfg-token","base_url":"https://ghe.example/api/v3"}`, 0o600)
	t.Setenv("GIST_TOKEN", "env-token")
	stubGH(t, "", errors.New("must not be called"))
	cfg, err := resolveConfig(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "env-token" {
		t.Errorf("Token = %q, want the GIST_TOKEN value", cfg.Token)
	}
	if cfg.BaseURL != "" {
		t.Errorf("BaseURL = %q; the config file must not be consulted when GIST_TOKEN supplies the identity", cfg.BaseURL)
	}
}

func TestResolveConfigTrimsEnvToken(t *testing.T) {
	// CRLF env files yield "tok\r", which the HTTP client would reject
	// opaquely; whitespace-only must behave as unset instead of shadowing
	// the working layers below.
	writeConfig(t, `{"token":"cfg-token"}`, 0o600)
	stubGH(t, "", errors.New("unused"))
	t.Setenv("GIST_TOKEN", "env-token\r\n")
	cfg, err := resolveConfig(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "env-token" {
		t.Errorf("Token = %q, want the trimmed value", cfg.Token)
	}
	t.Setenv("GIST_TOKEN", " \n")
	if cfg, err = resolveConfig(io.Discard); err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "cfg-token" {
		t.Errorf("Token = %q; whitespace-only GIST_TOKEN must behave as unset", cfg.Token)
	}
}

func TestResolveConfigFileBeatsGH(t *testing.T) {
	writeConfig(t, `{"token":"cfg-token","base_url":"https://ghe.example/api/v3"}`, 0o600)
	stubGH(t, "gh-token", nil)
	cfg, err := resolveConfig(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "cfg-token" || cfg.BaseURL != "https://ghe.example/api/v3" {
		t.Errorf("cfg = %+v, want the config file's token and base_url", cfg)
	}
}

func TestResolveConfigWarningsReachStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are synthetic on windows")
	}
	writeConfig(t, `{"token":"cfg-token"}`, 0o644)
	var stderr bytes.Buffer
	if _, err := resolveConfig(&stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "chmod 600") {
		t.Errorf("stderr = %q, want the permissions warning", stderr.String())
	}
}

func TestResolveConfigGHFallback(t *testing.T) {
	setConfigDir(t) // no config file
	stubGH(t, "gh-token", nil)
	cfg, err := resolveConfig(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "gh-token" {
		t.Errorf("Token = %q, want the gh token", cfg.Token)
	}
}

func TestResolveConfigMalformedIsFatal(t *testing.T) {
	writeConfig(t, `{not json`, 0o600)
	stubGH(t, "gh-token", nil)
	if _, err := resolveConfig(io.Discard); err == nil {
		t.Fatal("malformed config must be fatal, not fall through to gh")
	}
}

func TestResolveConfigTokenlessIsFatal(t *testing.T) {
	writeConfig(t, `{"default_user":"octocat"}`, 0o600)
	stubGH(t, "gh-token", nil)
	if _, err := resolveConfig(io.Discard); err == nil {
		t.Fatal("token-less config must be fatal, not fall through to gh")
	}
}

func TestResolveConfigNoConfigDir(t *testing.T) {
	// With HOME/XDG_CONFIG_HOME/AppData all empty, os.UserConfigDir errors;
	// the gh fallback must still be reachable (the pre-config stub worked
	// without HOME, and a minimal container must keep working).
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", "")
	t.Setenv("GIST_TOKEN", "")
	stubGH(t, "gh-token", nil)
	cfg, err := resolveConfig(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "gh-token" {
		t.Errorf("Token = %q, want the gh token", cfg.Token)
	}
}

func TestResolveConfigAllAbsent(t *testing.T) {
	setConfigDir(t)
	stubGH(t, "", errors.New("gh not authenticated"))
	_, err := resolveConfig(io.Discard)
	if err == nil || !strings.Contains(err.Error(), "GIST_TOKEN") {
		t.Errorf("err = %v, want an actionable message naming GIST_TOKEN", err)
	}
}
