package render

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzGet: a column's dotted path against a response the server chose.
//
// Both halves are outside this package's control — the path is a literal in a
// column table, but the map is whatever the API sent, and API-DISCREPANCIES.md
// exists because that is not always the documented shape. Get must therefore
// answer for every combination without panicking, and a missing segment must
// read as absent rather than as some other field's value.
func FuzzGet(f *testing.F) {
	for _, seed := range []struct{ body, path string }{
		{`{"Invoice":{"name":"Faktúra","id":"1042"},"Client":{"name":"John Doe"}}`, "Invoice.name"},
		{`{"Invoice":{"name":"Faktúra"}}`, "Client.name"},
		{`{"Invoice":{"name":"Faktúra"}}`, "Invoice.name.deeper"},
		{`{"Invoice":null}`, "Invoice.name"},
		{`{"Invoice":[1,2,3]}`, "Invoice.0"},
		{`{"":{"":1}}`, "."},
		{`{}`, ""},
		{`{"a":{"b":{"c":{"d":1}}}}`, "a.b.c.d"},
	} {
		f.Add(seed.body, seed.path)
	}

	f.Fuzz(func(t *testing.T, body, path string) {
		var obj map[string]any
		if err := json.Unmarshal([]byte(body), &obj); err != nil || obj == nil {
			t.Skip("not a decoded JSON object")
		}

		got := Get(obj, path)

		// Whatever came back must render, because every caller renders it.
		_ = Text(got)
		_ = Money(got)
		_ = Quantity(got)
		_ = Date(got)
		_ = Truncate(1)(got)

		// A path with no separator can only be a direct lookup.
		if !strings.Contains(path, ".") {
			want := obj[path]
			if got == nil && want != nil || got != nil && want == nil {
				t.Fatalf("Get(%q) = %#v, want %#v", path, got, want)
			}
		}

		// Anything found is reachable by walking the same segments by hand.
		if got != nil {
			var current any = obj
			for _, segment := range strings.Split(path, ".") {
				m, ok := current.(map[string]any)
				if !ok {
					t.Fatalf("Get(%q) returned %#v from a non-object", path, got)
				}
				current = m[segment]
			}
		}
	})
}

// FuzzTruncate: the width cap that keeps one long note from wrecking a table.
func FuzzTruncate(f *testing.F) {
	f.Add("Faktúra 2026005", 8)
	f.Add("", 1)
	f.Add("Klient nepatrí pod túto firmu.", 30)
	f.Add("ďžťň", 2)

	f.Fuzz(func(t *testing.T, text string, width int) {
		if width < 1 || width > 4096 {
			t.Skip("column widths are small positive literals")
		}
		got := Truncate(width)(text)
		if n := len([]rune(got)); n > width {
			t.Fatalf("Truncate(%d)(%q) = %q, which is %d wide", width, text, got, n)
		}
	})
}
