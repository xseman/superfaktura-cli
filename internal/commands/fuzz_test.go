package commands

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The parsers in this package take strings straight from the command line, so
// the interesting inputs are the ones nobody would type on purpose. Each target
// asserts a property the rest of the program relies on, not merely that the
// call returned.

// FuzzDocumentItemSpec: --item is free text split on colons.
//
// The property is that the result is safe to put in a request body. A returned
// item must carry a non-empty name and numbers that JSON can actually encode —
// strconv.ParseFloat accepts "NaN" and "Inf" without complaint, and those
// travel unnoticed until json.Marshal refuses the whole payload.
func FuzzDocumentItemSpec(f *testing.F) {
	for _, seed := range []string{
		"Consulting:2:500:23",
		"Consulting",
		"",
		":2:500",
		"Name:two",
		"a:1:2:3:4",
		"Práca:1,5:10:20",
		"Consulting:NaN",
		"Consulting:Inf:0:0",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, spec string) {
		item, err := documentItemSpec(spec)
		if err != nil {
			if item != nil {
				t.Fatalf("documentItemSpec(%q) returned both an item and an error", spec)
			}
			return
		}

		name, ok := item["name"].(string)
		if !ok || name == "" {
			t.Fatalf("documentItemSpec(%q) accepted an item with no name: %#v", spec, item)
		}
		if name != strings.Split(spec, ":")[0] {
			t.Fatalf("documentItemSpec(%q) named the item %q", spec, name)
		}
		for _, field := range []string{"quantity", "unit_price", "tax"} {
			value, present := item[field]
			if !present {
				continue
			}
			number, ok := value.(float64)
			if !ok {
				t.Fatalf("documentItemSpec(%q) put a %T in %s", spec, value, field)
			}
			if math.IsNaN(number) || math.IsInf(number, 0) {
				t.Fatalf("documentItemSpec(%q) accepted %v for %s", spec, number, field)
			}
		}
		if _, err := json.Marshal(item); err != nil {
			t.Fatalf("documentItemSpec(%q) built an unsendable item %#v: %v", spec, item, err)
		}
	})
}

// FuzzSplitUsage: a flag's help becomes a form label and a hint.
//
// Both halves are drawn in the browser, so they must be printable text: valid
// UTF-8, and a label that is not silently empty when the help said something.
func FuzzSplitUsage(f *testing.F) {
	for _, seed := range [][2]string{
		{"created", "Issue date (YYYY-MM-DD)"},
		{"client", "Existing client, by numeric ID or by name"},
		{"type", "Document type (see 'sf values invoice-types')"},
		{"comment", "Comment"},
		{"new-client", "Create the invoice for a new client with this name"},
		{"", "Create the invoice for a new client with this name"},
		{"dátum-splatnosti", "Create the invoice for a new client with this name"},
	} {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, name, usage string) {
		if !utf8.ValidString(name) || !utf8.ValidString(usage) {
			t.Skip("cobra flag help is always valid UTF-8")
		}

		label, hint := splitUsage(name, usage)

		if !utf8.ValidString(label) || !utf8.ValidString(hint) {
			t.Fatalf("splitUsage(%q, %q) = %q / %q: not valid UTF-8", name, usage, label, hint)
		}
		if strings.TrimSpace(usage) != "" && strings.TrimSpace(label) == "" {
			t.Fatalf("splitUsage(%q, %q) left the field unlabelled", name, usage)
		}
		if hint != strings.TrimSpace(hint) {
			t.Fatalf("splitUsage(%q, %q) hint %q is not trimmed", name, usage, hint)
		}
		if label != strings.TrimSpace(label) {
			t.Fatalf("splitUsage(%q, %q) label %q is not trimmed", name, usage, label)
		}
	})
}

// FuzzCheckDate: the one date shape that can be read wrongly without an error.
//
// checkDate is a gate, so the property is about what it lets through: a value
// it accepts must not contain a slash, and if it has the ISO shape it must be a
// day that really exists.
func FuzzCheckDate(f *testing.F) {
	for _, seed := range []string{
		"2026-08-15", "15.08.2026", "31.12.2026", "tomorrow", "+14 days",
		"next friday", "08/09/2026", "2026-02-30", "2026-13-01", "", "   ",
		"0000-00-00", "9999-99-99",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		err := checkDate(value)
		if err != nil {
			if err.Message == "" || err.Hint == "" {
				t.Fatalf("checkDate(%q) refused without saying why: %#v", value, err)
			}
			return
		}

		trimmed := strings.TrimSpace(value)
		if strings.Contains(trimmed, "/") {
			t.Fatalf("checkDate(%q) allowed an american-order date through", value)
		}
		if isoDate.MatchString(trimmed) {
			if _, perr := time.Parse("2006-01-02", trimmed); perr != nil {
				t.Fatalf("checkDate(%q) allowed a day that does not exist: %v", value, perr)
			}
		}
	})
}
