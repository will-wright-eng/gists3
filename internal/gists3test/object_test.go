package gists3test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/will-wright-eng/gists3"
)

func TestPutObject(t *testing.T) {
	mux, client, _ := newServer(t)
	var patch struct {
		Files map[string]struct {
			Content string `json:"content"`
		} `json:"files"`
	}
	mux.HandleFunc("PATCH /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Write([]byte(`{"id":"abc123","files":{}}`))
	})
	out, err := client.PutObject(ctx, &gists3.PutObjectInput{
		Bucket: "abc123", Key: "conf.json", Body: strings.NewReader("hello world"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := patch.Files["conf.json"].Content; got != "hello world" {
		t.Errorf("patched content = %q, want %q", got, "hello world")
	}
	if want := sha256hex("hello world"); out.ETag != want {
		t.Errorf("ETag = %q, want %q", out.ETag, want)
	}
}

func TestPutObjectValidation(t *testing.T) {
	client := unreachableClient()
	for name, tc := range map[string]struct {
		in      *gists3.PutObjectInput
		wantErr error
	}{
		"empty body":    {&gists3.PutObjectInput{Bucket: "b", Key: "k", Body: strings.NewReader("")}, gists3.ErrEmptyBody},
		"nil body":      {&gists3.PutObjectInput{Bucket: "b", Key: "k"}, gists3.ErrEmptyBody},
		"reserved key":  {&gists3.PutObjectInput{Bucket: "b", Key: "gistfile1.txt", Body: strings.NewReader("x")}, gists3.ErrReservedKey},
		"reserved caps": {&gists3.PutObjectInput{Bucket: "b", Key: "GistFile1.txt", Body: strings.NewReader("x")}, gists3.ErrReservedKey},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.PutObject(ctx, tc.in); !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
	if _, err := client.PutObject(ctx, &gists3.PutObjectInput{Bucket: "b", Body: strings.NewReader("x")}); err == nil {
		t.Error("empty key: want error, got nil")
	}
	if _, err := client.PutObject(ctx, &gists3.PutObjectInput{Key: "k", Body: strings.NewReader("x")}); err == nil {
		t.Error("empty bucket: want error, got nil")
	}
}

func TestPutObjectBucketNotFound(t *testing.T) {
	mux, client, _ := newServer(t)
	mux.HandleFunc("PATCH /gists/nope", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	})
	_, err := client.PutObject(ctx, &gists3.PutObjectInput{Bucket: "nope", Key: "k", Body: strings.NewReader("x")})
	var nf *gists3.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want *NotFoundError", err)
	}
	if nf.Bucket != "nope" || nf.Key != "" {
		t.Errorf("NotFoundError = %+v, want bucket-level miss", nf)
	}
}

func TestGetObject(t *testing.T) {
	mux, client, base := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "gist.json", base))
	})
	out, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: "abc123", Key: "conf.json"})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatal(err)
	}
	const want = "{\"debug\":true}\n"
	if string(b) != want {
		t.Errorf("body = %q, want %q", b, want)
	}
	if out.ContentLength != int64(len(want)) {
		t.Errorf("ContentLength = %d, want %d", out.ContentLength, len(want))
	}
	if out.ETag != sha256hex(want) {
		t.Errorf("ETag = %q, want %q", out.ETag, sha256hex(want))
	}
}

func TestGetObjectSlashKey(t *testing.T) {
	mux, client, base := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "gist.json", base))
	})
	out, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: "abc123", Key: "notes/2026.md"})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Body.Close()
	b, _ := io.ReadAll(out.Body)
	if string(b) != "# notes\n" {
		t.Errorf("body = %q, want %q", b, "# notes\n")
	}
}

