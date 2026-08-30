// Package render turns SuperFaktura's nested response maps into terminal output.
//
// Responses are CakePHP-shaped: a list item is a map keyed by model name, so an
// invoice number lives at "Invoice.invoice_no_formatted" and its client at
// "Client.name". Columns therefore address values by dotted path rather than by
// struct field, which keeps the CLI from having to model every response type.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
)

// Column describes one table column.
type Column struct {
	Header string
	Path   string
	// Format converts the raw value; nil means Text.
	Format func(any) string
	// Right aligns the column to its right edge, which is the only way a
	// column of amounts lines its decimal points up.
	Right bool
}

// Field describes one line of a detail view.
type Field struct {
	Label  string
	Path   string
	Format func(any) string
	// Right pushes the value to the right of the block, so amounts stack with
	// their decimal points under each other.
	Right bool
}

// Table writes rows as an aligned, uncolored table. Alignment is done by
// tabwriter, which counts runes — correct for the Latin-with-diacritics text
// SuperFaktura returns, and the reason this stays in the standard library.
func Table(w io.Writer, cols []Column, rows []map[string]any) {
	if len(rows) == 0 {
		_, _ = fmt.Fprintln(w, "No results.")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = strings.ToUpper(c.Header)
	}
	_, _ = fmt.Fprintln(tw, strings.Join(headers, "\t"))

	for _, row := range rows {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = format(c.Format, Get(row, c.Path))
		}
		_, _ = fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	_ = tw.Flush()
}

func format(fn func(any) string, v any) string {
	if fn != nil {
		return fn(v)
	}
	return Text(v)
}

// Get resolves a dotted path against a decoded JSON map. A missing segment
// yields nil rather than an error: half-populated records are normal here.
func Get(obj map[string]any, path string) any {
	var current any = obj
	for _, segment := range strings.Split(path, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = m[segment]
		if !ok {
			return nil
		}
	}
	return current
}

// Text renders a JSON value as a single-line string.
func Text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case bool:
		if t {
			return "yes"
		}
		return "no"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, Text(item))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(t)
	}
}

// Money formats an amount the way SuperFaktura returns it — a decimal string —
// trimmed to two places. Amounts arrive as strings like "12.000000".
func Money(v any) string {
	text := Text(v)
	if text == "" {
		return ""
	}
	amount, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return text
	}
	return strconv.FormatFloat(amount, 'f', 2, 64)
}

// Date trims the time component off a "2050-01-01 23:59:59" timestamp.
// Quantity drops the API's trailing zeros: "8.00000" is 8, "1.50000" is 1.5.
func Quantity(v any) string {
	text := Text(v)
	if !strings.Contains(text, ".") {
		return text
	}
	return strings.TrimSuffix(strings.TrimRight(text, "0"), ".")
}

// Percent renders a VAT rate.
func Percent(v any) string {
	if text := Quantity(v); text != "" {
		return text + "%"
	}
	return ""
}

func Date(v any) string {
	text := Text(v)
	if date, _, found := strings.Cut(text, " "); found {
		return date
	}
	return text
}

// Truncate caps a column's width so one long note cannot wreck the table.
func Truncate(width int) func(any) string {
	return func(v any) string {
		text := Text(v)
		if len([]rune(text)) <= width {
			return text
		}
		return string([]rune(text)[:width-1]) + "…"
	}
}
