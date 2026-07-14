// Command g3 is the gists3 CLI from the DESIGN.md roadmap, implementing cp
// and ls. Credentials resolve from GIST_TOKEN, then the gists3 config file
// (<user config dir>/gists3/config.json), then the gh CLI's stored token
// (`gh auth token`); the full contract lives in docs/001-cp-command.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

const usage = `usage: g3 <command> [arguments]

commands:
  cp <source> <destination>  copy one object between the local machine and a
                             gist; one side must be a g3://<gist-id>/<key>
                             URI, and "-" means stdin or stdout (a local file
                             named "-" is reachable as ./-). Directories are
                             not supported — there is no --recursive yet.
  ls [g3://<gist-id>[/<prefix>]]
                             list buckets (gists) the token can see, or one
                             bucket's objects, optionally prefix-filtered`

// usageError marks a command-line mistake: main exits 2 for these and 1 for
// every runtime failure.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

func main() {
	err := run(context.Background(), os.Args[1:], newClient, os.Stdin, os.Stdout, os.Stderr)
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "g3:", err)
	var ue *usageError
	if errors.As(err, &ue) {
		os.Exit(2)
	}
	os.Exit(1)
}

func run(ctx context.Context, args []string, newClient clientFn, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usagef("no command given\n%s", usage)
	}
	switch args[0] {
	case "cp":
		if len(args) != 3 {
			return usagef("cp takes exactly a source and a destination\n%s", usage)
		}
		// Classification precedes client construction so a bad invocation
		// exits 2 even on a machine with no credentials.
		src, dst, err := classify(args[1], args[2])
		if err != nil {
			return err
		}
		client, err := newClient(stderr)
		if err != nil {
			return err
		}
		return cp(ctx, client, src, dst, stdin, stdout)
	case "ls":
		if len(args) > 2 {
			return usagef("ls takes at most one g3:// URI\n%s", usage)
		}
		var loc location
		if len(args) == 2 {
			var err error
			if loc, err = parseArg(args[1]); err != nil {
				return err
			}
			if loc.kind != locRemote {
				return usagef("%q: ls lists gists; want g3://<gist-id>[/<prefix>]", args[1])
			}
		}
		client, err := newClient(stderr)
		if err != nil {
			return err
		}
		if loc.kind == locRemote {
			return lsObjects(ctx, client, loc, stdout)
		}
		return lsBuckets(ctx, client, stdout)
	default:
		return usagef("unknown command %q\n%s", args[0], usage)
	}
}