func TestGetObjectTruncated(t *testing.T) {
	mux, client, base := newServer(t)
	const full = "full content"
	rawAuth := "unset"
	mux.HandleFunc("GET /gists/tr123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "gist_truncated.json", base))
	})
	mux.HandleFunc("GET /raw/big.txt", func(w http.ResponseWriter, r *http.Request) {
		rawAuth = r.Header.Get("Authorization")
		w.Write([]byte(full))
	})
	out, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: "tr123", Key: "big.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Body.Close()
	b, _ := io.ReadAll(out.Body)
	if string(b) != full {
		t.Errorf("body = %q, want full raw content %q", b, full)
	}
	if out.ContentLength != int64(len(full)) || out.ETag != sha256hex(full) {
		t.Errorf("ContentLength/ETag computed from truncated content: %d, %s", out.ContentLength, out.ETag)
	}
	if rawAuth != "" {
		t.Errorf("raw fetch sent Authorization %q; the token must stay on the base URL", rawAuth)
	}
}

func TestGetObjectNoSuchKey(t *testing.T) {
	mux, client, base := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "gist.json", base))
	})
	_, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: "abc123", Key: "missing.txt"})
	var nf *gists3.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want *NotFoundError", err)
	}
	if nf.Bucket != "abc123" || nf.Key != "missing.txt" {
		t.Errorf("NotFoundError = %+v, want key-level miss", nf)
	}
}

func TestGetObjectNoSuchBucket(t *testing.T) {
	mux, client, _ := newServer(t)
	mux.HandleFunc("GET /gists/nope", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	})
	_, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: "nope", Key: "k"})
	var nf *gists3.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want *NotFoundError", err)
	}
	if nf.Bucket != "nope" || nf.Key != "" {
		t.Errorf("NotFoundError = %+v, want bucket-level miss", nf)
	}
}

func TestGetObjectPlaceholderReadable(t *testing.T) {
	mux, client, base := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "gist.json", base))
	})
	out, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: "abc123", Key: ".bucket"})
	if err != nil {
		t.Fatalf("GetObject(.bucket) should work for the curious: %v", err)
	}
	out.Body.Close()
}

func TestHeadObject(t *testing.T) {
	mux, client, base := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "gist.json", base))
	})
	out, err := client.HeadObject(ctx, &gists3.HeadObjectInput{Bucket: "abc123", Key: "conf.json"})
	if err != nil {
		t.Fatal(err)
	}
	if out.ContentLength != 15 {
		t.Errorf("ContentLength = %d, want 15", out.ContentLength)
	}
}

func TestHeadObjectNotFound(t *testing.T) {
	mux, client, base := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "gist.json", base))
	})
	_, err := client.HeadObject(ctx, &gists3.HeadObjectInput{Bucket: "abc123", Key: "missing.txt"})
	var nf *gists3.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want *NotFoundError", err)
	}
}

func TestDeleteObject(t *testing.T) {
	mux, client, _ := newServer(t)
	var raw []byte
	mux.HandleFunc("PATCH /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"id":"abc123","files":{}}`))
	})
	if _, err := client.DeleteObject(ctx, &gists3.DeleteObjectInput{Bucket: "abc123", Key: "conf.json"}); err != nil {
		t.Fatal(err)
	}
	var patch struct {
		Files map[string]json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(raw, &patch); err != nil {
		t.Fatalf("unmarshal PATCH body %s: %v", raw, err)
	}
	if string(patch.Files["conf.json"]) != "null" {
		t.Errorf(`files["conf.json"] = %s, want null`, patch.Files["conf.json"])
	}
}

