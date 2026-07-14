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
```

`g3 rm` is still to come, per the
[implementation plan](docs/002-cli-cp-ls-rm.md); the command contracts live in
[docs/001-cp-command.md](docs/001-cp-command.md) and
[docs/002-ls-command.md](docs/002-ls-command.md), the full design in
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

The layer that supplies the token supplies the whole identity: with
`GIST_TOKEN` set, the config file is not consulted, so its `base_url` does
not apply — GitHub Enterprise users relying on `base_url` should unset
`GIST_TOKEN`.

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
