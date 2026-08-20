package render

import (
	"io"
	"strings"
)

// A record laid out as a headline and two columns of fields.
//
// The flat label/value list this replaced gave every field the same weight, so
// answering "is this paid, and when was it due" meant reading fifteen lines to
// find three. The headline carries the answer; the columns carry the rest in
// half the height.
//
// Both the CLI's `view` commands and the browser's detail pane render through
// here. They print the same values already — letting them diverge in shape
// would be the drift the shared column list exists to prevent.

// Tone marks a headline badge as good or bad news. The CLI ignores it; the
// browser colors by it.
type Tone int

const (
	ToneNeutral Tone = iota
	ToneGood
	ToneBad
	ToneWarn
)

// Headline is the summary above a record. A zero Headline is omitted entirely,
// which is what records with no money or status use.
type Headline struct {
	// Title and Amount share the first line, pushed to opposite edges.
	Title  string
	Amount string
	// Subtitle and Badge share the second.
	Subtitle string
	Badge    string
	Tone     Tone
}

func (h Headline) empty() bool {
	return h.Title == "" && h.Amount == "" && h.Subtitle == "" && h.Badge == ""
}

// Styler colors one fragment. A nil Styler leaves everything plain, which is
// what the CLI passes.
type Styler func(Tone, string) string

func (s Styler) apply(tone Tone, text string) string {
	if s == nil {
		return text
	}
	return s(tone, text)
}

// labelTone marks the dim label column, distinct from a real badge tone.
const labelTone Tone = -1

// DetailLines lays out a record. width is the space available; zero or less
// means "as wide as the content needs", which is what a pipe gets.
func DetailLines(h Headline, fields []Field, obj map[string]any, width int, style Styler) []string {
	left, right := splitColumns(fields)
	leftLines := columnLines(left, obj, style)
	rightLines := columnLines(right, obj, style)

	// The second column starts at a fixed offset, not after the widest value on
	// the left. Measuring the content would slide the right column sideways as
	// the cursor moves down a list — one record has a comment, the next does
	// not — which is the same jitter the table's remembered widths fixed.
	// Content only pushes it when it genuinely will not fit in half the screen.
	gutter := max(width/2, 0)
	if natural := columnWidth(leftLines) + 3; natural > gutter {
		gutter = natural
	}

	body := make([]string, 0, max(len(leftLines), len(rightLines)))
	for i := range max(len(leftLines), len(rightLines)) {
		line := ""
		if i < len(leftLines) {
			line = leftLines[i]
		}
		if i < len(rightLines) {
			line = pad(line, gutter) + rightLines[i]
		}
		body = append(body, " "+strings.TrimRight(line, " "))
	}

	if h.empty() {
		return body
	}
	if width <= 0 {
		width = gutter + columnWidth(rightLines) + 1
	}
	return append(headlineLines(h, width, style), body...)
}

// headlineLines renders the summary block and the rule under it.
func headlineLines(h Headline, width int, style Styler) []string {
	lines := []string{
		" " + spread(h.Title, style.apply(h.Tone, h.Amount), width-1),
		" " + spread(style.apply(labelTone, h.Subtitle), style.apply(h.Tone, h.Badge), width-1),
		" " + style.apply(labelTone, strings.Repeat("─", max(1, width-1))),
	}
	// Drop an empty second line rather than print a blank one.
	if h.Subtitle == "" && h.Badge == "" {
		lines = append(lines[:1], lines[2])
	}
	return lines
}

