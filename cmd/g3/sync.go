package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

// linkSync is one named link resolved and ready to act on.
type linkSync struct {
	name   string
	link   gists3.Link
	state  *stateFile
	client *gists3.Client
	ls     *linkStatus
}

// loadLink resolves a named link end to end — config, state, client, both
// hashes, §5.1 state — validating the name before the client is constructed
// so an unknown link exits 2 even without credentials.
func loadLink(ctx context.Context, newClient clientFn, name string) (*linkSync, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	l, err := lookupLink(cfg, name)
	if err != nil {
		return nil, err
	}
	st, err := loadState()
	if err != nil {
		return nil, err
	}
	client, err := newClient()
	if err != nil {
		return nil, err
	}
	ls, err := resolveLink(ctx, client, l, st.Baselines[name].Hash)
	if err != nil {
		return nil, err
	}
	return &linkSync{name: name, link: l, state: st, client: client, ls: ls}, nil
}

// adopt records hash as the link's baseline. Callers pass the hash of the
// bytes they just transferred, never one from a confirming re-read — the
// backend is eventually consistent, and a read-back could poison the
// baseline with stale content (§6).
func (s *linkSync) adopt(hash string) error {
	if s.state.Baselines[s.name].Hash == hash {
		return nil
	}
	s.state.Baselines[s.name] = baseline{Hash: hash, At: time.Now().UTC()}
	return saveState(s.state)
}

// bothMissing is §5.1 row 1: not a refusal, just nothing to act on.
func (s *linkSync) bothMissing() error {
	return fmt.Errorf("link %s: %s and %s are both missing; create one side first, or drop the link with g3 link rm %s",
		s.name, s.link.Path, s.link.URI, s.name)
}

// refuse is the §5.2 contract: a refusal is final — no --force — it names
// the state and the way out, and it exits 1 as a runtime failure, per the
// 001 §4.5 exit-code taxonomy.
func refuse(name string, state syncState, why, wayOut string) error {
	return fmt.Errorf("refused: %s is %s — %s.\n    %s (see docs/004-linked-paths.md §5.2)", name, state, why, wayOut)
}

// cmdPull copies remote → local when §5.1 allows it: the local file is
// missing, or only the remote moved since the last sync.
func cmdPull(ctx context.Context, newClient clientFn, name string, stdout io.Writer) error {
	s, err := loadLink(ctx, newClient, name)
	if err != nil {
		return err
	}
	switch s.ls.state {
	case stateMissing:
		return s.bothMissing()
	case stateInSync:
		if err := s.adopt(s.ls.local); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "in-sync: %s\n", name)
		return nil
	case stateRemoteMissing:
		return refuse(name, s.ls.state, "there is no remote object to pull",
			fmt.Sprintf("g3 push %s creates it.", name))
	case stateLocalAhead:
		return refuse(name, s.ls.state, "the local file changed since the last sync, and pulling would overwrite that work",
			fmt.Sprintf("Push with g3 push %s, or reconcile with g3 cp, then re-run g3 status.", name))
	case stateDiverged:
		return refuse(name, s.ls.state, "local and remote both changed since the last sync",
			"Reconcile with g3 cp, then re-run g3 status.")
	}
	// stateLocalMissing or stateRemoteAhead: write the bytes the state was
	// resolved against, cp's download way (created 0644, existing files keep
	// their mode, parents created), then baseline those same bytes.
	if err := os.MkdirAll(filepath.Dir(s.ls.path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.ls.path, s.ls.body, 0o644); err != nil {
		return err
	}
	if err := s.adopt(s.ls.remote); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "pull: %s to %s\n", s.link.URI, s.link.Path)
	return nil
}

// cmdPush copies local → remote when §5.1 allows it: the remote key is
// missing, or only the local file moved since the last sync.
func cmdPush(ctx context.Context, newClient clientFn, name string, stdout io.Writer) error {
	s, err := loadLink(ctx, newClient, name)
	if err != nil {
		return err
	}
	switch s.ls.state {
	case stateMissing:
		return s.bothMissing()
	case stateInSync:
		if err := s.adopt(s.ls.local); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "in-sync: %s\n", name)
		return nil
	case stateLocalMissing:
		return refuse(name, s.ls.state, "there is no local file to push",
			fmt.Sprintf("g3 pull %s creates it.", name))
	case stateRemoteAhead:
		return refuse(name, s.ls.state, "the remote changed since the last sync, and pushing would overwrite that edit",
			fmt.Sprintf("Pull with g3 pull %s, or reconcile with g3 cp, then re-run g3 status.", name))
	case stateDiverged:
		return refuse(name, s.ls.state, "local and remote both changed since the last sync",
			"Reconcile with g3 cp, then re-run g3 status.")
	}
	// stateRemoteMissing or stateLocalAhead: upload through readBody so push
	// inherits cp's 10 MiB cap and UTF-8 guard — the same bytes to the same
	// destination must not be answered differently by the two commands (§6).
	// A vanished gist fails here with the API's own not-found error (§5.1
	// row 3 note).
	f, err := os.Open(s.ls.path)
	if err != nil {
		return err
	}
	defer f.Close()
	body, err := readBody(f)
	if err != nil {
		return err
	}
	out, err := s.client.PutObject(ctx, &gists3.PutObjectInput{
		Bucket: s.ls.loc.bucket,
		Key:    s.ls.loc.key,
		Body:   bytes.NewReader(body),
	})
	if err != nil {
		return err
	}
	if err := s.adopt(out.ETag); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "push: %s to %s\n", s.link.Path, s.link.URI)
	return nil
}
