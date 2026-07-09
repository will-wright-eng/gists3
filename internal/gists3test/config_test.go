package gists3test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/will-wright-eng/gists3"
)

// writeConfig points os.UserConfigDir at a temp dir (covering the Linux,
// macOS, and Windows lookup paths) and writes a config file there. The mode
// is applied with Chmod so the test is immune to umask.
func writeConfig(t *testing.T, content string, mode os.FileMode) string {
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
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfig(t *testing.T) {
	writeConfig(t, `{"default_user":"octocat","token":"ghp_test","base_url":"https://ghe.example/api/v3"}`, 0o600)
	cfg, err := gists3.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultUser != "octocat" || cfg.Token != "ghp_test" || cfg.BaseURL != "https://ghe.example/api/v3" {
		t.Errorf("cfg = %+v", cfg)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("unexpected warnings for a 0600 file: %v", cfg.Warnings)
	}
}

func TestLoadConfigMissingToken(t *testing.T) {
	writeConfig(t, `{"default_user":"octocat"}`, 0o600)
	if _, err := gists3.LoadConfig(); err == nil {
		t.Fatal("want error for config without token")
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))
	t.Setenv("AppData", filepath.Join(tmp, "appdata"))
	if _, err := gists3.LoadConfig(); err == nil {
		t.Fatal("want error for missing config file")
	}
}

func TestLoadConfigBadJSON(t *testing.T) {
	writeConfig(t, `{not json`, 0o600)
	if _, err := gists3.LoadConfig(); err == nil {
		t.Fatal("want error for malformed config")
	}
}

func TestLoadConfigPermissionWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are synthetic on windows")
	}
	writeConfig(t, `{"token":"ghp_test"}`, 0o644)
	cfg, err := gists3.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "chmod 600") {
		t.Errorf("Warnings = %v, want one chmod-600 warning", cfg.Warnings)
	}
}

func TestNewFromConfigUsesConfigBaseURL(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		if got := r.Header.Get("Authorization"); got != "Bearer cfg-token" {
			t.Errorf("Authorization = %q, want config token", got)
		}
		w.Write([]byte(`{"id":"x","files":{}}`))
	}))
	t.Cleanup(srv.Close)
	client := gists3.NewFromConfig(&gists3.Config{Token: "cfg-token", BaseURL: srv.URL})
	if _, err := client.HeadBucket(ctx, &gists3.HeadBucketInput{Bucket: "x"}); err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Error("request did not reach the config base_url server")
	}
}

func TestNewFromConfigOptionBeatsConfig(t *testing.T) {
	hit := ""
	newSrv := func(name string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hit = name
			w.Write([]byte(`{"id":"x","files":{}}`))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	cfgSrv, optSrv := newSrv("config"), newSrv("option")
	client := gists3.NewFromConfig(
		&gists3.Config{Token: "t", BaseURL: cfgSrv.URL},
		gists3.WithBaseURL(optSrv.URL),
	)
	if _, err := client.HeadBucket(ctx, &gists3.HeadBucketInput{Bucket: "x"}); err != nil {
		t.Fatal(err)
	}
	if hit != "option" {
		t.Errorf("request hit the %q server; WithBaseURL must override config", hit)
	}
}
