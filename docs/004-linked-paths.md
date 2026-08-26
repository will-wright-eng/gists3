# 004 — Linked paths: `pull`, `push`, `status`

**Status:** Draft v0.1 (2026-08-26)
**Scope:** `cmd/g3` only — no engine changes, no new module dependencies
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

Everything here is composition over existing engine operations — `GetObject`,
`PutObject` — plus two new files under the config directory. `internal/gists3`
does not change.

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

**What this cannot be.** The backend has no compare-and-swap ([000-design.md]
(000-design.md) §5.4: *last write wins*), so there is a TOCTOU window between
the check and the write. The baseline converts a silent clobber into a refused
command; it does not make the write atomic. `WithRetry`-style conditional
writes remain roadmap WP6.

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

**Exit codes.** 0/1/2 keep their meanings from 001 §5. One addition:

| Code | Meaning |
|---|---|
| 0 | success (including "already in sync") |
| 1 | runtime failure — network, API, filesystem |
| 2 | usage error — bad arity, unknown link, malformed URI |
| **3** | **refused: the requested direction would destroy data** |

3 is defined now rather than later on purpose. Folding refusals into 1 and
splitting them out afterwards would be a breaking change for anyone scripting
against it; the cost today is one `errors.As` arm in `main()`.

---

## 4. Files on disk

Three files under `<user config dir>/gists3/`, split by lifecycle. The
question that separates them is *"can this be committed to a dotfiles repo?"*
— and each has a different answer.

| File | Content | Owner | Shareable |
|---|---|---|---|
| `config.json` | token, `base_url` | user | **no** — plaintext secret (and deprecated, §8) |
| `links.json` | link declarations | user, hand-editable | **yes** — that is the point |
| `state.json` | baseline hashes | `g3`, never hand-edited | **no** — describes this machine |

Links get their own file precisely because `config.json` cannot leave a
machine. A link table that could not be carried between machines would defeat
the feature.

### 4.1 `links.json`

```json
{
  "version": 1,
  "links": {
    "claude": {
      "uri": "g3://b1e652a05136107f461cd796103508cc/CLAUDE.md",
      "path": "~/.claude/CLAUDE.md"
    }
  }
}
```

`version` is an integer; `g3` refuses a file whose version it does not know
rather than guessing. Paths are stored **unexpanded** so the file stays
portable across machines and users.

### 4.2 `state.json`

```json
{
  "version": 1,
  "baselines": {
    "claude": { "hash": "9f86d081884c7d65…", "at": "2026-08-26T13:49:43Z" }
  }
}
```

`hash` is the hex SHA-256 the engine already computes as
`GetObjectOutput.ETag` (`operations.go:196`), so local and remote hashes are
directly comparable with no second hashing scheme. `at` is informational only
— it appears in `status` output and never participates in a decision.

Baselines are keyed by link name. Renaming a link therefore drops its baseline,
which fails safe: an absent baseline can only ever cause a refusal, never a
clobber (§5).

### 4.3 Writing

Both files are written mode `0600` via temp-file + `os.Rename` in the same
directory. Neither holds a secret, but `0600` matches the directory's existing
posture and costs nothing. Unknown top-level keys are preserved across a
rewrite — decode into `map[string]json.RawMessage`, not straight into the
struct, so a field written by a newer `g3` survives an older one.

---

## 5. The state model

Three hashes:

| | |
|---|---|
| `L` | SHA-256 of the local file's current content |
| `R` | SHA-256 of the remote key's current content |
| `B` | the baseline — `L == R` as of the last successful pull/push |

Content hashing, not mtime: editors rewrite files, restore from backups, and
touch mtimes without changing bytes. Objects are sub-1 MB by contract, so
hashing is free.

### 5.1 Resolution

Evaluated top to bottom; the first matching row wins.

| # | Condition | State | `pull` | `push` |
|---|---|---|---|---|
| 1 | local missing, remote missing | — | error (2) | error (2) |
| 2 | local missing | `local-missing` | ✅ creates | refuse (3) |
| 3 | remote key missing | `remote-missing` | refuse (3) | ✅ creates |
| 4 | `L == R` | `in-sync` | no-op | no-op |
| 5 | `B` absent | `diverged` | refuse (3) | refuse (3) |
| 6 | `B == L` | `remote-ahead` | ✅ | refuse (3) |
| 7 | `B == R` | `local-ahead` | refuse (3) | ✅ |
| 8 | otherwise | `diverged` | refuse (3) | refuse (3) |

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
`status` is a report: it exits 0 whatever it finds, and exits 1 only when it
cannot reach the API. Any baseline it adopts under row 4 is persisted.

### `g3 pull <name>` / `g3 push <name>`

Resolve state per §5.1, then act or refuse. Confirmation lines follow `cp`'s
aws-cli voice:

```
pull: g3://b1e652…/CLAUDE.md to ~/.claude/CLAUDE.md
push: ~/.claude/CLAUDE.md to g3://b1e652…/CLAUDE.md
in-sync: claude
```

A refusal names the state and the way out:

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
Parent directories are created. `push` inherits every `PutObject` contract
unchanged: empty bodies refused, UTF-8 only, reserved `gistfile` keys rejected.

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

## 8. Deprecating the `config.json` token

