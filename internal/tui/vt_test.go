package tui_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// A terminal, enough of one to read a Bubble Tea frame back.
//
// The screen is what the test is about: a frame is written as cursor moves,
// erases and overwrites, so the bytes on the wire say nothing about what a
// person would see. This replays them onto a grid.
//
// It answers to the subset a Bubble Tea program actually emits — cursor
// movement, erase, SGR, the alternate screen — and ignores the rest rather
// than pretending to be a terminal. Anything it cannot account for is dropped,
// which is safe here because every escape the renderer sends is in the list.

type cell struct {
	r     rune
	style style
}

// style is the appearance of a cell. Kept as parsed attributes rather than as
// the escape sequence that set them: the same color arrives written several
// ways, and a golden file has to compare equal when it does.
type style struct {
	bold, faint, italic, underline, reverse bool
	fg, bg                                  string
}

func (s style) String() string {
	var parts []string
	for _, flag := range []struct {
		on   bool
		name string
	}{
		{s.bold, "bold"}, {s.faint, "faint"}, {s.italic, "italic"},
		{s.underline, "underline"}, {s.reverse, "reverse"},
	} {
		if flag.on {
			parts = append(parts, flag.name)
		}
	}
	if s.fg != "" {
		parts = append(parts, "fg="+s.fg)
	}
	if s.bg != "" {
		parts = append(parts, "bg="+s.bg)
	}
	return strings.Join(parts, " ")
}

type terminal struct {
	width, height int
	cells         [][]cell
	x, y          int
	current       style
	// partial holds an escape sequence split across two reads from the pty.
	partial []byte
}

func newTerminal(width, height int) *terminal {
	t := &terminal{width: width, height: height}
	t.clear()
	return t
}

func (t *terminal) clear() {
	t.cells = make([][]cell, t.height)
	for y := range t.cells {
		t.cells[y] = t.blankRow()
	}
	t.x, t.y = 0, 0
}

func (t *terminal) blankRow() []cell {
	row := make([]cell, t.width)
	for x := range row {
		row[x] = cell{r: ' '}
	}
	return row
}

// Write replays output onto the grid. It never fails: a terminal shown
// something it does not understand shows nothing, and so does this.
func (t *terminal) Write(p []byte) (int, error) {
	n := len(p)
	data := append(t.partial, p...) //nolint:gocritic // appendAssign: partial is deliberately consumed
	t.partial = nil

	for len(data) > 0 {
		if data[0] == 0x1b {
			consumed, complete := t.escape(data)
			if !complete {
				// The sequence is split across reads. Keep it for the next one
				// rather than painting half of it as text.
				t.partial = append([]byte(nil), data...)
				return n, nil
			}
			data = data[consumed:]
			continue
		}
		if data[0] < 0x20 || data[0] == 0x7f {
			t.control(data[0])
			data = data[1:]
			continue
		}
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size <= 1 {
			if !utf8.FullRune(data) {
				t.partial = append([]byte(nil), data...)
				return n, nil
			}
			data = data[1:]
			continue
		}
		t.put(r)
		data = data[size:]
	}
	return n, nil
}

func (t *terminal) control(b byte) {
	switch b {
	case '\r':
		t.x = 0
	case '\n':
		t.newline()
	case '\b':
		t.x = max(0, t.x-1)
	case '\t':
		t.x = min(t.width-1, (t.x/8+1)*8)
	}
}

func (t *terminal) newline() {
	t.y++
	if t.y >= t.height {
		t.y = t.height - 1
		t.cells = append(t.cells[1:], t.blankRow())
	}
}

func (t *terminal) put(r rune) {
	if t.x >= t.width {
		t.x = 0
		t.newline()
	}
	if t.y < 0 || t.y >= t.height || t.x < 0 {
		return
	}
	t.cells[t.y][t.x] = cell{r: r, style: t.current}
	t.x++
}

