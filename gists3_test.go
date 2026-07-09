// Shared fake-GitHub helpers and client-level contract tests. Tests for a
// specific operation live in the _test.go file mirroring its source file
// (bucket_test.go, object_test.go, list_test.go, config_test.go,
// errors_test.go).
package gists3_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/will-wright-eng/gists3"
)

var ctx = context.Background()

// newServer returns a mux-backed fake GitHub and a Client pointed at it.
func newServer(t *testing.T) (*http.ServeMux, *gists3.Client, string) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return mux, gists3.New("test-token", gists3.WithBaseURL(srv.URL)), srv.URL
}

// unreachableClient must never be dialed: validation is expected to fail
// before any request. Port 1 refuses connections immediately if it is.
func unreachableClient() *gists3.Client {
	return gists3.New("test-token", gists3.WithBaseURL("http://127.0.0.1:1"))
}

// fixture loads a canned response, substituting {{BASE}} with the fake
// server's URL so raw_url fields point back at it.
func fixture(t *testing.T, name, base string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return bytes.ReplaceAll(b, []byte("{{BASE}}"), []byte(base))
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestRequestHeaders(t *testing.T) {
	mux, client, _ := newServer(t)
	var got http.Header
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Write([]byte(`{"id":"abc123","files":{}}`))
	})
	if _, err := client.HeadBucket(ctx, &gists3.HeadBucketInput{Bucket: "abc123"}); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"Authorization":        "Bearer test-token",
		"Accept":               "application/vnd.github+json",
		"X-Github-Api-Version": "2022-11-28",
	} {
		if v := got.Get(k); v != want {
			t.Errorf("header %s = %q, want %q", k, v, want)
		}
	}
}

func TestNilInputsDoNotPanic(t *testing.T) {
	// AWS-SDK idiom: nil params must not panic. Validation (or, for
	// CreateBucket/ListBuckets, transport) errors are the expected result
	// against an unreachable endpoint.
	client := unreachableClient()
	for name, call := range map[string]func() error{
		"CreateBucket":  func() error { _, err := client.CreateBucket(ctx, nil); return err },
		"HeadBucket":    func() error { _, err := client.HeadBucket(ctx, nil); return err },
		"DeleteBucket":  func() error { _, err := client.DeleteBucket(ctx, nil); return err },
		"ListBuckets":   func() error { _, err := client.ListBuckets(ctx, nil); return err },
		"PutObject":     func() error { _, err := client.PutObject(ctx, nil); return err },
		"GetObject":     func() error { _, err := client.GetObject(ctx, nil); return err },
		"HeadObject":    func() error { _, err := client.HeadObject(ctx, nil); return err },
		"DeleteObject":  func() error { _, err := client.DeleteObject(ctx, nil); return err },
		"CopyObject":    func() error { _, err := client.CopyObject(ctx, nil); return err },
		"ListObjectsV2": func() error { _, err := client.ListObjectsV2(ctx, nil); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Errorf("%s(ctx, nil): want error, got nil", name)
			}
		})
	}
}
