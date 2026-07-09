// Package gistapi is the GitHub-facing half of gists3: an authenticated JSON
// round-tripper plus the Gist API's wire shapes and error semantics. It knows
// nothing about buckets or keys — the S3-shaped façade in the root package
// converts this package's typed errors to its public ones at the boundary.
//
// Error strings keep the "gists3:" prefix because untyped errors (transport
// failures, decode failures) pass through the façade verbatim.
package gistapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the public GitHub REST endpoint.
	DefaultBaseURL = "https://api.github.com"

	apiVersion = "2022-11-28"
)

// Client is the transport under gists3.Client. The façade constructor sets
// the fields once; nothing mutates them afterwards, keeping methods safe for
// concurrent use.
type Client struct {
	Token      string
	BaseURL    string // no trailing slash
	HTTPClient *http.Client
}

// Do executes one API round-trip: in (when non-nil) is marshaled as the JSON
// request body, a 2xx response body is decoded into out (when non-nil), and
// non-2xx responses map to *RateLimitError or *APIError.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("gists3: marshal %s %s request: %w", method, path, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("gists3: build %s %s request: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("gists3: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return responseError(resp, method, path)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("gists3: decode %s %s response: %w", method, path, err)
		}
	}
	return nil
}

// FetchRaw downloads truncated file content from its raw_url without the
// Authorization header: the token is only ever sent to BaseURL.
func (c *Client) FetchRaw(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("gists3: build raw fetch request: %w", err)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gists3: fetch raw content: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp, http.MethodGet, rawURL)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gists3: read raw content: %w", err)
	}
	return b, nil
}

// RateLimitError and APIError never reach gists3 callers: the façade
// converts them to its public equivalents. They carry data, not presentation
// — Error() exists to satisfy the error interface.

// RateLimitError signals a GitHub 403/429 rate-limit response. ResetAt is
// the zero time when GitHub sent no X-RateLimit-Reset header.
type RateLimitError struct {
	ResetAt time.Time
}

func (e *RateLimitError) Error() string {
	return "gistapi: rate limited"
}

// APIError is any other non-2xx GitHub response.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Message    string // GitHub's error body, truncated
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gistapi: %s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Message)
}

// responseError maps a non-2xx response to a typed error. GitHub signals
// rate limiting as 429, or as 403 with the X-RateLimit-Remaining quota
// exhausted.
func responseError(resp *http.Response, method, path string) error {
	if resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0") {
		var reset time.Time
		if sec, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			reset = time.Unix(sec, 0)
		}
		return &RateLimitError{ResetAt: reset}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return &APIError{
		StatusCode: resp.StatusCode,
		Method:     method,
		Path:       path,
		Message:    apiMessage(body),
	}
}

// apiMessage extracts GitHub's {"message": ...} field, falling back to the
// truncated raw body.
func apiMessage(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &m); err == nil && m.Message != "" {
		return m.Message
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
