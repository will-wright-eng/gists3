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
		"ls two arguments":     {"ls", "g3://a", "g3://b"},
		"ls local argument":    {"ls", "somedir"},
		"ls stdio argument":    {"ls", "-"},
		"ls foreign scheme":    {"ls", "s3://b"},
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
		w.Write([]byte(`[
			{"id":"abc123","description":"ci state","public":false,
			 "created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-02T09:00:00Z",
			 "files":{".bucket":{"filename":".bucket","size":17},
			          "conf.json":{"filename":"conf.json","size":1229}}},
			{"id":"def456","description":"","public":true,
			 "created_at":"2026-06-15T08:30:00Z","updated_at":"2026-06-15T08:30:00Z",
			 "files":{"a.txt":{"filename":"a.txt","size":100},
			          "b.txt":{"filename":"b.txt","size":712}}},
			{"id":"empty1","description":"fresh bucket","public":false,
			 "created_at":"2026-05-01T00:00:00Z","updated_at":"2026-05-01T00:00:00Z",
			 "files":{".bucket":{"filename":".bucket","size":17}}},
			{"id":"messy1","description":"line1\nline2  ","public":true,
			 "created_at":"2026-04-01T00:00:00Z","updated_at":"2026-04-01T00:00:00Z",
			 "files":{"big.txt":{"filename":"big.txt","size":1047276}}}
		]`))
	})
	var stdout bytes.Buffer
	if err := run(ctx, []string{"ls"}, stubClient(client), strings.NewReader(""), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	// messy1: the newline-bearing description flattens to one line, and
	// 1047276 bytes — inside the [999.95K, 1M) window — promotes to "1.0M"
	// instead of the seven-character "1022.7K" that would break the column.
	want := "2026-07-01 10:00  abc123  secret   1 object     1.2K  ci state\n" +
		"2026-06-15 08:30  def456  public   2 objects     812\n" +
		"2026-05-01 00:00  empty1  secret   0 objects       0  fresh bucket\n" +
		"2026-04-01 00:00  messy1  public   1 object     1.0M  line1 line2\n"
	if got := stdout.String(); got != want {
		t.Errorf("ls output:\n%q\nwant:\n%q", got, want)
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{
		0:        "0",
		812:      "812",
		1023:     "1023",
		1024:     "1.0K",
		1229:     "1.2K",
		1023948:  "999.9K", // last value the K unit can render in six chars
		1023949:  "1.0M",   // %.1fK would round to "1000.0K" — promoted
		1048575:  "1.0M",
		1048576:  "1.0M",
		10 << 20: "10.0M",
	} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
		if got := humanSize(n); len(got) > 6 {
			t.Errorf("humanSize(%d) = %q overflows the %%6s column", n, got)
		}
	}
}

func TestRunLSBucket(t *testing.T) {
	for name, tc := range map[string]struct {
		arg  string
		want string
	}{
		"all objects": {"g3://abc123", "  1.2K  conf.json\n   812  notes/2026.md\n"},
		"prefix":      {"g3://abc123/notes/", "   812  notes/2026.md\n"},
	} {
		t.Run(name, func(t *testing.T) {
			mux, client := newServer(t)
			mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"id":"abc123","files":{
					".bucket":{"filename":".bucket","size":17},
					"conf.json":{"filename":"conf.json","size":1229},
					"notes/2026.md":{"filename":"notes/2026.md","size":812}}}`))
			})
			var stdout bytes.Buffer
			if err := run(ctx, []string{"ls", tc.arg}, stubClient(client), strings.NewReader(""), &stdout, io.Discard); err != nil {
				t.Fatal(err)
			}
			if got := stdout.String(); got != tc.want {
				t.Errorf("ls %s output:\n%q\nwant:\n%q", tc.arg, got, tc.want)
			}
		})
	}
}
