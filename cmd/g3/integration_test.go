//go:build integration

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

// consistencyWindow bounds how long the test waits out the gist backend's
// eventual consistency, matching internal/gists3test.
const consistencyWindow = 20 * time.Second

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

func liveToken(t *testing.T) string {
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
	return token
}

func buildG3(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "g3")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// TestIntegrationLinkLifecycle drives the compiled binary through the
// docs/004 §11 lifecycle against the live API: link add → path → push →
// status → external remote edit → pull → refused pull → push → link rm.
// The child processes get a temp HOME so the developer's real config.json
// and state.json are untouched; identity passes through GIST_TOKEN.
func TestIntegrationLinkLifecycle(t *testing.T) {
	token := liveToken(t)
	bin := buildG3(t)
	home := t.TempDir()
	env := append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"AppData="+filepath.Join(home, "appdata"),
		"GIST_TOKEN="+token,
	)
	g3 := func(args ...string) (stdout, stderr string, err error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err = cmd.Run()
		return out.String(), errb.String(), err
	}
	mustG3 := func(args ...string) string {
		t.Helper()
		stdout, stderr, err := g3(args...)
		if err != nil {
			t.Fatalf("g3 %v: %v\nstderr: %s", args, err, stderr)
		}
		return stdout
	}
	// g3Eventually absorbs consistency lag for commands whose reads can
	// briefly trail the writes above them.
	g3Eventually := func(op string, args ...string) string {
		t.Helper()
		var stdout string
		eventually(t, op, func() error {
			out, stderr, err := g3(args...)
			if err != nil {
				return fmt.Errorf("%v: %s", err, stderr)
			}
			stdout = out
			return nil
		})
		return stdout
	}

	client := gists3.New(token)
	ctx := context.Background()
	create, err := client.CreateBucket(ctx, &gists3.CreateBucketInput{
		Description: fmt.Sprintf("gists3 integration test link-lifecycle %s (safe to delete)", time.Now().UTC().Format(time.RFC3339Nano)),
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
			t.Errorf("cleanup DeleteBucket(%s): %v", bucket, err)
		}
	})
	t.Logf("bucket %s", bucket)

	local := filepath.Join(home, "notes", "it.md")
	uri := "g3://" + bucket + "/it.md"
	mustG3("link", "add", "it", uri, local)

	// $(g3 path it) is the editor contract: exactly the expanded path.
	if got := mustG3("path", "it"); got != local+"\n" {
		t.Fatalf("path = %q, want %q", got, local)
	}

	// The §11 headline workflow: write through the path, push.
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g3Eventually("push", "push", "it")
	eventually(t, "GetObject after push", func() error {
		got, err := readRemote(ctx, client, bucket, "it.md")
		if err != nil {
			return err
		}
		if got != "v1\n" {
			return fmt.Errorf("remote = %q, want v1", got)
		}
		return nil
	})

	eventually(t, "status in-sync", func() error {
		out, stderr, err := g3("status", "it")
		if err != nil {
			return fmt.Errorf("%v: %s", err, stderr)
		}
		if !strings.HasPrefix(out, "in-sync") {
			return fmt.Errorf("status = %q, want in-sync", out)
		}
		return nil
	})

	// An "edited in the GitHub UI" remote change, then pull brings it down.
	eventually(t, "external PutObject", func() error {
		_, err := client.PutObject(ctx, &gists3.PutObjectInput{Bucket: bucket, Key: "it.md", Body: strings.NewReader("v2\n")})
		return err
	})
	eventually(t, "pull after remote edit", func() error {
		if _, stderr, err := g3("pull", "it"); err != nil {
			return fmt.Errorf("%v: %s", err, stderr)
		}
		b, err := os.ReadFile(local)
		if err != nil {
			return err
		}
		if string(b) != "v2\n" {
			return fmt.Errorf("local = %q; the read likely lagged, retrying", b)
		}
		return nil
	})

	// Unpushed local work refuses a pull: exit 1, refusal on stderr, file
	// untouched.
	if err := os.WriteFile(local, []byte("v3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := g3("pull", "it")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("pull with local edits = %v, want exit 1", err)
	}
	if !strings.Contains(stderr, "refused") {
		t.Fatalf("stderr = %q, want a refusal", stderr)
	}
	if b, _ := os.ReadFile(local); string(b) != "v3\n" {
		t.Fatalf("local = %q; a refusal must change nothing", b)
	}

	g3Eventually("push local edit", "push", "it")

	// The @<link> alias stands in for the URI on either side of cp — the §5.2
	// reconciliation recipe, minus the 32-hex ID.
	if got := g3Eventually("cp from alias", "cp", "@it", "-"); got != "v3\n" {
		t.Fatalf("cp @it - = %q, want the remote body", got)
	}
	v4 := filepath.Join(home, "v4.md")
	if err := os.WriteFile(v4, []byte("v4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g3Eventually("cp to alias", "cp", v4, "@it")

	// An undeclared alias is a usage error, and never reaches the network.
	_, stderr, err = g3("cp", "@nope", "-")
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("cp @nope - = %v, want exit 2", err)
	}
	if !strings.Contains(stderr, "unknown link") {
		t.Fatalf("stderr = %q, want the unknown-link error", stderr)
	}

	// rm keeps both sides.
	mustG3("link", "rm", "it")
	eventually(t, "GetObject after rm", func() error {
		got, err := readRemote(ctx, client, bucket, "it.md")
		if err != nil {
			return err
		}
		if got != "v4\n" {
			return fmt.Errorf("remote = %q, want v4 kept", got)
		}
		return nil
	})
	if _, err := os.Stat(local); err != nil {
		t.Fatalf("local file after rm: %v; link rm must keep it", err)
	}
}

func readRemote(ctx context.Context, client *gists3.Client, bucket, key string) (string, error) {
	out, err := client.GetObject(ctx, &gists3.GetObjectInput{Bucket: bucket, Key: key})
	if err != nil {
		return "", err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
