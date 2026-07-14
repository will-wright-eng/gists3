# 002 — `g3 ls`: detailed bucket and object listings

**Status:** Implemented — 2026-07-13
**Scope:** `cmd/g3` plus additive `gists3.Bucket` fields; no wire-cost change
**Depends on:** [001-cp-command.md](001-cp-command.md) (CLI structure, URI grammar, exit codes)

---

## 1. Overview

`g3 ls` grows two ways:

1. **Bucket listing (no argument)** gains detail: creation timestamp,
   visibility, object count, total size, and description — one line per gist,
   replacing the previous two-column output.
2. **Object listing (`g3 ls g3://<gist-id>[/<prefix>]`)** lists a bucket's
   objects, aws-style; previously any argument was a usage error.

```console
$ g3 ls
2026-06-21 09:14  b1e652a05136107f461cd796103508cc  secret   3 objects    4.1K  ci state for gists3
2026-05-12 17:40  98ea46b9676ec5d0aed5f2c8f789b3bd  public   1 object      812  dotfiles: zshrc + aliases
2025-12-15 08:02  b6d6c00d9c6f4c932d3ed2ffde3eb156  secret   0 objects       0

$ g3 ls g3://b1e652a05136107f461cd796103508cc
  1.2K  conf.json
   812  notes/2026.md
  2.1K  state.json

$ g3 ls g3://b1e652a05136107f461cd796103508cc/notes/
   812  notes/2026.md
```

Every field comes from API calls the command already makes: the `GET /gists`
list response carries description, visibility, timestamps, and per-file
sizes, so the richer output costs **zero additional round-trips**; the object
listing is one `ListObjectsV2` call (`GET /gists/{id}`).

Decisions (chosen from the alternatives in review, 2026-07-13):

- **Full detail is the default, not behind a `-l` flag.** All the data is
  free, the CLI is young enough to change its output, and zero flags remain
  the norm (001 §6.1).
- **The timestamp is `created_at`**, keeping `aws s3 ls` parity (S3 buckets
  only have a `CreationDate`). `updated_at` is mapped in the library for
  callers that want it.
- **Object counts and total sizes exclude the `.bucket` placeholder**,
  matching `ListObjectsV2`'s hiding of it — a fresh gists3 bucket honestly
  shows `0 objects`.

---

## 2. Output specification

### 2.1 Bucket listing

```
<created "2006-01-02 15:04">  <gist-id>  <public|secret>  <%2d object(s), padded>  <%6s size>  <description>
```

- `public`/`secret` are both six characters; no extra padding needed.
- The count pluralizes (`1 object`, `3 objects`) with the unit padded to
  seven characters so the size column stays aligned.
- Sizes are humanized: bytes verbatim below 1 KiB (`812`, `0`), one decimal
  with `K`/`M` above (`4.1K`, `38.2K`, `1.2M`) — a gist's ~10 MB ceiling
  needs nothing larger. The unit promotes as soon as `%.1f` rounding would
  reach a fourth integer digit, so the rendered size never exceeds the
  six-character column (`1023949` bytes is `1.0M`, never `1000.0K`).
  *(Added after review — the original draft overflowed just under 1 MiB.)*
- The description is appended only when non-empty, with control characters
  flattened to spaces and the result trimmed — a multi-line or
  escape-bearing description cannot break the one-line-per-gist contract or
  emit terminal control sequences. Lines never carry trailing whitespace.
  *(Sanitization added after review.)*
- Ordering is unchanged: as returned by GitHub (most recently updated first),
  full set via the library's internal pagination.

### 2.2 Object listing

```
<%6s size>  <key>
```

`aws s3 ls s3://bucket` prints a per-object `LastModified` first; gists have
no per-file timestamps, so the column is omitted rather than faked
(DESIGN.md goal 3). Keys sort ascending via `ListObjectsV2`; `.bucket` is
hidden by the library.

**Prefix filtering:** `g3 ls g3://<id>/<prefix>` passes everything after the
first slash — trailing slash or not — as `ListObjectsV2Input.Prefix`
(client-side `strings.HasPrefix`, 001's flat-namespace rules apply). A bare
`g3://<id>` or `g3://<id>/` lists everything.

### 2.3 Argument handling and exit codes

Extending 001 §4.5 (which originally declared `ls` argument-free):

| Invocation | Behavior |
|---|---|
| `g3 ls` | bucket listing |
| `g3 ls g3://<id>[/<prefix>]` | object listing |
| `g3 ls <local-path>` or `-` | usage error (exit 2) — ls lists gists |
| `g3 ls a b` | usage error (exit 2) — at most one argument |

Runtime failures (missing gist → `NotFoundError`, rate limits) exit 1, as
everywhere.

---

## 3. Library changes (additive)

`gists3.Bucket` grows beyond its S3-shaped core (`Name`, `CreationDate`) —
documented as a divergence that surfaces gist reality rather than inventing
S3 semantics:

```go
type Bucket struct {
	Name         string
	CreationDate time.Time

	// Beyond the S3 shape — gist metadata the list response carries anyway:
	Description string
	Public      bool
	UpdatedAt   time.Time
	ObjectCount int   // files excluding the ".bucket" placeholder
	TotalSize   int64 // bytes, same exclusion
}
```

`internal/gistapi.Gist` gains the matching wire fields (`description`,
`public`, `updated_at`); the list response's `files` map was already decoded.
No method signatures change; `ListBuckets`' cost is unchanged.

---

## 4. Testing

- `internal/gists3test/list_test.go`: a `ListBuckets` field-mapping test
  (description/public/updated_at, count and size excluding `.bucket`).
- `cmd/g3/main_test.go`: bucket-listing format locked against a full
  fixture (plural and singular counts, empty description, humanized sizes);
  object listing with and without a prefix; usage errors for local/stdio
  arguments and two arguments. The previous `{"ls", "g3://..."}` usage-error
  case inverts: it is now the object-listing happy path.

---

## 5. References

- [001-cp-command.md](001-cp-command.md) — URI grammar, exit codes, seams
- [DESIGN.md](DESIGN.md) — §5.2 (SDK-shape contract), §6.1 (flat namespace),
  §10 decision #2 (`ListBuckets` returns everything)
- `aws s3 ls` — <https://docs.aws.amazon.com/cli/latest/reference/s3/ls.html>
- Gist list API (fields per gist) — <https://docs.github.com/en/rest/gists/gists#list-gists-for-the-authenticated-user>
