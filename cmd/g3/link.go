package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

// linkNameRE is the §3 grammar from docs/004-linked-paths.md: no "/", ":" or
// leading "@", so a name can never be confused with a path or a g3:// URI.
var linkNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// linkAdd declares a link — pure bookkeeping, no network. The remote is not
// verified to exist: a typo'd gist ID surfaces on the first status, which
// keeps link add usable without credentials.
func linkAdd(name, uri, path string, stdout io.Writer) error {
	if !linkNameRE.MatchString(name) {
		return usagef("link name %q: want one or more of [A-Za-z0-9._-]", name)
	}
	loc, err := parseArg(uri)
	if err != nil {
		return err
	}
	if loc.kind != locRemote || loc.prefixForm() {
		return usagef("%q: a link needs a full g3://<gist-id>/<key> URI", uri)
	}
	if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~/") {
		return usagef("path %q: want an absolute or ~/-rooted path; a stored link has no base directory for a relative one", path)
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if _, ok := cfg.Links[name]; ok {
		return usagef("link %q already exists; run g3 link rm %s first", name, name)
	}
	links := cfg.Links
	if links == nil {
		links = map[string]gists3.Link{}
	}
	links[name] = gists3.Link{URI: loc.String(), Path: path}
	if err := saveLinks(links); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "linked: %s\n  %s\n  %s\n", name, path, loc.String())
	return nil
}

// linkLS prints one "name  path  uri" line per link, name-sorted, paths
// unexpanded as stored.
func linkLS(stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(cfg.Links))
	nameW, pathW := 0, 0
	for name, l := range cfg.Links {
		names = append(names, name)
		nameW = max(nameW, len(name))
		pathW = max(pathW, len(l.Path))
	}
	slices.Sort(names)
	for _, name := range names {
		l := cfg.Links[name]
		fmt.Fprintf(stdout, "%-*s  %-*s  %s\n", nameW, name, pathW, l.Path, l.URI)
	}
	return nil
}

// linkRM removes the declaration only. The output says what was kept because
// g3 rm (planned) deletes remote objects and lives one word away.
func linkRM(name string, stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	l, ok := cfg.Links[name]
	if !ok {
		return usagef("unknown link %q; g3 link ls shows the declared links", name)
	}
	delete(cfg.Links, name)
	if err := saveLinks(cfg.Links); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "unlinked: %s\n  kept %s\n  kept %s\n", name, l.Path, l.URI)
	return nil
}

// linkPath prints the expanded absolute path and nothing else, so
// $(g3 path <name>) is safe to interpolate. It does not check that the file
// exists; status is for that.
func linkPath(name string, stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	l, ok := cfg.Links[name]
	if !ok {
		return usagef("unknown link %q; g3 link ls shows the declared links", name)
	}
	p, err := expandPath(l.Path)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, p)
	return nil
}

// expandPath resolves a leading "~/" via os.UserHomeDir — nothing else: no
// ~user, no environment variables, no shell. Predictability beats power in a
// string that decides which file gets overwritten (docs/004 §7).
func expandPath(p string) (string, error) {
	if !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, p[2:]), nil
}

// saveLinks rewrites the links key of config.json in place. The file also
// holds default_user, base_url, and possibly fields written by a newer g3,
// so the rewrite goes through map[string]json.RawMessage — round-tripping
// the Config struct would silently drop everything it does not know about
// (docs/004 §4.3).
func saveLinks(links map[string]gists3.Link) error {
	p, err := gists3.ConfigPath()
	if err != nil {
		return err
	}
	raw := map[string]json.RawMessage{}
	switch data, err := os.ReadFile(p); {
	case err == nil:
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse config %s: %w", p, err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if len(links) == 0 {
		delete(raw, "links")
	} else {
		enc, err := json.Marshal(links)
		if err != nil {
			return err
		}
		raw["links"] = enc
	}
	return writeFileAtomic(p, raw)
}

// writeFileAtomic writes v as indented JSON, mode 0600, via temp file +
// os.Rename in the same directory, so a crash mid-write cannot leave a
// truncated file (docs/004 §4.3).
func writeFileAtomic(p string, v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}
