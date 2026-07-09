//go:build integration

package gists3_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/will-wright-eng/gists3"
)

// consistencyWindow bounds how long the tests wait out the gist backend's
// eventual consistency (reads briefly lag writes; rapid updates can 409).
const consistencyWindow = 20 * time.Second

// eventually retries fn until it succeeds or the consistency window closes.
// The library deliberately never retries — retries are the caller's policy —
// so the live tests encode the backend's consistency model here instead.
// A rate limit is an infra condition, not a defect: it skips the test
// instead of failing it (the reset is typically minutes-to-an-hour away).
func eventually(t *testing.T, op string, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(consistencyWindow)
	for {
		err := fn()
		if err == nil {
			return
		}
		var rl *gists3.RateLimitError
		if errors.As(err, &rl) {
			t.Skipf("%s: %v (rerun after the reset)", op, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %v (still failing after %s)", op, err, consistencyWindow)
		}
		time.Sleep(time.Second)
	}
}

// liveClient skips the test unless a token is available, from GIST_TOKEN or
// the gh CLI's stored credentials (`gh auth token`).
func liveClient(t *testing.T) *gists3.Client {
	t.Helper()
	token := os.Getenv("GIST_TOKEN")
	if token == "" {
		if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
			token = strings.TrimSpace(string(out))
		}
	}
	if token == "" {
		t.Skip("GIST_TOKEN not set and gh CLI not authenticated")
	}
	return gists3.New(token)
}

// liveBucket creates a uniquely-described secret gist and registers cleanup
// so failures don't strand gists.
func liveBucket(t *testing.T, client *gists3.Client, label string) string {
	t.Helper()
	create, err := client.CreateBucket(context.Background(), &gists3.CreateBucketInput{
		Description: fmt.Sprintf("gists3 integration test %s %s (safe to delete)", label, time.Now().UTC().Format(time.RFC3339Nano)),
	})
	var rl *gists3.RateLimitError
	if errors.As(err, &rl) {
		t.Skipf("CreateBucket: %v (rerun after the reset)", err)
	}
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	bucket := create.Bucket
	t.Cleanup(func() {
		if _, err := client.DeleteBucket(context.Background(), &gists3.DeleteBucketInput{Bucket: bucket}); err != nil {
			var rl *gists3.RateLimitError
			if errors.As(err, &rl) {
				t.Logf("cleanup DeleteBucket(%s) rate limited; gist left behind, described safe to delete: %v", bucket, err)
				return
			}
			t.Errorf("cleanup DeleteBucket(%s): %v", bucket, err)
		}
	})
	t.Logf("bucket %s", bucket)
	return bucket
}

// TestIntegrationLifecycle runs create → put → get → head → list → copy →
// delete → (delete bucket via cleanup) against the live GitHub API.
func TestIntegrationLifecycle(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()
	bucket := liveBucket(t, client, "lifecycle")

	eventually(t, "HeadBucket", func() error {
		_, err := client.HeadBucket(ctx, &gists3.HeadBucketInput{Bucket: bucket})
		return err
	})

	const content = "hello from gists3 integration\n"
	var putETag string
	eventually(t, "PutObject", func() error {
		put, err := client.PutObject(ctx, &gists3.PutObjectInput{Bucket: bucket, Key: "hello.txt", Body: strings.NewReader(content)})
		if err != nil {
			return err
		}
		putETag = put.ETag
		return nil
	})

	eventually(t, "GetObject", func() error {
		get, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: bucket, Key: "hello.txt"})
		if err != nil {
			return err
		}
		defer get.Body.Close()
		b, err := io.ReadAll(get.Body)
		if err != nil {
			return err
		}
		if string(b) != content {
			return fmt.Errorf("content = %q, want %q", b, content)
		}
		if get.ETag != putETag {
			return fmt.Errorf("ETag = %s, want put ETag %s", get.ETag, putETag)
		}
		return nil
	})

	eventually(t, "HeadObject", func() error {
		head, err := client.HeadObject(ctx, &gists3.HeadObjectInput{Bucket: bucket, Key: "hello.txt"})
		if err != nil {
			return err
		}
		if head.ContentLength != int64(len(content)) {
			return fmt.Errorf("ContentLength = %d, want %d", head.ContentLength, len(content))
		}
		return nil
	})

	eventually(t, "ListObjectsV2", func() error {
		list, err := client.ListObjectsV2(ctx, &gists3.ListObjectsV2Input{Bucket: bucket})
		if err != nil {
			return err
		}
		if len(list.Contents) != 1 || list.Contents[0].Key != "hello.txt" {
			return fmt.Errorf("Contents = %+v, want only hello.txt (.bucket excluded)", list.Contents)
		}
		return nil
	})

	eventually(t, "CopyObject", func() error {
		out, err := client.CopyObject(ctx, &gists3.CopyObjectInput{
			Bucket: bucket, Key: "hello-copy.txt", CopySource: bucket + "/hello.txt",
		})
		if err != nil {
			return err
		}
		if out.ETag != putETag {
			return fmt.Errorf("ETag = %s, want put ETag %s", out.ETag, putETag)
		}
		return nil
	})

	eventually(t, "DeleteObject", func() error {
		_, err := client.DeleteObject(ctx, &gists3.DeleteObjectInput{Bucket: bucket, Key: "hello-copy.txt"})
		return err
	})

	eventually(t, "GetObject after delete", func() error {
		_, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: bucket, Key: "hello-copy.txt"})
		var nf *gists3.NotFoundError
		if errors.As(err, &nf) {
			return nil
		}
		if err == nil {
			return errors.New("hello-copy.txt still visible after delete")
		}
		return err
	})
}

// TestIntegrationTruncatedGet writes a >1 MB object and reads it back,
// exercising the raw_url fallback against the live API.
func TestIntegrationTruncatedGet(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()
	bucket := liveBucket(t, client, "truncation")

	big := bytes.Repeat([]byte("0123456789abcdef\n"), 80_000) // ~1.4 MB, above the 1 MB inline cap
	var putETag string
	eventually(t, "PutObject", func() error {
		put, err := client.PutObject(ctx, &gists3.PutObjectInput{Bucket: bucket, Key: "big.txt", Body: bytes.NewReader(big)})
		if err != nil {
			return err
		}
		putETag = put.ETag
		return nil
	})

	eventually(t, "GetObject via raw_url", func() error {
		get, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: bucket, Key: "big.txt"})
		if err != nil {
			return err
		}
		defer get.Body.Close()
		b, err := io.ReadAll(get.Body)
		if err != nil {
			return err
		}
		if len(b) != len(big) {
			return fmt.Errorf("round-trip length = %d, want %d", len(b), len(big))
		}
		if get.ETag != putETag {
			return fmt.Errorf("round-trip ETag = %s, want %s", get.ETag, putETag)
		}
		return nil
	})
}
