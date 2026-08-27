# 004 — Linked paths: `pull`, `push`, `status`

**Status:** Draft v0.2 (2026-08-26)
**Scope:** `cmd/g3`, plus removal of config-file token auth from
`internal/gists3` (§8); no new module dependencies
**Depends on:** [003-cli-first.md](003-cli-first.md) (CLI verbs need not be S3
verbs), [001-cp-command.md](001-cp-command.md) (URI grammar, exit codes, seams)

---

## 1. Overview

A **link** declares that a local path *is* the working copy of a remote key:

```
~/.claude/CLAUDE.md  ⟷  g3://b1e652a05136107f461cd796103508cc/CLAUDE.md
```

Today, editing that file round-trips through four commands, an opaque 32-hex
ID typed twice, and a scratch file in whatever directory you happened to be in:

```sh
g3 cp g3://b1e652a05136107f461cd796103508cc/CLAUDE.md .
vim CLAUDE.md
g3 cp CLAUDE.md g3://b1e652a05136107f461cd796103508cc/CLAUDE.md
mv CLAUDE.md ~/.claude/CLAUDE.md
```

With a link named `claude`:

```sh
g3 pull claude
vim $(g3 path claude)
g3 push claude
```

No ID, no scratch file, no `mv` — the file is edited where it lives. `g3 path`
is deliberately a plain path on stdout rather than an `edit` subcommand, so it
composes with any editor, pager, or diff tool.

The command surface is composition over existing engine operations —
`GetObject`, `PutObject` — plus a `links` section in the config file and one
new state file. The single engine change is a subtraction: config-file token
auth goes away (§8), which is what lets links live in `config.json` at all.

---

## 2. Why `cp` is not enough

`cp` is a directional copy with an explicit source and destination, so
clobbering is always the caller's stated intent. Naming a pair and reducing it
to `g3 push claude` removes that statement, and with it the safety: **both
sides can change independently.** A `push` after someone edited the gist in the
GitHub UI silently destroys their edit; a `pull` over unsaved local work
silently destroys yours.

So a link is not just an address book entry. It is an address book entry plus a
**baseline** — a record of what both sides last agreed on — and the baseline is
what makes `pull`/`push` safe enough to be worth having.

**What this cannot be.** The backend has no compare-and-swap
([000-design.md](000-design.md) §5.4: *last write wins*; §9 lists conditional
writes as an unproven v1.2 investigation), so there is a TOCTOU window between
the check and the write. The baseline converts a silent clobber into a refused
command; it does not make the write atomic. Conditional writes remain roadmap
WP6 — a different item from WP4's `WithRetry`, which is retry policy, not
compare-and-swap.

---

## 3. Command surface

```
g3 link add <name> g3://<gist-id>/<key> <path>   declare a link (no network)
g3 link ls                                        list declarations
g3 link rm <name>                                 remove a declaration only
g3 status [<name>]                                report sync state
g3 pull <name>                                    remote → local, if safe
g3 push <name>                                    local → remote, if safe
g3 path <name>                                    print the local path
```

Names match `[A-Za-z0-9._-]+`. That excludes `/`, `:` and a leading `@`, so a
name can never be confused with a path or a `g3://` URI, and leaves `@name`
free as a future sigil for links inside `cp`.

**Streams** follow 001 §4.5 unchanged: status lines and command data go to
**stdout**, every diagnostic to **stderr**. Stdout purity is load-bearing for
`$(g3 path claude)` exactly as it is for `g3 cp g3://b/k -`.

**Exit codes** stay the three 001 §4.5 defines — no new code:

| Code | Meaning |
|---|---|
| 0 | success, including "already in sync" |
| 1 | runtime failure — network, API, filesystem, **and refused directions** |
| 2 | usage error — bad arity, unknown link, malformed URI |

A refused `pull`/`push` is an exit-1 runtime failure, not its own code. That
follows the precedent already in 001 §4.5, which puts "guard rejections (§4.6)"
— the UTF-8 and size refusals, refusals in exactly this sense — at 1. Scripts
distinguish a refusal from a network failure by reading stderr; giving them a
dedicated code is deferred (§12) rather than spent here.

