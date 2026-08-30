package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}
	return obj
}

func TestGetWalksTheNestedModelMaps(t *testing.T) {
	obj := decode(t, `{"Invoice":{"id":"1","name":"Faktúra"},"Client":{"name":"John Doe"}}`)

	if got := Text(Get(obj, "Invoice.name")); got != "Faktúra" {
		t.Errorf("Invoice.name = %q", got)
	}
	if got := Text(Get(obj, "Client.name")); got != "John Doe" {
		t.Errorf("Client.name = %q", got)
	}
}

func TestGetReturnsNilRatherThanPanickingOnMissingSegments(t *testing.T) {
	obj := decode(t, `{"Invoice":{"id":"1"}}`)

	for _, path := range []string{"Client.name", "Invoice.name.deeper", "", "Invoice.id.nope"} {
		if got := Get(obj, path); got != nil {
			t.Errorf("Get(%q) = %v, want nil", path, got)
		}
	}
}

func TestTextRendersEachJSONKind(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"  padded  ", "padded"},
		{true, "yes"},
		{false, "no"},
		{float64(12), "12"},
		{float64(12.5), "12.5"},
		{[]any{"a", "b"}, "a, b"},
	}
	for _, c := range cases {
		if got := Text(c.in); got != c.want {
			t.Errorf("Text(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMoneyNormalizesTheAPIsDecimalStrings(t *testing.T) {
	cases := map[string]string{
		"12.000000": "12.00",
		"12.00":     "12.00",
		"0":         "0.00",
		"":          "",
		"not money": "not money",
	}
	for in, want := range cases {
		if got := Money(in); got != want {
			t.Errorf("Money(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDateDropsTheTimeComponent(t *testing.T) {
	if got := Date("2050-01-01 23:59:59"); got != "2050-01-01" {
		t.Errorf("Date = %q", got)
	}
	if got := Date("2050-01-01"); got != "2050-01-01" {
		t.Errorf("Date = %q", got)
	}
}

func TestTruncateCountsRunesNotBytes(t *testing.T) {
	// Ten runes, more than ten bytes because of the diacritics.
	truncate := Truncate(5)
	if got := truncate("ľščťžýáíé"); got != "ľščť…" {
		t.Errorf("Truncate = %q, want %q", got, "ľščť…")
	}
	if got := truncate("abc"); got != "abc" {
		t.Errorf("short values should pass through, got %q", got)
	}
}

func TestTableAlignsColumnsAndUppercasesHeaders(t *testing.T) {
	rows := []map[string]any{
		decode(t, `{"Invoice":{"id":"1","invoice_no_formatted":"2026001"},"Client":{"name":"Acme"},"0":{"total":"1240.000000"}}`),
		decode(t, `{"Invoice":{"id":"22","invoice_no_formatted":"2026002"},"Client":{"name":"Beta"},"0":{"total":"9.5"}}`),
	}
	cols := []Column{
		{Header: "ID", Path: "Invoice.id"},
		{Header: "Number", Path: "Invoice.invoice_no_formatted"},
		{Header: "Client", Path: "Client.name"},
		{Header: "Total", Path: "0.total", Format: Money},
	}

	var buf bytes.Buffer
	Table(&buf, cols, rows)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")

	if len(lines) != 3 {
		t.Fatalf("expected a header and two rows, got %d lines:\n%s", len(lines), buf.String())
	}
	if !strings.HasPrefix(lines[0], "ID  NUMBER") {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "1240.00") {
		t.Errorf("row = %q", lines[1])
	}
	// The ID column must be wide enough for the longest value.
	if !strings.HasPrefix(lines[2], "22  ") {
		t.Errorf("row = %q", lines[2])
	}
}

func TestTableSaysSoWhenThereIsNothingToShow(t *testing.T) {
	var buf bytes.Buffer
	Table(&buf, []Column{{Header: "ID", Path: "id"}}, nil)
	if !strings.Contains(buf.String(), "No results") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestDetailSkipsEmptyFields(t *testing.T) {
	obj := decode(t, `{"Invoice":{"id":"1","comment":"","name":"Faktúra"}}`)
	fields := []Field{
		{Label: "ID", Path: "Invoice.id"},
		{Label: "Comment", Path: "Invoice.comment"},
		{Label: "Name", Path: "Invoice.name"},
		{Label: "Missing", Path: "Invoice.nope"},
	}

	var buf bytes.Buffer
	Detail(&buf, Headline{}, fields, obj)
	got := buf.String()

	if strings.Contains(got, "Comment") || strings.Contains(got, "Missing") {
		t.Errorf("empty fields should be skipped:\n%s", got)
	}
	if !strings.Contains(got, "Faktúra") {
		t.Errorf("output = %q", got)
	}
}

// The detail view is two columns under a headline. What matters is that a
// label never changes column because the record next to it happened to have a
// comment — the split comes from the declared list, not from what has a value.

func TestTheColumnSplitDoesNotDependOnTheData(t *testing.T) {
	fields := []Field{
		{Label: "Net", Path: "Invoice.amount"},
		{Label: "VAT", Path: "Invoice.vat"},
		{Label: "Comment", Path: "Invoice.comment"},
		{Label: "Issued", Path: "Invoice.created"},
		{Label: "ID", Path: "Invoice.id"},
		{Label: "Token", Path: "Invoice.token"},
	}

	column := func(raw string) map[string]int {
		lines := DetailLines(Headline{}, fields, decode(t, raw), 60, nil)
		at := map[string]int{}
		for _, line := range lines {
			for _, label := range []string{"Net", "VAT", "Comment", "Issued", "ID", "Token"} {
				if i := strings.Index(line, label+" "); i >= 0 {
					at[label] = i
				}
			}
		}
		return at
	}

	full := column(`{"Invoice":{"amount":"1","vat":"0","comment":"hi","created":"2026-08-01","id":"7","token":"t"}}`)
	sparse := column(`{"Invoice":{"amount":"1","vat":"0","created":"2026-08-01","id":"7","token":"t"}}`)

	for _, label := range []string{"Issued", "ID", "Token"} {
		if full[label] != sparse[label] {
			t.Errorf("%s moved from column %d to %d when the comment went away",
				label, full[label], sparse[label])
		}
	}
	if _, ok := sparse["Comment"]; ok {
		t.Error("an empty field was rendered")
	}
}

func TestTheHeadlineIsOmittedWhenEmpty(t *testing.T) {
	// A client or a stock item has no money and no settlement state, so there
	// is nothing to summarize and the rule above the columns would be noise.
	fields := []Field{{Label: "Name", Path: "Client.name"}}
	obj := decode(t, `{"Client":{"name":"Acme s.r.o."}}`)

	lines := DetailLines(Headline{}, fields, obj, 40, nil)
	for _, line := range lines {
		if strings.Contains(line, "─") {
			t.Errorf("an empty headline still drew its rule: %q", line)
		}
	}

	withHead := DetailLines(Headline{Title: "X", Badge: "PAID"}, fields, obj, 40, nil)
	if len(withHead) <= len(lines) {
		t.Error("a headline was asked for and not drawn")
	}
}

func TestStyledLabelsStillLineUp(t *testing.T) {
	// The browser colors the label column. Escape sequences must not count
	// towards the width or the two columns drift apart on styled output only.
	fields := []Field{
		{Label: "Net", Path: "Invoice.amount"},
		{Label: "ID", Path: "Invoice.id"},
	}
	obj := decode(t, `{"Invoice":{"amount":"1.00","id":"7"}}`)

	plain := DetailLines(Headline{}, fields, obj, 0, nil)
	styled := DetailLines(Headline{}, fields, obj, 0, func(_ Tone, s string) string {
		return "\x1b[2m" + s + "\x1b[0m"
	})

	if len(plain) != len(styled) {
		t.Fatalf("%d plain lines, %d styled", len(plain), len(styled))
	}
	for i := range plain {
		if visibleLen(plain[i]) != visibleLen(styled[i]) {
			t.Errorf("line %d: %d visible columns plain, %d styled\n  %q\n  %q",
				i, visibleLen(plain[i]), visibleLen(styled[i]), plain[i], styled[i])
		}
	}
}
