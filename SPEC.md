# gists3 — Design Document

**Status:** Draft v0.2 — open questions resolved (2026-07-04)
**Language:** Go (1.22+)
**License:** MIT (proposed)

---

## 1. Overview

`gists3` is a Go library that wraps the GitHub Gist REST API behind an interface shaped like the AWS SDK for Go v2 `s3.Client`. It lets a developer read and write small blobs of data using familiar S3 vocabulary — `PutObject`, `GetObject`, `DeleteObject`, `ListObjectsV2` — while the storage backend is a GitHub gist.

### 1.1 What this is

A **syntax-compatible facade**, not a protocol implementation. Code written against `gists3` reads like code written against the AWS SDK: context-first methods, pointer `Input` structs, pointer `Output` structs, typed errors. Migrating a small tool from `gists3` to real S3 (or vice versa) should be a mechanical change of constructor and import, not a rewrite.

### 1.2 What this is not

This is **not** an S3-compatible endpoint. An S3 SDK, `boto3`, `mc`, or `rclone` cannot point at it. We do not implement AWS signature v4, the S3 XML wire format, presigned URLs, multipart uploads, or bucket policies. Anyone needing wire-level S3 compatibility should run MinIO or Garage instead.

### 1.3 Why

Gists are free, durable, versioned (every edit is a git commit), and available anywhere with a GitHub token. For low-write/low-volume use cases — CLI tool state, CI job artifacts under 1 MB, shared config, demo persistence for side projects — a gist is "good enough object storage," and the S3-shaped interface makes the code portable to real object storage the day the project outgrows it.

### 1.4 Concept mapping

| S3 concept | Gist backing | Notes |
|---|---|---|
| Bucket | A gist (identified by gist ID) | IDs are GitHub-assigned; no chosen bucket names |
| Key | A filename within the gist | Flat namespace; `/` is legal in a filename but has no delimiter semantics |
| Object body | File content | Text-oriented; see §6.2 for binary handling |
| ETag | SHA-256 of content, computed client-side | Not comparable to anything GitHub returns |
| Region / endpoint | GitHub API base URL | Overridable for GitHub Enterprise |
| Credentials | Personal access token with `gist` scope | Single-principal; no IAM analogue |
| Object versioning | Gist revision history (git commits) | Read-only access via revision SHA (stretch goal, §9) |

---

## 2. Goals and non-goals

### Goals

1. **Drop-in familiarity.** Method names, signatures, and struct shapes mirror `aws-sdk-go-v2/service/s3` closely enough that an S3-literate reader needs no docs to understand call sites.
2. **Zero third-party dependencies in the core library.** `net/http`, `encoding/json`, and the standard library are sufficient. The dependency graph of a storage shim should be empty.
3. **Honest failure modes.** Every Gist-API limitation (rate limits, 1 MB truncation, no empty files, flat namespace) surfaces as a documented, typed error or a documented behavior — never a silent corruption.
4. **Context-first, test-friendly.** Every network call takes a `context.Context`; the HTTP client and base URL are injectable so the entire library is testable against `httptest.Server` with no mocks framework.

### Non-goals

1. Wire-level S3 protocol compatibility (signatures, XML, presigned URLs).
2. Multipart upload, byte-range GETs, or streaming writes. Bodies are buffered in memory; the backend caps practical object size anyway.
3. Concurrency control beyond what the Gist API gives us. There is no compare-and-swap on the Gist REST API; last write wins. (Optimistic locking via revision SHAs is a possible v2 exploration, §9.)
4. Access control. A gist is public or secret; that's the whole model.
5. Performance. GitHub allows 5,000 authenticated REST requests/hour. This library is for convenience, not throughput.

---

## 3. Directory structure

Idiomatic flat library layout. The root package is the product; everything else is auxiliary.

```
gists3/
├── go.mod                    # module github.com/<owner>/gists3; no requires
├── go.sum                    # empty / absent (stdlib only)
├── LICENSE
├── README.md                 # quickstart, badge, link to this doc
├── DESIGN.md                 # this document
├── gists3.go                 # Client, New(), Option, do() transport helper
├── errors.go                 # NotFoundError, RateLimitError, APIError
├── config.go                 # Config, LoadConfig, NewFromConfig — opt-in file config (§5.6)
├── bucket.go                 # CreateBucket, DeleteBucket, HeadBucket
├── object.go                 # PutObject, GetObject, HeadObject, DeleteObject, CopyObject
├── list.go                   # ListObjectsV2, ListBuckets
├── wire.go                   # unexported GitHub JSON wire types
├── gists3_test.go            # unit tests against httptest.Server fixtures
├── integration_test.go       # live-API tests, gated by //go:build integration
├── testdata/
│   └── fixtures/             # canned Gist API JSON responses
├── example/
│   └── main.go               # runnable end-to-end lifecycle demo
└── cmd/
    └── g3/                   # (stretch, §9) small CLI: cp/ls/rm, aws-cli flavored
        └── main.go
```