// escape consumes one escape sequence, reporting how many bytes it took and
// whether the whole of it had arrived.
func (t *terminal) escape(data []byte) (int, bool) {
	if len(data) < 2 {
		return 0, false
	}
	switch data[1] {
	case '[':
		return t.csi(data)
	case ']':
		return t.osc(data)
	case 'P', 'X', '^', '_': // DCS, SOS, PM, APC — string sequences, skipped whole
		return skipString(data)
	}
	// ESC with intermediates and a final byte: charset selection and the like.
	for i := 1; i < len(data); i++ {
		if data[i] >= 0x30 && data[i] <= 0x7e {
			return i + 1, true
		}
	}
	return 0, false
}

func (t *terminal) csi(data []byte) (int, bool) {
	i := 2
	for i < len(data) && data[i] >= 0x30 && data[i] <= 0x3f {
		i++
	}
	for i < len(data) && data[i] >= 0x20 && data[i] <= 0x2f {
		i++
	}
	if i >= len(data) {
		return 0, false
	}
	final := data[i]
	body := string(data[2:i])
	t.apply(body, final)
	return i + 1, true
}

func (t *terminal) osc(data []byte) (int, bool) {
	for i := 2; i < len(data); i++ {
		if data[i] == 0x07 {
			return i + 1, true
		}
		if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
			return i + 2, true
		}
	}
	return 0, false
}

func skipString(data []byte) (int, bool) {
	for i := 2; i < len(data); i++ {
		if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
			return i + 2, true
		}
	}
	return 0, false
}

func (t *terminal) apply(body string, final byte) {
	private := strings.HasPrefix(body, "?")
	params := parseParams(strings.TrimPrefix(body, "?"))
	at := func(i, fallback int) int {
		if i < len(params) && params[i] != 0 {
			return params[i]
		}
		return fallback
	}

	switch final {
	case 'A':
		t.y = max(0, t.y-at(0, 1))
	case 'B':
		t.y = min(t.height-1, t.y+at(0, 1))
	case 'C':
		t.x = min(t.width-1, t.x+at(0, 1))
	case 'D':
		t.x = max(0, t.x-at(0, 1))
	case 'E':
		t.x, t.y = 0, min(t.height-1, t.y+at(0, 1))
	case 'F':
		t.x, t.y = 0, max(0, t.y-at(0, 1))
	case 'G':
		t.x = clamp(at(0, 1)-1, 0, t.width-1)
	case 'd':
		t.y = clamp(at(0, 1)-1, 0, t.height-1)
	case 'H', 'f':
		t.y = clamp(at(0, 1)-1, 0, t.height-1)
		t.x = clamp(at(1, 1)-1, 0, t.width-1)
	case 'J':
		t.eraseDisplay(at(0, 0))
	case 'K':
		t.eraseLine(at(0, 0))
	case 'L':
		t.insertLines(at(0, 1))
	case 'M':
		t.deleteLines(at(0, 1))
	case 'm':
		t.sgr(params, body)
	case 'h', 'l':
		// The alternate screen starts empty; every other mode — cursor
		// visibility, bracketed paste, mouse reporting — changes nothing that
		// can be read off the grid.
		if private && len(params) > 0 && (params[0] == 1049 || params[0] == 47 || params[0] == 1047) {
			t.clear()
		}
	}
}

func (t *terminal) eraseLine(mode int) {
	if t.y < 0 || t.y >= t.height {
		return
	}
	from, to := t.x, t.width
	switch mode {
	case 1:
		from, to = 0, min(t.x+1, t.width)
	case 2:
		from, to = 0, t.width
	}
	for x := from; x < to; x++ {
		t.cells[t.y][x] = cell{r: ' '}
	}
}

func (t *terminal) eraseDisplay(mode int) {
	switch mode {
	case 0:
		t.eraseLine(0)
		for y := t.y + 1; y < t.height; y++ {
			t.cells[y] = t.blankRow()
		}
	case 1:
		t.eraseLine(1)
		for y := 0; y < t.y; y++ {
			t.cells[y] = t.blankRow()
		}
	default:
		for y := range t.cells {
			t.cells[y] = t.blankRow()
		}
	}
}

