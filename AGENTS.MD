# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`g3` is a CLI that treats GitHub Gists as object storage with aws-cli vocabulary: a **bucket is a
gist** (addressed by its GitHub-assigned ID), a **key is a filename** inside it, an **object body**
is that file's content. Zero dependencies beyond the Go standard library — `go.mod` has no
`require` block, and keeping it that way is a design pillar (Cobra was explicitly rejected).

## Commands

```sh
make check                       # what CI runs: fmt-check, vet (both tag sets), staticcheck, race tests, build
make test                        # hermetic unit tests
make cover                       # engine coverage (-coverpkg names gists3+gistapi; tests live in gists3test)
make cover-cli                   # cmd/g3 coverage, kept separate so the engine number stays honest
make build                       # binary lands in dist/g3
make install                     # dist/g3 -> $HOME/go/bin

go test -run TestRunUsageErrors ./cmd/g3          # single test
go test -tags integration -run Integration ./...  # live API; needs GIST_TOKEN or `gh auth login`
make example                                      # end-to-end engine smoke test against the live API
```

Integration tests and `cmd/example` hit the real GitHub API and create/delete real gists; CI runs
them weekly, not per-PR, to respect rate limits. `pre-commit install` wires the same hooks as
`make check`.

## Architecture

```
cmd/g3            the product: argument grammar, link table, sync state machine
  internal/gists3     S3-shaped engine: Client, Input/Output structs, typed errors, config file
    internal/gistapi  GitHub REST transport + JSON wire shapes
internal/gists3test   black-box suite over the engine (public API only, fake GitHub)
cmd/example           engine lifecycle demo, doubles as a live smoke test
```

The engine is `internal/` on purpose — the CLI is the product and there are no library consumers
(docs/003-cli-first.md). Changing the engine's API needs no deprecation ceremony.

Three boundaries carry real rules:

- **`gistapi` wire types never escape `gists3`.** `gists3.do()` converts `gistapi.APIError` /
  `RateLimitError` into the façade's own types so public godoc stays complete. If GitHub changes a
  field, `internal/gistapi/wire.go` is the blast radius.
- **The engine never resolves credentials.** `gists3.New` takes a token; ambient resolution
  (`GIST_TOKEN`, then `gh auth token`) lives in `cmd/g3/client.go`. The config file holds no
  secrets — that is what makes it shareable, and what lets the link table live in it.
- **The token goes only to the configured `base_url`.** `GetObject` follows `raw_url` for
  truncated files *without* the Authorization header; `WithBaseURL` is security-sensitive.

### cmd/g3 internals

| File | Role |
|---|---|
| `main.go` | `run()` dispatch; `usageError` → exit 2, everything else → exit 1 |
| `uri.go` | `parseArg` classifies one argument as local / remote / stdio; pure, no I/O |
| `cp.go` | `classify` (alias expansion + pair validation) then upload/download/remoteCopy; `readBody` holds the 10 MiB cap and UTF-8 guard |
| `link.go` | link table CRUD in `config.json`, `@<alias>` expansion, `writeFileAtomic` |
| `state.go` | `resolve(local, remote, baseline)` — the sync-state table; baselines in `state.json` |
| `status.go`, `sync.go` | `link status` / `pull` / `push` on top of `resolve` |

Two invariants the tests enforce and that are easy to break:

1. **Argument validation precedes client construction**, so a bad invocation or an unknown link
   exits 2 on a machine with no credentials. Tests pass a `clientFn` that returns an error, or one
   pointed at `127.0.0.1:1`, to prove nothing dialed.
2. **`link push` uploads through `cp`'s `readBody`**, so the same bytes to the same destination are
   never answered differently by the two commands.

### The sync model

A link declares that a local path *is* the working copy of a gist key. Every successful pull/push
records a **baseline** — the content hash both sides last agreed on — and the state resolves from
three hashes (`""` means absent): in-sync / local-ahead / remote-ahead / diverged / local-missing /
remote-missing / missing. A direction that would overwrite unseen work is **refused, with no
`--force`**: the backend has no compare-and-swap, so the refusal turns a silent clobber into a
failed command. Reconcile with `g3 cp` and the link heals on the next `status`.

Hashing is content-based (hex SHA-256, the same value the engine reports as `ETag`), never mtime.
Baselines record the bytes just transferred, never a confirming re-read — the backend is eventually
consistent and a read-back can poison the baseline with stale content.

`config.json` is shared/portable (links, paths stored unexpanded); `state.json` sits beside it and
describes this machine only. Writes to `config.json` go through `map[string]json.RawMessage` so
fields written by a newer `g3` survive the round-trip.

### Backend contracts that leak into every layer

Empty bodies refused · non-UTF-8 refused (gists are text) · keys starting with `gistfile` refused
on write, accepted on read · flat namespace (`/` in a key is not a delimiter; prefix filtering is
client-side) · last write wins · eventually consistent, rapid updates can 409 · new buckets seed a
`.bucket` placeholder that object listings hide · a secret gist is unlisted, **not**
access-controlled.

## Docs

`docs/` holds the design and per-feature contracts, and code comments cite them by section
(`docs/004-linked-paths.md §5.1`). When changing sync, `cp`, or `ls` behavior, check the contract
doc first and update it in the same change — the comments are load-bearing references, not
decoration. Numeric prefixes are not unique (`001-roadmap.md` vs `001-cp-command.md`); headers
carry a `Status:` line — Draft, Implemented, or Accepted.

## Conventions

- Godoc on each exported engine symbol states **round-trip cost** and **divergence from S3**.
- Tests are hermetic by default: a `http.ServeMux`-backed fake GitHub behind `WithBaseURL`, inline
  or `testdata/fixtures` JSON. `internal/gists3test` drives the public API only.
- Live-API tests go behind `//go:build integration` and must clean up after themselves.
- Error messages name the state and the way out, and `g3` failures print as `g3: <err>`.
