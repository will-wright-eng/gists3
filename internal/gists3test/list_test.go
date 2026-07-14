package gists3test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

func TestListObjectsV2(t *testing.T) {
	mux, client, base := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "gist.json", base))
	})
	out, err := client.ListObjectsV2(ctx, &gists3.ListObjectsV2Input{Bucket: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	want := []gists3.Object{
		{Key: "conf.json", Size: 15},
		{Key: "notes/2026.md", Size: 8},
	}
	if len(out.Contents) != len(want) {
		t.Fatalf("Contents = %+v, want %+v (.bucket excluded, sorted)", out.Contents, want)
	}
	for i := range want {
		if out.Contents[i] != want[i] {
			t.Errorf("Contents[%d] = %+v, want %+v", i, out.Contents[i], want[i])
		}
	}
}

func TestListObjectsV2Prefix(t *testing.T) {
	mux, client, base := newServer(t)
	mux.HandleFunc("GET /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "gist.json", base))
	})
	out, err := client.ListObjectsV2(ctx, &gists3.ListObjectsV2Input{Bucket: "abc123", Prefix: "notes/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Contents) != 1 || out.Contents[0].Key != "notes/2026.md" {
		t.Errorf("Contents = %+v, want only notes/2026.md", out.Contents)
	}
}

func TestListBucketsDetails(t *testing.T) {
	mux, client, _ := newServer(t)
	mux.HandleFunc("GET /gists", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"abc123","description":"ci state","public":true,
			"created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-02T09:00:00Z",
			"files":{".bucket":{"filename":".bucket","size":17},
			         "conf.json":{"filename":"conf.json","size":1200},
			         "notes/2026.md":{"filename":"notes/2026.md","size":800}}}]`))
	})
	out, err := client.ListBuckets(ctx, &gists3.ListBucketsInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Buckets) != 1 {
		t.Fatalf("got %d buckets, want 1", len(out.Buckets))
	}
	b := out.Buckets[0]
	if b.Description != "ci state" || !b.Public {
		t.Errorf("Description/Public = %q/%v, want \"ci state\"/true", b.Description, b.Public)
	}
	if want := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC); !b.UpdatedAt.Equal(want) {
		t.Errorf("UpdatedAt = %v, want %v", b.UpdatedAt, want)
	}
	// The ".bucket" placeholder is excluded, matching ListObjectsV2.
	if b.ObjectCount != 2 || b.TotalSize != 2000 {
		t.Errorf("ObjectCount/TotalSize = %d/%d, want 2/2000", b.ObjectCount, b.TotalSize)
	}
}

func TestListBucketsPagination(t *testing.T) {
	mux, client, _ := newServer(t)
	pages := map[string]int{}
	mux.HandleFunc("GET /gists", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("per_page") != "100" {
			t.Errorf("per_page = %q, want 100", q.Get("per_page"))
		}
		page := q.Get("page")
		pages[page]++
		switch page {
		case "1":
			var b strings.Builder
			b.WriteString("[")
			for i := range 100 {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"id":"g%03d","created_at":"2026-07-01T00:00:00Z"}`, i)
			}
			b.WriteString("]")
			w.Write([]byte(b.String()))
		case "2":
			w.Write([]byte(`[{"id":"last","created_at":"2026-07-02T00:00:00Z"}]`))
		default:
			t.Errorf("unexpected page %q", page)
			w.Write([]byte(`[]`))
		}
	})
	out, err := client.ListBuckets(ctx, &gists3.ListBucketsInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Buckets) != 101 {
		t.Fatalf("got %d buckets, want 101", len(out.Buckets))
	}
	if out.Buckets[0].Name != "g000" || out.Buckets[100].Name != "last" {
		t.Errorf("Buckets[0] = %q, Buckets[100] = %q", out.Buckets[0].Name, out.Buckets[100].Name)
	}
	if want := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC); !out.Buckets[100].CreationDate.Equal(want) {
		t.Errorf("CreationDate = %v, want %v", out.Buckets[100].CreationDate, want)
	}
	if pages["1"] != 1 || pages["2"] != 1 {
		t.Errorf("pages fetched %v, want each of 1 and 2 exactly once", pages)
	}
}
