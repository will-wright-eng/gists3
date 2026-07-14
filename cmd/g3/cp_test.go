package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/will-wright-eng/gists3"
)

// gistJSON builds a gist response fixture holding the given files.
func gistJSON(t *testing.T, id string, files map[string]string) []byte {
	t.Helper()
	type file struct {
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
		Content  string `json:"content"`
	}
	fs := make(map[string]file, len(files))
	for k, v := range files {
		fs[k] = file{Filename: k, Size: int64(len(v)), Content: v}
	}
	b, err := json.Marshal(map[string]any{"id": id, "files": fs})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type patchBody struct {
	Files map[string]struct {
		Content string `json:"content"`
	} `json:"files"`
}

// handlePatch captures PATCH bodies sent to one gist.
func handlePatch(t *testing.T, mux *http.ServeMux, gistID string) *patchBody {
	t.Helper()
	got := &patchBody{}
	mux.HandleFunc("PATCH /gists/"+gistID, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Write([]byte(`{"id":"` + gistID + `","files":{}}`))
	})
	return got
}

// runCP drives the full command path — dispatch, classify, cp — and returns
// what landed on stdout.
func runCP(t *testing.T, client *gists3.Client, srcArg, dstArg string, stdin string) (string, error) {
	t.Helper()
	var in io.Reader = explodingStdin{t}
	if srcArg == "-" {
		in = strings.NewReader(stdin)
	}
	var stdout bytes.Buffer
	err := run(ctx, []string{"cp", srcArg, dstArg}, stubClient(client), in, &stdout, io.Discard)
	return stdout.String(), err
}

func writeSrc(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCPUpload(t *testing.T) {
	mux, client := newServer(t)
	got := handlePatch(t, mux, "abc123")
	src := writeSrc(t, "in.txt", "hello")
	stdout, err := runCP(t, client, src, "g3://abc123/conf.json", "")
	if err != nil {
		t.Fatal(err)
	}
	if c := got.Files["conf.json"].Content; c != "hello" {
		t.Errorf("uploaded content = %q, want %q", c, "hello")
	}
	if want := "upload: " + src + " to g3://abc123/conf.json\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestCPUploadInfersKey(t *testing.T) {
	for name, tc := range map[string]struct {
		dst     string
		wantKey string
	}{
		"bare bucket":    {"g3://abc123", "in.txt"},
		"trailing slash": {"g3://abc123/", "in.txt"},
		"prefix":         {"g3://abc123/x/", "x/in.txt"},
	} {
		t.Run(name, func(t *testing.T) {
			mux, client := newServer(t)
			got := handlePatch(t, mux, "abc123")
			src := writeSrc(t, "in.txt", "hello")
			stdout, err := runCP(t, client, src, tc.dst, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := got.Files[tc.wantKey]; !ok {
				t.Errorf("PATCH keys = %v, want %q", got.Files, tc.wantKey)
			}
			if want := "upload: " + src + " to g3://abc123/" + tc.wantKey + "\n"; stdout != want {
				t.Errorf("stdout = %q, want %q (resolved key)", stdout, want)
			}
		})
	}
}

func TestCPUploadFromStdin(t *testing.T) {
	mux, client := newServer(t)
	got := handlePatch(t, mux, "abc123")
	stdout, err := runCP(t, client, "-", "g3://abc123/last-run", "data")
	if err != nil {
		t.Fatal(err)
	}
	if c := got.Files["last-run"].Content; c != "data" {
		t.Errorf("uploaded content = %q, want %q", c, "data")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want no status line for a stdio endpoint", stdout)
	}
}

func TestCPDownload(t *testing.T) {
	mux, client := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(gistJSON(t, "abc123", map[string]string{"conf.json": "hello"}))
	})
	// The parent directory does not exist: MkdirAll must create it.
	dst := filepath.Join(t.TempDir(), "sub", "out.txt")
	stdout, err := runCP(t, client, "g3://abc123/conf.json", dst, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello" {
		t.Errorf("file = %q, want %q", b, "hello")
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	// The exact mode is umask-dependent; owner read/write must survive.
	if fi.Mode().Perm()&0o600 != 0o600 {
		t.Errorf("mode = %v, want owner-readable and -writable", fi.Mode())
	}
	if want := "download: g3://abc123/conf.json to " + dst + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestCPDownloadToDir(t *testing.T) {
	for name, tc := range map[string]struct {
		key string // remote key
		dst func(tmp string) string
	}{
		"existing dir":            {"conf.json", func(tmp string) string { return tmp }},
		"trailing slash existing": {"conf.json", func(tmp string) string { return tmp + string(os.PathSeparator) }},
		"trailing slash new dir":  {"conf.json", func(tmp string) string { return filepath.Join(tmp, "new") + "/" }},
		"slash key uses basename": {"a/conf.json", func(tmp string) string { return tmp }},
	} {
		t.Run(name, func(t *testing.T) {
			mux, client := newServer(t)
			mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
				w.Write(gistJSON(t, "abc123", map[string]string{tc.key: "hello"}))
			})
			tmp := t.TempDir()
			dst := tc.dst(tmp)
			stdout, err := runCP(t, client, "g3://abc123/"+tc.key, dst, "")
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dst, "conf.json")
			b, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != "hello" {
				t.Errorf("file = %q, want %q", b, "hello")
			}
			if want := "download: g3://abc123/" + tc.key + " to " + target + "\n"; stdout != want {
				t.Errorf("stdout = %q, want %q (resolved target)", stdout, want)
			}
		})
	}
}

