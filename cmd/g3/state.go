package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

// syncState is the outcome of the §5.1 resolution table
// (docs/004-linked-paths.md).
type syncState int

const (
	// stateMissing is row 1 — both sides gone. The table leaves it
	// unlabeled; pull and push treat it as a plain error.
	stateMissing syncState = iota
	stateLocalMissing
	stateRemoteMissing
	stateInSync
	stateDiverged
	stateRemoteAhead
	stateLocalAhead
)

// String returns §5.1's hyphenated labels — greppable, fixed vocabulary.
func (s syncState) String() string {
	switch s {
	case stateMissing:
		return "missing"
	case stateLocalMissing:
		return "local-missing"
	case stateRemoteMissing:
		return "remote-missing"
	case stateInSync:
		return "in-sync"
	case stateDiverged:
		return "diverged"
	case stateRemoteAhead:
		return "remote-ahead"
	case stateLocalAhead:
		return "local-ahead"
	}
	return "unknown"
}

// resolve is the §5.1 table, evaluated top to bottom, first match wins. The
// inputs are content hashes with "" meaning absent: l local, r remote, b the
// baseline. l == r means in-sync regardless of the baseline — that one rule
// covers the first status after link add, a baseline lost to a rename, and
// the §5.2 recovery path.
func resolve(l, r, b string) syncState {
	switch {
	case l == "" && r == "":
		return stateMissing
	case l == "":
		return stateLocalMissing
	case r == "":
		return stateRemoteMissing
	case l == r:
		return stateInSync
	case b == "":
		return stateDiverged
	case b == l:
		return stateRemoteAhead
	case b == r:
		return stateLocalAhead
	default:
		return stateDiverged
	}
}

// stateFile is state.json (docs/004 §4.2): baselines keyed by link name,
// owned by g3, never hand-edited, and never shared — a baseline describes
// the working copy on this machine. Unlike config.json it carries a version
// from the start.
type stateFile struct {
	Version   int                 `json:"version"`
	Baselines map[string]baseline `json:"baselines"`
}

// baseline records what both sides last agreed on. Hash is the hex SHA-256
// the engine computes as GetObjectOutput.ETag, so local and remote hashes
// compare directly. At is informational only — it never enters a decision.
type baseline struct {
	Hash string    `json:"hash"`
	At   time.Time `json:"at"`
}

const stateVersion = 1

func statePath() (string, error) {
	p, err := gists3.ConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "state.json"), nil
}

// loadState reads state.json; an absent file is an empty version-1 state. A
// version this g3 does not know is refused rather than guessed at — a
// missing version field reads as 0 and is refused the same way.
func loadState() (*stateFile, error) {
	p, err := statePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return &stateFile{Version: stateVersion, Baselines: map[string]baseline{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var st stateFile
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", p, err)
	}
	if st.Version != stateVersion {
		return nil, fmt.Errorf("state %s: version %d, but this g3 understands only version %d — upgrade g3 rather than editing the file", p, st.Version, stateVersion)
	}
	if st.Baselines == nil {
		st.Baselines = map[string]baseline{}
	}
	return &st, nil
}

func saveState(st *stateFile) error {
	p, err := statePath()
	if err != nil {
		return err
	}
	return writeFileAtomic(p, st)
}

// hashLocal returns the hex SHA-256 of the file's content, or "" when the
// file does not exist. Content hashing, not mtime: editors rewrite files and
// touch mtimes without changing bytes (§5).
func hashLocal(path string) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fetchRemote returns the remote content and its hash — the ETag the engine
// computes from those same bytes — or (nil, "") when the key is missing. A
// vanished gist reports the same way: §5.1 row 3 deliberately absorbs both
// *NotFoundError shapes. The body comes back so pull can write the exact
// bytes the state was resolved against, and its baseline with them.
func fetchRemote(ctx context.Context, client *gists3.Client, loc location) ([]byte, string, error) {
	out, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: loc.bucket, Key: loc.key})
	var nf *gists3.NotFoundError
	if errors.As(err, &nf) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, "", err
	}
	return body, out.ETag, nil
}