// validation422 writes GitHub's no-effective-change rejection, emitted
// identically for missing keys, the duplicate-content quirk, and last-file
// deletes.
func validation422(w http.ResponseWriter) {
	w.WriteHeader(http.StatusUnprocessableEntity)
	w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"Gist","code":"missing_field","field":"files"}]}`))
}

func TestDeleteObjectMissingKeyIsIdempotent(t *testing.T) {
	mux, client, base := newServer(t)
	patches := 0
	mux.HandleFunc("PATCH /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		patches++
		validation422(w)
	})
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "gist.json", base)) // no such key in the gist
	})
	if _, err := client.DeleteObject(ctx, &gists3.DeleteObjectInput{Bucket: "abc123", Key: "missing.txt"}); err != nil {
		t.Fatalf("deleting an absent key must succeed like S3, got %v", err)
	}
	if patches != 1 {
		t.Errorf("PATCH count = %d, want 1 (no pointless retries)", patches)
	}
}

func TestDeleteObjectDuplicateContentQuirk(t *testing.T) {
	mux, client, base := newServer(t)
	var bodies []string
	mux.HandleFunc("PATCH /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			validation422(w) // first null-delete rejected despite the key existing
			return
		}
		w.Write([]byte(`{"id":"abc123","files":{}}`))
	})
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "gist.json", base)) // conf.json exists among others
	})
	if _, err := client.DeleteObject(ctx, &gists3.DeleteObjectInput{Bucket: "abc123", Key: "conf.json"}); err != nil {
		t.Fatalf("quirk recovery failed: %v", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("PATCH count = %d, want 3 (delete, rewrite, delete)", len(bodies))
	}
	if !strings.Contains(bodies[0], `"conf.json":null`) {
		t.Errorf("first PATCH = %s, want null delete", bodies[0])
	}
	if !strings.Contains(bodies[1], `gists3: deleting`) {
		t.Errorf("second PATCH = %s, want unique-content rewrite", bodies[1])
	}
	if !strings.Contains(bodies[2], `"conf.json":null`) {
		t.Errorf("third PATCH = %s, want null delete", bodies[2])
	}
}

func TestDeleteObjectLastFile(t *testing.T) {
	mux, client, _ := newServer(t)
	mux.HandleFunc("PATCH /gists/solo", func(w http.ResponseWriter, r *http.Request) {
		validation422(w)
	})
	mux.HandleFunc("GET /gists/solo", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"solo","files":{"only.txt":{"filename":"only.txt","size":4,"content":"solo"}}}`))
	})
	_, err := client.DeleteObject(ctx, &gists3.DeleteObjectInput{Bucket: "solo", Key: "only.txt"})
	var ae *gists3.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if !strings.Contains(ae.Message, "at least one file") {
		t.Errorf("Message = %q, want last-file explanation", ae.Message)
	}
}

func TestCopyObject(t *testing.T) {
	mux, client, _ := newServer(t)
	mux.HandleFunc("GET /gists/src123", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"src123","files":{"a.txt":{"filename":"a.txt","size":7,"content":"copy me"}}}`))
	})
	var patch struct {
		Files map[string]struct {
			Content string `json:"content"`
		} `json:"files"`
	}
	mux.HandleFunc("PATCH /gists/dst456", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Write([]byte(`{"id":"dst456","files":{}}`))
	})
	out, err := client.CopyObject(ctx, &gists3.CopyObjectInput{
		Bucket: "dst456", Key: "b.txt", CopySource: "src123/a.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := patch.Files["b.txt"].Content; got != "copy me" {
		t.Errorf("copied content = %q, want %q", got, "copy me")
	}
	if out.ETag != sha256hex("copy me") {
		t.Errorf("ETag = %q, want %q", out.ETag, sha256hex("copy me"))
	}
}

func TestCopyObjectSourceForms(t *testing.T) {
	mux, client, _ := newServer(t)
	mux.HandleFunc("GET /gists/src123", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"src123","files":{"notes/a.txt":{"filename":"notes/a.txt","size":1,"content":"x"}}}`))
	})
	mux.HandleFunc("PATCH /gists/dst456", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"dst456","files":{}}`))
	})
	// Leading slash tolerated; source key may itself contain slashes.
	if _, err := client.CopyObject(ctx, &gists3.CopyObjectInput{
		Bucket: "dst456", Key: "b.txt", CopySource: "/src123/notes/a.txt",
	}); err != nil {
		t.Errorf("CopySource with leading slash and nested key: %v", err)
	}
	for _, bad := range []string{"", "no-slash", "/", "bucket/"} {
		if _, err := client.CopyObject(ctx, &gists3.CopyObjectInput{Bucket: "dst456", Key: "b.txt", CopySource: bad}); err == nil {
			t.Errorf("CopySource %q: want error, got nil", bad)
		}
	}
}