func (t *terminal) insertLines(n int) {
	for range n {
		if t.y >= t.height {
			return
		}
		rows := append([][]cell{}, t.cells[:t.y]...)
		rows = append(rows, t.blankRow())
		rows = append(rows, t.cells[t.y:t.height-1]...)
		t.cells = rows
	}
}

func (t *terminal) deleteLines(n int) {
	for range n {
		if t.y >= t.height {
			return
		}
		rows := append([][]cell{}, t.cells[:t.y]...)
		rows = append(rows, t.cells[t.y+1:]...)
		t.cells = append(rows, t.blankRow())
	}
}

func (t *terminal) sgr(params []int, body string) {
	if body == "" || len(params) == 0 {
		t.current = style{}
		return
	}
	for i := 0; i < len(params); i++ {
		switch p := params[i]; {
		case p == 0:
			t.current = style{}
		case p == 1:
			t.current.bold = true
		case p == 2:
			t.current.faint = true
		case p == 3:
			t.current.italic = true
		case p == 4:
			t.current.underline = true
		case p == 7:
			t.current.reverse = true
		case p == 22:
			t.current.bold, t.current.faint = false, false
		case p == 23:
			t.current.italic = false
		case p == 24:
			t.current.underline = false
		case p == 27:
			t.current.reverse = false
		case p == 39:
			t.current.fg = ""
		case p == 49:
			t.current.bg = ""
		case p >= 30 && p <= 37:
			t.current.fg = strconv.Itoa(p - 30)
		case p >= 40 && p <= 47:
			t.current.bg = strconv.Itoa(p - 40)
		case p >= 90 && p <= 97:
			t.current.fg = strconv.Itoa(p - 90 + 8)
		case p >= 100 && p <= 107:
			t.current.bg = strconv.Itoa(p - 100 + 8)
		case p == 38 || p == 48:
			color, used := extendedColour(params[i:])
			if p == 38 {
				t.current.fg = color
			} else {
				t.current.bg = color
			}
			i += used
		}
	}
}

// extendedColour reads a 256-color or 24-bit color argument, returning it and
// how many extra parameters it consumed.
func extendedColour(params []int) (string, int) {
	if len(params) < 2 {
		return "", 0
	}
	switch params[1] {
	case 5:
		if len(params) < 3 {
			return "", 1
		}
		return strconv.Itoa(params[2]), 2
	case 2:
		if len(params) < 5 {
			return "", len(params) - 1
		}
		return fmt.Sprintf("#%02x%02x%02x", params[2], params[3], params[4]), 4
	}
	return "", 1
}

func parseParams(body string) []int {
	if body == "" {
		return nil
	}
	// Sub-parameters (colon separated) are flattened: nothing here needs to
	// tell 38:5:244 from 38;5;244.
	fields := strings.FieldsFunc(body, func(r rune) bool { return r == ';' || r == ':' })
	params := make([]int, 0, len(fields))
	for _, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil {
			n = 0
		}
		params = append(params, n)
	}
	return params
}

func clamp(v, lo, hi int) int { return max(lo, min(hi, v)) }

// text is the screen as a person would read it, trailing blanks trimmed off
// each line.
func (t *terminal) text() []string {
	lines := make([]string, t.height)
	for y, row := range t.cells {
		var b strings.Builder
		for _, c := range row {
			b.WriteRune(c.r)
		}
		lines[y] = strings.TrimRight(b.String(), " ")
	}
	return lines
}

// styleRun is a stretch of one row wearing the same appearance.
type styleRun struct {
	row, from, to int
	style         style
}

