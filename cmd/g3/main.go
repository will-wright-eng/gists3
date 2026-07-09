// Command g3 is the gists3 CLI from the DESIGN.md roadmap. v0 implements
// only ls; credentials resolve from GIST_TOKEN, falling back to the gh CLI's
// stored token (`gh auth token`).
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/will-wright-eng/gists3/internal/gists3"
)

const usage = `usage: g3 <command>

commands:
  ls    list buckets (gists) the token can see`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "g3:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no command given\n%s", usage)
	}
	switch args[0] {
	case "ls":
		return ls(context.Background())
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

func ls(ctx context.Context) error {
	token, err := resolveToken()
	if err != nil {
		return err
	}
	out, err := gists3.New(token).ListBuckets(ctx, &gists3.ListBucketsInput{})
	if err != nil {
		return err
	}
	for _, b := range out.Buckets {
		fmt.Printf("%s  %s\n", b.CreationDate.Format("2006-01-02"), b.Name)
	}
	return nil
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
