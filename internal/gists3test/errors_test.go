package gists3test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

func TestRateLimitError(t *testing.T) {
	mux, client, _ := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1782000000")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	})
	_, err := client.HeadBucket(ctx, &gists3.HeadBucketInput{Bucket: "abc123"})
	var rl *gists3.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
	if want := time.Unix(1782000000, 0); !rl.ResetAt.Equal(want) {
		t.Errorf("ResetAt = %v, want %v", rl.ResetAt, want)
	}
}

func TestRateLimitError429(t *testing.T) {
	mux, client, _ := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := client.HeadBucket(ctx, &gists3.HeadBucketInput{Bucket: "abc123"})
	var rl *gists3.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
	if !rl.ResetAt.IsZero() {
		t.Errorf("ResetAt = %v, want zero time without a reset header", rl.ResetAt)
	}
}

func TestPlainForbiddenIsAPIError(t *testing.T) {
	mux, client, _ := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "42")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"Must have admin rights"}`))
	})
	_, err := client.HeadBucket(ctx, &gists3.HeadBucketInput{Bucket: "abc123"})
	var ae *gists3.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *APIError for non-rate-limit 403", err)
	}
	if ae.StatusCode != http.StatusForbidden || ae.Message != "Must have admin rights" {
		t.Errorf("APIError = %+v", ae)
	}
}

func TestAPIError(t *testing.T) {
	mux, client, _ := newServer(t)
	mux.HandleFunc("PATCH /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"Validation Failed"}`))
	})
	_, err := client.PutObject(ctx, &gists3.PutObjectInput{Bucket: "abc123", Key: "k", Body: strings.NewReader("x")})
	var ae *gists3.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if ae.StatusCode != 422 || ae.Method != "PATCH" || ae.Message != "Validation Failed" {
		t.Errorf("APIError = %+v", ae)
	}
}
