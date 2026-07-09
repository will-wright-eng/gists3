// Package gists3 wraps the GitHub Gist REST API behind an interface shaped
// like the AWS SDK for Go v2 s3.Client: context-first methods, pointer Input
// structs, pointer Output structs, typed errors.
//
// It is a syntax-compatible facade, not a protocol implementation — no AWS
// signatures, XML wire format, presigned URLs, or multipart uploads. A bucket
// is a gist (addressed by its GitHub-assigned ID), a key is a filename within
// it, and an object body is that file's content.
//
// Security: a secret gist is unlisted, NOT access-controlled — anyone with
// the gist ID can read it without authentication. Nothing sensitive belongs
// in a gists3 bucket, public or secret, without application-layer encryption.
//
// Consistency: the gist backend is eventually consistent. A read immediately
// after a write can briefly miss it, and rapid sequential updates to one
// gist can fail with HTTP 409. gists3 surfaces both honestly (NotFoundError,
// APIError) rather than retrying — retries are the caller's policy; wrap the
// injected http.Client or retry at the call site when it matters.
package gists3

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/will-wright-eng/gists3/internal/gistapi"
)

// Client reads and writes gists through the GitHub REST API. Construct with
// New, NewFromConfig, or NewFromDefaultConfig; the zero value is not usable.
// Methods are safe for concurrent use, but writes to the same bucket are
// last-write-wins at whole-gist granularity — the Gist API has no
// compare-and-swap.
//
// This package is the S3-shaped façade; the GitHub transport and wire types
// live in internal/gistapi.
type Client struct {
	api *gistapi.Client
}

// Option configures a Client during construction.
type Option func(*Client)

// WithHTTPClient replaces the http.Client used for every request
// (http.DefaultClient by default). Retries are policy and live here too:
// wrap the transport if you want them — the library never retries
// internally.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.api.HTTPClient = hc }
}

// WithBaseURL overrides the GitHub API base URL, e.g.
// "https://github.example.com/api/v3" for GitHub Enterprise. Security
// sensitive: the bearer token accompanies every request to this URL, so
// misdirecting it exfiltrates the token.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.api.BaseURL = strings.TrimSuffix(u, "/") }
}

// New returns a Client authenticated with a GitHub personal access token
// holding the gist scope. It never reads environment variables or files;
// ambient configuration is opt-in via NewFromDefaultConfig.
func New(token string, opts ...Option) *Client {
	c := &Client{api: &gistapi.Client{
		Token:      token,
		BaseURL:    gistapi.DefaultBaseURL,
		HTTPClient: http.DefaultClient,
	}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// do delegates one API round-trip to the transport, converting its typed
// errors to this package's public ones on the way out.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	return publicErr(c.api.Do(ctx, method, path, in, out))
}

// fetchRaw downloads truncated file content from its raw_url without the
// Authorization header: the token is only ever sent to the configured base
// URL.
func (c *Client) fetchRaw(ctx context.Context, rawURL string) ([]byte, error) {
	b, err := c.api.FetchRaw(ctx, rawURL)
	return b, publicErr(err)
}

// publicErr keeps gistapi's error types from escaping the façade: callers
// see *RateLimitError and *APIError from this package only.
func publicErr(err error) error {
	var rl *gistapi.RateLimitError
	if errors.As(err, &rl) {
		return &RateLimitError{ResetAt: rl.ResetAt}
	}
	var ae *gistapi.APIError
	if errors.As(err, &ae) {
		return &APIError{StatusCode: ae.StatusCode, Method: ae.Method, Path: ae.Path, Message: ae.Message}
	}
	return err
}

// notFound converts a 404 *APIError into a *NotFoundError for bucket (and
// key, when the miss is key-level). Other errors pass through unchanged.
func notFound(err error, bucket, key string) error {
	var ae *APIError
	if errors.As(err, &ae) && ae.StatusCode == http.StatusNotFound {
		return &NotFoundError{Bucket: bucket, Key: key}
	}
	return err
}

// getGist fetches one gist, mapping 404 to *NotFoundError.
func (c *Client) getGist(ctx context.Context, bucket string) (*gistapi.Gist, error) {
	if err := validateBucket(bucket); err != nil {
		return nil, err
	}
	var g gistapi.Gist
	if err := c.do(ctx, http.MethodGet, "/gists/"+url.PathEscape(bucket), nil, &g); err != nil {
		return nil, notFound(err, bucket, "")
	}
	return &g, nil
}

func validateBucket(bucket string) error {
	if bucket == "" {
		return errors.New("gists3: Bucket (gist ID) is required")
	}
	return nil
}
