package client

import (
	"net/url"
	"strings"
	"testing"
)

// FuzzEscapeSegment: filter values become path segments.
//
// escapeSegment deliberately leaves the comma raw, which is a hole punched in
// url.PathEscape by hand — so the properties worth pinning are that nothing
// else leaked out with it. A raw "/" would invent a segment, a raw space or
// control byte would not survive an HTTP request line, and the value has to
// come back out unchanged on the far side.
func FuzzEscapeSegment(f *testing.F) {
	for _, seed := range []string{
		"", "2026-08-15", "sent", "Faktúra 2026005", EncodeSearch("John Doe"),
		"a,b", "a/b", "a b", "%2C", "100%", "a?b#c", "a:b", "\n", "\x00",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		got := escapeSegment(value)

		for _, forbidden := range []string{"/", " ", "?", "#"} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("escapeSegment(%q) = %q, which contains a raw %q", value, got, forbidden)
			}
		}
		for _, r := range got {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("escapeSegment(%q) = %q, which contains a control byte", value, got)
			}
		}

		// The comma is the only substitution, so putting it back yields
		// ordinary percent-encoding that must decode to the input.
		back, err := url.PathUnescape(strings.ReplaceAll(got, ",", "%2C"))
		if err != nil {
			t.Fatalf("escapeSegment(%q) = %q, which does not decode: %v", value, got, err)
		}
		if back != value {
			t.Fatalf("escapeSegment(%q) = %q, which decodes to %q", value, got, back)
		}
	})
}

// FuzzParamsPath: the whole segment list, not one value.
//
// Params.path() joins "/key:value" pairs, so a value must never be able to
// close its own segment and start another one — that would turn one filter
// into two and quietly change which records the server returns.
func FuzzParamsPath(f *testing.F) {
	f.Add("created", "2026-08-15", "status", "sent")
	f.Add("search", EncodeSearch("John Doe"), "per_page", "50")
	f.Add("a", "b/c", "d", "e f")

	f.Fuzz(func(t *testing.T, k1, v1, k2, v2 string) {
		// Keys are written in this package, never by a user; values are not.
		for _, k := range []string{k1, k2} {
			for _, r := range k {
				if ('a' > r || r > 'z') && r != '_' {
					t.Skip("filter keys are literals in this package")
				}
			}
			if k == "" {
				t.Skip("filter keys are literals in this package")
			}
		}
		if k1 == k2 {
			t.Skip("a map cannot hold the same key twice")
		}

		p := Params{k1: v1, k2: v2}
		path := p.path()

		if strings.Count(path, "/") != 2 {
			t.Fatalf("Params{%q:%q, %q:%q}.path() = %q, which is not two segments",
				k1, v1, k2, v2, path)
		}
		if _, err := url.Parse("https://example.test/invoices/index.json" + path); err != nil {
			t.Fatalf("path %q is not a usable URL: %v", path, err)
		}
	})
}
