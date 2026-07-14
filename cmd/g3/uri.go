package main

import "strings"

// locKind classifies one cp argument.
type locKind int

const (
	locLocal locKind = iota
	locRemote
	locStdio
)

// location is one parsed cp argument. For remotes, an empty key or one
// ending in "/" records the prefix form (bare bucket or key prefix): legal
// only as a destination, with the final key resolved by inference in cp.
type location struct {
	kind   locKind
	bucket string // remote: gist ID
	key    string // remote: filename, verbatim — slashes carry no delimiter semantics
	path   string // local
}

func (l location) prefixForm() bool {
	return l.key == "" || strings.HasSuffix(l.key, "/")
}

// String renders the location the way status lines and errors print it.
func (l location) String() string {
	switch l.kind {
	case locRemote:
		return "g3://" + l.bucket + "/" + l.key
	case locStdio:
		return "-"
	default:
		return l.path
	}
}

// parseArg classifies one argument without touching the network or the
// filesystem: "-" is stdio, g3://<gist-id>[/<key>] is remote, any other URI
// scheme is refused, and everything else is a local path.
func parseArg(arg string) (location, error) {
	if arg == "-" {
		return location{kind: locStdio}, nil
	}
	if rest, ok := strings.CutPrefix(arg, "g3://"); ok {
		bucket, key, _ := strings.Cut(rest, "/")
		if bucket == "" {
			return location{}, usagef("%q: missing gist ID; want g3://<gist-id>/<key>", arg)
		}
		return location{kind: locRemote, bucket: bucket, key: key}, nil
	}
	if strings.Contains(arg, "://") {
		return location{}, usagef("%q: unsupported URI scheme; only g3:// is understood", arg)
	}
	return location{kind: locLocal, path: arg}, nil
}
