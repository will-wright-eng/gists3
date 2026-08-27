package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// TestResolveTable asserts every row of the §5.1 table, in table order, plus
// the precedence cases the prose calls out: row 1 beats a stray baseline,
// and row 4 wins regardless of what the baseline says or whether one exists.
func TestResolveTable(t *testing.T) {
	const h1, h2, h3 = "h1", "h2", "h3"
	for _, c := range []struct {
		row     int
		l, r, b string
		want    syncState
	}{
		{1, "", "", "", stateMissing},
		{1, "", "", h1, stateMissing},
		{2, "", h1, "", stateLocalMissing},
		{2, "", h1, h1, stateLocalMissing},
		{3, h1, "", "", stateRemoteMissing},
		{3, h1, "", h1, stateRemoteMissing},
		{4, h1, h1, "", stateInSync},
		{4, h1, h1, h1, stateInSync},
		{4, h1, h1, h2, stateInSync},
		{5, h1, h2, "", stateDiverged},
		{6, h1, h2, h1, stateRemoteAhead},
		{7, h1, h2, h2, stateLocalAhead},
		{8, h1, h2, h3, stateDiverged},
	} {
		if got := resolve(c.l, c.r, c.b); got != c.want {
			t.Errorf("row %d: resolve(%q, %q, %q) = %v, want %v", c.row, c.l, c.r, c.b, got, c.want)
		}
	}
}

func TestSyncStateLabels(t *testing.T) {
	for state, want := range map[syncState]string{
		stateMissing:       "missing",
		stateLocalMissing:  "local-missing",
		stateRemoteMissing: "remote-missing",
		stateInSync:        "in-sync",
		stateDiverged:      "diverged",
		stateRemoteAhead:   "remote-ahead",
		stateLocalAhead:    "local-ahead",
	} {
		if state.String() != want {
			t.Errorf("String() = %q, want %q", state.String(), want)
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	setConfigDir(t)
	st, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != stateVersion || len(st.Baselines) != 0 {
		t.Fatalf("fresh state = %+v, want empty version-%d state", st, stateVersion)
	}
	st.Baselines["claude"] = baseline{Hash: sha256hex("hello"), At: time.Now().UTC()}
	if err := saveState(st); err != nil {
		t.Fatal(err)
	}
	got, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if got.Baselines["claude"].Hash != sha256hex("hello") {
		t.Errorf("Baselines[claude] = %+v, want the saved hash", got.Baselines["claude"])
	}
	if got.Baselines["claude"].At.IsZero() {
		t.Error("At was not persisted")
	}
	if runtime.GOOS != "windows" {
		p, err := statePath()
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("state.json mode = %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestStateUnknownVersionRefused(t *testing.T) {
	setConfigDir(t)
	for name, content := range map[string]string{
		"newer version":   `{"version":2,"baselines":{}}`,
		"missing version": `{"baselines":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, err := statePath()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadState(); err == nil || !strings.Contains(err.Error(), "version") {
				t.Errorf("loadState = %v, want a version refusal", err)
			}
		})
	}
}

func TestHashLocal(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.md")
	if err := os.WriteFile(p, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := hashLocal(p)
	if err != nil {
		t.Fatal(err)
	}
	if h != sha256hex("hello") {
		t.Errorf("hashLocal = %q, want the hex SHA-256 comparable to GetObjectOutput.ETag", h)
	}
	h, err = hashLocal(filepath.Join(dir, "absent"))
	if err != nil || h != "" {
		t.Errorf(`hashLocal(absent) = %q, %v; want "", nil`, h, err)
	}
}