// styleRuns is what plain text cannot say: which cells are inverted, which are
// dim, and how far the highlight bar reaches. The selected row being padded to
// full width *before* styling is a rule of this layout, and a golden of the
// characters alone would not notice it breaking.
func (t *terminal) styleRuns() []styleRun {
	var runs []styleRun
	for y, row := range t.cells {
		start, current := -1, style{}
		flush := func(end int) {
			if start >= 0 {
				runs = append(runs, styleRun{row: y, from: start, to: end, style: current})
			}
			start = -1
		}
		for x, c := range row {
			if c.style != current {
				flush(x)
				if (c.style != style{}) {
					start, current = x, c.style
				} else {
					current = style{}
				}
			}
		}
		flush(t.width)
	}
	return runs
}

// The emulator is what the golden frames are read through, so it gets a test
// of its own: a fault here would be regenerated into every golden file and
// then defended by them.

func TestTheTerminalReplaysWhatWasWritten(t *testing.T) {
	term := newTerminal(12, 4)

	// A frame the way Bubble Tea writes one: lines separated by CRLF, each
	// erased to the end before it is drawn.
	_, _ = term.Write([]byte("\x1b[?1049h\x1b[Hone\x1b[K\r\ntwo\x1b[K\r\nthree\x1b[K"))
	if got := term.text(); got[0] != "one" || got[1] != "two" || got[2] != "three" || got[3] != "" {
		t.Fatalf("lines = %q", got)
	}

	// A redraw: back up two lines, overwrite the first with something shorter.
	// The tail of what was there must not survive, and the lines below it must
	// not move.
	_, _ = term.Write([]byte("\x1b[2A\rz\x1b[K"))
	if got := term.text(); got[0] != "z" || got[2] != "three" {
		t.Errorf("lines = %q", got)
	}

	// The alternate screen starts empty, or the previous frame bleeds into the
	// next capture.
	_, _ = term.Write([]byte("\x1b[?1049h"))
	if got := strings.TrimSpace(strings.Join(term.text(), "")); got != "" {
		t.Errorf("the screen was not cleared: %q", got)
	}
}

func TestAppearanceIsReadOffTheEscapes(t *testing.T) {
	term := newTerminal(20, 1)
	// Bold white on the accent, then a reset and plain text: the shape every
	// lipgloss style comes out as.
	_, _ = term.Write([]byte("\x1b[1;38;2;255;255;255;48;5;23mTabs\x1b[0m plain"))

	runs := term.styleRuns()
	if len(runs) != 1 {
		t.Fatalf("%d styled runs, want 1: %+v", len(runs), runs)
	}
	got := runs[0]
	if got.from != 0 || got.to != 4 {
		t.Errorf("run covers %d-%d, want 0-4", got.from, got.to)
	}
	if want := "bold fg=#ffffff bg=23"; got.style.String() != want {
		t.Errorf("style = %q, want %q", got.style.String(), want)
	}

	// A sequence split across two reads must not be painted as text.
	term = newTerminal(20, 1)
	_, _ = term.Write([]byte("\x1b[38;5"))
	_, _ = term.Write([]byte(";244mdim"))
	if got := term.text()[0]; got != "dim" {
		t.Errorf("text = %q, want %q", got, "dim")
	}
	if runs := term.styleRuns(); len(runs) != 1 || runs[0].style.fg != "244" {
		t.Errorf("runs = %+v", runs)
	}
}

func TestAnAgeIsNormalisedInPlace(t *testing.T) {
	// "fetched 12s ago" and "fetched just now" are the same fact, and only one
	// of them can be in a golden file.
	term := newTerminal(30, 1)
	_, _ = term.Write([]byte("\x1b[38;5;244mfetched \x1b[0m\x1b[38;5;172m12s ago"))
	term.normaliseAges()

	if got := term.text()[0]; got != "fetched just now" {
		t.Fatalf("line = %q", got)
	}
	// The age keeps the appearance it had: a cached page is warned about in
	// amber, and the golden has to keep saying so.
	for _, run := range term.styleRuns() {
		if run.from == 8 && run.style.fg != "172" {
			t.Errorf("the age lost its color: %+v", run)
		}
	}
}