**`gh auth token` is the primary credential path.** `GIST_TOKEN` stays for CI.
The plaintext `token` field in `config.json` is **deprecated** as of this
document: it stores a secret at rest, forces the file to `0600`, and makes the
one file a user might otherwise share unshareable.

v1 keeps reading it — no behavior change — and adds one stderr line when the
field is present:

```
g3: warning: config.json "token" is deprecated; use gh auth login or GIST_TOKEN
```

`base_url` is not a secret and is unaffected; if `config.json` is eventually
retired entirely, `base_url` moves rather than disappears.

### 8.1 Two bugs the split avoids

Both are live today and are the reason links do **not** go in `config.json`.
Both should be fixed as part of the deprecation regardless of this feature.

1. **`GIST_TOKEN` suppresses the whole file.** `resolveConfig`
   (`client.go:45-47`) returns on the env token before `LoadConfig` runs, and
   `client_test.go:69` asserts it as a contract. Links stored there would
   silently vanish — not error, just be absent — for every user with
   `GIST_TOKEN` exported. **Links must load independently of the identity
   chain**, which they do by construction once they live in `links.json`.

2. **A token-less `config.json` is fatal.** `LoadConfig` errors with
   `token is required` (`config.go:62`) and `resolveConfig` returns any
   non-`fs.ErrNotExist` error (`client.go:56-57`), so it never falls through to
   `gh`. A `gh`-authenticated user who writes a config file for any non-token
   reason bricks the binary. Deprecating the field means an absent token must
   become a fall-through, not an error.

---

## 9. Stages

Each stage is one commit, lands green (`make check` plus its own tests), and
leaves the binary usable.

### Stage 1 — `links.json` and the `link` command set

Schema, load/save with unknown-key preservation, `link add|ls|rm`, name and
path validation, `~` expansion. No network, no engine calls.

*Done when:* a link can be declared, listed, and removed across processes.

### Stage 2 — `g3 path`

Reads stage 1's file, prints one line. Small enough to ride along with stage 1
if that reads better as a single commit.

*Done when:* `vim $(g3 path claude)` opens the right file.

### Stage 3 — `state.json` and `g3 status`

Baseline load/save, `hashLocal`/`hashRemote`, the §5.1 resolution table as a
pure function, `status` output, row-4 baseline adoption.

The resolution table is the thing to get right, and it is pure — `(L, R, B) →
state`, no I/O — so it gets an exhaustive table test.

*Done when:* every row of §5.1 is reachable and asserted.

### Stage 4 — `pull` and `push`

The acting half: refuse per the table with exit 3, otherwise transfer and write
the baseline from the transferred bytes.

*Done when:* the §11 lifecycle passes against the `httptest` fake.

### Stage 5 — deprecation, docs, integration

The §8 warning and the token fall-through fix; README section; a tagged
integration test driving the compiled binary through the live lifecycle;
roadmap conformance table updated.

---

## 10. Test matrix

| Layer | Stages | Network |
|---|---|---|
| Pure: `~` expansion, name validation, §5.1 resolution | 1, 3 | none |
| File round-trip vs temp config dir | 1, 3 | none |
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
- [ ] A refused `pull`/`push` exits 3, changes nothing on either side, and
      names the recovery path.
- [ ] Reconciling a diverged link with `g3 cp` restores `in-sync` on the next
      `status` with no repair command (§5.2).
- [ ] Links resolve identically with `GIST_TOKEN` set, unset, and with no
      `config.json` at all.
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
| Resolving links by path (`g3 push ~/.zshrc`) | Wanted for tab-completion; needs a name-vs-path disambiguation rule |
| Symlink-aware writes, atomic rename, mode preservation | Interacts: `EvalSymlinks` + temp-rename preserves the symlink but drops the mode, which `os.WriteFile` currently keeps for free (§7) |
| Directory ⟷ bucket mounts | Drags in delete propagation, per-file state, partial-failure semantics — its own document |
| Per-bucket batching for `status` | Needs an engine multi-key read (§5.3) |
| `status` exit code reflecting sync state | `git diff --quiet` shaped; only if scripting demand appears |
| Bucket alias table (`g3://claude/…`) | A link names a file, an alias names a bucket; separable, and links may prove sufficient |

---

## 13. References

- [000-design.md](000-design.md) — §5.4 (behavioral contracts: last write wins,
  eventual consistency), §5.6 (config file), §6.1 (flat namespace), §10
  decision #5 (eventual consistency observed live)
- [001-cp-command.md](001-cp-command.md) — URI grammar, exit codes, testability
  seams
- [001-roadmap.md](001-roadmap.md) — WP6 (conditional writes, the only real fix
  for the §2 TOCTOU window)
- [003-cli-first.md](003-cli-first.md) — the CLI is the product; S3 vocabulary
  binds the engine, the command surface is only aws-cli *flavored*
- Source: [`cmd/g3/client.go`](../cmd/g3/client.go) (identity chain),
  [`cmd/g3/cp.go`](../cmd/g3/cp.go) (download/upload paths),
  [`internal/gists3/operations.go`](../internal/gists3/operations.go)
  (`GetObject` ETag, `HeadObject` cost warning)
- `aws s3 sync` — the verb this deliberately does not implement —
  <https://docs.aws.amazon.com/cli/latest/reference/s3/sync.html>
- GitHub Gist REST API — <https://docs.github.com/en/rest/gists/gists>
