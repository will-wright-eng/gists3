package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

// clientFn is the seam between run and client construction; tests substitute
// one returning an httptest-backed client.
type clientFn func(stderr io.Writer) (*gists3.Client, error)

// ghAuthToken shells out to the gh CLI's stored credentials. A package var
// so tests can stub the exec away.
var ghAuthToken = func() (string, error) {
	out, err := exec.Command("gh", "auth", "token").Output()
	return strings.TrimSpace(string(out)), err
}

func newClient(stderr io.Writer) (*gists3.Client, error) {
	cfg, err := resolveConfig(stderr)
	if err != nil {
		return nil, err
	}
	return gists3.NewFromConfig(cfg), nil
}

// resolveConfig picks the identity for this invocation: GIST_TOKEN when set
// to a non-empty value, then the gists3 config file, then `gh auth token`.
// The layer that supplies the token supplies the whole identity — with
// GIST_TOKEN set the config file is never consulted, so its base_url does
// not apply. Only an absent config (no file, or an unresolvable config dir,
// e.g. $HOME unset) falls through to gh; a malformed or token-less file is
// fatal so a typo cannot silently switch identity.
func resolveConfig(stderr io.Writer) (*gists3.Config, error) {
	// Trimmed like the gh token: env files with CRLF endings would otherwise
	// yield a control character the HTTP client rejects opaquely, and a
	// whitespace-only value would shadow a working config or gh login.
	if token := strings.TrimSpace(os.Getenv("GIST_TOKEN")); token != "" {
		return &gists3.Config{Token: token}, nil
	}
	if _, err := os.UserConfigDir(); err == nil {
		cfg, err := gists3.LoadConfig()
		if err == nil {
			for _, w := range cfg.Warnings {
				fmt.Fprintln(stderr, "g3: warning:", w)
			}
			return cfg, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	if token, err := ghAuthToken(); err == nil && token != "" {
		return &gists3.Config{Token: token}, nil
	}
	return nil, errors.New("no credentials: set GIST_TOKEN to a GitHub personal access token with the gist scope, add a token to the gists3 config file, or authenticate the gh CLI (gh auth login)")
}