Rationale for the notable choices:

- **Root package, not `pkg/`.** Consumers import `github.com/<owner>/gists3` and call `gists3.New(...)`. A `pkg/` wrapper adds a path segment and nothing else for a single-package library.
- **Split by S3 noun (`bucket.go`, `object.go`, `list.go`), not by layer.** Each file maps to a slice of the S3 surface area, so "where is PutObject" has an obvious answer. The shared transport (`do`) and client construction live in `gists3.go`.
- **`wire.go` isolates GitHub's JSON shapes.** The public API never leaks a GitHub wire type; if GitHub changes a field, the blast radius is one file.
- **Integration tests behind a build tag.** `go test ./...` stays fast and hermetic; `go test -tags integration ./...` with `GIST_TOKEN` set exercises the real API and cleans up after itself.

---

## 4. Dependencies

### 4.1 Core library

**None beyond the Go standard library.**

| Need | Stdlib answer |
|---|---|
| HTTP transport | `net/http` |
| JSON encode/decode | `encoding/json` |
| ETag computation | `crypto/sha256`, `encoding/hex` |
| Cancellation/timeouts | `context` |
| Body handling | `io`, `bytes`, `strings` |

This is a deliberate constraint, not an accident. A library whose job is "call one REST API" should cost its consumers zero transitive dependencies. It also sidesteps the temptation to depend on `google/go-github`, which is large, moves fast, and would dominate our dependency graph for the ~6 endpoints we call.

### 4.2 Tooling (dev-time only, not in `go.mod` requires)

- `gofmt` / `go vet` — enforced in CI.
- `staticcheck` — recommended, run in CI, not a module dependency.
- `golangci-lint` — optional aggregate.

### 4.3 Stretch CLI (`cmd/g3`, if built)

Standard library `flag` is sufficient for a `cp`/`ls`/`rm` surface. If subcommand ergonomics demand it, `spf13/cobra` is acceptable **inside `cmd/` only** — the library module stays clean because Go module graphs include `cmd/` deps. If that trade-off bites, the CLI moves to a separate module (`gists3/cmd/g3/go.mod`). The binary is named `g3` to match the `g3://` URI scheme it consumes.

### 4.4 External service dependencies

- **GitHub REST API v3** (`https://api.github.com`), version header `2022-11-28`.
  - Endpoints used: `POST /gists`, `GET /gists/{id}`, `PATCH /gists/{id}`, `DELETE /gists/{id}`, `GET /gists` (list), plus unauthenticated fetches of `raw_url` for truncated content.
- **Auth:** fine-grained or classic PAT with `gist` scope, supplied by the caller. The core constructor `New(token)` never reads env vars or files on its own. Callers who want ambient credentials opt in through the config file mechanism (§5.6), which lives behind explicitly named constructors rather than a magic default chain.

---

## 5. Ergonomic interface

The contract: **if you know the AWS SDK for Go v2, you already know this library.**

### 5.1 Construction

```go
client := gists3.New(token)

// with options
client := gists3.New(token,
    gists3.WithHTTPClient(&http.Client{Timeout: 15 * time.Second}),
    gists3.WithBaseURL("https://github.example.com/api/v3"), // GHE
)
```

Functional options, not a config struct: the zero-value path (`New(token)`) is one argument, and options compose without breaking API compatibility as they're added. No global state, no `init()`, no env-var reading.

### 5.2 Method surface (v1)

Every method: `func (c *Client) Verb(ctx context.Context, in *VerbInput) (*VerbOutput, error)`. Input/Output structs exist even when nearly empty — that's the SDK convention that keeps signatures stable as fields are added.

