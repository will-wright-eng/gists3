package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

// fakeGist is a mutable gist behind the mux: GET serves the current files,
// PATCH merges edits into them, so pull/push round trips observe their own
// writes and "external" edits are a set() away.
type fakeGist struct {
	t     *testing.T
	id    string
	mu    sync.Mutex
	files map[string]string
	gone  bool
}

func serveGist(t *testing.T, mux *http.ServeMux, id string, files map[string]string) *fakeGist {
	t.Helper()
	if files == nil {
		files = map[string]string{}
	}
	fg := &fakeGist{t: t, id: id, files: files}
	mux.HandleFunc("GET /gists/"+id, func(w http.ResponseWriter, r *http.Request) {
		fg.mu.Lock()
		defer fg.mu.Unlock()
		if fg.gone {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(gistJSON(t, id, fg.files))
	})
	mux.HandleFunc("PATCH /gists/"+id, func(w http.ResponseWriter, r *http.Request) {
		fg.mu.Lock()
		defer fg.mu.Unlock()
		if fg.gone {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body patchBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode PATCH: %v", err)
		}
		for k, f := range body.Files {
			fg.files[k] = f.Content
		}
		w.Write(gistJSON(t, id, fg.files))
	})
	return fg
}

func (f *fakeGist) set(key, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[key] = content
}

func (f *fakeGist) get(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[key]
}

func runG3(t *testing.T, client *gists3.Client, args ...string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	err := run(ctx, args, stubClient(client), strings.NewReader(""), &stdout, io.Discard)
	return stdout.String(), err
}

func mustRunG3(t *testing.T, client *gists3.Client, args ...string) string {
	t.Helper()
	out, err := runG3(t, client, args...)
	if err != nil {
		t.Fatalf("g3 %v: %v", args, err)
	}
	return out
}

func baselineHash(t *testing.T, name string) string {
	t.Helper()
	st, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	return st.Baselines[name].Hash
}

// TestPullPushLifecycle is §11's acceptance walk: create by pull, no-op,
// edit-and-push, external-edit-and-pull, diverge into double refusal, and
// recover through g3 cp with no repair command.
func TestPullPushLifecycle(t *testing.T) {
	setConfigDir(t)
	// The parent directory does not exist yet: the first pull must create it.
	local := filepath.Join(t.TempDir(), "sub", "CLAUDE.md")
	if err := linkAdd("claude", "g3://abc123/CLAUDE.md", local, io.Discard); err != nil {
		t.Fatal(err)
	}
	mux, client := newServer(t)
	gist := serveGist(t, mux, "abc123", map[string]string{"CLAUDE.md": "v1\n"})

	out := mustRunG3(t, client, "pull", "claude")
	if want := "pull: g3://abc123/CLAUDE.md to " + local + "\n"; out != want {
		t.Errorf("pull output = %q, want %q", out, want)
	}
	if b, err := os.ReadFile(local); err != nil || string(b) != "v1\n" {
		t.Fatalf("local after pull = %q, %v; want v1", b, err)
	}
	if h := baselineHash(t, "claude"); h != sha256hex("v1\n") {
		t.Errorf("baseline = %q, want the hash of the transferred bytes", h)
	}

	if out := mustRunG3(t, client, "pull", "claude"); out != "in-sync: claude\n" {
		t.Errorf("second pull = %q, want the in-sync no-op", out)
	}

	if err := os.WriteFile(local, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = mustRunG3(t, client, "push", "claude")
	if want := "push: " + local + " to g3://abc123/CLAUDE.md\n"; out != want {
		t.Errorf("push output = %q, want %q", out, want)
	}
	if got := gist.get("CLAUDE.md"); got != "v2\n" {
		t.Errorf("remote after push = %q, want v2", got)
	}
	if h := baselineHash(t, "claude"); h != sha256hex("v2\n") {
		t.Errorf("baseline = %q, want the pushed bytes' hash", h)
	}

	gist.set("CLAUDE.md", "v3\n")
	if out := mustRunG3(t, client, "status", "claude"); !strings.HasPrefix(out, "remote-ahead") {
		t.Errorf("status = %q, want remote-ahead", out)
	}
	mustRunG3(t, client, "pull", "claude")
	if b, _ := os.ReadFile(local); string(b) != "v3\n" {
		t.Errorf("local after pull = %q, want v3", b)
	}

	// Both sides move: refusal in both directions, nothing changes.
	if err := os.WriteFile(local, []byte("L\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gist.set("CLAUDE.md", "R\n")
	for _, cmd := range []string{"pull", "push"} {
		_, err := runG3(t, client, cmd, "claude")
		if err == nil || !strings.Contains(err.Error(), "diverged") {
			t.Fatalf("%s on diverged = %v, want a refusal naming the state", cmd, err)
		}
	}
	if b, _ := os.ReadFile(local); string(b) != "L\n" {
		t.Errorf("local after refusals = %q; a refusal must change nothing", b)
	}
	if got := gist.get("CLAUDE.md"); got != "R\n" {
		t.Errorf("remote after refusals = %q; a refusal must change nothing", got)
	}
	if h := baselineHash(t, "claude"); h != sha256hex("v3\n") {
		t.Errorf("baseline = %q; a refusal must not move it", h)
	}

	// §5.2 recovery: pick a winner with cp, then row 4 heals on status.
	mustRunG3(t, client, "cp", local, "g3://abc123/CLAUDE.md")
	if out := mustRunG3(t, client, "status", "claude"); !strings.HasPrefix(out, "in-sync") {
		t.Errorf("status after reconcile = %q, want in-sync", out)
	}
	if out := mustRunG3(t, client, "push", "claude"); out != "in-sync: claude\n" {
		t.Errorf("push after heal = %q, want the no-op", out)
	}
}

func TestPullRefusals(t *testing.T) {
	const content, other = "hello\n", "other\n"
	for name, tc := range map[string]struct {
		local, remote, base string
		wantState, wantWay  string
	}{
		"remote-missing":       {local: content, wantState: "remote-missing", wantWay: "g3 push claude"},
		"local-ahead":          {local: content, remote: other, base: sha256hex(other), wantState: "local-ahead", wantWay: "g3 push claude"},
		"diverged":             {local: content, remote: other, base: "stale", wantState: "diverged", wantWay: "g3 cp"},
		"diverged no baseline": {local: content, remote: other, wantState: "diverged", wantWay: "g3 cp"},
	} {
		t.Run(name, func(t *testing.T) {
			gist, client, local := setupSync(t, tc.local, tc.remote, tc.base)
			_, err := runG3(t, client, "pull", "claude")
			assertRefusal(t, err, tc.wantState, tc.wantWay)
			if b, _ := os.ReadFile(local); string(b) != tc.local {
				t.Errorf("local = %q, want untouched %q", b, tc.local)
			}
			if got := gist.get("f.md"); got != tc.remote {
				t.Errorf("remote = %q, want untouched %q", got, tc.remote)
			}
			if h := baselineHash(t, "claude"); h != tc.base {
				t.Errorf("baseline = %q, want untouched %q", h, tc.base)
			}
		})
	}
}

func TestPushRefusals(t *testing.T) {
	const content, other = "hello\n", "other\n"
	for name, tc := range map[string]struct {
		local, remote, base string
		wantState, wantWay  string
	}{
		"local-missing":        {remote: content, wantState: "local-missing", wantWay: "g3 pull claude"},
		"remote-ahead":         {local: content, remote: other, base: sha256hex(content), wantState: "remote-ahead", wantWay: "g3 pull claude"},
		"diverged":             {local: content, remote: other, base: "stale", wantState: "diverged", wantWay: "g3 cp"},
		"diverged no baseline": {local: content, remote: other, wantState: "diverged", wantWay: "g3 cp"},
	} {
		t.Run(name, func(t *testing.T) {
			gist, client, local := setupSync(t, tc.local, tc.remote, tc.base)
			_, err := runG3(t, client, "push", "claude")
			assertRefusal(t, err, tc.wantState, tc.wantWay)
			if got := gist.get("f.md"); got != tc.remote {
				t.Errorf("remote = %q, want untouched %q", got, tc.remote)
			}
			if tc.local == "" {
				if _, err := os.Stat(local); !errors.Is(err, os.ErrNotExist) {
					t.Error("push must not create the local file")
				}
			}
		})
	}
}

// setupSync declares one link "claude" → g3://abc123/f.md and arranges the
// three §5 hashes: "" means that side or the baseline is absent.
func setupSync(t *testing.T, local, remote, base string) (*fakeGist, *gists3.Client, string) {
	t.Helper()
	setConfigDir(t)
	path := filepath.Join(t.TempDir(), "f.md")
	if err := linkAdd("claude", "g3://abc123/f.md", path, io.Discard); err != nil {
		t.Fatal(err)
	}
	if local != "" {
		if err := os.WriteFile(path, []byte(local), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if base != "" {
		seedBaseline(t, "claude", base)
	}
	mux, client := newServer(t)
	files := map[string]string{}
	if remote != "" {
		files["f.md"] = remote
	}
	gist := serveGist(t, mux, "abc123", files)
	return gist, client, path
}

func assertRefusal(t *testing.T, err error, state, wayOut string) {
	t.Helper()
	if err == nil {
		t.Fatal("want a refusal, got success")
	}
	var ue *usageError
	if errors.As(err, &ue) {
		t.Errorf("err = %v, want a runtime refusal (exit 1), not usage", err)
	}
	for _, want := range []string{"refused", state, wayOut} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
}

func TestPullPushBothMissing(t *testing.T) {
	_, client, _ := setupSync(t, "", "", "")
	for _, cmd := range []string{"pull", "push"} {
		_, err := runG3(t, client, cmd, "claude")
		if err == nil || !strings.Contains(err.Error(), "both missing") {
			t.Errorf("%s = %v, want the row-1 both-missing error", cmd, err)
		}
	}
}

// TestPushNonUTF8MatchesCP is §11's guard-parity acceptance: the same bytes
// to the same destination must get the same answer from push and cp.
func TestPushNonUTF8MatchesCP(t *testing.T) {
	gist, client, local := setupSync(t, "", "", "")
	if err := os.WriteFile(local, []byte{0xff, 0xfe, 0xfd}, 0o600); err != nil {
		t.Fatal(err)
	}
	_, pushErr := runG3(t, client, "push", "claude")
	_, cpErr := runG3(t, client, "cp", local, "g3://abc123/f.md")
	if pushErr == nil || cpErr == nil {
		t.Fatalf("push = %v, cp = %v; both must refuse non-UTF-8", pushErr, cpErr)
	}
	if pushErr.Error() != cpErr.Error() {
		t.Errorf("push error %q != cp error %q; push must give cp's own guard error", pushErr, cpErr)
	}
	if got := gist.get("f.md"); got != "" {
		t.Errorf("remote = %q; a guard rejection must not upload", got)
	}
	if h := baselineHash(t, "claude"); h != "" {
		t.Errorf("baseline = %q; a guard rejection must not move it", h)
	}
}

func TestPushDeadGistFailsWithAPINotFound(t *testing.T) {
	gist, client, _ := setupSync(t, "hello\n", "", "")
	gist.gone = true
	_, err := runG3(t, client, "push", "claude")
	var nf *gists3.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want the API's own *NotFoundError (§5.1 row 3 note)", err)
	}
	if strings.Contains(err.Error(), "refused") {
		t.Errorf("err = %q; a dead gist is an API failure, not a refusal", err)
	}
}

func TestPullKeepsExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are synthetic on windows")
	}
	const content, other = "hello\n", "other\n"
	_, client, local := setupSync(t, content, other, sha256hex(content)) // remote-ahead
	mustRunG3(t, client, "pull", "claude")
	if b, _ := os.ReadFile(local); string(b) != other {
		t.Fatalf("local = %q, want the pulled content", b)
	}
	fi, err := os.Stat(local)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want the existing 0600 kept", fi.Mode().Perm())
	}
}

func TestPullPushUnknownNameIsUsage(t *testing.T) {
	setConfigDir(t)
	for _, cmd := range []string{"pull", "push"} {
		var stdout bytes.Buffer
		err := run(ctx, []string{cmd, "nope"}, failingClient(errors.New("must not be constructed")), strings.NewReader(""), &stdout, io.Discard)
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%s nope = %v, want *usageError before any client exists", cmd, err)
		}
	}
}
