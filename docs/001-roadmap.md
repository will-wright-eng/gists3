# gists3 — Remaining Work to Design Conformance

**Status:** Draft v0.1 (2026-07-08)
**Baseline:** [000-design.md](000-design.md) Draft v0.2; code as of this date
**Scope:** every gap between the design and the working tree, specified far
enough that each item can be built without reopening design discussion

---

## 1. Purpose

000-design.md describes the destination; this document is the delta. It has two
halves: places where the **code must move toward the design** (§4, the work
packages), and places where the **design must move toward the code** (§3) —
the implementation made deliberate improvements the design doc predates, and
those get recorded as amendments, not reverted.

## 2. Conformance snapshot

| Design area | Status |
|---|---|
| §5.2 method surface (10 methods) | ✅ Complete, tested (37 unit tests, 85% coverage) |
| §5.3 error model | ✅ Complete (`NotFoundError`, `RateLimitError`, `APIError`, sentinels) |
| §5.4 behavioral contracts | ✅ Complete, incl. the 422-disambiguation and truncation contracts |
| §5.6 config file | ✅ Complete except `token_command` (v1.1, WP2) |
| §4.1 zero dependencies | ✅ `go.mod` has no requires |
| §7 testing strategy | ✅ Hermetic unit suite + tagged integration suite + example smoke test |
| §3 directory structure | ⚠️ Code is *better* than the design; 000-design.md is stale (WP8) |
| §9 v1.1 items (base64, retry, token_command) | ❌ Not started (WP2–WP4) |
| §9 v1.2 items (VersionID, conditional writes) | ❌ Not started (WP5–WP6) |
| §9 v2 CLI (`cmd/g3`) | ⚠️ Stub only: `ls` works, `--help` errors, no config identity, no `cp`/`rm`, no `g3://` URIs (WP1) |
| §8 encryption example | ❌ Not written (WP7) |

## 3. Design amendments (code wins, 000-design.md updates)

These divergences were deliberate and are hereby adopted; WP8 folds them
into 000-design.md.

1. **Layout.** The flat root of DESIGN §3 evolved: the GitHub transport and
   wire types live in `internal/gistapi` (compiler-enforced encapsulation),
   the black-box test suite and fixtures live in `internal/gists3test`, the
   root package is three files (`gists3.go`, `operations.go`, `config.go`),
   the demo lives at `cmd/example`, and design docs live under `docs/`.
2. **Error conversion at the boundary.** `gistapi` has its own error types;
   the façade's `do()` converts them to the public types. Chosen over type
   aliases so public godoc stays complete.
3. **Binary credential fallback.** `cmd/example` and `cmd/g3` fall back to
   `gh auth token` when `GIST_TOKEN` is unset. This is a *binary-level*
   convenience and does not touch the library's "never ambient" rule
   (§5.6.1). WP1 formalizes the full CLI chain.
4. **Build/install tooling.** `make build` emits `dist/g3`; `make install`
   copies it to `$HOME/go/bin`; pre-commit hooks mirror `make check`. None
   of this is design-relevant but §3's tree should show `dist/` as ignored
   output.
5. **The CLI is the product** *(added 2026-07-08)*. The engine moved from
   the module root to `internal/gists3`; the library-first premise is
   superseded. Decision record: [003-cli-first.md](003-cli-first.md).

## 4. Work packages

Ordered by the milestone plan in §5. Every work package inherits two
standing requirements from the design: each exported symbol gets a godoc
comment stating round-trip cost and S3 divergence (§5.5), and each behavior
lands with hermetic unit tests, plus integration coverage where live-API
behavior is the point (§7).

### WP1 — `g3` CLI: from stub to the §9 surface