```go
// Buckets
CreateBucket(ctx, *CreateBucketInput) (*CreateBucketOutput, error)   // POST /gists
HeadBucket(ctx, *HeadBucketInput) (*HeadBucketOutput, error)         // GET /gists/{id}, existence check
DeleteBucket(ctx, *DeleteBucketInput) (*DeleteBucketOutput, error)   // DELETE /gists/{id}
ListBuckets(ctx, *ListBucketsInput) (*ListBucketsOutput, error)      // GET /gists — ALL caller's gists, unfiltered (§10)

// Objects
PutObject(ctx, *PutObjectInput) (*PutObjectOutput, error)            // PATCH /gists/{id}
GetObject(ctx, *GetObjectInput) (*GetObjectOutput, error)            // GET /gists/{id} (+ raw_url fallback)
HeadObject(ctx, *HeadObjectInput) (*HeadObjectOutput, error)         // GET /gists/{id}, metadata only
DeleteObject(ctx, *DeleteObjectInput) (*DeleteObjectOutput, error)   // PATCH /gists/{id} with null file
CopyObject(ctx, *CopyObjectInput) (*CopyObjectOutput, error)         // GET + PATCH composition

// Listing
ListObjectsV2(ctx, *ListObjectsV2Input) (*ListObjectsV2Output, error) // GET /gists/{id}, client-side filter
```

Representative struct shapes:

```go
type PutObjectInput struct {
    Bucket string    // gist ID
    Key    string    // filename
    Body   io.Reader // buffered fully in memory; see §6.2
}

type PutObjectOutput struct {
    ETag string // hex sha256 of the written content
}

type GetObjectOutput struct {
    Body          io.ReadCloser // caller must Close, exactly like the AWS SDK
    ContentLength int64
    ETag          string
}

type ListObjectsV2Input struct {
    Bucket string
    Prefix string // client-side strings.HasPrefix; no Delimiter field (see §6.1)
}

type ListObjectsV2Output struct {
    Contents []Object // sorted by Key ascending, matching S3 ordering
}

type Object struct {
    Key  string
    Size int64
}
```

Deliberate deviations from the AWS SDK, chosen to avoid lying:

1. **Fields are plain values, not pointers.** The AWS SDK's `*string` fields exist for wire-level presence semantics we don't have. `aws.String("x")` noise is ergonomic tax with no payoff here.
2. **No `Delimiter` / `CommonPrefixes` on ListObjectsV2.** Gist filenames are a flat namespace. Offering delimiter grouping would simulate hierarchy the backend can't honor. `Prefix` is honest (documented as a client-side filter); `Delimiter` would be theater.
3. **No paginator types.** A gist holds at most a few hundred files returned in one response. `ContinuationToken` fields would be dead weight. `ListBuckets` handles GitHub's page-based pagination internally and returns the full set.

### 5.3 Error model

Typed, sentinel-checkable errors that mirror how S3 users already branch:

```go
// NotFoundError covers both NoSuchBucket and NoSuchKey.
// Key == "" means the bucket itself was not found.
type NotFoundError struct {
    Bucket string
    Key    string
}

// RateLimitError surfaces GitHub 403/429 rate-limit responses.
type RateLimitError struct {
    ResetAt time.Time // parsed from X-RateLimit-Reset
}

// APIError is the fallback for any other non-2xx response.
type APIError struct {
    StatusCode int
    Method     string
    Path       string
    Message    string // GitHub's error body, truncated
}
```

Usage matches AWS-SDK idiom:

```go
out, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: b, Key: "conf.json"})
var nf *gists3.NotFoundError
if errors.As(err, &nf) {
    // create-on-first-read path
}
```

Rules: every error is wrapped with `%w` from a stable root, `errors.As`/`errors.Is` always work, and no GitHub wire detail is required to handle the common cases. The library does **not** retry internally in v1 — retries are policy, and callers who want them can wrap the injected `http.Client` (this stance is revisited in §9).

### 5.4 Behavioral contracts (the fine print, stated up front)

| Behavior | Contract |
|---|---|
| Empty bodies | `PutObject` with an empty body returns an error. The Gist API rejects empty file content; we refuse loudly rather than write a sentinel byte. |
| Large files | `GetObject` transparently follows `raw_url` when GitHub marks content truncated (>~1 MB inline). GitHub hard-caps pushed files well below typical S3 object sizes; treat ~10 MB as the practical ceiling and <1 MB as the comfort zone. |
| `HeadObject` cost | Not cheaper than the gist fetch — the Gist API has no metadata-only endpoint. It fetches the gist and discards content. Documented so nobody "optimizes" by calling Head first. |
| Write semantics | `PutObject` is an **upsert**: a single PATCH creates the key if absent and replaces its content if present. There is no separate "update" verb, matching S3. |
| Write concurrency | Last write wins. Two concurrent `PutObject` calls to the same bucket can interleave at the whole-gist level. |
| `ListBuckets` scope | Returns **every** gist the token can see, gists3-created or not. No filtering is applied; callers who care can check for the presence of a `.bucket` key themselves. |
| `CreateBucket` placeholder | Gist requires ≥1 file at creation; a `.bucket` placeholder file is seeded. `ListObjectsV2` **excludes** `.bucket` from results so the abstraction doesn't leak by default; `GetObject(".bucket")` still works for the curious. |
| Key restrictions | `gistfile*` names are reserved by GitHub's positional-rename behavior; the library rejects them in `PutObject` to prevent surprising renames. |

