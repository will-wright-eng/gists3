# 001 — `g3 cp`: single-object copy for the g3 CLI

**Status:** Implemented — 2026-07-13
**Scope:** `cmd/g3` only; no library changes
**Depends on:** [DESIGN.md](DESIGN.md) §5.6 (config file), §9 (v2 CLI roadmap row), §10 (decision log #3)

---

## 1. Overview

`g3 cp <source> <destination>` copies one object between the local machine and
a gist-backed bucket, in the flavor of `aws s3 cp`. Exactly one of three
directions applies per invocation:

| Direction | Example | Library call |
|---|---|---|
| Upload | `g3 cp notes.md g3://abc123/notes.md` | `PutObject` |
| Download | `g3 cp g3://abc123/notes.md notes.md` | `GetObject` |
| Remote copy | `g3 cp g3://abc123/notes.md g3://def456/notes.md` | `CopyObject` |

`-` stands for stdin (as source) or stdout (as destination), matching aws-cli.
`cp` is an **upsert** — the underlying PATCH creates or replaces the file, so
there is no separate update subcommand (DESIGN.md §10, decision #3).

The command is a thin composition over the existing library: every behavioral
contract in [`operations.go`](../operations.go) (empty-body rejection, reserved
keys, truncation fallback, typed errors) passes through rather than being
re-implemented. What `cp` adds is argument classification, `g3://` URI parsing,
key/filename inference, the upload guards (§4.6), and the credential chain the
DESIGN roadmap assigns to the CLI (§4.8).

Out of scope for v1: `--recursive`, `mv`/`sync`, flags of any kind, retries,
and progress output (§8).

---

## 2. Background

- The CLI stub ([`cmd/g3/main.go`](../cmd/g3/main.go)) currently implements
  only `ls`, resolves credentials from `GIST_TOKEN` falling back to
  `gh auth token`, and does **not** yet read the §5.6 config file the roadmap
  assigns to the CLI.
- The library surface `cp` needs is complete and tested: `PutObject`,
  `GetObject`, and `CopyObject` in `operations.go`, with hermetic fixtures in
  `internal/gists3test/`.
- DESIGN.md pins the CLI's shape: binary named `g3` to match the `g3://` URI
  scheme (§4.3), aws-cli flavored `cp`/`ls`/`rm` (§9), stdlib `flag` preferred
  over cobra (§4.3), config file for identity (§5.6, §9).

The guiding rule throughout this spec, inherited from DESIGN.md §5.2: mirror
aws exactly unless mirroring would lie about the backend; where the backend
forces a divergence, diverge loudly.

---

## 3. Command surface

```
g3 cp <source> <destination>

  <source>       local path | g3://<gist-id>/<key> | -  (stdin)
  <destination>  local path | g3://<gist-id>[/<key-or-prefix/>] | -  (stdout)
```

Examples (status lines shown where one is printed):

```console
$ g3 cp conf.json g3://abc123/conf.json
upload: conf.json to g3://abc123/conf.json

$ g3 cp conf.json g3://abc123/            # key inferred from source basename
upload: conf.json to g3://abc123/conf.json

$ g3 cp g3://abc123/conf.json ./backup/   # filename inferred from key
download: g3://abc123/conf.json to backup/conf.json

$ g3 cp g3://abc123/conf.json g3://def456/
copy: g3://abc123/conf.json to g3://def456/conf.json

$ date | g3 cp - g3://abc123/last-run     # stdin needs an explicit key;
                                          # no status line — a `-` endpoint
                                          # suppresses it (§4.4)

$ g3 cp g3://abc123/conf.json - | jq .    # body only; status line suppressed
```

---

## 4. Behavioral specification

### 4.1 Argument grammar (`parseArg`)

Each positional argument is classified without touching the network:

```
arg = "-"                                   → stdio
    | "g3://" gist-id [ "/" key ]           → remote
    | anything containing "://"             → error: unsupported URI scheme
    | anything else                         → local path
```

Rules:

1. **Gist ID** is everything between `g3://` and the first `/` (or end of
   string). An empty ID (`g3://`, `g3:///k`) is a usage error. No further ID
   validation — IDs are GitHub-assigned opaque strings and a malformed one
   404s honestly (`validateBucket` in `gists3.go` checks only non-emptiness).
2. **Key** is everything after that first `/`, verbatim — embedded slashes
   included (`g3://id/a/b.txt` → key `a/b.txt`). Keys are opaque strings in a
   flat namespace (DESIGN.md §6.1); the parser never splits or normalizes
   them.
3. **Bare bucket and prefix forms.** A missing key (`g3://id`), an empty key
   (`g3://id/`), and a key ending in `/` (`g3://id/prefix/`) all mark the
   argument as a *prefix form*: legal only as a destination, and even there
   only when a filename can be inferred — not with a stdin source (§4.3). The
   final key is resolved by inference (§4.3). `parseArg` merely *records* the
   prefix form (empty key or trailing `/`) — it cannot know the argument's
   position; `classify` (§4.2) rejects a prefix-form remote in source
   position with `source must name a key`, before any file or network I/O.
4. **A local file literally named `-`** is reachable as `./-`, the standard
   Unix convention. Documented in the usage text, not special-cased.

Representation:

```go
type locKind int

const (
	locLocal locKind = iota
	locRemote
	locStdio
)

type location struct {
	kind   locKind
	bucket string // remote: gist ID
	key    string // remote: "" or trailing "/" means prefix form (§4.1 rule 3)
	path   string // local
}

func parseArg(arg string) (location, error)
```

### 4.2 Direction classification

A pure `classify(srcArg, dstArg)` function parses both arguments and maps the
pair to an operation or a usage error:

| source \ dest | remote | local | stdio |
|---|---|---|---|
| **local** | upload | usage error | usage error |
| **remote** | remote copy | download | download to stdout |
| **stdio** | upload from stdin | usage error | usage error |

The four rejected pairs share one message: `at least one side must be a
g3:// URI`. `classify` also rejects the two positional prefix-form errors:
a prefix-form remote as source (§4.1 rule 3) and a stdin source paired with
a prefix-form destination (§4.3).

All classification failures are usage errors (exit 2, §4.5), raised before
any network call or file read — and before client construction: `run()`
classifies first and calls `newClient` only afterwards, so a usage error is
never masked by a credential failure (§6.2).

### 4.3 Key and filename inference

Mirrors `aws s3 cp` single-object rules exactly:

| Case | Rule | Example |
|---|---|---|
| Upload to bare bucket or `…/` prefix | key = prefix + `filepath.Base(source path)` | `g3 cp dir/test.txt g3://b/x/` → key `x/test.txt` |
| Upload from stdin to prefix destination | **usage error** — no filename to infer | `date \| g3 cp - g3://b/` → error |
| Download to existing directory, or path ending in a separator | filename = `path.Base(key)`, joined to the directory. A trailing separator always means directory treatment, existing or not — §4.7's `MkdirAll` creates it | `g3 cp g3://b/a/x.txt dir/` → `dir/x.txt` |
| Download to any other path | path used verbatim as the target file | `g3 cp g3://b/x.txt out.txt` → `out.txt` |
| Remote copy to bare bucket or `…/` prefix | key = prefix + `path.Base(source key)` | `g3 cp g3://a/x/y.txt g3://b/` → key `y.txt` |
| Explicit destination key | used verbatim, never probed remotely | — |
| Self-copy (same bucket and key) | allowed; harmlessly rewrites the file (2 requests). aws's refusal is not mirrored — it exists to catch metadata-free no-ops, and g3 has no metadata that could make a self-copy meaningful | — |

Upload sources must be existing regular files: a missing, unreadable, or
directory source surfaces the `os.ReadFile` error verbatim (exit 1) —
directories stay unsupported until `--recursive` (§8), and the usage text
says so.

Two consequences worth stating:

- **`path.Base` on a remote key is a naming convenience, not namespace
  emulation.** DESIGN.md §6.1 refuses `Delimiter` semantics in the *library*
  (no `CommonPrefixes`, no folder listings). Choosing a local filename — or a
  short destination key — from the last key segment is what aws itself does
  for single objects, and the resolved name is always printed in the status
  line, so nothing is silent. *Alternative considered and rejected:*
  preserving the full source key on bare-bucket remote copies
  (`g3://b/x/y.txt`) is arguably "flatter" but breaks aws muscle memory, and
  parity is this project's first goal; users who want the full key type it.
- **Key slashes are never materialized as local directories.** Downloading
  key `a/b.txt` into `dir/` writes `dir/b.txt`, not `dir/a/b.txt`. Single-file
  aws behaves the same way.
- **The inferred local filename must be a safe single path component.**
  `path.Base` of a key like `a/..` is `..`, which would resolve the write
  target *outside* the named directory, and it is backslash-blind, which on
  Windows would materialize directories. When the inferred name is `.`,
  `..`, or contains a separator or backslash, the download refuses with
  `cannot infer a safe local filename from key %q; name the destination file
  explicitly`. *(Added after review — the original draft joined `path.Base`
  unsanitized.)*

### 4.4 stdin / stdout streaming

- **`- → remote`**: stdin is read fully into memory (the library buffers
  bodies anyway, `operations.go` `PutObjectInput.Body` doc), subject to the
  size cap in §4.6. An explicit, non-prefix destination key is required.
- **`remote → -`**: the body is fully buffered before the first byte reaches
  stdout, so a failed fetch emits **zero** bytes — all-or-nothing piping, a
  guarantee full buffering gives us for free. Only body bytes go to stdout;
  errors and warnings go to stderr.
- **Status-line suppression follows aws exactly**: a `-` at *either* end
  makes the transfer a stream (aws sets `is_stream`, which implies
  `--only-show-errors`), so both `- → remote` and `remote → -` print no
  status line.
- stdin is read only when the source argument is `-`; for every other
  direction it is never touched, so `g3 cp a.txt g3://b/ < data` cannot
  drain a pipeline.
- Empty stdin fails with the library's `ErrEmptyBody` (§4.6).

### 4.5 Status output and exit codes

Status lines go to **stdout** (as in aws-cli), one per successful copy, with
the *resolved* destination:

```
upload: <source> to g3://<bucket>/<key>
download: g3://<bucket>/<key> to <path>
copy: g3://<src-bucket>/<src-key> to g3://<dst-bucket>/<dst-key>
```

Suppressed entirely when either endpoint is `-` (§4.4). All diagnostics —
errors and config-permission warnings — go to stderr, keeping stdout clean
for piping.

Local paths print as typed for sources, and for destinations as the resolved
target exactly as passed to `os.WriteFile` — the `filepath.Join`-cleaned path
when the filename was inferred (`./backup/` → `backup/conf.json`), or the
user's argument verbatim when it named the file explicitly. Paths are never
made absolute.

Exit codes collapse aws-cli v2's taxonomy to the three a script can act on:

| Code | Meaning |
|---|---|
| 0 | Copy succeeded |
| 1 | Runtime failure: `NotFoundError`, `RateLimitError`, `APIError`, `ErrEmptyBody`, guard rejections (§4.6), local I/O, network |
| 2 | Usage error: wrong arity, unparseable `g3://` URI (empty gist ID) or non-`g3` URI scheme, prefix/bare-bucket form used as a source, stdin source with a prefix destination, rejected direction pair, unknown command |

Implementation: a `usageError` wrapper type in `cmd/g3`; `main()` checks it
with `errors.As` and picks the exit code. `ls` argument handling is
specified in [002-ls-command.md](002-ls-command.md) §2.3 (originally
argument-free; it now takes an optional `g3://` URI, and non-remote
arguments remain usage errors).

### 4.6 Upload-path guards

Three guards protect the upload path, all resolving before the first HTTP
request: the size cap fires *during* the body read, the UTF-8 check runs
once the read completes, and the empty-body check is the library's own,
inside `PutObject` ahead of the PATCH. In that evaluation order — size,
UTF-8, empty — each turns a silent corruption or an opaque API rejection
into a named, immediate error: DESIGN.md's goal 3 applied at the CLI layer.

1. **Size cap: 10 MiB.** Bodies are fully buffered on both sides of the wire,
   and DESIGN.md §5.4 names ~10 MB as the backend's practical ceiling. `cp`
   reads at most cap+1 bytes via `io.LimitReader(r, capBytes+1)` and fails
   with an error naming the cap when the buffered body exceeds `capBytes`
   (10 << 20) — instead of buffering an unbounded pipe and letting the API
   reject it opaquely. (A plain `io.LimitedReader` used naively returns EOF
   at the cap with no over-limit signal, which would *silently truncate* the
   upload — the exact corruption this guard exists to prevent; hence the
   cap+1 idiom.) The size check runs **before** the UTF-8 guard, because the
   limited read can split a multi-byte rune at the cap boundary and a valid
   oversize file must be reported as oversize, not as invalid UTF-8. The API
   remains the final authority below the cap.
2. **Non-UTF-8 content is refused**, not corrupted. Gist file content lives
   in a JSON string field, and Go's `encoding/json` coerces strings to valid
   UTF-8, *replacing invalid bytes with the Unicode replacement rune* — so a
   PNG piped through `cp` would round-trip wrong with no error from anyone.
   `cp` checks `utf8.Valid` on the body and fails with:
   `content is not valid UTF-8; the gist backend stores text only — encode binary data first (e.g. base64)`.
   A future `--base64` flag maps to the library's planned `WithBase64Bodies`
   option (DESIGN.md §6.2, §9 v1.1). *Note this is deliberately stricter than
   the library, which stores bodies as-is and makes binary safety the
   caller's responsibility — at the CLI, the caller is a human.*
3. **Empty body.** Delegated to the library: `PutObject` returns
   `ErrEmptyBody` for empty files and empty stdin (`operations.go`). This is
   the one mandatory divergence from aws, where 0-byte objects are legal —
   the Gist API rejects empty file content, and gists3 refuses loudly rather
   than writing a sentinel byte (DESIGN.md §5.4). `cp` lets the sentinel's
   own message surface. (`utf8.Valid` on an empty body is true, so the
   ordering is safe.)

Guards that already live in the library and simply pass through: reserved
`gistfile*` keys (`ErrReservedKey`, applies to upload and remote-copy
destinations via `PutObject`), and bucket/key non-emptiness. One timing
caveat: on the remote-copy path, `CopyObject` validates only `CopySource` up
front and fetches the source *before* `PutObject` validates the destination
key (`operations.go`), so `g3 cp g3://a/k g3://b/gistfile1` spends one source
GET before failing — accepted for v1 rather than duplicating `validatePutKey`
in the CLI. `cp` adds **no** rule the library doesn't have for key names — in
particular, `.bucket` (the `CreateBucket` placeholder) remains explicitly
readable and writable, matching `GetObject`'s documented behavior.

### 4.7 Downloads to disk

- Parent directories are created with `os.MkdirAll(filepath.Dir(dst), 0o755)`
  — aws does the equivalent (`os.makedirs`) and scripted use expects it. If a
  path component is occupied by a regular file (`ENOTDIR`) or the resolved
  target is itself a directory (`EISDIR`), the OS error surfaces verbatim,
  exit 1.
- The file is created `0o644` (before umask) via `os.WriteFile`, only after
  the full body is in memory: a failed fetch **never creates or truncates**
  the destination file. (aws deletes the partial file on failure; full
  buffering lets `cp` never produce one.) An existing destination keeps its
  mode — `os.WriteFile` replaces content only.
- An existing destination file is overwritten silently, like aws. There is no
  `--no-clobber`; the backend has no compare-and-swap to build a non-racy one
  on (DESIGN.md §2 non-goal 3).
- Truncated objects (>~1 MB inline) are handled invisibly: `GetObject`
  already follows `raw_url` unauthenticated (`operations.go`).

### 4.8 Credentials and client construction

`cp` lands together with the roadmap's "CLI reads §5.6 config" behavior,
shared with `ls` through a new `newClient` helper. Precedence:

1. **`GIST_TOKEN`** env var, when set to a non-empty value — explicit
   per-invocation override (aws convention: env beats config file). The
   value is whitespace-trimmed like the gh token (CRLF env files would
   otherwise produce a control character the HTTP client rejects opaquely),
   and empty-after-trim behaves as unset, matching the stub's
   `resolveToken`; this is also what lets tests neutralize the layer with
   `t.Setenv("GIST_TOKEN", "")`.
2. **Config file** via `gists3.LoadConfig()` — the deliberate identity store
   (DESIGN.md §5.6). Its `Warnings` (e.g. group-readable token file) print to
   stderr. The client is built with `gists3.NewFromConfig(cfg)` so
   `base_url` applies.
3. **`gh auth token`** — borrowed ambient credential, last resort (current
   stub behavior, kept).

Two rules make the chain predictable:

- **The layer that supplies the token supplies the whole identity.** If
  `GIST_TOKEN` is set, the config file is not consulted at all — its
  `base_url` does *not* apply. A GHE user who sets both gets
  `api.github.com`; the `newClient` godoc and README state this.
- **Only an *absent* config falls through** — a missing file
  (`errors.Is(err, fs.ErrNotExist)`, which works because `LoadConfig` wraps
  `os.ReadFile` with `%w`, `config.go`) or an unresolvable config path
  (`os.UserConfigDir` error, e.g. `$HOME` unset in a minimal container —
  the current stub never touches the filesystem and must keep working
  there). A malformed or token-less config file is fatal, so a typo cannot
  silently switch identity to the `gh` token.

### 4.9 Error surfacing

The façade guarantees a closed set of error shapes: `*NotFoundError`,
`*RateLimitError`, `*APIError`, the two sentinels, and other
`gists3:`-prefixed plain errors (input validation, `CopySource` format,
transport). Their `Error()` strings
are already user-fit — `NotFoundError` names bucket *and* key (so a failed
remote copy is attributable to its source or destination side without extra
CLI logic), and `RateLimitError` includes the reset time when GitHub sent
one. `cp` therefore prints errors verbatim behind the existing `g3:` prefix
and adds no message rewriting in v1.

Two consistency notes inherited from DESIGN.md decision #5, worth carrying
into the README:

- No read-back verification after upload — an immediate `GetObject` can
  spuriously miss the write (eventual consistency). The `ETag` returned by
  `PutObject` already proves what was sent.
- No retries, including on the sporadic `409 Gist cannot be updated` from
  rapid sequential writes to one gist. Retries are caller policy (§5.3);
  scripted loops writing one gist should serialize and pace themselves.

---

## 5. Divergences from `aws s3 cp`

Everything not listed here mirrors aws exactly (grammar, inference, silent
overwrite, `MkdirAll`, status-line wording, `-` streaming, no prompts).

| Area | aws | g3, and why |
|---|---|---|
| 0-byte objects | legal | error (`ErrEmptyBody`) — the Gist API rejects empty content |
| Binary bodies | byte-transparent | refused unless valid UTF-8 (§4.6) — JSON-string storage corrupts silently otherwise |
| Remote copy | server-side, atomic per object | client-side GET+PATCH, non-atomic, last-write-wins, 2 requests of the 5,000/hr budget |
| Object size | up to 5 TiB | 10 MiB CLI cap; <1 MB comfort zone (truncation threshold) |
| Metadata flags (`--content-type`, `--acl`, `--sse`, …) | supported | omitted; gists have no per-object metadata — accepting and dropping them would be silent data loss |
| Progress meter, `--quiet`, `--no-progress` | supported | omitted in v1; one ≤1 MiB request has no meaningful progress (`--quiet` is future work, §8) |
| Downloaded-file mtime | set to S3 `LastModified` | left as local write time; gists have per-gist, not per-file, timestamps |
| Self-copy (same bucket + key) | refused unless metadata changes | allowed; a harmless 2-request rewrite — g3 has no metadata to make aws's refusal meaningful (§4.3) |
| Exit codes | 0/1/2 plus v2's 130/252–255 | 0/1/2 only |
| Reserved key names | none | `gistfile*` rejected (`ErrReservedKey`, library rule) |

---

## 6. Implementation plan

### 6.1 File layout

`cmd/g3` stays `package main` and stdlib-only. No cobra and — since `cp` and
`ls` take only positional arguments — no `flag` either, until the first real
flag appears. Split by concern so each file and its test stay review-sized:

```
cmd/g3/
├── main.go        # (exists, slims down) main(), run() dispatch, usage, ls()
├── client.go      # resolveConfig(), newClient(), ghAuthToken seam
├── uri.go         # locKind, location, parseArg()
├── cp.go          # classify() and cp()
├── main_test.go   # run() dispatch: usage, unknown command, arity, ls
├── client_test.go # credential-precedence table tests
├── uri_test.go    # parseArg grammar table tests
└── cp_test.go     # httptest-backed tests, all directions + local files
```

*Superseded in part by [002](002-ls-command.md): `ls` later moved out of
`main.go` into `cmd/g3/ls.go` as `lsBuckets`/`lsObjects`.*

### 6.2 Seams

The stub's testability flaw is that `ls()` builds its own client and prints
with `fmt.Printf`. Three seams fix it; `main()` becomes the only place
`os.Args`/`os.Stdin`/`os.Stdout`/`os.Stderr` appear:

```go
type clientFn func(stderr io.Writer) (*gists3.Client, error)

func run(ctx context.Context, args []string, newClient clientFn,
	stdin io.Reader, stdout, stderr io.Writer) error

func ls(ctx context.Context, client *gists3.Client, stdout io.Writer) error
// (ls was later split into lsBuckets/lsObjects in ls.go — see 002)

// cp.go — classify is pure (parse + pair validation, all the exit-2 cases);
// cp receives already-classified locations and does the I/O
func classify(srcArg, dstArg string) (src, dst location, err error)
func cp(ctx context.Context, client *gists3.Client, src, dst location,
	stdin io.Reader, stdout io.Writer) error

// client.go
func resolveConfig(stderr io.Writer) (*gists3.Config, error) // §4.8 chain
func newClient(stderr io.Writer) (*gists3.Client, error)     // NewFromConfig(resolveConfig(...))

// package var so tests stub the exec.Command("gh", ...) call
var ghAuthToken = func() (string, error) { ... }
```

Ordering inside `run()` for the `cp` case: arity check → `classify` → only
then `newClient` → `cp`. `newClient` reads a file and may exec `gh`, so it
must not run before usage validation — on a credential-less machine,
`g3 cp a b` still exits 2 with the usage message, not 1 with a token error
(§4.2). `main()` passes `context.Background()`; there is no per-command
timeout or signal handling in v1 (§8).

Tests never call `newClient`: they build
`gists3.New("test-token", gists3.WithBaseURL(srv.URL))` against an
`httptest.Server` and invoke `cp()`/`ls()` directly; `run()`-level tests pass
a `clientFn` returning that client.

### 6.3 Ordered steps (one reviewable commit each)

1. **`refactor(cmd/g3): thread dependencies through run()`** — the seam
   change above, zero behavior change; `main()` passes a temporary
   `clientFn` closure wrapping the existing `resolveToken` +
   `gists3.New(token)` (step 2 replaces it and deletes `resolveToken`).
   `main_test.go` locks the seam (usage, unknown command, `ls` against a
   `ListBuckets` fixture).
2. **`feat(cmd/g3): resolve identity from GIST_TOKEN, config file, gh`** —
   `client.go` + `client_test.go` (§4.8). Delete `resolveToken` from
   `main.go`. This is the roadmap's behavior change; the commit message says
   so.
3. **`feat(cmd/g3): add g3:// URI parser`** — `uri.go` + exhaustive
   `uri_test.go`. Pure; nothing wired.
4. **`feat(cmd/g3): add cp command`** — `cp.go`, the `case "cp"` dispatch,
   arity check, `usageError` + exit code 2 in `main()`, updated usage string
   and package doc comment (which still says "v0 implements only ls"), and
   `cp_test.go` covering every direction and guard.
5. **`docs+build`** — README gains a CLI section (install, one example per
   direction, `-` convention, empty-body/binary caveats, credential
   precedence, GHE base-url rule); Makefile gains a `cover-cli` line
   (`go test -cover ./cmd/g3` — see §7 note); DESIGN.md §9's v2 CLI row can
   be annotated in-progress.

---

## 7. Testing strategy

All tests live in `cmd/g3` itself (internal tests in `package main`;
`internal/gists3test`'s helpers are unexported in a `_test` package and can't
be imported — its ~10-line `newServer` httptest pattern is duplicated, and
its inline-JSON fixture style is copied rather than adding a `testdata/`
dir). `make test`, `race`, `vet`, and `lint` already cover `cmd/g3` via
`./...` patterns.

1. **`uri_test.go`** — pure table test: happy paths (`g3://id/k`, nested
   `g3://id/a/b.txt`), bare bucket `g3://id` and `g3://id/` (both →
   prefix destination), prefix `g3://id/x/`, `-`, plain and relative local
   paths, and errors (`g3://`, `g3:///k`, `s3://x/y`).
2. **`cp_test.go`** — one `httptest.Server` mux per case, fixtures inline
   (mirroring `internal/gists3test/object_test.go`'s `TestCopyObject`
   arrangement):
   - upload from `t.TempDir()` file → assert the captured PATCH body's
     `files[<key>].content`;
   - upload from stdin (`strings.NewReader`), asserting no status line
     (§4.4);
   - key inference cases from §4.3 (bare-bucket dest, dir dest);
   - download → assert file bytes, parent `MkdirAll`, and that the file is
     owner-readable/writable (`mode&0o600 == 0o600` — the exact mode is
     umask-dependent, the same non-hermeticity trap §9 flags for
     credentials);
   - download to stdout → assert body-only output, status line absent;
   - remote copy (GET src gist + captured PATCH dst gist);
   - guards and errors: missing and directory local source, empty file/stdin
     (`errors.Is(err, gists3.ErrEmptyBody)`), invalid UTF-8, over-cap body
     of valid UTF-8 whose *final rune spans the cap boundary* — the capped
     read truncates it mid-rune, so only that input distinguishes the §4.6
     guard order (the error must name the 10 MiB cap, not UTF-8) —
     unsafe inferred download names (key `a/..`), 404 → `NotFoundError`,
     bare-bucket source,
     stdin with prefix destination, each rejected direction pair (assert
     `usageError`).
3. **`client_test.go`** — precedence table with `t.Setenv` for
   `GIST_TOKEN`/`HOME`/`XDG_CONFIG_HOME`/`AppData` (all three OS lookup
   paths, so `os.UserConfigDir` resolves into a `t.TempDir` — exactly the
   `writeConfig` helper pattern in `internal/gists3test/config_test.go`) and
   a stubbed `ghAuthToken`: env beats config; config beats gh and its
   `base_url` + `Warnings` take effect; gh fallback; **missing** config
   falls through but **malformed** config is fatal; unresolvable config path
   (`HOME`/`XDG_CONFIG_HOME` empty) still reaches the gh fallback;
   all-absent yields the actionable message. Every case sets `GIST_TOKEN` to
   `""` first — a developer's real token or `gh` login must not flip
   results.
4. **`main_test.go`** — dispatch: no args, unknown command, `cp` arity,
   exit-code classification via the `usageError` type, and a rejected
   direction pair with a *failing* `clientFn` asserting `usageError` — not
   the credential error — to lock the classify-before-client ordering
   (§6.2).

**Coverage note:** the Makefile `cover` target
(`-coverpkg=.,./internal/gistapi ./internal/gists3test`) intentionally
excludes `cmd/g3`; merging it in would dilute the library number with CLI
lines the library tests never touch. A separate `go test -cover ./cmd/g3`
target keeps the numbers honest.

No live-API `cp` test: an upload-then-download round-trip would flake on the
backend's eventual consistency (DESIGN.md decision #5). The existing
integration suite already exercises the underlying library calls.

---

## 8. Out of scope / future work

| Item | Trigger |
|---|---|
| `--quiet` | first user request; wording reserved to match aws |
| `--base64` | library ships `WithBase64Bodies` (DESIGN.md §9, v1.1) |
| `--recursive`, `sync` | needs its own doc: prefix listing + N sequential PATCHes to one gist walks into the 409 rapid-write quirk, so pacing/serialization is a design problem, not a loop |
| `rm` (and `mv`) | separate doc; `mv` composes the documented duplicate-content 422 quirk that `DeleteObject` absorbs |
| SIGINT → exit 130, `signal.NotifyContext` | flag-worthy only when copies get long enough to interrupt; today's are single sub-second requests |
| Retry/backoff on 409/rate-limit | stays caller policy per DESIGN.md §5.3; revisit with library `WithRetry` (v1.1) |

---

## 9. Risks and gotchas

- **Stdout purity is load-bearing** for `g3 cp g3://b/k - | …`: config
  warnings and all diagnostics must go to stderr. The step-1 refactor
  eliminates the stub's raw `fmt.Printf`, and the stdout-download test
  asserts body-only output.
- **The GHE cross-layer surprise** (`GIST_TOKEN` set ⇒ config `base_url`
  ignored) is the price of "one layer supplies the whole identity"; it is
  documented in three places (newClient godoc, README, this doc) because it
  will be reported as a bug otherwise.
- **Non-hermetic credential tests**: any test that forgets to neutralize
  `GIST_TOKEN` and stub `ghAuthToken` passes on CI and fails on a developer
  laptop (or vice versa).
- **Don't "optimize" with `HeadObject` preflights** — it costs a full gist
  fetch (no metadata-only endpoint, `operations.go` godoc). Download is just
  `GetObject` + error handling.
- **Sequential rapid writes to one gist can 409** (DESIGN.md decision #5).
  Not `cp`'s problem for a single object, but the reason `--recursive` is
  future work rather than a loop around `cp`.
- **The UTF-8 guard changes behavior for one niche case**: a user who
  *deliberately* stores Latin-1 text today via the library can't push it
  through `cp`. Accepted: the CLI optimizes for not corrupting binaries; the
  library remains available for intentional raw writes.

---

## 10. References

- [DESIGN.md](DESIGN.md) — §4.3 (CLI deps), §5.2 (SDK-shape contract), §5.3
  (error model), §5.4 (behavioral contracts), §5.6 (config file), §6.1 (flat
  namespace), §6.2 (text-first bodies), §9 (roadmap), §10 (decision log)
- Library source: [`gists3.go`](../gists3.go), [`operations.go`](../operations.go),
  [`config.go`](../config.go); CLI stub: [`cmd/g3/main.go`](../cmd/g3/main.go)
- `aws s3 cp` reference — <https://docs.aws.amazon.com/cli/latest/reference/s3/cp.html>
- aws-cli return codes — <https://docs.aws.amazon.com/cli/latest/topic/return-codes.html>
- GitHub Gist REST API — <https://docs.github.com/en/rest/gists/gists>
- Go `encoding/json` UTF-8 coercion (invalid bytes → U+FFFD) —
  <https://pkg.go.dev/encoding/json#Marshal>