---

## 4. Files on disk

Two files under `<user config dir>/gists3/`, split by the question *"can this
be committed to a dotfiles repo?"*:

| File | Content | Owner | Shareable |
|---|---|---|---|
| `config.json` | `default_user`, `base_url`, `links` | user, hand-editable | **yes** — nothing secret in it once §8 lands |
| `state.json` | baseline hashes | `g3`, never hand-edited | **no** — describes this machine |

Links belong in `config.json` because §8 removes the only thing that ever made
that file unshareable. What cannot join them is `state.json`: a baseline
describes the working copy on one machine, so carrying it to another would
assert agreement that was never established there.

### 4.1 `config.json`

```json
{
  "default_user": "octocat",
  "base_url": "",
  "links": {
    "claude": {
      "uri": "g3://b1e652a05136107f461cd796103508cc/CLAUDE.md",
      "path": "~/.claude/CLAUDE.md"
    }
  }
}
```

`links` is an addition to the existing schema, so the file keeps its current
shape and stays readable by a `g3` that predates this document. Paths are
stored **unexpanded** so the file stays portable across machines and users.

### 4.2 `state.json`

```json
{
  "version": 1,
  "baselines": {
    "claude": { "hash": "9f86d081884c7d65…", "at": "2026-08-26T13:49:43Z" }
  }
}
```

A new file, so it carries a version from the start; `g3` refuses a version it
does not know rather than guessing. `config.json` gets no version field —
it has none today, and requiring one would break exactly the older binaries
the additive `links` key is designed not to disturb.

`hash` is the hex SHA-256 the engine already computes as
`GetObjectOutput.ETag` (`operations.go:196`), so local and remote hashes are
directly comparable with no second hashing scheme. `at` is informational only
— it appears in `status` output and never participates in a decision.

Baselines are keyed by link name. Renaming a link therefore drops its baseline,
which fails safe: an absent baseline can only ever cause a refusal, never a
clobber (§5).

### 4.3 Writing

Both files are written mode `0600` via temp-file + `os.Rename` in the same
directory. Neither holds a secret once §8 lands — `0600` is simply the
directory's existing posture, and widening it buys nothing.

**Unknown-key preservation is now load-bearing**, not merely polite:
`link add` and `link rm` rewrite a file that also holds the user's
`default_user` and `base_url`, and may hold fields written by a newer `g3`.
Decode into `map[string]json.RawMessage`, edit the `links` key, re-encode —
never round-trip through the `Config` struct, which would silently drop
everything it does not know about.

---

## 5. The state model

Three hashes:

| | |
|---|---|
| `L` | SHA-256 of the local file's current content |
| `R` | SHA-256 of the remote key's current content |
| `B` | the baseline — `L == R` as of the last successful pull/push |

Content hashing, not mtime: editors rewrite files, restore from backups, and
touch mtimes without changing bytes. Bodies are capped at 10 MiB by `cp`'s
upload guard (001 §4.6) with <1 MB the documented comfort zone, so hashing
costs nothing worth measuring.

### 5.1 Resolution

Evaluated top to bottom; the first matching row wins.

| # | Condition | State | `pull` | `push` |
|---|---|---|---|---|
| 1 | local missing, remote missing | — | error (1) | error (1) |
| 2 | local missing | `local-missing` | ✅ creates | refuse (1) |
| 3 | remote key missing | `remote-missing` | refuse (1) | ✅ creates |
| 4 | `L == R` | `in-sync` | no-op | no-op |
| 5 | `B` absent | `diverged` | refuse (1) | refuse (1) |
| 6 | `B == L` | `remote-ahead` | ✅ | refuse (1) |
| 7 | `B == R` | `local-ahead` | refuse (1) | ✅ |
| 8 | otherwise | `diverged` | refuse (1) | refuse (1) |