### 5.5 Doc comments as interface

Every exported symbol carries a godoc comment, and every method's comment states its GitHub-API cost (how many HTTP round-trips) and its S3 divergences. The measure of success: `go doc gists3.ListObjectsV2` answers "what's different from real S3" without opening this document.

### 5.6 Configuration file

For CLI use and quick scripts, gists3 supports an **opt-in** config file that specifies the default user and credentials:

```
~/.config/gists3/config.json                       # Linux (or $XDG_CONFIG_HOME/gists3/)
~/Library/Application Support/gists3/config.json   # macOS
%AppData%\gists3\config.json                       # Windows
```

The path is resolved via `os.UserConfigDir()`, which honors `$XDG_CONFIG_HOME` on Linux and falls back to `$HOME/.config`. Schema and API:

```json
{
  "default_user": "octocat",
  "token": "ghp_...",
  "base_url": ""
}
```

```go
type Config struct {
    DefaultUser string `json:"default_user"`
    Token       string `json:"token,omitempty"`
    BaseURL     string `json:"base_url,omitempty"` // empty = https://api.github.com
}

func LoadConfig() (*Config, error)                         // read + validate the default path
func NewFromConfig(cfg *Config, opts ...Option) *Client    // construct from an explicit Config
func NewFromDefaultConfig(opts ...Option) (*Client, error) // LoadConfig + NewFromConfig
```

Rules:

1. **Opt-in, never ambient.** `New(token)` remains the primary constructor and never touches the filesystem. Config enters only through the explicitly named `...FromConfig` constructors, so any call site makes it visible whether ambient state is in play.
2. **Precedence.** Functional options beat config fields (`WithBaseURL` overrides `base_url`); config fields beat built-in defaults. There is no env-var layer in v1.
3. **JSON, not TOML or YAML.** Preserves the zero-dependency pillar (§4.1). Comments in config files would be pleasant; a parser dependency to get them is not.
4. **Permissions.** The token sits in plaintext, so the CLI creates the file `0600`, and `LoadConfig` surfaces a warning when the file is group- or world-readable. A `token_command` field (shell out to a secret manager, e.g. `pass show github/gists3`) is specced for v1.1 as the safer alternative.
5. **Role of `default_user`.** Informational in v1 — the token alone determines API identity. It exists so the CLI can label output (`g3 ls` showing whose gists are listed) and reserves room for multi-profile support later without a schema break.

---

## 6. Design decisions and trade-offs

### 6.1 Flat namespace, honestly

S3 users expect `photos/2024/img.png` to behave like a path. In a gist it's just a filename that happens to contain slashes. We support such keys (GitHub allows them) and support `Prefix` filtering, but we refuse `Delimiter` semantics rather than emulate folders client-side. Emulation would work right up until someone's mental model of `CommonPrefixes` met a 300-file gist, and the debugging session that follows costs more than the feature is worth.

### 6.2 Text-first bodies, binary via encoding

Gist file content is UTF-8 text in a JSON field. Arbitrary binary bytes are not safe to round-trip. v1 stores bodies as-is and documents that binary-unsafe content is the caller's responsibility. A `WithBase64Bodies()` client option is specced for v1.1: transparently base64-encode on `PutObject` / decode on `GetObject`, trading 33% size overhead for binary safety. It is an option, not a default, because it breaks human-readability of the gist in the web UI — one of the genuine perks of this backend.

### 6.3 Buffered bodies

`PutObjectInput.Body` is an `io.Reader` for SDK familiarity, but it is read fully into memory. With a practical object ceiling around 1 MB, streaming machinery would be complexity without benefit. The godoc says so plainly.

### 6.4 Client-side ETags

