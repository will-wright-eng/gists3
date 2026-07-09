# gists3

[![CI](https://github.com/will-wright-eng/gists3/actions/workflows/ci.yml/badge.svg)](https://github.com/will-wright-eng/gists3/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/will-wright-eng/gists3.svg)](https://pkg.go.dev/github.com/will-wright-eng/gists3)

GitHub Gists behind an S3-shaped Go interface. If you know the AWS SDK for Go
v2, you already know this library: `PutObject`, `GetObject`, `DeleteObject`,
`ListObjectsV2` — context-first methods, pointer `Input`/`Output` structs,
typed errors. The storage backend is a gist.

This is a **syntax-compatible facade, not a protocol implementation**. An S3
SDK, `boto3`, or `rclone` cannot point at it — there are no AWS signatures,
no XML wire format, no presigned URLs. What you get instead: free, durable,
versioned (every edit is a git commit) storage for small blobs, and code that
migrates to real S3 by swapping the constructor. See [DESIGN.md](docs/DESIGN.md)
for the full design.

Zero dependencies beyond the Go standard library.

## Install

```sh
go get github.com/will-wright-eng/gists3
```

## Quickstart

```go
client := gists3.New(token) // PAT with the gist scope

// A bucket is a gist; GitHub assigns the ID.
create, err := client.CreateBucket(ctx, &gists3.CreateBucketInput{
    Description: "my tool's state",
})
bucket := create.Bucket

_, err = client.PutObject(ctx, &gists3.PutObjectInput{
    Bucket: bucket,
    Key:    "state.json",
    Body:   strings.NewReader(`{"count": 42}`),
})

out, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: bucket, Key: "state.json"})
defer out.Body.Close()
data, err := io.ReadAll(out.Body)
```

Errors branch the way S3 users expect:

```go
var nf *gists3.NotFoundError
if errors.As(err, &nf) {
    // create-on-first-read path; nf.Key == "" means the bucket itself is gone
}
```

## The fine print

Every behavioral divergence from S3 is documented on the method's godoc —
`go doc gists3.ListObjectsV2` answers "what's different" without leaving the
terminal. The highlights:

| Behavior | Contract |
|---|---|
| Empty bodies | `PutObject` refuses them (`ErrEmptyBody`); the Gist API rejects empty files |
| Binary content | Bodies are UTF-8 text; encode binary yourself (base64) |
| Large files | `GetObject` follows `raw_url` past GitHub's ~1 MB inline cap; treat <1 MB as the comfort zone |
| `HeadObject` | Not cheaper than `GetObject` — there is no metadata-only endpoint |
| Namespace | Flat. `/` is legal in keys, `Prefix` filters client-side, but there is no `Delimiter` — folders would be theater |
| ETags | Client-side hex SHA-256, not comparable to S3 ETags or anything GitHub returns |
| Concurrency | Last write wins; the Gist API has no compare-and-swap |
| Consistency | Eventually consistent: reads can briefly lag writes, and rapid sequential updates can return HTTP 409. No internal retries — wrap the HTTP client or retry at the call site |
| `DeleteObject` | Idempotent like S3: deleting a missing key succeeds. GitHub's opaque no-change 422s are disambiguated and absorbed (see godoc); deleting a gist's last file still errors, clearly |
| Keys | Names starting with `gistfile` are rejected (`ErrReservedKey`) — GitHub renames them positionally |
| `ListBuckets` | Returns every gist the token can see, gists3-created or not |
| `CreateBucket` | Seeds a `.bucket` placeholder (gists can't be empty); `ListObjectsV2` hides it |

## Config file (opt-in)

`New(token)` never reads env vars or files. For CLI use, an explicit
constructor loads `<user config dir>/gists3/config.json`
(`~/.config/gists3/` on Linux, `~/Library/Application Support/gists3/` on
macOS, `%AppData%\gists3\` on Windows):

```json
{
  "default_user": "octocat",
  "token": "ghp_...",
  "base_url": ""
}
```

```go
client, err := gists3.NewFromDefaultConfig()
```

Options beat config fields; config fields beat defaults. Keep the file mode
`0600` — the token is plaintext and `LoadConfig` warns when other users can
read it.

## Security

A **secret gist is unlisted, not access-controlled**: anyone with the gist ID
can read it without authentication. Nothing sensitive belongs in a gists3
bucket, public or secret, without application-layer encryption. The token is
sent only as a bearer header to the configured base URL — which makes
`WithBaseURL` security-sensitive, so point it only at hosts you trust.

## Testing

```sh
go test ./...                    # hermetic; fake GitHub via httptest
GIST_TOKEN=ghp_... go test -tags integration ./...   # live API, cleans up after itself
go test -tags integration ./...  # same, using the gh CLI's token (gh auth token)
```

## License

[MIT](LICENSE)
