package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

// maxObjectBytes caps upload bodies at the backend's practical ceiling
// (DESIGN.md §5.4) so an oversized pipe fails with a named error instead of
// buffering unbounded input and letting the API reject it opaquely.
const maxObjectBytes = 10 << 20

// classify parses both cp arguments and validates the pair. Every error it
// returns is a usage error.
func classify(srcArg, dstArg string) (src, dst location, err error) {
	if src, err = parseArg(srcArg); err != nil {
		return src, dst, err
	}
	if dst, err = parseArg(dstArg); err != nil {
		return src, dst, err
	}
	if src.kind == locRemote && src.prefixForm() {
		return src, dst, usagef("%q: source must name a key", srcArg)
	}
	if src.kind != locRemote && dst.kind != locRemote {
		return src, dst, usagef("at least one side must be a g3:// URI")
	}
	if src.kind == locStdio && dst.prefixForm() {
		return src, dst, usagef("cannot infer a key for stdin; name one: g3://%s/<key>", dst.bucket)
	}
	return src, dst, nil
}

// cp executes one classified copy. Status lines go to stdout and are
// suppressed whenever either endpoint is stdio, matching aws s3 cp.
func cp(ctx context.Context, client *gists3.Client, src, dst location, stdin io.Reader, stdout io.Writer) error {
	switch {
	case src.kind == locRemote && dst.kind == locRemote:
		return remoteCopy(ctx, client, src, dst, stdout)
	case src.kind == locRemote:
		return download(ctx, client, src, dst, stdout)
	default:
		return upload(ctx, client, src, dst, stdin, stdout)
	}
}

// upload puts a local file or stdin to the destination key, inferring the
// key from the source basename for prefix-form destinations. stdin is only
// touched when the source argument is "-".
func upload(ctx context.Context, client *gists3.Client, src, dst location, stdin io.Reader, stdout io.Writer) error {
	body := stdin
	if src.kind == locLocal {
		f, err := os.Open(src.path)
		if err != nil {
			return err
		}
		defer f.Close()
		body = f
		if dst.prefixForm() {
			dst.key += filepath.Base(src.path)
		}
	}
	b, err := readBody(body)
	if err != nil {
		return err
	}
	in := &gists3.PutObjectInput{Bucket: dst.bucket, Key: dst.key, Body: bytes.NewReader(b)}
	if _, err := client.PutObject(ctx, in); err != nil {
		return err
	}
	if src.kind == locLocal {
		fmt.Fprintf(stdout, "upload: %s to %s\n", src, dst)
	}
	return nil
}

// readBody buffers an upload body, enforcing the guards in evaluation order
// (docs/001-cp-command.md §4.6): size first — reading one byte past the cap
// distinguishes "too big" from a body ending exactly at it, where a plain
// LimitedReader would silently truncate — then UTF-8, since the capped read
// can split a rune. Empty bodies are left to PutObject's ErrEmptyBody.
func readBody(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxObjectBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxObjectBytes {
		return nil, fmt.Errorf("content exceeds %d MiB, more than a gist can hold", maxObjectBytes>>20)
	}
	if !utf8.Valid(b) {
		return nil, errors.New("content is not valid UTF-8; the gist backend stores text only — encode binary data first (e.g. base64)")
	}
	return b, nil
}

// download fetches the object to a local file or stdout. The body is fully
// buffered before the first byte is written, so a failed fetch never
// creates, truncates, or partially writes the destination.
func download(ctx context.Context, client *gists3.Client, src, dst location, stdout io.Writer) error {
	out, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: src.bucket, Key: src.key})
	if err != nil {
		return err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return err
	}
	if dst.kind == locStdio {
		_, err = stdout.Write(b)
		return err
	}
	target := dst.path
	if dirTarget(target) {
		// The inferred name must stay a single path component inside the
		// directory: path.Base can return ".." (key "a/.."), escaping it,
		// and is backslash-blind, which on Windows would materialize
		// directories the flat namespace does not have.
		base := path.Base(src.key)
		if base == "." || base == ".." || strings.ContainsAny(base, `/\`) {
			return fmt.Errorf("cannot infer a safe local filename from key %q; name the destination file explicitly", src.key)
		}
		target = filepath.Join(target, base)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(target, b, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "download: %s to %s\n", src, target)
	return nil
}

// dirTarget reports whether a download destination means "into this
// directory": a trailing separator always does, existing or not, and an
// existing directory does.
func dirTarget(p string) bool {
	if strings.HasSuffix(p, "/") || strings.HasSuffix(p, string(os.PathSeparator)) {
		return true
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// remoteCopy is the library's GetObject+PutObject composition: two API
// requests, not atomic. A prefix-form destination takes the source key's
// last segment, as aws does for single objects.
func remoteCopy(ctx context.Context, client *gists3.Client, src, dst location, stdout io.Writer) error {
	if dst.prefixForm() {
		dst.key += path.Base(src.key)
	}
	_, err := client.CopyObject(ctx, &gists3.CopyObjectInput{
		Bucket:     dst.bucket,
		Key:        dst.key,
		CopySource: src.bucket + "/" + src.key,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "copy: %s to %s\n", src, dst)
	return nil
}