GitHub doesn't return a content hash we can use as an S3-style ETag. We compute SHA-256 client-side on `PutObject` and `GetObject`. This gives callers integrity checking and change detection between their own reads/writes, with the documented caveat that it's our hash, not GitHub's, and `ListObjectsV2` omits it (computing it would require fetching every file).

### 6.5 No interface type in v1

We export the concrete `*Client`, not a `Storage` interface. Consumers who need to swap gists3/real-S3 behind an interface should define that interface themselves at their call site with exactly the methods they use — the standard Go advice ("accept interfaces, return structs; define interfaces where they're consumed"). Shipping our own interface would freeze our method set prematurely.

---

## 7. Testing strategy

**Unit tests (default, hermetic).** `httptest.Server` plays GitHub using fixtures in `testdata/fixtures/`. Coverage targets: every method's happy path; 404 → `NotFoundError` mapping; 403-with-rate-limit-headers → `RateLimitError`; truncated-file → `raw_url` fallback; empty-body and reserved-key rejection; `.bucket` exclusion in listings.

**Integration tests (opt-in).** `//go:build integration`, driven by `GIST_TOKEN`, run the full lifecycle (create → put → get → head → list → copy → delete → delete bucket) against the live API in a uniquely-described gist, with cleanup in `t.Cleanup` so failures don't strand gists. CI runs these on a schedule rather than per-PR to respect rate limits.

**Example as smoke test.** `example/main.go` is the runnable end-to-end demo and doubles as living documentation; CI compiles it always, runs it only in the integration job.

---

## 8. Security considerations

- **Tokens.** The library holds the PAT in memory, sends it only as `Authorization: Bearer` over TLS to the configured base URL, and never logs it. `WithBaseURL` is documented as security-sensitive (misdirecting it exfiltrates the token).
- **Secret ≠ private.** Secret gists are unlisted, not access-controlled: anyone with the URL (or gist ID) can read them, no auth required. The godoc for `CreateBucket` states this in bold terms. **Nothing sensitive belongs in a gists3 bucket, public or secret, without application-layer encryption.**
- **No encryption features in v1.** SSE-style options would imply a security posture the backend can't deliver. Callers needing confidentiality should encrypt before `PutObject`; a documented example will show `crypto/aes` GCM usage rather than the library growing a half-serious crypto layer.

---

## 9. Roadmap / stretch goals

| Version | Item | Sketch |
|---|---|---|
| v1.1 | `WithBase64Bodies()` | Binary-safe bodies, opt-in (§6.2) |
| v1.1 | `WithRetry(n)` | Opt-in retry with backoff honoring `X-RateLimit-Reset`; default stays no-retry |
| v1.1 | `token_command` config field | Shell out to a secret manager instead of storing a plaintext token (§5.6) |
| v1.2 | `GetObjectInput.VersionID` | Read a key at a historical gist revision SHA — S3 versioning vocabulary over gist commit history, read-only |
| v1.2 | Conditional writes | Investigate `If-Match`-style optimistic concurrency via revision SHA on PATCH; only if the API can actually honor it |
| v2 | `cmd/g3` CLI | `g3 cp local.txt g3://<gist-id>/remote.txt`, `ls`, `rm`; aws-cli flavored. `cp` is an upsert — the underlying PATCH creates or replaces the file, so there is no separate update subcommand. Reads §5.6 config for identity. |
| v2 | Multi-gist buckets | Shard one logical bucket across gists to bypass per-gist file limits; adds an index-consistency problem — needs its own design doc before any code |

---

## 10. Decision log

Resolved 2026-07-04:

1. **Default identity comes from a config file.** The default user (and optionally token and base URL) lives in `~/.config/gists3/config.json` — per-OS path resolved via `os.UserConfigDir()` — loaded only through explicit opt-in constructors. Full spec in §5.6. The Go module path itself remains a placeholder (`github.com/<owner>/gists3`) until the repository is created; as an import path it simply follows wherever the code is published.
2. **`ListBuckets` returns everything.** Every gist the token can see comes back, gists3-created or not. No implicit filtering, no `OnlyBuckets` flag; callers who care can filter on the presence of a `.bucket` key themselves.
3. **No `RenameObject`; updates fold into `cp`.** S3 has no rename abstraction, so neither does gists3 — the vocabulary stays strictly S3. Updating a file is not a distinct operation either: `PutObject` (and therefore `g3 cp <local-source> <remote-gist>` in the CLI) is an upsert, with the Gist PATCH underneath creating or replacing the file as needed. Gist's native in-place rename capability goes deliberately unused.
