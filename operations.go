package gists3

// This file holds the S3-shaped operations: bucket-level calls (a bucket is
// a gist), then object-level calls (an object is a file within it), then
// listings.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/will-wright-eng/gists3/internal/gistapi"
)

// bucketPlaceholder seeds new buckets: the Gist API requires at least one
// file per gist. ListObjectsV2 hides it; GetObject can still read it.
const bucketPlaceholder = ".bucket"

// CreateBucketInput carries gist-level settings for a new bucket. There is
// no Bucket (name) field: gist IDs are GitHub-assigned.
type CreateBucketInput struct {
	// Description labels the gist in the GitHub UI. Optional.
	Description string

	// Public creates a public gist; the default is a secret one. A secret
	// gist is unlisted, NOT access-controlled: anyone with the gist ID can
	// read it without authentication. Do not store sensitive data in any
	// gists3 bucket without application-layer encryption.
	Public bool
}

// CreateBucketOutput returns the GitHub-assigned gist ID. Keep it — it is
// the only handle to the bucket.
type CreateBucketOutput struct {
	Bucket string
}

// CreateBucket creates a gist seeded with a ".bucket" placeholder file
// (gists cannot be created empty).
//
// A SECRET GIST IS UNLISTED, NOT ACCESS-CONTROLLED: anyone who has or
// guesses the gist ID can read it without authentication — Public only
// controls discoverability. Do not store sensitive data in any gists3
// bucket without application-layer encryption.
//
// Cost: 1 round-trip (POST /gists). Divergence from S3: no bucket name is
// chosen; the returned ID addresses the bucket from here on.
func (c *Client) CreateBucket(ctx context.Context, in *CreateBucketInput) (*CreateBucketOutput, error) {
	if in == nil {
		in = &CreateBucketInput{}
	}
	body := &gistapi.GistCreate{
		Description: in.Description,
		Public:      in.Public,
		Files: map[string]*gistapi.FileEdit{
			bucketPlaceholder: {Content: "created by gists3"},
		},
	}
	var g gistapi.Gist
	if err := c.do(ctx, http.MethodPost, "/gists", body, &g); err != nil {
		return nil, err
	}
	return &CreateBucketOutput{Bucket: g.ID}, nil
}

// HeadBucketInput identifies the gist to check.
type HeadBucketInput struct {
	Bucket string
}

// HeadBucketOutput is empty; existence is signaled by a nil error.
type HeadBucketOutput struct{}

// HeadBucket verifies the bucket exists and the token can see it, returning
// *NotFoundError otherwise.
//
// Cost: 1 round-trip (GET /gists/{id}); the response body is discarded.
func (c *Client) HeadBucket(ctx context.Context, in *HeadBucketInput) (*HeadBucketOutput, error) {
	if in == nil {
		in = &HeadBucketInput{}
	}
	if err := validateBucket(in.Bucket); err != nil {
		return nil, err
	}
	if err := c.do(ctx, http.MethodGet, "/gists/"+url.PathEscape(in.Bucket), nil, nil); err != nil {
		return nil, notFound(err, in.Bucket, "")
	}
	return &HeadBucketOutput{}, nil
}

// DeleteBucketInput identifies the gist to delete.
type DeleteBucketInput struct {
	Bucket string
}

// DeleteBucketOutput is empty.
type DeleteBucketOutput struct{}

// DeleteBucket deletes the gist and everything in it. Divergence from S3:
// the bucket does not have to be empty first.
//
// Cost: 1 round-trip (DELETE /gists/{id}).
func (c *Client) DeleteBucket(ctx context.Context, in *DeleteBucketInput) (*DeleteBucketOutput, error) {
	if in == nil {
		in = &DeleteBucketInput{}
	}
	if err := validateBucket(in.Bucket); err != nil {
		return nil, err
	}
	if err := c.do(ctx, http.MethodDelete, "/gists/"+url.PathEscape(in.Bucket), nil, nil); err != nil {
		return nil, notFound(err, in.Bucket, "")
	}
	return &DeleteBucketOutput{}, nil
}

// PutObjectInput names the destination and supplies the content.
type PutObjectInput struct {
	Bucket string // gist ID
	Key    string // filename within the gist

	// Body is read fully into memory before upload — there is no streaming;
	// the backend's ~1 MB comfort zone makes buffering the honest choice.
	// Content must round-trip as UTF-8 text: the Gist API stores file
	// content in a JSON string, so arbitrary binary bytes are not safe
	// (encode them yourself, e.g. base64, until a WithBase64Bodies option
	// ships).
	Body io.Reader
}

