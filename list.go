package gists3

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/will-wright-eng/gists3/internal/gistapi"
)

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
