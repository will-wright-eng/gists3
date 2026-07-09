package gists3

import (
	"context"
	"net/http"
	"net/url"

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