// PutObjectOutput reports the client-side content hash.
type PutObjectOutput struct {
	// ETag is the hex SHA-256 of the written content, computed by this
	// library. It is not comparable to S3 ETags or anything GitHub returns.
	ETag string
}

// PutObject is an upsert: one PATCH creates the key if absent and replaces
// its content if present, matching S3 semantics. Empty bodies return
// ErrEmptyBody (the Gist API rejects empty files) and keys beginning with
// "gistfile" return ErrReservedKey (GitHub renames such files positionally).
// Concurrent writers to the same bucket are last-write-wins at whole-gist
// granularity.
//
// Cost: 1 round-trip (PATCH /gists/{id}).
func (c *Client) PutObject(ctx context.Context, in *PutObjectInput) (*PutObjectOutput, error) {
	if in == nil {
		in = &PutObjectInput{}
	}
	if err := validateBucket(in.Bucket); err != nil {
		return nil, err
	}
	if err := validatePutKey(in.Key); err != nil {
		return nil, err
	}
	if in.Body == nil {
		return nil, ErrEmptyBody
	}
	b, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, fmt.Errorf("gists3: read PutObject body: %w", err)
	}
	if len(b) == 0 {
		return nil, ErrEmptyBody
	}
	patch := &gistapi.GistPatch{Files: map[string]*gistapi.FileEdit{in.Key: {Content: string(b)}}}
	if err := c.do(ctx, http.MethodPatch, "/gists/"+url.PathEscape(in.Bucket), patch, nil); err != nil {
		return nil, notFound(err, in.Bucket, "")
	}
	return &PutObjectOutput{ETag: etag(b)}, nil
}

// GetObjectInput names the object to fetch.
type GetObjectInput struct {
	Bucket string
	Key    string
}

// GetObjectOutput carries the object content, fully buffered in memory.
type GetObjectOutput struct {
	// Body must be Closed by the caller, exactly like the AWS SDK. The
	// content is already buffered; Close never fails.
	Body          io.ReadCloser
	ContentLength int64

	// ETag is the hex SHA-256 of the returned content, computed
	// client-side.
	ETag string
}

// GetObject fetches the object. When GitHub truncates inline content (files
// over ~1 MB), the file's raw_url is followed transparently; that second
// request is unauthenticated — the token never leaves the configured base
// URL. A missing key returns *NotFoundError with Key set; a missing bucket
// returns one with Key == "".
//
// Cost: 1 round-trip (GET /gists/{id}), +1 unauthenticated raw fetch when
// truncated.
func (c *Client) GetObject(ctx context.Context, in *GetObjectInput) (*GetObjectOutput, error) {
	if in == nil {
		in = &GetObjectInput{}
	}
	if err := validateObjectKey(in.Key); err != nil {
		return nil, err
	}
	g, err := c.getGist(ctx, in.Bucket)
	if err != nil {
		return nil, err
	}
	f, ok := g.Files[in.Key]
	if !ok {
		return nil, &NotFoundError{Bucket: in.Bucket, Key: in.Key}
	}
	content := []byte(f.Content)
	if f.Truncated {
		if content, err = c.fetchRaw(ctx, f.RawURL); err != nil {
			return nil, err
		}
	}
	return &GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader(content)),
		ContentLength: int64(len(content)),
		ETag:          etag(content),
	}, nil
}

// HeadObjectInput names the object to describe.
type HeadObjectInput struct {
	Bucket string
	Key    string
}

// HeadObjectOutput carries metadata only. No ETag: computing one would need
// the full content; use GetObject when you need it.
type HeadObjectOutput struct {
	// ContentLength is the size GitHub reports for the file.
	ContentLength int64
}

// HeadObject reports object metadata. It is NOT cheaper than GetObject: the
// Gist API has no metadata-only endpoint, so the whole gist is fetched and
// its content discarded. Do not "optimize" by calling this first.
//
// Cost: 1 round-trip (GET /gists/{id}).
func (c *Client) HeadObject(ctx context.Context, in *HeadObjectInput) (*HeadObjectOutput, error) {
	if in == nil {
		in = &HeadObjectInput{}
	}
	if err := validateObjectKey(in.Key); err != nil {
		return nil, err
	}
	g, err := c.getGist(ctx, in.Bucket)
	if err != nil {
		return nil, err
	}
	f, ok := g.Files[in.Key]
	if !ok {
		return nil, &NotFoundError{Bucket: in.Bucket, Key: in.Key}
	}
	return &HeadObjectOutput{ContentLength: f.Size}, nil
}