func TestCPDownloadRefusesUnsafeInferredName(t *testing.T) {
	// path.Base of a key like "a/.." is ".." — joining it would write
	// OUTSIDE the destination directory. Inference must refuse instead.
	for _, key := range []string{"a/..", "..", `a\..\..\x`} {
		t.Run(key, func(t *testing.T) {
			mux, client := newServer(t)
			mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
				w.Write(gistJSON(t, "abc123", map[string]string{key: "payload"}))
			})
			dst := t.TempDir() + "/"
			_, err := runCP(t, client, "g3://abc123/"+key, dst, "")
			if err == nil || !strings.Contains(err.Error(), "name the destination file explicitly") {
				t.Errorf("err = %v, want the unsafe-inferred-name rejection", err)
			}
		})
	}
}

func TestCPDownloadToStdout(t *testing.T) {
	mux, client := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(gistJSON(t, "abc123", map[string]string{"conf.json": "{\"debug\":true}\n"}))
	})
	stdout, err := runCP(t, client, "g3://abc123/conf.json", "-", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\"debug\":true}\n"; stdout != want {
		t.Errorf("stdout = %q, want the body bytes only", stdout)
	}
}

func TestCPDownloadFailureLeavesNoFile(t *testing.T) {
	mux, client := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	})
	dst := filepath.Join(t.TempDir(), "out.txt")
	_, err := runCP(t, client, "g3://abc123/conf.json", dst, "")
	var nf *gists3.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want *NotFoundError", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("destination exists after a failed fetch; want no file")
	}
}

func TestCPRemoteCopy(t *testing.T) {
	for name, tc := range map[string]struct {
		srcKey  string
		dst     string
		wantKey string
	}{
		"explicit key":       {"a.txt", "g3://dst456/b.txt", "b.txt"},
		"bare bucket":        {"a.txt", "g3://dst456", "a.txt"},
		"basename inference": {"x/y.txt", "g3://dst456/", "y.txt"},
		"prefix":             {"x/y.txt", "g3://dst456/p/", "p/y.txt"},
	} {
		t.Run(name, func(t *testing.T) {
			mux, client := newServer(t)
			mux.HandleFunc("GET /gists/src123", func(w http.ResponseWriter, r *http.Request) {
				w.Write(gistJSON(t, "src123", map[string]string{tc.srcKey: "copy me"}))
			})
			got := handlePatch(t, mux, "dst456")
			stdout, err := runCP(t, client, "g3://src123/"+tc.srcKey, tc.dst, "")
			if err != nil {
				t.Fatal(err)
			}
			if c := got.Files[tc.wantKey].Content; c != "copy me" {
				t.Errorf("copied content under %q = %q; PATCH keys = %v", tc.wantKey, c, got.Files)
			}
			want := "copy: g3://src123/" + tc.srcKey + " to g3://dst456/" + tc.wantKey + "\n"
			if stdout != want {
				t.Errorf("stdout = %q, want %q (resolved key)", stdout, want)
			}
		})
	}
}

func TestCPUploadGuards(t *testing.T) {
	client := unreachableClient()
	t.Run("empty file", func(t *testing.T) {
		src := writeSrc(t, "empty.txt", "")
		_, err := runCP(t, client, src, "g3://abc123/k", "")
		if !errors.Is(err, gists3.ErrEmptyBody) {
			t.Errorf("err = %v, want ErrEmptyBody", err)
		}
	})
	t.Run("empty stdin", func(t *testing.T) {
		_, err := runCP(t, client, "-", "g3://abc123/k", "")
		if !errors.Is(err, gists3.ErrEmptyBody) {
			t.Errorf("err = %v, want ErrEmptyBody", err)
		}
	})
	t.Run("invalid UTF-8", func(t *testing.T) {
		src := writeSrc(t, "bin.dat", "\xff\xfe\x00binary")
		_, err := runCP(t, client, src, "g3://abc123/k", "")
		if err == nil || !strings.Contains(err.Error(), "UTF-8") {
			t.Errorf("err = %v, want the UTF-8 rejection", err)
		}
	})
	t.Run("over-cap valid UTF-8 reports size, not UTF-8", func(t *testing.T) {
		// A 2-byte rune spanning the cap boundary: the capped read stops on
		// its first byte, so a reversed guard order would misreport this
		// valid oversize stream as invalid UTF-8 — this locks size-first.
		_, err := runCP(t, client, "-", "g3://abc123/k", strings.Repeat("a", maxObjectBytes)+"é")
		if err == nil || !strings.Contains(err.Error(), "10 MiB") {
			t.Errorf("err = %v, want the size-cap rejection", err)
		}
		if err != nil && strings.Contains(err.Error(), "UTF-8") {
			t.Errorf("err = %v; an oversize body must be reported as oversize", err)
		}
	})
	t.Run("reserved key", func(t *testing.T) {
		src := writeSrc(t, "in.txt", "x")
		_, err := runCP(t, client, src, "g3://abc123/gistfile1.txt", "")
		if !errors.Is(err, gists3.ErrReservedKey) {
			t.Errorf("err = %v, want ErrReservedKey", err)
		}
	})
	t.Run("missing source", func(t *testing.T) {
		_, err := runCP(t, client, filepath.Join(t.TempDir(), "nope.txt"), "g3://abc123/k", "")
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("err = %v, want the OS not-exist error", err)
		}
	})
	t.Run("directory source", func(t *testing.T) {
		_, err := runCP(t, client, t.TempDir(), "g3://abc123/k", "")
		if err == nil {
			t.Error("want an error for a directory source")
		}
	})
}
