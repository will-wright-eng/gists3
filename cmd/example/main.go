// Command example walks the full gists3 lifecycle against the live GitHub
// API: create a bucket, put, get, head, copy, list, delete, then delete the
// bucket. It authenticates from GIST_TOKEN (a personal access token with the
// gist scope), falling back to the gh CLI's stored credentials
// (`gh auth token`).
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// resolveToken prefers GIST_TOKEN and falls back to the gh CLI's stored
// credentials (`gh auth token`).
func resolveToken() (string, error) {
	if token := os.Getenv("GIST_TOKEN"); token != "" {
		return token, nil
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if token := strings.TrimSpace(string(out)); err == nil && token != "" {
		return token, nil
	}
	return "", fmt.Errorf("set GIST_TOKEN to a GitHub personal access token with the gist scope, or authenticate the gh CLI (gh auth login)")
}

func run() error {
	token, err := resolveToken()
	if err != nil {
		return err
	}
	ctx := context.Background()
	client := gists3.New(token)

	create, err := client.CreateBucket(ctx, &gists3.CreateBucketInput{
		Description: "gists3 example bucket (safe to delete)",
	})
	if err != nil {
		return fmt.Errorf("CreateBucket: %w", err)
	}
	bucket := create.Bucket
	fmt.Printf("created bucket %s\n", bucket)
	defer func() {
		if _, err := client.DeleteBucket(ctx, &gists3.DeleteBucketInput{Bucket: bucket}); err != nil {
			log.Printf("cleanup DeleteBucket: %v", err)
			return
		}
		fmt.Printf("deleted bucket %s\n", bucket)
	}()

	put, err := client.PutObject(ctx, &gists3.PutObjectInput{
		Bucket: bucket,
		Key:    "greeting.txt",
		Body:   strings.NewReader("hello, object storage cosplay\n"),
	})
	if err != nil {
		return fmt.Errorf("PutObject: %w", err)
	}
	fmt.Printf("put greeting.txt (etag %s)\n", put.ETag)

	get, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: bucket, Key: "greeting.txt"})
	if err != nil {
		return fmt.Errorf("GetObject: %w", err)
	}
	content, err := io.ReadAll(get.Body)
	get.Body.Close()
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	fmt.Printf("got greeting.txt: %q\n", content)

	head, err := client.HeadObject(ctx, &gists3.HeadObjectInput{Bucket: bucket, Key: "greeting.txt"})
	if err != nil {
		return fmt.Errorf("HeadObject: %w", err)
	}
	fmt.Printf("head greeting.txt: %d bytes\n", head.ContentLength)

	if _, err := client.CopyObject(ctx, &gists3.CopyObjectInput{
		Bucket: bucket, Key: "greeting-copy.txt", CopySource: bucket + "/greeting.txt",
	}); err != nil {
		return fmt.Errorf("CopyObject: %w", err)
	}
	fmt.Println("copied greeting.txt -> greeting-copy.txt")

	list, err := client.ListObjectsV2(ctx, &gists3.ListObjectsV2Input{Bucket: bucket})
	if err != nil {
		return fmt.Errorf("ListObjectsV2: %w", err)
	}
	fmt.Println("objects:")
	for _, o := range list.Contents {
		fmt.Printf("  %8d  %s\n", o.Size, o.Key)
	}

	if _, err := client.DeleteObject(ctx, &gists3.DeleteObjectInput{Bucket: bucket, Key: "greeting-copy.txt"}); err != nil {
		return fmt.Errorf("DeleteObject: %w", err)
	}
	fmt.Println("deleted greeting-copy.txt")
	return nil
}
