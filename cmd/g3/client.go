package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"strings"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

// clientFn is the seam between run and client construction; tests substitute
// one returning an httptest-backed client.
type clientFn func() (*gists3.Client, error)

// ghAuthToken shells out to the gh CLI's stored credentials. A package var
// so tests can stub the exec away.
var ghAuthToken = func() (string, error) {
	out, err := exec.Command("gh", "auth", "token").Output()
	return strings.TrimSpace(string(out)), err
}

func newClient() (*gists3.Client, error) {
	token, err := resolveToken()
	if err != nil {
		return nil, err
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	var opts []gists3.Option
	if cfg.BaseURL != "" {
		opts = append(opts, gists3.WithBaseURL(cfg.BaseURL))
	}
	return gists3.New(token, opts...), nil
}

// resolveToken picks the identity for this invocation: GIST_TOKEN when set
// to a non-empty value, then `gh auth token`. The config file never supplies
// identity — it holds no secrets, which is what lets links live in it
// (docs/004-linked-paths.md §8) — so base_url applies whichever layer wins.
func resolveToken() (string, error) {
	// Trimmed like the gh token: env files with CRLF endings would otherwise
	// yield a control character the HTTP client rejects opaquely, and a
	// whitespace-only value would shadow a working gh login.
	if token := strings.TrimSpace(os.Getenv("GIST_TOKEN")); token != "" {
		return token, nil
	}
	if token, err := ghAuthToken(); err == nil && token != "" {
		return token, nil
	}
	return "", errors.New("no credentials: set GIST_TOKEN to a GitHub personal access token with the gist scope, or authenticate the gh CLI (gh auth login)")
}

// loadConfig reads the optional config file for its non-identity settings.
// An absent file — or an unresolvable config dir, e.g. $HOME unset — is a
// zero Config. A malformed file is fatal: silently ignoring it would drop
// base_url and send the bearer token to api.github.com instead of the host
// the user configured.
func loadConfig() (*gists3.Config, error) {
	if _, err := os.UserConfigDir(); err != nil {
		return &gists3.Config{}, nil
	}
	cfg, err := gists3.LoadConfig()
	if errors.Is(err, fs.ErrNotExist) {
		return &gists3.Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	return cfg, nil
}
