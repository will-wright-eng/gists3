# gists3

[![CI](https://github.com/will-wright-eng/gists3/actions/workflows/ci.yml/badge.svg)](https://github.com/will-wright-eng/gists3/actions/workflows/ci.yml)

`g3` is a CLI that treats GitHub Gists as scrappy object storage, speaking
aws-cli vocabulary: a **bucket is a gist**, a **key is a file** inside it.
Free, durable, versioned (every edit is a git commit) storage for small
blobs — CLI tool state, shared config, CI artifacts under 1 MB.

```sh
g3 ls                                 # list buckets (gists)          — works today
g3 ls g3://<gist-id>/                 # list objects                  — planned
g3 cp notes.md g3://<gist-id>/notes.md  # upload (upsert)             — planned
g3 cp g3://<gist-id>/notes.md -       # download to stdout            — planned
g3 rm g3://<gist-id>/notes.md         # delete                        — planned
```

The planned surface lands in stages per the
[implementation plan](docs/002-cli-cp-ls-rm.md); the full design lives in
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
2. the config file (below)
3. `gh auth token` — if you use the GitHub CLI, `g3` just works

## Config file (optional)

`<user config dir>/gists3/config.json` (`~/.config/gists3/` on Linux,
`~/Library/Application Support/gists3/` on macOS, `%AppData%\gists3\` on
Windows):

```json
{
  "default_user": "octocat",
  "token": "ghp_...",
  "base_url": ""
}
```

`base_url` targets GitHub Enterprise. Keep the file mode `0600` — the token
is plaintext, and `g3` warns when other users can read it.

## The fine print

The engine under the CLI is S3-shaped (`internal/gists3`), and its
behavioral contracts surface directly in `g3`'s semantics:

| Behavior | Contract |
|---|---|
| Empty files | Uploads of empty content are refused; the Gist API rejects empty files |
| Binary content | Gist content is UTF-8 text; encode binary yourself (base64) |
| Large files | Downloads follow `raw_url` past GitHub's ~1 MB inline cap; treat <1 MB as the comfort zone |
| Namespace | Flat. `/` is legal in keys and prefix filtering works, but there are no real folders |
| Concurrency | Last write wins; the Gist API has no compare-and-swap |
| Consistency | Eventually consistent: reads can briefly lag writes; rapid sequential updates can return HTTP 409 |
| Deletes | Idempotent like S3: removing a missing key succeeds. Deleting a gist's last file errors clearly |
| Keys | Names starting with `gistfile` are rejected — GitHub renames them positionally |
| `ls` scope | Lists every gist the token can see, `g3`-created or not |
| Bucket creation | Seeds a `.bucket` placeholder (gists can't be empty); object listings hide it |

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
