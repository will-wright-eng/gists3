# g3 CLI — cp/ls/rm Implementation Plan

**Status:** Draft v0.1 (2026-07-08)
**Baseline:** [001-roadmap.md](001-roadmap.md) WP1; [000-design.md](000-design.md) §4.3, §5.6, §9
**Scope:** decompose WP1 into commit-sized stages, each with its own tests
and done-criteria. In scope: help/usage handling, the `g3://` URI parser,
the identity chain, `ls`, `cp`, `rm`, and the CLI test harness. Out of
scope: `mb`/`rb`, `cat`, and the `ls` description column (roadmap open
questions #1–3), and every other work package.

---

## 1. Where the stub stands

`cmd/g3/main.go` (~70 lines) dispatches with a bare `switch`, implements
`ls` (buckets only), resolves tokens from `GIST_TOKEN` → `gh auth token`
with no config step, treats `--help` as an unknown command, and exits 1
for every failure including usage mistakes.

## 2. Target shape

All code stays `package main` inside `cmd/g3` — nothing here is library
material. One file per concern, mirroring how the library is organized:

```
cmd/g3/
├── main.go              # main(), run() dispatch, top-level usage, exit codes
├── uri.go               # parseURI — the g3:// grammar
├── identity.go          # resolveIdentity — token chain + config for labeling
├── ls.go / cp.go / rm.go
├── uri_test.go, identity_test.go, ls_test.go, cp_test.go, rm_test.go
└── integration_test.go  # //go:build integration; drives the compiled binary
```

**The testability seam.** Subcommands never construct clients, read env, or
call `os.Exit`. They take everything injected:

```go
func cmdLs(ctx context.Context, client *gists3.Client, cfg *gists3.Config, args []string, stdout, stderr io.Writer) error
func cmdCp(ctx context.Context, client *gists3.Client, args []string, stdin io.Reader, stdout, stderr io.Writer) error
func cmdRm(ctx context.Context, client *gists3.Client, args []string, stderr io.Writer) error
```

Unit tests call these directly with a client pointed at the same
`httptest` fake pattern the library suite uses, and assert on the
writers. `run()` is the thin wiring layer (identity → client → dispatch),
exercised only by the integration test. `main()` is four lines: call
`run`, map the error to an exit code.

**Exit-code mechanics.** A `usageError` string type distinguishes "you
called it wrong" from "it failed":

```go
main() exit codes:
  nil                     → 0
  flag.ErrHelp            → 0   (help was requested and printed)
  *usageError             → 2   (bad command, bad arity, bad URI, local→local cp)
  anything else           → 1   (network, API, filesystem)
```

## 3. Stages

Each stage is one commit/PR, lands green (`make check` + its new tests),
and leaves the binary usable.

### Stage 1 — dispatch, help, exit codes (roadmap M0)

Replace the bare `switch` with per-subcommand `flag.FlagSet`s
(`ContinueOnError`, custom `Usage` funcs writing to stderr). Top level:
`-h`/`--help` and `g3 help` print usage and exit 0; unknown commands and
missing subcommand return `usageError`. No subcommand takes flags yet, but
every one parses its `FlagSet` so `g3 ls --help` works from day one.

*Tests:* table test over argv → (exit code, stderr contains). No network.
*Done when:* `g3 --help` exits 0 (the transcript bug), `g3 bogus` exits 2.

### Stage 2 — `g3://` URI parser

```go
// parseURI("g3://abc123/notes/2026.md") → bucket "abc123", key "notes/2026.md"
func parseURI(s string) (bucket, key string, err error)
```

Grammar per WP1: strip the mandatory `g3://` prefix, split the remainder
at the **first** `/`; the key keeps any further slashes (flat namespace,
design §6.1). Bucket must be non-empty; key may be empty (bucket form —
`ls` prefix listing, and the future `mb`/`rb` slot in here). Errors are
`usageError`s naming the bad input.

*Tests (the full table lives in `uri_test.go`):*

| Input | bucket | key | err |
|---|---|---|---|
| `g3://abc123` | abc123 | "" | – |
| `g3://abc123/` | abc123 | "" | – |
| `g3://abc123/a.txt` | abc123 | a.txt | – |
| `g3://abc123/notes/2026.md` | abc123 | notes/2026.md | – |
| `g3://` | | | usage |
| `s3://abc123/k`, `abc123/k`, `""` | | | usage |

### Stage 3 — identity chain

```go
// resolveIdentity returns the token and, when the config file supplied it,
// the Config (for DefaultUser labeling and Warnings).
func resolveIdentity(stderr io.Writer) (token string, cfg *gists3.Config, err error)
```

Per WP1, first hit wins:

1. `GIST_TOKEN` — return immediately, cfg nil.
2. `gists3.LoadConfig()` — distinguish absence from breakage:
   `errors.Is(err, fs.ErrNotExist)` (LoadConfig wraps with `%w`) falls
   through to source 3; any other error — malformed JSON, missing token —
   is fatal, because silently skipping a file the user wrote hides their
   bug. On success, print each `cfg.Warnings` line to stderr prefixed
   `g3: warning:`.
3. `gh auth token` — trimmed stdout; ignore all failures.

Exhausting the chain returns one error naming all three sources (extends
the message the stub already has). `cmd/example` keeps its simpler
env→gh helper — it demos the library, not the CLI.

*Tests:* `t.Setenv` for `GIST_TOKEN`, plus the `HOME`/`XDG_CONFIG_HOME`
redirection trick from `internal/gists3test/config_test.go` to point
`LoadConfig` at temp fixtures: config used, config malformed (fatal),
config absent (falls through), warning passthrough. The gh step is not
unit-testable without stubbing `exec` — covered by integration only.

### Stage 4 — `ls`, both forms

- `g3 ls` → `ListBuckets`, current format: `2006-01-02  <gist-id>`.
- `g3 ls g3://<id>[/<prefix>]` → `ListObjectsV2` with the key part as
  `Prefix`; format `%9d  %s` (size, key), matching `aws s3 ls` shape.
- When identity came from config and `cfg.DefaultUser != ""`, print
  `g3: gists for <user>` to **stderr** (design §5.6.5) — stdout stays
  machine-parseable.

*Tests:* fake server returns two gists / a fixture gist; assert stdout
rows, stderr label, prefix filtering, and `NotFoundError` → exit-1 error
for a bad bucket.

### Stage 5 — `cp`

Classify each of the two required args as remote (`g3://` prefix), stdin/
stdout (`-`), or local path; dispatch on the pair:

| src → dst | Maps to | Notes |
|---|---|---|
| local → remote | `PutObject` | `-` reads stdin; empty file surfaces `ErrEmptyBody` as-is |
| remote → local | `GetObject` | `-` streams to stdout; else `os.WriteFile` 0644 |
| remote → remote | `CopyObject` | `CopySource` = `<bucket>/<key>` |
| local → local | `usageError` | "use cp(1)" |

Both args `-` is a usage error. Remote endpoints require a non-empty key
(bucket-only forms are for `ls`). Confirmations go to stderr in aws-cli
voice — `upload: a.txt to g3://id/a.txt`, `download:`, `copy:` — never to
stdout, which is reserved for `-` data.

*Tests:* upload asserts the PATCHed content at the fake; download asserts
file bytes and the stdout path; remote-remote asserts source-GET +
dest-PATCH; usage-error table for the invalid pairs.

### Stage 6 — `rm`

`g3 rm g3://<id>/<key>` → `DeleteObject`; missing keys succeed silently
(S3-idempotent, design §5.4 — the library already guarantees it).
Bucket-only URI is a `usageError` whose text mentions that bucket removal
is not implemented (breadcrumb for open question #1). Confirmation
`delete: g3://<id>/<key>` to stderr.

*Tests:* delete asserts the null-file PATCH; bucket-only URI exits 2.

### Stage 7 — integration test and docs

- `cmd/g3/integration_test.go` (build tag `integration`): `go build` the
  binary into `t.TempDir()`, create a scratch bucket via the library,
  then drive the WP1 acceptance lifecycle through the binary with
  `os/exec`: `cp` up → `ls` shows it → `cp` down to `-` round-trips bytes
  → `rm` → `ls` no longer shows it. Token via the same env/gh chain as
  `internal/gists3test`; skip when absent. Cleanup in `t.Cleanup`.
- README gains a short `g3` usage block; `make integration`'s
  `-run Integration` already matches the new test's name.
- Tick WP1 in the 001-roadmap conformance table.

## 4. Test matrix

| Layer | Stages | Network |
|---|---|---|
| Pure functions (`parseURI`, argv → exit code) | 1, 2 | none |
| Command funcs vs `httptest` fake | 4, 5, 6 | loopback |
| Identity chain vs temp config dirs | 3 | none |
| Compiled binary vs live API (tagged) | 7 | live, self-cleaning |

## 5. Acceptance (restates WP1)

- [ ] `g3 --help`, `g3 help`, and every `g3 <cmd> --help` exit 0 with usage.
- [ ] Usage mistakes exit 2; runtime failures exit 1; data only on stdout.
- [ ] Live lifecycle: `cp` up, `ls` shows it, `cp` down round-trips, `rm` removes.
- [ ] Config file without `GIST_TOKEN`: identity from config, warnings and
      `default_user` label on stderr.
- [ ] `make check` green at every stage; zero new module dependencies.

## 6. Deferred decisions

Blocked on the roadmap's open questions, but pre-wired: the URI grammar
already parses bucket-only forms (`mb`/`rb`, OQ1), `rm`'s bucket-only
error text points there, `cat` (OQ3) would be a five-line alias for
`cp <uri> -`, and the `ls` description column (OQ2) waits on a library
API addition.
