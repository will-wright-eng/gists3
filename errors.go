package gists3

import (
	"errors"
	"fmt"
	"time"
)

// NotFoundError reports a missing bucket or key, covering both of S3's
// NoSuchBucket and NoSuchKey. Key == "" means the bucket (gist) itself was
// not found.
type NotFoundError struct {
	Bucket string
	Key    string
}

func (e *NotFoundError) Error() string {
	if e.Key == "" {
		return fmt.Sprintf("gists3: bucket %q not found", e.Bucket)
	}
	return fmt.Sprintf("gists3: key %q not found in bucket %q", e.Key, e.Bucket)
}

// RateLimitError surfaces a GitHub 403/429 rate-limit response. ResetAt is
// the zero time when GitHub sent no X-RateLimit-Reset header.
type RateLimitError struct {
	ResetAt time.Time
}

func (e *RateLimitError) Error() string {
	if e.ResetAt.IsZero() {
		return "gists3: GitHub API rate limit exceeded"
	}
	return fmt.Sprintf("gists3: GitHub API rate limit exceeded, resets at %s", e.ResetAt.Format(time.RFC3339))
}

// APIError is the fallback for any non-2xx GitHub response that is not a
// rate limit or a not-found already mapped by this library.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Message    string // GitHub's error body, truncated
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gists3: %s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Message)
}

// Input-validation sentinels, checkable with errors.Is.
var (
	// ErrEmptyBody rejects PutObject calls with no content: the Gist API
	// refuses empty files, and gists3 fails loudly rather than writing a
	// sentinel byte.
	ErrEmptyBody = errors.New("gists3: body is empty; the Gist API rejects empty file content")

	// ErrReservedKey rejects keys starting with "gistfile": GitHub treats
	// such names positionally and may rename them, so PutObject refuses
	// them to prevent surprises.
	ErrReservedKey = errors.New(`gists3: keys beginning with "gistfile" are reserved by GitHub`)
)