The design (§9, decision log #3) promises an aws-cli-flavored `cp`/`ls`/`rm`
over `g3://` URIs, reading §5.6 config for identity. The stub has none of
this and mishandles `--help`. Staged implementation plan:
[002-cli-cp-ls-rm.md](002-cli-cp-ls-rm.md).

**Dependencies.** Standard-library `flag` only (§4.3). Cobra stays out: the
module graph includes `cmd/` deps, and three subcommands do not justify
breaking the zero-dependency pillar or splitting a second module.

**URI scheme.** `g3://<gist-id>[/<key>]`. The key may contain further
slashes (flat namespace, §6.1); parsing splits at the first `/` after the
gist ID. A bare `g3://<gist-id>` addresses the bucket.

**Command surface.**

| Command | Maps to | Notes |
|---|---|---|
| `g3 ls` | `ListBuckets` | Current behavior; gains `default_user` labeling on stderr when config supplies it (§5.6.5) |
| `g3 ls g3://<id>[/<prefix>]` | `ListObjectsV2` | Prefix is the client-side filter the library already implements |
| `g3 cp <local> g3://<id>/<key>` | `PutObject` | Upsert; there is deliberately no update subcommand (decision log #3) |
| `g3 cp g3://<id>/<key> <local>` | `GetObject` | Writes file; `-` means stdout |
| `g3 cp g3://<a>/<k1> g3://<b>/<k2>` | `CopyObject` | Remote-to-remote |
| `g3 rm g3://<id>/<key>` | `DeleteObject` | Inherits S3-idempotent success on missing keys (§5.4) |

Bucket lifecycle subcommands (`mb`/`rb`) are **not** in the design's list —
see Open Questions.

**Identity chain** (first hit wins; formalizes amendment #3):

1. `GIST_TOKEN` environment variable — explicit always beats stored.
2. `~/.config/gists3/config.json` via `LoadConfig` — a *missing* file moves
   to the next source; a malformed file or one without a usable token is a
   hard error (silently skipping a file the user wrote hides their bug).
   `Config.Warnings` (e.g. the chmod-600 warning) print to stderr.
3. `gh auth token` — the zero-setup floor.

**Conventions.** Data to stdout, diagnostics to stderr. Exit 0 on success,
1 on runtime errors, 2 on usage errors. `-h`/`--help` at top level and per
subcommand exit 0 with usage — the current stub's `unknown command "--help"`
is the bug that motivates this row of acceptance criteria.

**Testing.** Refactor `main` so each subcommand is a function taking a
`*gists3.Client` and returning an error; unit-test those against the same
`httptest` fake the library suite uses (URI parsing gets its own table
test). One integration-tagged test drives the compiled binary end to end.

**Acceptance.**
- `g3 --help`, `g3 ls --help`, etc. exit 0 and print usage.
- Full lifecycle works against the live API: `cp` up, `ls` shows it, `cp`
  down round-trips bytes, `rm` removes it.
- With a config file present and no `GIST_TOKEN`, identity comes from the
  config and `ls` labels output with `default_user`.

### WP2 — `token_command` config field (v1.1, §5.6.4)

A safer alternative to the plaintext `token` field: the config names a
command whose stdout is the token (e.g. `pass show github/gists3`).

**Spec.**
- New field `token_command string` in `Config`. Setting both `token` and
  `token_command` is a validation error — precedence between two credential
  sources inside one file is a footgun, not a feature.
- `LoadConfig` resolves it: runs the command via the user's shell
  (`$SHELL -c`, falling back to `sh -c`), trims trailing whitespace, and
  stores the result in `Config.Token` so `NewFromConfig`'s signature is
  untouched. Empty output or non-zero exit is an error naming the command.
- Security note in godoc: the command executes with the caller's
  privileges; the config file location is only as trustworthy as its
  permissions, which is exactly why the 0600 warning exists.

**Acceptance.** Unit tests cover: command success, command failure, empty
output, both-fields-set rejection. The permission warning still fires on a
world-readable file even when it holds no plaintext token.

### WP3 — `WithBase64Bodies()` client option (v1.1, §6.2)

Opt-in binary safety: encode on `PutObject`, decode on `GetObject`.

**Decisions the sketch left open, resolved here.**
- **ETag is computed over the caller's bytes** (plaintext), on both put and
  get. The ETag contract is "your content's hash", not "what GitHub
  stored".
- `GetObject.ContentLength` is the decoded length. `HeadObject` and
  `ListObjectsV2` report GitHub's stored (encoded) size — computing decoded
  size without fetching is guesswork; the godoc states the divergence.
- The `.bucket` placeholder and `DeleteObject`'s quirk-recovery rewrite
  content stay plain text: they are library-internal writes, and keeping
  them readable in the gist UI outweighs symmetry.
- A base64 client reading a plain-text object (or vice versa) fails with a
  decode error, documented: the option is per-client, so use dedicated
  buckets. No sniffing, no per-object flags — that would be a format
  negotiation protocol the backend can't honor.

**Acceptance.** Round-trip test with bytes invalid as UTF-8; ETag equality
between put and get; truncated-file path decodes after the `raw_url` fetch.

### WP4 — `WithRetry(n)` client option (v1.1, §9)

Default stays no-retry (§5.3); the option is for callers who met §5.4's
eventual consistency (decision log #5) and want it handled.

**Spec.**
- Retries up to `n` additional attempts on: `RateLimitError` (sleep until
  `ResetAt`, capped and context-aware), HTTP 409 (the observed
  gist-update race), and 5xx. Never on other 4xx.
- Implemented around the façade's `do()`/`fetchRaw()` so `publicErr` typing
  is preserved. Request bodies are wire structs re-marshaled per attempt —
  the buffered-body design (§6.3) makes replay safe for free.
- Backoff: exponential from 250 ms, jittered, ceiling 10 s; `ResetAt`
  overrides the schedule when present. All sleeps respect `ctx`.

**Acceptance.** Fake-server tests: 409-then-success succeeds with n=1;
rate-limit reset is honored; context cancellation aborts a pending sleep;
n=0 (or option absent) preserves today's single-attempt behavior byte for
byte.

### WP5 — `GetObjectInput.VersionID` (v1.2, §9)

Read-only S3-versioning vocabulary over gist revision history:
`VersionID` is a gist revision SHA, fetched via `GET /gists/{id}/{sha}`.
Applies to `GetObject` (and `HeadObject` for symmetry); write paths reject
a non-empty `VersionID` with a validation error rather than ignoring it.
Listing revisions is out of scope until a use case demands it.

### WP6 — Conditional-writes spike (v1.2, §9)

Time-boxed investigation, not a feature commitment: can PATCH be made
conditional on a revision SHA (`If-Match` or equivalent)? The deliverable
is a decision-log entry with live-API evidence. If the API can't honor it,
the entry says so and the roadmap row closes as won't-do — per §2 non-goal
3, we do not fake compare-and-swap.

### WP7 — Encryption example (§8)

A documented example (`cmd/example` gains a flag, or a testable example in
the package docs — implementer's choice) showing AES-GCM encrypt-before-put
and decrypt-after-get, fulfilling §8's promise that confidentiality lives
in the caller, not in a half-serious library crypto layer.

### WP8 — 000-design.md amendments

Fold §3 of this document into 000-design.md: rewrite its §3 tree to match
reality, add decision-log entries for the transport split, boundary error
conversion, and the CLI identity chain, and update §9's CLI row to point at
WP1 here. Delete this document's §3 once absorbed.

## 5. Sequencing

| Milestone | Contents | Rationale |
|---|---|---|
| M0 — stub honesty | WP1 help/usage handling only; WP8 | Smallest fix for the observable defect (`--help` errors); design doc stops lying about layout |
| M1 — CLI conformance | Rest of WP1 | The largest visible gap; unblocks real dogfooding, which historically surfaced API quirks (decision log #4–5) |
| M2 — v1.1 library | WP2, WP3, WP4 | Independent of each other; parallelizable |
| M3 — v1.2 + docs | WP5, WP6, WP7 | Versioned reads benefit from CLI dogfooding; spike may close as won't-do |

Multi-gist buckets (§9, v2) stay out of scope pending their own design
document — the index-consistency problem is not solvable in a roadmap row.

## 6. Open questions

1. **`mb`/`rb` subcommands.** The design's CLI list is `cp`/`ls`/`rm`, but
   without `mb` (CreateBucket) the CLI can only write into gists created
   elsewhere. Proposal: add `mb`/`rb` as aws-cli users expect. Needs a yes
   before WP1 implements them; the URI grammar already accommodates
   bucket-only forms.
2. **Description column in `g3 ls`.** `Bucket` exposes only `Name` and
   `CreationDate`; showing gist descriptions would require adding a
   `Description` field to `ListBuckets` output (backward-compatible, but an
   API addition the design didn't spec).
3. **`g3 cat`.** `g3 cp g3://… -` covers it; a `cat` alias is sugar. Adopt
   or drop during WP1 review.
