package main

import (
	"errors"
	"testing"
)

func TestParseArg(t *testing.T) {
	for name, tc := range map[string]struct {
		arg  string
		want location
	}{
		"remote":         {"g3://abc123/conf.json", location{kind: locRemote, bucket: "abc123", key: "conf.json"}},
		"nested key":     {"g3://abc123/a/b.txt", location{kind: locRemote, bucket: "abc123", key: "a/b.txt"}},
		"bare bucket":    {"g3://abc123", location{kind: locRemote, bucket: "abc123"}},
		"trailing slash": {"g3://abc123/", location{kind: locRemote, bucket: "abc123"}},
		"prefix":         {"g3://abc123/x/", location{kind: locRemote, bucket: "abc123", key: "x/"}},
		"stdio":          {"-", location{kind: locStdio}},
		"local":          {"file.txt", location{kind: locLocal, path: "file.txt"}},
		"local relative": {"./dir/file.txt", location{kind: locLocal, path: "./dir/file.txt"}},
		"local dash":     {"./-", location{kind: locLocal, path: "./-"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseArg(tc.arg)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("parseArg(%q) = %+v, want %+v", tc.arg, got, tc.want)
			}
		})
	}
}

func TestParseArgErrors(t *testing.T) {
	for _, arg := range []string{"g3://", "g3:///k", "s3://x/y", "https://example.com/x"} {
		t.Run(arg, func(t *testing.T) {
			_, err := parseArg(arg)
			var ue *usageError
			if !errors.As(err, &ue) {
				t.Errorf("parseArg(%q) err = %v, want *usageError", arg, err)
			}
		})
	}
}

func TestPrefixForm(t *testing.T) {
	for arg, want := range map[string]bool{
		"g3://b":       true,
		"g3://b/":      true,
		"g3://b/x/":    true,
		"g3://b/x":     false,
		"g3://b/x/y.t": false,
	} {
		t.Run(arg, func(t *testing.T) {
			loc, err := parseArg(arg)
			if err != nil {
				t.Fatal(err)
			}
			if got := loc.prefixForm(); got != want {
				t.Errorf("prefixForm(%q) = %v, want %v", arg, got, want)
			}
		})
	}
}
