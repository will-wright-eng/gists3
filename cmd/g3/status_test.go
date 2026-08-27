package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runStatus drives the full command path — dispatch, config, state — and
// returns stdout.
func runStatus(t *testing.T, client clientFn, args ...string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	err := run(ctx, append([]string{"status"}, args...), client, strings.NewReader(""), &stdout, io.Discard)
	return stdout.String(), err
}

func seedBaseline(t *testing.T, name, hash string) {
	t.Helper()
	st, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	st.Baselines[name] = baseline{Hash: hash, At: time.Now().UTC()}
	if err := saveState(st); err != nil {
		t.Fatal(err)
	}
}

// TestStatusStates drives every §5.1 state through the real command path:
// config file, state file, local filesystem, and the httptest fake.
func TestStatusStates(t *testing.T) {
	const content, other = "hello\n", "other\n"
	for name, tc := range map[string]struct {
		local    string // "" = no local file
		remote   string // "" = key absent from the gist
		gistGone bool   // the whole gist 404s
		base     string // "" = no baseline
		want     string
	}{
		"missing":              {want: "missing"},
		"local-missing":        {remote: content, want: "local-missing"},
		"remote-missing":       {local: content, want: "remote-missing"},
		"dead gist absorbed":   {local: content, gistGone: true, want: "remote-missing"},
		"in-sync no baseline":  {local: content, remote: content, want: "in-sync"},
		"in-sync stale base":   {local: content, remote: content, base: sha256hex(other), want: "in-sync"},
		"diverged no baseline": {local: content, remote: other, want: "diverged"},
		"remote-ahead":         {local: content, remote: other, base: sha256hex(content), want: "remote-ahead"},
		"local-ahead":          {local: content, remote: other, base: sha256hex(other), want: "local-ahead"},
		"diverged":             {local: content, remote: other, base: sha256hex("older\n"), want: "diverged"},
	} {
		t.Run(name, func(t *testing.T) {
			setConfigDir(t)
			local := filepath.Join(t.TempDir(), "f.md")
			if err := linkAdd("l1", "g3://abc123/f.md", local, io.Discard); err != nil {
				t.Fatal(err)
			}
			if tc.local != "" {
				if err := os.WriteFile(local, []byte(tc.local), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.base != "" {
				seedBaseline(t, "l1", tc.base)
			}
			mux, client := newServer(t)
			mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
				if tc.gistGone {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				files := map[string]string{}
				if tc.remote != "" {
					files["f.md"] = tc.remote
				}
				w.Write(gistJSON(t, "abc123", files))
			})
			out, err := runStatus(t, stubClient(client), "l1")
			if err != nil {
				t.Fatal(err)
			}
			fields := strings.Fields(out)
			if len(fields) != 3 || fields[0] != tc.want || fields[1] != "l1" || fields[2] != local {
				t.Errorf("status = %q, want %q l1 %s", out, tc.want, local)
			}
		})
	}
}

func TestStatusAdoptsBaselineOnInSync(t *testing.T) {
	setConfigDir(t)
	const content = "hello\n"
	local := filepath.Join(t.TempDir(), "f.md")
	if err := linkAdd("l1", "g3://abc123/f.md", local, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	seedBaseline(t, "l1", "stale-hash")
	mux, client := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(gistJSON(t, "abc123", map[string]string{"f.md": content}))
	})
	if _, err := runStatus(t, stubClient(client), "l1"); err != nil {
		t.Fatal(err)
	}
	st, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Baselines["l1"].Hash != sha256hex(content) {
		t.Errorf("baseline = %q, want row 4 to adopt %q", st.Baselines["l1"].Hash, sha256hex(content))
	}
}

func TestStatusAllLinksSorted(t *testing.T) {
	setConfigDir(t)
	dir := t.TempDir()
	for _, n := range []string{"zshrc", "claude"} {
		if err := linkAdd(n, "g3://abc123/"+n, filepath.Join(dir, n), io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mux, client := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(gistJSON(t, "abc123", map[string]string{"claude": "hello\n"}))
	})
	out, err := runStatus(t, stubClient(client))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("status printed %d lines, want 2: %q", len(lines), out)
	}
	if f := strings.Fields(lines[0]); f[0] != "in-sync" || f[1] != "claude" {
		t.Errorf("line 0 = %q, want in-sync claude first (name-sorted)", lines[0])
	}
	if f := strings.Fields(lines[1]); f[0] != "missing" || f[1] != "zshrc" {
		t.Errorf("line 1 = %q, want missing zshrc", lines[1])
	}
}

// TestStatusAbortsOnGlobalError: a non-NotFound failure is a global
// condition, so the report stops — completed rows stay, the error returns,
// and baselines adopted before the abort are persisted (§6).
func TestStatusAbortsOnGlobalError(t *testing.T) {
	setConfigDir(t)
	const content = "hello\n"
	dir := t.TempDir()
	for n, gist := range map[string]string{"aaa": "gist-a", "bbb": "gist-b"} {
		if err := linkAdd(n, "g3://"+gist+"/f.md", filepath.Join(dir, n), io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "aaa"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	mux, client := newServer(t)
	mux.HandleFunc("GET /gists/gist-a", func(w http.ResponseWriter, r *http.Request) {
		w.Write(gistJSON(t, "gist-a", map[string]string{"f.md": content}))
	})
	mux.HandleFunc("GET /gists/gist-b", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	out, err := runStatus(t, stubClient(client))
	if err == nil {
		t.Fatal("status must abort on a non-NotFound error")
	}
	var ue *usageError
	if errors.As(err, &ue) {
		t.Errorf("err = %v, want a runtime error, not usage", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "in-sync") {
		t.Errorf("out = %q, want exactly the completed in-sync row for aaa", out)
	}
	st, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Baselines["aaa"].Hash != sha256hex(content) {
		t.Errorf("baseline for aaa = %q; adoption before the abort must persist", st.Baselines["aaa"].Hash)
	}
}

func TestStatusUnknownNameIsUsage(t *testing.T) {
	// Validation precedes client construction: exit 2 with no credentials.
	setConfigDir(t)
	_, err := runStatus(t, failingClient(errors.New("no credentials")), "nope")
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Errorf("err = %v, want *usageError", err)
	}
}

func TestStatusNoLinksNoClient(t *testing.T) {
	setConfigDir(t)
	out, err := runStatus(t, failingClient(errors.New("must not be constructed")))
	if err != nil {
		t.Fatalf("status with no links = %v, want silence and exit 0", err)
	}
	if out != "" {
		t.Errorf("out = %q, want nothing", out)
	}
}

func TestLinkRMDropsBaseline(t *testing.T) {
	setConfigDir(t)
	if err := linkAdd("claude", testURI, "/x.md", io.Discard); err != nil {
		t.Fatal(err)
	}
	seedBaseline(t, "claude", "some-hash")
	if err := linkRM("claude", io.Discard); err != nil {
		t.Fatal(err)
	}
	st, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Baselines["claude"]; ok {
		t.Error("link rm must remove the baseline with the declaration")
	}
}

func TestLinkRMDoesNotCreateStateFile(t *testing.T) {
	setConfigDir(t)
	if err := linkAdd("claude", testURI, "/x.md", io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := linkRM("claude", io.Discard); err != nil {
		t.Fatal(err)
	}
	p, err := statePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Error("link rm with no baseline must not create state.json")
	}
}