**Row 3 also absorbs a dead bucket.** `GetObject` reports both "this gist is
gone" and "this gist has no such file" as `*NotFoundError`, distinguished only
by whether `Key` is empty (`operations.go:202-203`). The table does not split
them: a vanished gist is labelled `remote-missing`, and `push` against it fails
with the API's own not-found error rather than a state the tool predicted. The
cost is a status line that reads as if `push` would fix a gist that no longer
exists; the saving is one state and one branch. Re-point or drop such a link
with `g3 link rm`.

**Row 4 is the load-bearing one.** `L == R` means in sync *regardless of what
the baseline says or whether one exists* — and it adopts `L` as the new
baseline. That single rule covers three situations at once: the first `status`
after `link add`, a baseline lost to a rename, and — most importantly — the
recovery path below.

### 5.2 There is no `--force`

A refusal is final. The tool will not offer to overwrite, because a flag that
resolves a conflict by discarding one side is not a resolution, it is a
faster way to lose the work you were being warned about.

Resolution is manual, with the tools that already exist:

```sh
# see the difference — the remote side streams to stdout, the local side is a file
diff $(g3 path claude) <(g3 cp g3://<id>/CLAUDE.md -)

# reconcile by hand, or edit the gist in the GitHub UI, then pick a winner:
g3 cp $(g3 path claude) g3://<id>/CLAUDE.md    # local wins
g3 cp g3://<id>/CLAUDE.md $(g3 path claude)    # remote wins

g3 status claude                               # L == R → row 4 → baseline adopted
```

The last step is why row 4 matters: once the two sides genuinely agree, the
link heals itself and `pull`/`push` work again. No repair subcommand, no
`--adopt`, no state surgery.

### 5.3 Cost

One `GetObject` per link — `status` with no argument is N round trips for N
links. `HeadObject` is not an optimization; its godoc says so outright
(`operations.go:248`: *"It is NOT cheaper than GetObject"*), because the Gist
API has no metadata endpoint and both calls fetch the whole gist.

Links sharing a bucket therefore re-fetch the same gist. For the handful of
dotfiles this feature targets that is acceptable, and fixing it properly needs
an engine addition (a bucket-level multi-key read), which §1 puts out of
scope. Revisit if a real link table grows past ~10.

---

## 6. Command contracts

### `g3 link add <name> <uri> <path>`

Pure declaration — **no network**. Validates: name charset, name not already
present (a clobber requires an explicit `link rm` first), URI parses with a
non-empty key (prefix forms are for `ls`), path is absolute or `~`-rooted.
Relative paths are rejected: a stored link has no meaningful base directory.

The remote is not verified to exist. A typo'd gist ID surfaces on the next
`status` as a `NotFoundError`, which is soon enough and keeps `link add`
usable without credentials.

### `g3 link ls`

One line per link, name-sorted:

```
claude   ~/.claude/CLAUDE.md   g3://b1e652a05136107f461cd796103508cc/CLAUDE.md
```

### `g3 link rm <name>`

Removes the declaration and its baseline. **Never touches the gist or the local
file** — and says so, because `g3 rm` (planned, 002 stage 6) deletes remote
objects and lives one word away:

```
unlinked: claude
  kept ~/.claude/CLAUDE.md
  kept g3://b1e652a05136107f461cd796103508cc/CLAUDE.md
```

### `g3 status [<name>]`

State, name, path — one line per link, name-sorted. With a name, just that one.

```
in-sync        claude   ~/.claude/CLAUDE.md
local-ahead    zshrc    ~/.zshrc
remote-ahead   gitcfg   ~/.gitconfig
diverged       notes    ~/notes/scratch.md
remote-missing new      ~/new.md
```

States are the hyphenated labels from §5.1 — greppable, fixed vocabulary.
`status` exits 0 whatever states it finds; only a failure to complete the
report is an error.

**Errors abort the report.** `NotFoundError` is already absorbed as
`remote-missing` (§5.1), so anything else reaching `status` — `RateLimitError`,
`APIError`, a network failure — is a global condition rather than a property of
one link: if link 3 of 5 hits the rate limit, links 4 and 5 will too. `status`
prints the rows it completed, writes the error to stderr, and exits 1, instead
of repeating one global failure once per remaining row. Any baseline adopted
under row 4 before the abort is still persisted.

