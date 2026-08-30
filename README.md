# gists3

[![CI](https://github.com/will-wright-eng/gists3/actions/workflows/ci.yml/badge.svg)](https://github.com/will-wright-eng/gists3/actions/workflows/ci.yml)

`g3` is a CLI that treats GitHub Gists as scrappy object storage, speaking
aws-cli vocabulary: a **bucket is a gist**, a **key is a file** inside it.
Free, durable, versioned (every edit is a git commit) storage for small
blobs — CLI tool state, shared config, CI artifacts under 1 MB.

```sh
g3 ls                                       # buckets: created, id, visibility,
                                            #   objects, size, description
g3 ls g3://<gist-id>/                       # one bucket's objects: size, key
g3 ls g3://<gist-id>/notes/                 # prefix-filtered
g3 cp conf.json g3://<gist-id>/conf.json    # upload (an upsert, like PutObject)
g3 cp conf.json g3://<gist-id>/             # key inferred from the filename
g3 cp g3://<gist-id>/conf.json backup/      # download; parent dirs created
g3 cp g3://<a>/conf.json g3://<b>/          # remote copy (client-side GET+PATCH)
date | g3 cp - g3://<gist-id>/last-run      # stdin; "-" also means stdout
g3 cp g3://<gist-id>/conf.json - | jq .     # body only — status lines are
                                            # suppressed when either end is "-"
g3 link add claudemd g3://<gist-id>/CLAUDE.md ~/.claude/CLAUDE.md
vim $(g3 path claudemd) && g3 push claudemd # linked paths: no ID, no scratch
                                            # file, no mv (below)
g3 cp @claudemd -                           # @<link> stands in for a link's
                                            # URI anywhere cp takes one
```

`g3 rm` is still to come, per the
[implementation plan](docs/002-cli-cp-ls-rm.md); the command contracts live in
[docs/001-cp-command.md](docs/001-cp-command.md),
[docs/002-ls-command.md](docs/002-ls-command.md), and
[docs/004-linked-paths.md](docs/004-linked-paths.md), the full design in
[docs/](docs/). Zero dependencies beyond the Go standard library.

## Install

```sh
make install    # builds dist/g3, copies it to $HOME/go/bin
# or, without cloning:
go install github.com/will-wright-eng/gists3/cmd/g3@latest
```

## Auth

`g3` needs a GitHub token with the `gist` scope, resolved in order:

1. `GIST_TOKEN` environment variable
2. `gh auth token` — if you use the GitHub CLI, `g3` just works

The config file never holds a token: identity and endpoint are independent
layers, so `base_url` applies whichever layer supplies the token. (Before
v0.2 a plaintext `token` field was supported; it is ignored now — migrate
with `gh auth login` or `GIST_TOKEN`.)

## Config file (optional)

`<user config dir>/gists3/config.json` (`~/.config/gists3/` on Linux,
`~/Library/Application Support/gists3/` on macOS, `%AppData%\gists3\` on
Windows):

```json
{
  "default_user": "octocat",
  "base_url": ""
}
```

`base_url` targets GitHub Enterprise. The file holds no secrets, so it is
safe to commit to a dotfiles repo.

## Linked paths

A link declares that a local file *is* the working copy of a gist key, so
round-tripping an edit stops being four commands with a 32-hex ID typed
twice:

```sh
g3 link add claudemd g3://b1e652a05136107f461cd796103508cc/CLAUDE.md ~/.claude/CLAUDE.md
g3 pull claudemd                # remote → local, if safe
vim $(g3 path claudemd)         # edit the file where it lives
g3 push claudemd                # local → remote, if safe
g3 cp @claudemd -               # @<link> is that link's URI, on either
                                #   side of cp — no ID typed
g3 status                       # per link: in-sync / local-ahead /
                                #   remote-ahead / diverged / local-missing /
                                #   remote-missing / missing
g3 link ls                      # list declarations
g3 link rm claudemd             # remove the declaration; keeps both sides
```

Every successful pull or push records a **baseline** — the content both
sides last agreed on. A direction that would overwrite unseen work (the gist
edited in the GitHub UI, unpushed local changes) is **refused**, and there
is no `--force`: reconcile with `g3 cp` in whichever direction should win,
and the link heals itself on the next `status`. The backend has no
compare-and-swap, so this turns a silent clobber into a refused command; it
does not make writes atomic.

Reconciling uses the same two names the link already gave you — `@claudemd`
for the gist, `$(g3 path claudemd)` for the file:

```sh
diff $(g3 path claudemd) <(g3 cp @claudemd -)   # see the difference
g3 cp $(g3 path claudemd) @claudemd             # local wins
g3 cp @claudemd $(g3 path claudemd)             # remote wins
g3 status claudemd                              # → in-sync, baseline adopted
```

`@<link>` always means the link's **remote URI**, whichever side of `cp` it
appears on; the local half is `g3 path`. Link names can't start with `@`, so
the sigil is unambiguous — and a local file genuinely named `@x` is still
reachable as `./@x`.

Links live in `config.json` — shareable, paths stored unexpanded, a leading
`~/` resolved per machine. Baselines live next to it in `state.json`, which
describes this machine only: don't commit it to dotfiles. `push` enforces
the same guards as `cp` (10 MiB cap, UTF-8 only). Full contract:
[docs/004-linked-paths.md](docs/004-linked-paths.md).

## The fine print

The engine under the CLI is S3-shaped (`internal/gists3`), and its
behavioral contracts surface directly in `g3`'s semantics:

| Behavior | Contract |
|---|---|
| Empty files | Uploads of empty content are refused; the Gist API rejects empty files |
| Binary content | Gist content is UTF-8 text; non-UTF-8 uploads are refused rather than silently corrupted — encode binary yourself (base64) |
| Large files | Downloads follow `raw_url` past GitHub's ~1 MB inline cap; uploads cap at 10 MiB; treat <1 MB as the comfort zone |
| Namespace | Flat. `/` is legal in keys and prefix filtering works, but there are no real folders |
| Concurrency | Last write wins; the Gist API has no compare-and-swap |
| Consistency | Eventually consistent: reads can briefly lag writes; rapid sequential updates can return HTTP 409 |
| Deletes | Idempotent like S3: removing a missing key succeeds. Deleting a gist's last file errors clearly |
| Keys | Names starting with `gistfile` are rejected — GitHub renames them positionally |
| `ls` scope | Lists every gist the token can see, `g3`-created or not |
| Bucket creation | Seeds a `.bucket` placeholder (gists can't be empty); object listings hide it |

Exit codes: 0 success, 1 runtime failure, 2 usage error.

## Security

A **secret gist is unlisted, not access-controlled**: anyone with the gist
ID can read it without authentication. Nothing sensitive belongs in a gist,
public or secret, without encrypting it first. The token is sent only as a
bearer header to the configured API base URL — which makes `base_url`
security-sensitive, so point it only at hosts you trust.

## Development

```sh
make check                       # fmt-check, vet, staticcheck, race tests, build
make cover                       # engine coverage via the black-box suite
go test -tags integration ./...  # live API (GIST_TOKEN or gh auth), cleans up after itself
```

Layout: `cmd/g3` (the product) → `internal/gists3` (S3-shaped engine) →
`internal/gistapi` (GitHub transport). `internal/gists3test` holds the
black-box suite. See [docs/003-cli-first.md](docs/003-cli-first.md) for why
the engine is internal.

## License

[MIT](LICENSE)