// spread pushes left and right to opposite edges of width, and gives up
// (single space) when they will not both fit.
func spread(left, right string, width int) string {
	gap := width - visibleLen(left) - visibleLen(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// splitColumns divides the declared fields in two.
//
// The split point comes from the declared list, not from the fields that
// happen to have a value, so a label never moves between columns because the
// previous record had a comment and this one does not.
func splitColumns(fields []Field) (left, right []Field) {
	half := (len(fields) + 1) / 2
	return fields[:half], fields[half:]
}

// columnLines renders one column's label/value pairs, skipping the empty ones
// so a sparse record does not print a screen of blanks.
func columnLines(fields []Field, obj map[string]any, style Styler) []string {
	labels := make([]string, 0, len(fields))
	values := make([]string, 0, len(fields))
	for _, f := range fields {
		value := format(f.Format, Get(obj, f.Path))
		if value == "" {
			continue
		}
		labels = append(labels, f.Label)
		values = append(values, value)
	}

	labelWidth := 0
	for _, l := range labels {
		labelWidth = max(labelWidth, len([]rune(l)))
	}

	lines := make([]string, 0, len(labels))
	for i, label := range labels {
		lines = append(lines, style.apply(labelTone, pad(label, labelWidth+2))+values[i])
	}
	return lines
}

func columnWidth(lines []string) int {
	width := 0
	for _, l := range lines {
		width = max(width, visibleLen(l))
	}
	return width
}

func pad(text string, width int) string {
	if n := width - visibleLen(text); n > 0 {
		return text + strings.Repeat(" ", n)
	}
	return text
}

// visibleLen counts runes, ignoring ANSI escape sequences so a styled label
// still lines up with an unstyled one.
func visibleLen(text string) int {
	n, escaped := 0, false
	for _, r := range text {
		switch {
		case escaped && r == 'm':
			escaped = false
		case escaped:
		case r == 0x1b:
			escaped = true
		default:
			n++
		}
	}
	return n
}

// Detail writes a record as plain text, for the CLI.
func Detail(w io.Writer, h Headline, fields []Field, obj map[string]any) {
	for _, line := range DetailLines(h, fields, obj, 0, nil) {
		_, _ = io.WriteString(w, line+"\n")
	}
}

// A record is rendered by a Renderer. Most resources want the default one —
// a headline over two columns of label/value pairs — but a document is not a
// bag of fields: it has line items, payments and a total, and showing them is
// the difference between describing an invoice and showing one.
//
// The pieces below are the vocabulary a resource composes its own view from.
// A declarative section type was the alternative and came out longer than the
// two layouts that would have used it.

// Renderer turns a record into lines. width is the space available; zero or
// less means "as wide as the content needs", which is what a pipe gets.
type Renderer func(obj map[string]any, width int, style Styler) []string

// Pairs is the default renderer: an optional headline over two columns.
func Pairs(headline func(map[string]any) Headline, fields []Field) Renderer {
	return func(obj map[string]any, width int, style Styler) []string {
		var h Headline
		if headline != nil {
			h = headline(obj)
		}
		return DetailLines(h, fields, obj, width, style)
	}
}

// Rule is a horizontal divider.
func Rule(width int, style Styler) string {
	return " " + style.apply(labelTone, strings.Repeat("─", max(1, width-1)))
}

// Rows tabulates a repeated sub-record — an invoice's line items, a record's
// payments. It returns nothing when there are none, so an invoice with no
// payments does not print an empty heading.
func Rows(cols []Column, rows []map[string]any, style Styler) []string {
	if len(rows) == 0 {
		return nil
	}

	// A header row of empty names is a blank line, not a header. Payments are
	// self-evident — a date, a method and an amount — and captioning each one
	// costs a line the pane does not have.
	cells := make([][]string, 0, len(rows)+1)
	header := make([]string, len(cols))
	named := false
	for i, c := range cols {
		header[i] = strings.ToUpper(c.Header)
		named = named || c.Header != ""
	}
	if named {
		cells = append(cells, header)
	}
	for _, row := range rows {
		line := make([]string, len(cols))
		for i, c := range cols {
			line[i] = format(c.Format, Get(row, c.Path))
		}
		cells = append(cells, line)
	}

	widths := make([]int, len(cols))
	for _, line := range cells {
		for i, cell := range line {
			widths[i] = max(widths[i], visibleLen(cell))
		}
	}

	out := make([]string, 0, len(cells))
	for n, line := range cells {
		parts := make([]string, len(cols))
		for i, cell := range line {
			if cols[i].Right {
				parts[i] = padLeft(cell, widths[i])
				continue
			}
			parts[i] = pad(cell, widths[i])
		}
		text := " " + strings.Join(parts, "  ")
		if named && n == 0 {
			text = style.apply(labelTone, text)
		}
		out = append(out, text)
	}
	return out
}

// Values renders label/value pairs in one column. A field marked Right has its
// value pushed to the block's right edge, which is what stacks decimal points.
func Values(fields []Field, obj map[string]any, style Styler) []string {
	labels := make([]string, 0, len(fields))
	values := make([]string, 0, len(fields))
	right := make([]bool, 0, len(fields))
	for _, f := range fields {
		value := format(f.Format, Get(obj, f.Path))
		if value == "" {
			continue
		}
		labels = append(labels, f.Label)
		values = append(values, value)
		right = append(right, f.Right)
	}

	labelWidth, valueWidth := 0, 0
	for i := range labels {
		labelWidth = max(labelWidth, visibleLen(labels[i]))
		valueWidth = max(valueWidth, visibleLen(values[i]))
	}

	lines := make([]string, 0, len(labels))
	for i, label := range labels {
		value := values[i]
		if right[i] {
			value = padLeft(value, valueWidth)
		}
		lines = append(lines, style.apply(labelTone, " "+pad(label, labelWidth+2))+value)
	}
	return lines
}

// Caption is a section heading.
func Caption(text string, style Styler) string {
	return style.apply(labelTone, " "+strings.ToUpper(text))
}

// SideBySide puts two blocks next to each other, the right one starting at
// column at. The taller decides the height.
func SideBySide(left, right []string, at int) []string {
	out := make([]string, 0, max(len(left), len(right)))
	for i := range max(len(left), len(right)) {
		line := ""
		if i < len(left) {
			line = left[i]
		}
		if i < len(right) {
			line = pad(line, at) + right[i]
		}
		out = append(out, strings.TrimRight(line, " "))
	}
	return out
}

// Inline joins a few values onto one line, for the references nobody reads
// until they need one.
func Inline(fields []Field, obj map[string]any, style Styler) string {
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		value := format(f.Format, Get(obj, f.Path))
		if value == "" {
			continue
		}
		parts = append(parts, style.apply(labelTone, f.Label+" ")+value)
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, style.apply(labelTone, "  ·  "))
}

// List returns a repeated sub-record from a row, e.g. "InvoiceItem".
func List(obj map[string]any, key string) []map[string]any {
	raw, _ := obj[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func padLeft(text string, width int) string {
	if n := width - visibleLen(text); n > 0 {
		return strings.Repeat(" ", n) + text
	}
	return text
}