// DeleteObjectInput names the object to remove.
type DeleteObjectInput struct {
	Bucket string
	Key    string
}

// DeleteObjectOutput is empty.
type DeleteObjectOutput struct{}

// DeleteObject removes the key by PATCHing a null file entry, then absorbs
// two Gist-API quirks that the plain PATCH surfaces as opaque 422s
// (verified against the live API, 2026-07):
//
//   - Deleting a key that does not exist: GitHub rejects the no-op PATCH,
//     but S3 semantics say deleting a missing key succeeds, so gists3
//     verifies absence with one extra GET and returns success.
//   - Deleting a file whose content duplicates another file's sporadically
//     trips GitHub's no-change detection even though the file exists.
//     gists3 recovers by rewriting the file with unique content and
//     deleting again — at the price of one extra gist revision.
//
// Deleting the final remaining file still fails, with an *APIError that
// says so: GitHub requires every gist to keep at least one file. The
// ".bucket" placeholder normally keeps buckets clear of this edge; use
// DeleteBucket when everything should go.
//
// Cost: 1 round-trip (PATCH /gists/{id}); the quirk paths above add 1-3
// more.
func (c *Client) DeleteObject(ctx context.Context, in *DeleteObjectInput) (*DeleteObjectOutput, error) {
	if in == nil {
		in = &DeleteObjectInput{}
	}
	if err := validateBucket(in.Bucket); err != nil {
		return nil, err
	}
	if err := validateObjectKey(in.Key); err != nil {
		return nil, err
	}
	err := c.deleteFile(ctx, in.Bucket, in.Key)
	if err == nil {
		return &DeleteObjectOutput{}, nil
	}
	var ae *APIError
	if !errors.As(err, &ae) || ae.StatusCode != http.StatusUnprocessableEntity {
		return nil, notFound(err, in.Bucket, "")
	}
	// GitHub reports every "no effective change" PATCH as the same 422
	// Validation Failed; one GET disambiguates its three causes.
	g, err := c.getGist(ctx, in.Bucket)
	if err != nil {
		return nil, err
	}
	if _, ok := g.Files[in.Key]; !ok {
		return &DeleteObjectOutput{}, nil // already absent: S3-idempotent success
	}
	if len(g.Files) == 1 {
		ae.Message = "a gist must keep at least one file, refusing to delete the last one (" + ae.Message + ")"
		return nil, ae
	}
	// Duplicate-content quirk: make the content unique, then delete again.
	rewrite := &gistapi.GistPatch{Files: map[string]*gistapi.FileEdit{
		in.Key: {Content: fmt.Sprintf("gists3: deleting %q", in.Key)},
	}}
	if err := c.do(ctx, http.MethodPatch, "/gists/"+url.PathEscape(in.Bucket), rewrite, nil); err != nil {
		return nil, notFound(err, in.Bucket, "")
	}
	if err := c.deleteFile(ctx, in.Bucket, in.Key); err != nil {
		return nil, notFound(err, in.Bucket, "")
	}
	return &DeleteObjectOutput{}, nil
}

// deleteFile issues the null-file PATCH that removes one key.
func (c *Client) deleteFile(ctx context.Context, bucket, key string) error {
	patch := &gistapi.GistPatch{Files: map[string]*gistapi.FileEdit{key: nil}}
	return c.do(ctx, http.MethodPatch, "/gists/"+url.PathEscape(bucket), patch, nil)
}

// CopyObjectInput names a destination and an S3-style CopySource.
type CopyObjectInput struct {
	Bucket string // destination gist ID
	Key    string // destination filename

	// CopySource is "<gist-id>/<key>" (a leading "/" is tolerated),
	// matching the S3 convention. The key may itself contain slashes; the
	// split happens at the first one.
	CopySource string
}

// CopyObjectOutput reports the hash of the copied content.
type CopyObjectOutput struct {
	ETag string
}

// CopyObject is a GetObject + PutObject composition — not atomic, and the
// destination write is last-write-wins like any PutObject.
//
// Cost: 2 round-trips (GET source gist, PATCH destination gist), +1 raw
// fetch if the source is truncated.
func (c *Client) CopyObject(ctx context.Context, in *CopyObjectInput) (*CopyObjectOutput, error) {
	if in == nil {
		in = &CopyObjectInput{}
	}
	srcBucket, srcKey, err := splitCopySource(in.CopySource)
	if err != nil {
		return nil, err
	}
	get, err := c.GetObject(ctx, &GetObjectInput{Bucket: srcBucket, Key: srcKey})
	if err != nil {
		return nil, err
	}
	defer get.Body.Close()
	put, err := c.PutObject(ctx, &PutObjectInput{Bucket: in.Bucket, Key: in.Key, Body: get.Body})
	if err != nil {
		return nil, err
	}
	return &CopyObjectOutput{ETag: put.ETag}, nil
}