### `g3 pull <name>` / `g3 push <name>`

Resolve state per §5.1, then act or refuse. Confirmation lines go to stdout in
`cp`'s aws-cli voice (001 §4.5):

```
pull: g3://b1e652…/CLAUDE.md to ~/.claude/CLAUDE.md
push: ~/.claude/CLAUDE.md to g3://b1e652…/CLAUDE.md
in-sync: claude
```

A refusal goes to stderr, names the state and the way out, and exits 1:

```
g3: refused: claude is diverged — local and remote both changed since the last sync.
    Reconcile with g3 cp, then re-run g3 status. (see docs/004-linked-paths.md §5.2)
```

**The baseline is written from the bytes just transferred, never from a
confirming re-read.** The backend is eventually consistent (000-design.md §10,
decision #5: a `GET` immediately after a `PATCH` can miss the write), so a
read-back would poison the baseline with stale content and manufacture a phantom
`remote-ahead` on the next status.

`pull` writes with `os.WriteFile`, matching `cp`'s existing download path
(`cp.go:138`) — created files land `0644`, existing files keep their mode.
Parent directories are created.

`push` reads the local file through `cp`'s `readBody` (`cp.go:92-104`) before
calling `PutObject`, so it inherits the 10 MiB cap and the UTF-8 check that
001 §4.6 specifies for uploads. That reuse is deliberate: routing straight to
`PutObject` would let `g3 push` upload a binary file that `g3 cp` refuses —
the same bytes to the same destination, answered differently depending on which
command was typed — and 001 §5 records that non-UTF-8 content is *corrupted
silently* by JSON-string storage rather than merely rejected. Guard rejections
exit 1, per 001 §4.5. From `PutObject` itself, `push` inherits empty-body
rejection and the reserved-`gistfile*`-key rule (`operations.go:406-414`).

### `g3 path <name>`

Prints the fully expanded absolute path and nothing else — no trailing text, no
stderr chatter on success — so `$(g3 path claude)` is safe to interpolate. It
does not check that the file exists; `status` is for that. Unknown name exits 2.

---

## 7. Path semantics

- **`~` expansion**: a leading `~/` only, resolved via `os.UserHomeDir`. No
  `~user`, no environment variables, no shell. Predictability beats power in a
  string that decides which file gets overwritten.
- **Storage**: unexpanded, exactly as the user typed it.
- **Symlinks**: deferred. v1 writes through whatever the path resolves to,
  which is what `os.WriteFile` already does. A symlinked dotfile is therefore
  updated in place rather than replaced — the behavior most dotfile setups
  want, but it is untested and unspecified until it gets its own pass (§12).

---

## 8. Removing config-file token auth

**`gh auth token` is the primary credential path**, with `GIST_TOKEN` for CI.
The plaintext `token` field in `config.json` is **removed outright** — not
deprecated through a release. Carrying it any longer would mean shipping links
inside a file that cannot be shared, which is the one property that makes a
link table worth having.

This is a breaking change for anyone whose identity came from the config file.
The migration is `gh auth login`, or exporting `GIST_TOKEN`.

### 8.1 What goes

`Config` keeps only what is not a secret:

```go
type Config struct {
    DefaultUser string `json:"default_user"`
    BaseURL     string `json:"base_url,omitempty"`
    Links       map[string]Link `json:"links,omitempty"`
}
```

- **`Config.Token`** and `LoadConfig`'s `token is required` error
  (`config.go:61-63`).
- **`NewFromConfig` and `NewFromDefaultConfig`** (`config.go:74-94`). With no
  token in the file, a config-shaped constructor has nothing left to construct
  from; `cmd/g3` calls `gists3.New(token, gists3.WithBaseURL(cfg.BaseURL))`
  directly. 000 §5.6.1's "opt-in, never ambient" rule loses its subject rather
  than being violated — with no token in the file, nothing ambient reaches the
  engine at all.
- **`Config.Warnings` and the `0600` permission warning** (`config.go:27-29`,
  `64-70`), which existed solely to protect a plaintext token at rest.

### 8.2 Two bugs that cease to exist

Neither needs fixing, because the removal deletes the conditions that produce
them. Both were the original reason to keep links out of `config.json`.

1. **`GIST_TOKEN` suppressed the whole file.** `resolveConfig`
   (`client.go:45-47`) returned on the env token before `LoadConfig` ran, so
   anything else in the file — including `base_url` — was ignored;
   `client_test.go:69` asserts exactly that. Once the file no longer supplies
   identity, there is no identity layering to short-circuit, and `base_url`
   applies unconditionally. **That is a behavior change**: the README currently
   documents the opposite ("with `GIST_TOKEN` set… its `base_url` does not
   apply — GitHub Enterprise users relying on `base_url` should unset
   `GIST_TOKEN`"), and needs updating.

2. **A token-less `config.json` was fatal.** `LoadConfig` errored with
   `token is required` and `resolveConfig` returned any non-`fs.ErrNotExist`
   error (`client.go:56-57`), so a `gh`-authenticated user who wrote a config
   file for any other reason bricked the binary. There is no token field left
   to be absent.

Two contract tests assert the removed behavior and invert:
`TestLoadConfigMissingToken` (`internal/gists3test/config_test.go:56`) and the
`"token-less config must be fatal, not fall through to gh"` case
(`cmd/g3/client_test.go:146`).

---

## 9. Stages

Each stage is one commit, lands green (`make check` plus its own tests), and
leaves the binary usable.

### Stage 1 — remove config-file token auth

All of §8: shrink `Config`, delete the `...FromConfig` constructors and the
warning mechanism, rewrite `resolveConfig` to `GIST_TOKEN` → `gh auth token`
with the file consulted only for `base_url`/`default_user`, invert the two
contract tests, update the README auth section.

First because it reshapes `Config`, which every later stage extends.

*Done when:* identity resolves from `gh` with no `config.json` present, and
`base_url` applies with `GIST_TOKEN` set.

### Stage 2 — `links` in `config.json`, the `link` command set, and `g3 path`

Schema addition, load/save with unknown-key preservation, `link add|ls|rm`,
name and path validation, `~` expansion, `g3 path`. No network, no engine calls.

*Done when:* a link survives a round trip through the file and
`vim $(g3 path claude)` opens the right file — with `default_user` and
`base_url` untouched by the rewrite.

### Stage 3 — `state.json` and `g3 status`

Baseline load/save, `hashLocal`/`hashRemote`, the §5.1 resolution table as a
pure function, `status` output, row-4 baseline adoption, abort-on-error.

The resolution table is the thing to get right, and it is pure — `(L, R, B) →
state`, no I/O — so it gets an exhaustive table test.

*Done when:* every row of §5.1 is reachable and asserted.

### Stage 4 — `pull` and `push`

The acting half: refuse per the table with exit 1, otherwise transfer and write
the baseline from the transferred bytes. `push` routes through `readBody`.

*Done when:* the §11 lifecycle passes against the `httptest` fake.

### Stage 5 — docs and integration

README link section; a tagged integration test driving the compiled binary
through the live lifecycle; roadmap conformance table updated, including WP2's
status now that `token_command` has no `token` field to improve on.

---

## 10. Test matrix

| Layer | Stages | Network |
|---|---|---|
| Pure: `~` expansion, name validation, §5.1 resolution | 2, 3 | none |
| Config round-trip vs temp config dir, unknown-key preservation | 1, 2, 3 | none |
| Command funcs vs `httptest` fake | 3, 4 | loopback |
| Compiled binary vs live API (tagged) | 5 | live, self-cleaning |

Config-dir redirection already exists: `setConfigDir` (`client_test.go:18`)
points `os.UserConfigDir` at a temp dir across Linux, macOS, and Windows by
setting `HOME`/`XDG_CONFIG_HOME`/`AppData`. Reuse it rather than adding a
package-level seam.

---

## 11. Acceptance

- [ ] `g3 link add claude g3://<id>/CLAUDE.md ~/.claude/CLAUDE.md` then
      `vim $(g3 path claude)` then `g3 push claude` — the original four-command
      workflow, minus the ID, the scratch file, and the `mv`.
- [ ] Every state in §5.1 is reachable and correctly labeled by `status`.
- [ ] A refused `pull`/`push` exits 1, changes nothing on either side, and
      names the recovery path.
- [ ] Reconciling a diverged link with `g3 cp` restores `in-sync` on the next
      `status` with no repair command (§5.2).
- [ ] `link add` on a config file holding `default_user` and `base_url` leaves
      both intact.
- [ ] Identity resolves with `GIST_TOKEN` set, unset, and with no `config.json`
      at all; `base_url` applies in every case.
- [ ] `push` refuses a non-UTF-8 file with the same error `cp` gives.
- [ ] `link rm` leaves both the gist and the local file in place.
- [ ] `make check` green at every stage; zero new module dependencies.

---

## 12. Deferred

**Deliberately absent, not merely unbuilt:** `--force` and any other
conflict-resolution flag (§5.2). Resolution belongs to `g3 cp` and the GitHub
UI.

Genuinely deferred:

| Item | Blocked on / note |
|---|---|
| `g3 edit <name>` | `g3 path` piped into an editor covers it; revisit only if the pipe chafes |
| A distinct exit code for refusals | Would break 001 §4.5's 0/1/2 contract; revisit if scripts need it without parsing stderr (§3) |
| A `bucket-missing` state | Row 3 absorbs a dead gist today (§5.1); split it if the misleading status line proves annoying |
| Resolving links by path (`g3 push ~/.zshrc`) | Wanted for tab-completion; needs a name-vs-path disambiguation rule |
| Symlink-aware writes, atomic rename, mode preservation | Interacts: `EvalSymlinks` + temp-rename preserves the symlink but drops the mode, which `os.WriteFile` currently keeps for free (§7) |
| Directory ⟷ bucket mounts | Drags in delete propagation, per-file state, partial-failure semantics — its own document |
| Per-bucket batching for `status` | Needs an engine multi-key read (§5.3) |
| `status` exit code reflecting sync state | `git diff --quiet` shaped; only if scripting demand appears |
| Bucket alias table (`g3://claude/…`) | A link names a file, an alias names a bucket; separable, and links may prove sufficient |

---

## 13. References

- [000-design.md](000-design.md) — §5.4 (behavioral contracts: last write wins,
  `HeadObject` cost), §5.6 (config file, and §5.6.1's "opt-in, never ambient"
  rule that §8 retires), §6.1 (flat namespace), §9 (conditional writes as a
  v1.2 investigation), §10 decision #5 (eventual consistency observed live)
- [001-cp-command.md](001-cp-command.md) — §4.5 (status output, streams, and
  the 0/1/2 exit-code taxonomy), §4.6 (upload guards), §5 (divergences,
  including silent corruption of non-UTF-8 bodies)
- [001-roadmap.md](001-roadmap.md) — WP2 (`token_command`, obsoleted by §8),
  WP6 (conditional writes, the only real fix for the §2 TOCTOU window)
- [003-cli-first.md](003-cli-first.md) — the CLI is the product; S3 vocabulary
  binds the engine, the command surface is only aws-cli *flavored*
- Source: [`cmd/g3/client.go`](../cmd/g3/client.go) (identity chain),
  [`cmd/g3/cp.go`](../cmd/g3/cp.go) (`readBody` guards, download path),
  [`internal/gists3/config.go`](../internal/gists3/config.go) (the file §8
  shrinks), [`internal/gists3/operations.go`](../internal/gists3/operations.go)
  (`GetObject` ETag and not-found shapes, `HeadObject` cost warning)
- `aws s3 sync` — the verb this deliberately does not implement —
  <https://docs.aws.amazon.com/cli/latest/reference/s3/sync.html>
- GitHub Gist REST API — <https://docs.github.com/en/rest/gists/gists>
