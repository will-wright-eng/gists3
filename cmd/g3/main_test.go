// The cmd/g3 tests mirror internal/gists3test's approach: a mux-backed fake
// GitHub behind gists3.WithBaseURL, inline JSON fixtures, no network. This
// file holds the shared helpers and the run() dispatch tests; parseArg,
// credential resolution, and cp itself are covered in uri_test.go,
// client_test.go, and cp_test.go.
package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/will-wright-eng/gists3"
)

var ctx = context.Background()

// newServer returns a mux-backed fake GitHub and a client pointed at it.
func newServer(t *testing.T) (*http.ServeMux, *gists3.Client) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return mux, gists3.New("test-token", gists3.WithBaseURL(srv.URL))
}

// unreachableClient must never be dialed: the call under test is expected to
// fail before any request. Port 1 refuses connections immediately if not.
func unreachableClient() *gists3.Client {
	return gists3.New("test-token", gists3.WithBaseURL("http://127.0.0.1:1"))
}

func stubClient(client *gists3.Client) clientFn {
	return func(io.Writer) (*gists3.Client, error) { return client, nil }
}

func failingClient(err error) clientFn {
	return func(io.Writer) (*gists3.Client, error) { return nil, err }
}

// explodingStdin fails the test if anything reads it: stdin must only be
// consumed when the source argument is "-".
type explodingStdin struct{ t *testing.T }

func (r explodingStdin) Read([]byte) (int, error) {
	r.t.Error("stdin was read for a non-stdio source")
	return 0, io.EOF
}

func TestRunUsageErrors(t *testing.T) {
	// Every case runs with a failing clientFn: classification must precede
	// client construction, so the usage error — not the credential error —
	// is what comes back.
	creds := errors.New("no credentials")
	for name, args := range map[string][]string{
		"no args":              {},
		"unknown command":      {"mv", "a", "b"},
		"cp arity one":         {"cp", "only-one"},
		"cp arity three":       {"cp", "a", "b", "c"},
		"ls with arguments":    {"ls", "g3://abc123/"},
		"local to local":       {"cp", "a.txt", "b.txt"},
		"stdin to stdout":      {"cp", "-", "-"},
		"stdin to local":       {"cp", "-", "a.txt"},
		"local to stdout":      {"cp", "a.txt", "-"},
		"bare-bucket source":   {"cp", "g3://abc123", "out.txt"},
		"prefix source":        {"cp", "g3://abc123/x/", "out.txt"},
		"stdin to bare bucket": {"cp", "-", "g3://abc123"},
		"stdin to prefix":      {"cp", "-", "g3://abc123/x/"},
		"empty gist id":        {"cp", "g3:///k", "out.txt"},
		"foreign scheme":       {"cp", "s3://b/k", "out.txt"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(ctx, args, failingClient(creds), strings.NewReader(""), io.Discard, io.Discard)
			var ue *usageError
			if !errors.As(err, &ue) {
				t.Errorf("run(%v) = %v, want *usageError", args, err)
			}
		})
	}
}

func TestRunCredentialErrorIsNotUsage(t *testing.T) {
	err := run(ctx, []string{"ls"}, failingClient(errors.New("no credentials")), strings.NewReader(""), io.Discard, io.Discard)
	var ue *usageError
	if err == nil || errors.As(err, &ue) {
		t.Errorf("credential failure = %v, want a non-usage error", err)
	}
}

func TestRunLS(t *testing.T) {
	mux, client := newServer(t)
	mux.HandleFunc("GET /gists", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"abc123","created_at":"2026-07-01T10:00:00Z","files":{}}]`))
	})
	var stdout bytes.Buffer
	if err := run(ctx, []string{"ls"}, stubClient(client), strings.NewReader(""), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "2026-07-01  abc123\n"; got != want {
		t.Errorf("ls output = %q, want %q", got, want)
	}
}
