package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

// linkStatus is one resolved link: the parsed remote, the expanded local
// path, both content hashes ("" = missing), the remote body those hashes
// were computed against, and the §5.1 state.
type linkStatus struct {
	loc    location
	path   string
	local  string
	remote string
	body   []byte
	state  syncState
}

// resolveLink computes one link's state — one GetObject, one local hash.
func resolveLink(ctx context.Context, client *gists3.Client, l gists3.Link, base string) (*linkStatus, error) {
	loc, err := parseArg(l.URI)
	if err != nil {
		return nil, err
	}
	if loc.kind != locRemote || loc.prefixForm() {
		return nil, fmt.Errorf("link URI %q is not a full g3://<gist-id>/<key> URI; re-declare the link with g3 link rm and link add", l.URI)
	}
	path, err := expandPath(l.Path)
	if err != nil {
		return nil, err
	}
	local, err := hashLocal(path)
	if err != nil {
		return nil, err
	}
	body, remote, err := fetchRemote(ctx, client, loc)
	if err != nil {
		return nil, err
	}
	return &linkStatus{loc: loc, path: path, local: local, remote: remote, body: body, state: resolve(local, remote, base)}, nil
}

// cmdStatus reports each link's state, name-sorted, one "state name path"
// line per link. The client is constructed only after the name argument is
// validated, so an unknown link exits 2 even without credentials. It exits 0
// whatever states it finds; only a failure to complete the report is an
// error, and any error is global (§6) — NotFound is already absorbed as
// remote-missing — so the report aborts rather than repeating the failure
// once per remaining row.
func cmdStatus(ctx context.Context, newClient clientFn, name string, stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	var names []string
	if name != "" {
		if _, ok := cfg.Links[name]; !ok {
			return usagef("unknown link %q; g3 link ls shows the declared links", name)
		}
		names = []string{name}
	} else {
		for n := range cfg.Links {
			names = append(names, n)
		}
		slices.Sort(names)
	}
	if len(names) == 0 {
		return nil
	}
	st, err := loadState()
	if err != nil {
		return err
	}
	client, err := newClient()
	if err != nil {
		return err
	}
	nameW := 0
	for _, n := range names {
		nameW = max(nameW, len(n))
	}
	// Rows print as they resolve, so an abort keeps the completed ones; the
	// state column is padded to the widest §5.1 label ("remote-missing") to
	// stay aligned regardless.
	dirty := false
	var abort error
	for _, n := range names {
		l := cfg.Links[n]
		ls, err := resolveLink(ctx, client, l, st.Baselines[n].Hash)
		if err != nil {
			abort = err
			break
		}
		if ls.state == stateInSync && st.Baselines[n].Hash != ls.local {
			// Row 4 adopts the agreed content as the new baseline: it heals
			// a fresh link add, a baseline lost to a rename, and the §5.2
			// manual reconciliation alike.
			st.Baselines[n] = baseline{Hash: ls.local, At: time.Now().UTC()}
			dirty = true
		}
		fmt.Fprintf(stdout, "%-14s  %-*s  %s\n", ls.state, nameW, n, l.Path)
	}
	if dirty {
		// A baseline adopted before an abort records a real agreement;
		// persist it even when the report fails (§6).
		if err := saveState(st); err != nil {
			abort = errors.Join(abort, err)
		}
	}
	return abort
}
