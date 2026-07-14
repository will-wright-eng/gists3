package gists3test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

func TestCreateBucket(t *testing.T) {
	mux, client, base := newServer(t)
	var reqBody map[string]any
	mux.HandleFunc("POST /gists", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write(fixture(t, "created_gist.json", base))
	})
	out, err := client.CreateBucket(ctx, &gists3.CreateBucketInput{Description: "demo", Public: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Bucket != "newgist123" {
		t.Errorf("Bucket = %q, want newgist123", out.Bucket)
	}
	if reqBody["description"] != "demo" || reqBody["public"] != true {
		t.Errorf("request body = %v", reqBody)
	}
	files, _ := reqBody["files"].(map[string]any)
	placeholder, ok := files[".bucket"].(map[string]any)
	if !ok || placeholder["content"] == "" {
		t.Errorf(".bucket placeholder missing or empty in request: %v", files)
	}
}

func TestHeadBucketNotFound(t *testing.T) {
	mux, client, _ := newServer(t)
	mux.HandleFunc("GET /gists/nope", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	})
	_, err := client.HeadBucket(ctx, &gists3.HeadBucketInput{Bucket: "nope"})
	var nf *gists3.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want *NotFoundError", err)
	}
	if nf.Bucket != "nope" || nf.Key != "" {
		t.Errorf("NotFoundError = %+v, want Bucket=nope Key=\"\"", nf)
	}
}

func TestDeleteBucket(t *testing.T) {
	mux, client, _ := newServer(t)
	called := false
	mux.HandleFunc("DELETE /gists/abc123", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	if _, err := client.DeleteBucket(ctx, &gists3.DeleteBucketInput{Bucket: "abc123"}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("DELETE /gists/abc123 was not called")
	}
}

func TestDeleteBucketNotFound(t *testing.T) {
	mux, client, _ := newServer(t)
	mux.HandleFunc("DELETE /gists/nope", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	})
	_, err := client.DeleteBucket(ctx, &gists3.DeleteBucketInput{Bucket: "nope"})
	var nf *gists3.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want *NotFoundError", err)
	}
}