func splitCopySource(s string) (bucket, key string, err error) {
	bucket, key, ok := strings.Cut(strings.TrimPrefix(s, "/"), "/")
	if !ok || bucket == "" || key == "" {
		return "", "", fmt.Errorf(`gists3: CopySource must be "<gist-id>/<key>", got %q`, s)
	}
	return bucket, key, nil
}

// etag is the client-side content hash used for ETag fields.
func etag(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// validatePutKey enforces write-side key rules; reads accept any name so
// files created outside gists3 stay reachable.
func validatePutKey(key string) error {
	if key == "" {
		return errors.New("gists3: Key is required")
	}
	if strings.HasPrefix(strings.ToLower(key), "gistfile") {
		return ErrReservedKey
	}
	return nil
}

func validateObjectKey(key string) error {
	if key == "" {
		return errors.New("gists3: Key is required")
	}
	return nil
}

// listPageSize is GitHub's per_page maximum for GET /gists.
const listPageSize = 100

// ListObjectsV2Input names the bucket and an optional key filter.
type ListObjectsV2Input struct {
	Bucket string

	// Prefix filters keys client-side with strings.HasPrefix. There is
	// deliberately no Delimiter: gist filenames are a flat namespace, and
	// simulating folders would lie about the backend.
	Prefix string
}

// ListObjectsV2Output lists every matching object in one response. No
// pagination fields: a gist holds at most a few hundred files, returned in
// a single API call.
type ListObjectsV2Output struct {
	// Contents is sorted by Key ascending, matching S3 ordering.
	Contents []Object
}

// Object is one listing entry. No ETag: computing it would require fetching
// every file's content.
type Object struct {
	Key  string
	Size int64
}

// ListObjectsV2 lists the bucket's objects, excluding the ".bucket"
// placeholder seeded by CreateBucket. Prefix filtering happens client-side.
//
// Cost: 1 round-trip (GET /gists/{id}).
func (c *Client) ListObjectsV2(ctx context.Context, in *ListObjectsV2Input) (*ListObjectsV2Output, error) {
	if in == nil {
		in = &ListObjectsV2Input{}
	}
	g, err := c.getGist(ctx, in.Bucket)
	if err != nil {
		return nil, err
	}
	out := &ListObjectsV2Output{}
	for name, f := range g.Files {
		if name == bucketPlaceholder || !strings.HasPrefix(name, in.Prefix) {
			continue
		}
		out.Contents = append(out.Contents, Object{Key: name, Size: f.Size})
	}
	sort.Slice(out.Contents, func(i, j int) bool { return out.Contents[i].Key < out.Contents[j].Key })
	return out, nil
}

// ListBucketsInput is empty; it exists so the signature stays stable as
// fields are added.
type ListBucketsInput struct{}

// ListBucketsOutput holds every gist the token can see.
type ListBucketsOutput struct {
	Buckets []Bucket
}

// Bucket is one listing entry. Name is the gist ID.
type Bucket struct {
	Name         string
	CreationDate time.Time
}

// ListBuckets returns EVERY gist the token can see, gists3-created or not —
// no filtering is applied (callers who care can probe buckets for a
// ".bucket" key themselves). GitHub's page-based pagination is handled
// internally and the full set is returned; there are no paginator types.
//
// Cost: ceil(gists/100) round-trips (GET /gists?per_page=100&page=N).
func (c *Client) ListBuckets(ctx context.Context, _ *ListBucketsInput) (*ListBucketsOutput, error) {
	out := &ListBucketsOutput{}
	for page := 1; ; page++ {
		var gists []gistapi.Gist
		path := fmt.Sprintf("/gists?per_page=%d&page=%d", listPageSize, page)
		if err := c.do(ctx, http.MethodGet, path, nil, &gists); err != nil {
			return nil, err
		}
		for _, g := range gists {
			out.Buckets = append(out.Buckets, Bucket{Name: g.ID, CreationDate: g.CreatedAt})
		}
		if len(gists) < listPageSize {
			return out, nil
		}
	}
}
