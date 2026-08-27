package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

const testURI = "g3://b1e652a05136107f461cd796103508cc/CLAUDE.md"

func wantUsageError(t *testing.T, err error) {
	t.Helper()
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Errorf("err = %v, want *usageError", err)
	}
}

func readConfigFile(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	p, err := gists3.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"~/.claude/CLAUDE.md": filepath.Join(home, ".claude/CLAUDE.md"),
		"/abs/path.md":        "/abs/path.md",
		"~user/x.md":          "~user/x.md", // no ~user expansion, §7
		"$HOME/x.md":          "$HOME/x.md", // no env expansion, §7
	} {
		got, err := expandPath(in)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("expandPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinkAddValidation(t *testing.T) {
	dir := setConfigDir(t)
	for name, args := range map[string][3]string{
		"name with slash": {"a/b", testURI, "/x.md"},
		"name with colon": {"a:b", testURI, "/x.md"},
		"name with at":    {"@a", testURI, "/x.md"},
		"empty name":      {"", testURI, "/x.md"},
		"name with space": {"a b", testURI, "/x.md"},
		"bare bucket URI": {"n", "g3://abc123", "/x.md"},
		"prefix URI":      {"n", "g3://abc123/dir/", "/x.md"},
		"empty gist id":   {"n", "g3:///k", "/x.md"},
		"local as URI":    {"n", "notauri", "/x.md"},
		"stdio as URI":    {"n", "-", "/x.md"},
		"foreign scheme":  {"n", "s3://b/k", "/x.md"},
		"relative path":   {"n", testURI, "rel/x.md"},
		"dot path":        {"n", testURI, "./x.md"},
		"bare tilde":      {"n", testURI, "~"},
		"tilde user path": {"n", testURI, "~user/x.md"},
		"env-shaped path": {"n", testURI, "$HOME/x.md"},
	} {
		t.Run(name, func(t *testing.T) {
			wantUsageError(t, linkAdd(args[0], args[1], args[2], io.Discard))
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "gists3", "config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused link add must not create the config file")
	}
}

func TestLinkAddRoundTrip(t *testing.T) {
	setConfigDir(t)
	var out bytes.Buffer
	if err := linkAdd("claude", testURI, "~/.claude/CLAUDE.md", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "linked: claude") {
		t.Errorf("add output = %q, want a linked: confirmation", out.String())
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := gists3.Link{URI: testURI, Path: "~/.claude/CLAUDE.md"}
	if cfg.Links["claude"] != want {
		t.Errorf("Links[claude] = %+v, want %+v (path stored unexpanded)", cfg.Links["claude"], want)
	}
}

func TestLinkAddDuplicateName(t *testing.T) {
	setConfigDir(t)
	if err := linkAdd("claude", testURI, "/x.md", io.Discard); err != nil {
		t.Fatal(err)
	}
	wantUsageError(t, linkAdd("claude", testURI, "/y.md", io.Discard))
}

func TestLinkRewritePreservesUnknownKeys(t *testing.T) {
	// §4.3: the rewrite is load-bearing — it must not disturb default_user,
	// base_url, or fields written by a newer g3.
	writeConfig(t, `{"default_user":"octocat","base_url":"https://ghe.example/api/v3","future":{"keep":true}}`)
	if err := linkAdd("claude", testURI, "~/.claude/CLAUDE.md", io.Discard); err != nil {
		t.Fatal(err)
	}
	raw := readConfigFile(t)
	if string(raw["default_user"]) != `"octocat"` {
		t.Errorf("default_user = %s, want it untouched", raw["default_user"])
	}
	if string(raw["base_url"]) != `"https://ghe.example/api/v3"` {
		t.Errorf("base_url = %s, want it untouched", raw["base_url"])
	}
	if !strings.Contains(string(raw["future"]), "keep") {
		t.Errorf("future = %s, want the unknown key preserved", raw["future"])
	}
	var rm bytes.Buffer
	if err := linkRM("claude", &rm); err != nil {
		t.Fatal(err)
	}
	raw = readConfigFile(t)
	if _, ok := raw["links"]; ok {
		t.Error("links key must be dropped once the last link is removed")
	}
	if string(raw["default_user"]) != `"octocat"` || !strings.Contains(string(raw["future"]), "keep") {
		t.Errorf("config after rm = %v, want unknown keys still intact", raw)
	}
	for _, kept := range []string{"unlinked: claude", "kept ~/.claude/CLAUDE.md", "kept " + testURI} {
		if !strings.Contains(rm.String(), kept) {
			t.Errorf("rm output = %q, want %q", rm.String(), kept)
		}
	}
}

func TestLinkRMUnknown(t *testing.T) {
	setConfigDir(t)
	wantUsageError(t, linkRM("nope", io.Discard))
}

func TestLinkLSSortedByName(t *testing.T) {
	setConfigDir(t)
	for _, name := range []string{"zshrc", "claude", "gitcfg"} {
		if err := linkAdd(name, testURI, "/"+name, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := linkLS(&out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("ls printed %d lines, want 3: %q", len(lines), out.String())
	}
	for i, name := range []string{"claude", "gitcfg", "zshrc"} {
		fields := strings.Fields(lines[i])
		if len(fields) != 3 || fields[0] != name || fields[1] != "/"+name || fields[2] != testURI {
			t.Errorf("line %d = %q, want %q, its path, and its URI", i, lines[i], name)
		}
	}
}

func TestLinkLSEmpty(t *testing.T) {
	setConfigDir(t)
	var out bytes.Buffer
	if err := linkLS(&out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("ls on no links printed %q, want nothing", out.String())
	}
}

func TestLinkPath(t *testing.T) {
	setConfigDir(t)
	if err := linkAdd("claude", testURI, "~/.claude/CLAUDE.md", io.Discard); err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := linkPath("claude", &out); err != nil {
		t.Fatal(err)
	}
	// Exactly the expanded path and a newline: $(g3 path claude) is
	// load-bearing, so stdout purity is the contract, not a nicety.
	if want := filepath.Join(home, ".claude/CLAUDE.md") + "\n"; out.String() != want {
		t.Errorf("path output = %q, want exactly %q", out.String(), want)
	}
}

func TestLinkPathUnknown(t *testing.T) {
	setConfigDir(t)
	wantUsageError(t, linkPath("nope", io.Discard))
}

func TestLinkMalformedConfigIsFatalNotUsage(t *testing.T) {
	writeConfig(t, `{not json`)
	for name, err := range map[string]error{
		"add":  linkAdd("n", testURI, "/x.md", io.Discard),
		"ls":   linkLS(io.Discard),
		"rm":   linkRM("n", io.Discard),
		"path": linkPath("n", io.Discard),
	} {
		var ue *usageError
		if err == nil || errors.As(err, &ue) {
			t.Errorf("%s on malformed config = %v, want a non-usage runtime error", name, err)
		}
	}
}
