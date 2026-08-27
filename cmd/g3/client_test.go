package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/will-wright-eng/gists3/internal/gists3"
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

// writeConfig writes a config file at the temp config path.
func writeConfig(t *testing.T, content string) {
	t.Helper()
	dir := filepath.Join(setConfigDir(t), "gists3")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o600); err != nil {
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

// authServer returns a fake GitHub that records the Authorization header of
// the last request, for asserting which identity layer won.
func authServer(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"id":"x","files":{}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

func TestResolveTokenEnvWins(t *testing.T) {
	setConfigDir(t)
	t.Setenv("GIST_TOKEN", "env-token")
	stubGH(t, "", errors.New("must not be called"))
	token, err := resolveToken()
	if err != nil {
		t.Fatal(err)
	}
	if token != "env-token" {
		t.Errorf("token = %q, want the GIST_TOKEN value", token)
	}
}

func TestResolveTokenTrimsEnv(t *testing.T) {
	// CRLF env files yield "tok\r", which the HTTP client would reject
	// opaquely; whitespace-only must behave as unset instead of shadowing
	// the working gh login below.
	setConfigDir(t)
	stubGH(t, "gh-token", nil)
	t.Setenv("GIST_TOKEN", "env-token\r\n")
	token, err := resolveToken()
	if err != nil {
		t.Fatal(err)
	}
	if token != "env-token" {
		t.Errorf("token = %q, want the trimmed value", token)
	}
	t.Setenv("GIST_TOKEN", " \n")
	if token, err = resolveToken(); err != nil {
		t.Fatal(err)
	}
	if token != "gh-token" {
		t.Errorf("token = %q; whitespace-only GIST_TOKEN must behave as unset", token)
	}
}

func TestResolveTokenGHFallback(t *testing.T) {
	setConfigDir(t)
	stubGH(t, "gh-token", nil)
	token, err := resolveToken()
	if err != nil {
		t.Fatal(err)
	}
	if token != "gh-token" {
		t.Errorf("token = %q, want the gh token", token)
	}
}

func TestResolveTokenAllAbsent(t *testing.T) {
	setConfigDir(t)
	stubGH(t, "", errors.New("gh not authenticated"))
	_, err := resolveToken()
	if err == nil || !strings.Contains(err.Error(), "GIST_TOKEN") {
		t.Errorf("err = %v, want an actionable message naming GIST_TOKEN", err)
	}
}

func TestNewClientBaseURLAppliesWithEnvToken(t *testing.T) {
	// The inversion of the pre-004 contract: GIST_TOKEN used to suppress the
	// whole config file, base_url included. Identity and endpoint are now
	// independent layers.
	srv, auth := authServer(t)
	writeConfig(t, `{"base_url":"`+srv.URL+`"}`)
	t.Setenv("GIST_TOKEN", "env-token")
	stubGH(t, "", errors.New("must not be called"))
	client, err := newClient()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.HeadBucket(ctx, &gists3.HeadBucketInput{Bucket: "x"}); err != nil {
		t.Fatal(err)
	}
	if *auth != "Bearer env-token" {
		t.Errorf("Authorization = %q, want the GIST_TOKEN identity at the config base_url", *auth)
	}
}

func TestNewClientBaseURLAppliesWithGHToken(t *testing.T) {
	srv, auth := authServer(t)
	writeConfig(t, `{"base_url":"`+srv.URL+`"}`)
	stubGH(t, "gh-token", nil)
	client, err := newClient()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.HeadBucket(ctx, &gists3.HeadBucketInput{Bucket: "x"}); err != nil {
		t.Fatal(err)
	}
	if *auth != "Bearer gh-token" {
		t.Errorf("Authorization = %q, want the gh identity at the config base_url", *auth)
	}
}

func TestNewClientTokenlessConfigIsFine(t *testing.T) {
	// The inversion of the pre-004 contract: a config file without a token
	// used to be fatal. There is no token field left to be absent.
	writeConfig(t, `{"default_user":"octocat"}`)
	stubGH(t, "gh-token", nil)
	if _, err := newClient(); err != nil {
		t.Fatalf("token-less config must not be fatal: %v", err)
	}
}

func TestNewClientMalformedConfigIsFatal(t *testing.T) {
	// Ignoring a malformed file would silently drop base_url and send the
	// token to api.github.com instead of the configured host.
	writeConfig(t, `{not json`)
	stubGH(t, "gh-token", nil)
	if _, err := newClient(); err == nil {
		t.Fatal("malformed config must be fatal, not silently ignored")
	}
}

func TestNewClientNoConfigFile(t *testing.T) {
	setConfigDir(t)
	stubGH(t, "gh-token", nil)
	if _, err := newClient(); err != nil {
		t.Fatalf("absent config must not be fatal: %v", err)
	}
}

func TestNewClientNoConfigDir(t *testing.T) {
	// With HOME/XDG_CONFIG_HOME/AppData all empty, os.UserConfigDir errors;
	// the gh fallback must still be reachable (the pre-config stub worked
	// without HOME, and a minimal container must keep working).
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", "")
	t.Setenv("GIST_TOKEN", "")
	stubGH(t, "gh-token", nil)
	if _, err := newClient(); err != nil {
		t.Fatalf("unresolvable config dir must not be fatal: %v", err)
	}
}
