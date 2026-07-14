package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/will-wright-eng/gists3"
)

// lsBuckets prints one line per gist: created, id, visibility, object count
// and total size (both excluding the ".bucket" placeholder, as the library
// reports them), and the description when the gist has one.
func lsBuckets(ctx context.Context, client *gists3.Client, stdout io.Writer) error {
	out, err := client.ListBuckets(ctx, &gists3.ListBucketsInput{})
	if err != nil {
		return err
	}
	for _, b := range out.Buckets {
		visibility, unit := "secret", "objects"
		if b.Public {
			visibility = "public"
		}
		if b.ObjectCount == 1 {
			unit = "object"
		}
		desc := ""
		if d := printable(b.Description); d != "" {
			desc = "  " + d
		}
		fmt.Fprintf(stdout, "%s  %s  %s  %2d %-7s  %6s%s\n",
			b.CreationDate.Format("2006-01-02 15:04"), b.Name, visibility,
			b.ObjectCount, unit, humanSize(b.TotalSize), desc)
	}
	return nil
}

// lsObjects prints one "size  key" line per object. No per-object timestamp
// column: gists have no per-file times, and the flat namespace means the
// key prints whole. loc.key — empty, or anything after the gist ID — is the
// prefix filter.
func lsObjects(ctx context.Context, client *gists3.Client, loc location, stdout io.Writer) error {
	out, err := client.ListObjectsV2(ctx, &gists3.ListObjectsV2Input{Bucket: loc.bucket, Prefix: loc.key})
	if err != nil {
		return err
	}
	for _, o := range out.Contents {
		fmt.Fprintf(stdout, "%6s  %s\n", humanSize(o.Size), o.Key)
	}
	return nil
}

// printable flattens control characters to spaces and trims the result, so
// a multi-line or escape-bearing gist description cannot break the
// one-line-per-gist output or smuggle terminal control sequences.
func printable(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s))
}

// humanSize renders bytes verbatim below 1 KiB and with one decimal above,
// promoting to the next unit once %.1f rounding would reach a fourth integer
// digit — "1000.0K" is seven characters and would overflow the %6s columns,
// so [1023949, 1048575] bytes print as "1.0M". A gist's ~10 MB ceiling needs
// nothing past M.
func humanSize(n int64) string {
	const k, m = 1 << 10, 1 << 20
	v := float64(n)
	switch {
	case v >= 999.95*m:
		return fmt.Sprintf("%.0fM", v/m)
	case v >= 999.95*k:
		return fmt.Sprintf("%.1fM", v/m)
	case n >= k:
		return fmt.Sprintf("%.1fK", v/k)
	default:
		return strconv.FormatInt(n, 10)
	}
}
