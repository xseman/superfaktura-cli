package tui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/xseman/superfaktura-cli/internal/render"
)

// The list is rendered here rather than by bubbles/table, which removed two
// things that bit earlier: a column count that had to match rows from the
// previous resource, and a cursor that starts at -1 and stays there until
// moved.
//
// Every row is exactly one line, selected or not. Framing the cursor with a
// rule above and below made it three lines tall, so the rows below shifted
// every time the cursor moved — the list appeared to jump as you walked it.
// Inverting is the same information without the movement.

// listLines renders the visible window: a header, then the rows, with the
// cursor framed.
func (m appModel) listLines(height int) []string {
	cols := m.current().Columns
	widths := m.columnWidths()

	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = pad(strings.ToUpper(c.Header), widths[i])
	}
	rule := styleRule.Render(strings.Repeat("─", max(1, m.width)))
	lines := []string{
		styleColumn.Render(" " + strings.Join(header, "  ")),
		rule,
	}

	// The rows are the part that is missing, so the indicator belongs in their
	// place rather than over the whole frame. The columns are a property of the
	// resource, not of the response — they are already known, and blanking them
	// throws away the one piece of the table that never had to wait.
	if m.loading && len(m.filtered) == 0 {
		return append(lines, m.loadingLines(height-len(lines))...)
	}

	if len(m.filtered) == 0 {
		lines = append(lines, styleMuted.Render("  Nothing here."))
		return fit(lines, height)
	}

	body := make([]string, 0, len(m.filtered))
	for i, row := range m.filtered {
		cells := make([]string, len(cols))
		for j, c := range cols {
			cells[j] = pad(cellOf(c, row), widths[j])
		}
		text := " " + strings.Join(cells, "  ")

		if i == m.cursor {
			// Pad first: the inversion should read as a bar across the list,
			// not as a highlight that stops where the text does.
			body = append(body, styleSelected.Render(pad(text, m.width)))
			continue
		}
		body = append(body, styleRow.Render(text))
	}

	// A question or a failure goes immediately under the row it is about, not
	// under the table. The list is padded to a fixed height, so at the bottom
	// of that block it can be twenty blank lines away from the record it names
	// — far enough to have to look up which row is highlighted before
	// answering "Delete expense 2026002?".
	if notice := m.noticeLines(); len(notice) > 0 {
		body = slices.Insert(body, min(m.cursor+1, len(body)), notice...)
	}

	// Always fill the height. A short list that returned short would let the
	// detail pane and footer float up the screen.
	return fit(append(lines, window(body, m.cursor, height-len(lines))...), height)
}

// loadingLines gives the whole list area to the indicator.
//
// It belongs here rather than on the status line: the eye is on the region
// that is about to change, and a glyph in the last row of the terminal is not
// where anyone is looking. On a fast account the wait is a fraction of a
// second, so the indicator has to be somewhere it can be caught.
//
// No artificial delay and no minimum duration. Both were tempting — a 150ms
// flash reads as a flicker — but a browser whose whole argument is speed
// should not be made to feel slower to prove it was working. What made the
// old transition feel like flicker was the layout moving underneath it, and
// that is fixed by reserving the same height either way.
func (m appModel) loadingLines(height int) []string {
	label := m.spin.View() + " " + styleMuted.Render(m.status+"…")

	lines := make([]string, 0, height)
	above := max(0, (height-1)/2)
	for range above {
		lines = append(lines, "")
	}
	lines = append(lines, center(label, m.width))
	return fit(lines, height)
}

// center pads a rendered string to sit in the middle of the given width.
// lipgloss.Width, not len: the label carries escape sequences.
func center(text string, width int) string {
	indent := max(0, (width-lipgloss.Width(text))/2)
	return strings.Repeat(" ", indent) + text
}

// window returns at most height lines, keeping the focus line in view.
func window(lines []string, focus, height int) []string {
	if height <= 0 || len(lines) == 0 {
		return nil
	}
	if len(lines) <= height {
		return lines
	}

	start := focus - height/2
	start = max(0, min(start, len(lines)-height))
	return lines[start : start+height]
}

// fit pads or trims to an exact height so the panes below never move.
func fit(lines []string, height int) []string {
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:max(0, height)]
}

func pad(text string, width int) string {
	runes := []rune(text)
	if len(runes) > width {
		if width <= 1 {
			return string(runes[:max(0, width)])
		}
		return string(runes[:width-1]) + "…"
	}
	return text + strings.Repeat(" ", width-len(runes))
}

func cellOf(c render.Column, row map[string]any) string {
	value := render.Get(row, c.Path)
	if c.Format != nil {
		return c.Format(value)
	}
	return render.Text(value)
}

// columnWidths sizes each column to its widest value on screen, then gives
// back whatever the terminal cannot fit, taking it from the widest column
// first — that is the free-text one, and it survives truncation best.
//
// With no rows the only measurable thing is the headers, which come out far
// narrower than the loaded table and make the columns jump sideways the moment
// data lands. So the widths from the last successful load of this resource are
// reused while a fetch is in flight. Switching away and back — the common case
// — then draws the headers exactly where they will stay. A first visit has
// nothing to reuse and still shifts once.
func (m appModel) columnWidths() []int {
	cols := m.current().Columns
	if len(m.filtered) == 0 {
		if cached, ok := m.widths[m.resource]; ok && len(cached) == len(cols) && m.fits(cached) {
			return cached
		}
	}

	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = len([]rune(c.Header))
	}
	for _, row := range m.filtered {
		for i, c := range cols {
			widths[i] = max(widths[i], len([]rune(cellOf(c, row))))
		}
	}

	const gap = 2
	total := 1
	for _, w := range widths {
		total += w + gap
	}
	for total > m.width {
		widest := 0
		for i, w := range widths {
			if w > widths[widest] {
				widest = i
			}
		}
		if widths[widest] <= 6 {
			break
		}
		widths[widest]--
		total--
	}
	return widths
}

// fits reports whether a remembered set of widths still spans the terminal.
// It may not: the window can be resized while another resource is on screen.
func (m appModel) fits(widths []int) bool {
	total := 1
	for _, w := range widths {
		total += w + 2
	}
	return total <= m.width
}

// rememberWidths records what the loaded table measured, so the next fetch of
// this resource can draw its headers in their final position.
func (m *appModel) rememberWidths() {
	if len(m.filtered) == 0 {
		return
	}
	if m.widths == nil {
		m.widths = map[int][]int{}
	}
	m.widths[m.resource] = m.columnWidths()
}

// moveCursor walks the list, clamped to its ends.
//
// Moving clears any failure on screen: it was reported against the row it was
// about, and leaving it above a different record would make it describe the
// wrong one.
func (m *appModel) moveCursor(delta int) {
	m.err = ""
	if len(m.filtered) == 0 {
		m.cursor = 0
		return
	}
	m.cursor = max(0, min(m.cursor+delta, len(m.filtered)-1))
	m.syncDetail()
}

var (
	styleColumn = lipgloss.NewStyle().Bold(true).Foreground(colDim)
	styleRow    = lipgloss.NewStyle()
	// Reverse rather than a fixed pair of colors: it inverts whatever theme
	// the terminal is set to, so the contrast is right on light and dark both.
	styleSelected = lipgloss.NewStyle().Reverse(true)
	styleRule     = lipgloss.NewStyle().Foreground(colRule)
)
