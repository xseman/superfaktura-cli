package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/xseman/superfaktura-cli/internal/render"
)

// Layout: a header naming the account and the remaining quota, the list, the
// detail of whatever is selected, and a footer of available keys. The detail
// is always on rather than a separate screen — it costs nothing to render,
// because the row already holds it.

// The palette follows GitHub Copilot's terminal UI: a single dark teal accent,
// otherwise the terminal's own foreground. Few colors, high contrast — a dim
// grey for everything secondary reads as broken on half the themes in use.
var (
	colAccent = lipgloss.Color("#0e5c6b")
	colPaper  = lipgloss.Color("#ffffff")
	colDim    = lipgloss.Color("244")
	colRule   = lipgloss.Color("240")
	colBad    = lipgloss.Color("#b23a3a")
	colGood   = lipgloss.Color("#2e8b3d")
	colAmber  = lipgloss.Color("#c8871a")
)

var (
	// The active tab is filled; the others are plain readable text rather than
	// dimmed, which is what keeps the contrast where the screenshot has it.
	styleTabOn  = lipgloss.NewStyle().Bold(true).Foreground(colPaper).Background(colAccent).Padding(0, 1)
	styleTabOff = lipgloss.NewStyle().Padding(0, 1)

	styleMuted    = lipgloss.NewStyle().Foreground(colDim)
	styleKey      = lipgloss.NewStyle().Bold(true)
	styleWarn     = lipgloss.NewStyle().Foreground(colAmber)
	styleErr      = lipgloss.NewStyle().Foreground(colBad)
	styleReadOnly = lipgloss.NewStyle().Bold(true).Foreground(colAmber)
	// A filter value is highlighted rather than colored, so it reads as a
	// chip against the label beside it.
	styleChip = lipgloss.NewStyle().Foreground(colPaper).Background(colAccent).Padding(0, 1)
	// styleRequired marks a field the form will not submit without.
	styleRequired = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
)

// toneStyle colors a headline fragment. Tones come from render, so the CLI and
// the browser agree on what is good news without render knowing about lipgloss.
func toneStyle(tone render.Tone, text string) string {
	if text == "" {
		return ""
	}
	switch tone {
	case render.ToneGood:
		return lipgloss.NewStyle().Bold(true).Foreground(colGood).Render(text)
	case render.ToneBad:
		return lipgloss.NewStyle().Bold(true).Foreground(colBad).Render(text)
	case render.ToneWarn:
		return lipgloss.NewStyle().Bold(true).Foreground(colAmber).Render(text)
	default:
		return styleMuted.Render(text)
	}
}

// quotaWarnRatio matches the CLI's: the last tenth of the day is worth
// flagging while there is still room to act.
const quotaWarnRatio = 10

// resize recomputes what depends on the terminal size.
func (m *appModel) resize() {
	if m.width == 0 {
		return
	}
	m.applyFilter()
	m.syncDetail()
}

// Layout arithmetic. The view has to come to exactly the terminal height: one
// line too many and the top scrolls away, which silently detaches the detail
// pane from the row the cursor is on.
// Each band is followed by a blank line. Stacked directly on top of one
// another the tabs, the filter and the column headers read as one dense block
// with no telling where one ends and the next begins.
const (
	headerLines = 2
	filterLines = 2
	footerLines = 3
)

// detailHeight is what the detail pane gets.
//
// It depends only on the terminal, never on how many fields the current
// resource happens to have: a height that changed with the tab made the whole
// layout jump on every switch.
func (m appModel) detailHeight() int {
	return max(4, min(14, m.height/3))
}

func (m appModel) listHeight() int {
	return max(3, m.height-headerLines-filterLines-footerLines-m.detailHeight())
}

// formView hands the screen to huh. A form is a modal thing; sharing the
// terminal with a table underneath would only invite mis-aimed keystrokes.
func (m appModel) formView() string {
	lines := []string{m.header(), ""}
	lines = append(lines, strings.Split(m.form.View(), "\n")...)
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for len(lines) < m.height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// expandedView hides the table so a long record can be read whole.
func (m appModel) expandedView() string {
	lines := []string{m.header(), ""}
	lines = append(lines, m.detailLines()...)

	// Push the footer to the bottom. A short record left it floating in the
	// middle of the screen, in a different place on every row.
	body := strings.Split(m.footer(), "\n")
	for len(lines)+len(body) < m.height {
		lines = append(lines, "")
	}
	lines = append(lines, body...)

	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	return strings.Join(lines, "\n")
}

func (m appModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}

	if m.mode == modeForm && m.form != nil {
		return m.formView()
	}
	if m.expanded {
		return m.expandedView()
	}

	lines := make([]string, 0, m.height)
	lines = append(lines, m.header(), "")
	lines = append(lines, m.filterLine(), "")
	lines = append(lines, m.listLines(m.listHeight())...)
	lines = append(lines, m.detailLines()...)
	lines = append(lines, "")
	lines = append(lines, strings.Split(m.footer(), "\n")...)

	// Trim rather than let the terminal scroll: an overflowing frame pushes
	// the header and the selected row off the top.
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	return strings.Join(lines, "\n")
}

func (m appModel) header() string {
	tabs := make([]string, 0, len(m.cfg.Resources))
	for i, r := range m.cfg.Resources {
		if i == m.resource {
			tabs = append(tabs, styleTabOn.Render(r.Title))
			continue
		}
		tabs = append(tabs, styleTabOff.Render(r.Title))
	}

	left := strings.Join(tabs, "")
	if m.cfg.ReadOnly {
		left += "  " + styleReadOnly.Render("read-only")
	}

	right := m.quota()
	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		return left
	}
	return left + strings.Repeat(" ", pad) + right
}

// filterLine sits above the list and shows both filters: the scope, which is a
// server-side narrowing that costs a request, and the text, which only narrows
// what is already loaded.
//
// The line is always drawn, even when empty. Appearing and disappearing would
// shift the list by one row, which is the jump this layout keeps fixing.
func (m appModel) filterLine() string {
	// While typing, the input takes the line — that is where the result will
	// appear, so that is where the question belongs.
	if m.mode == modeInput && m.pending == nil {
		return " " + m.input.View()
	}

	var parts []string
	if label := m.scopeLabel(); label != "" {
		parts = append(parts, styleMuted.Render("filter: ")+styleChip.Render(label))
	}
	if label := m.periodLabel(); label != "" {
		parts = append(parts, styleMuted.Render("period: ")+styleChip.Render(label))
	}
	if m.filter != "" {
		parts = append(parts, styleMuted.Render("on this page: ")+styleChip.Render(m.filter))
	}
	if label, age := m.freshness(); age != "" {
		// Always, not only on a cache hit. Showing an accounting figure without
		// saying how old it is, is the failure the disk cache avoids by refusing
		// to hold documents at all; this one holds them and admits it. It also
		// gives the band something true to say on a resource with no filters,
		// where it was otherwise blank.
		tone := styleMuted
		if m.fromCache {
			tone = styleWarn
		}
		parts = append(parts, styleMuted.Render(label+" ")+tone.Render(age))
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, styleMuted.Render("   "))
}

// freshness says how old the data on screen is, and whether a request was made
// for it. "cached" and "fetched" are the same fact from the reader's side — how
// far behind the server this might be — but the distinction says whether the
// quota moved.
//
// The age is computed at render time rather than stored. Nothing here polls, so
// a stored string would be a timestamp frozen at the last keystroke and slowly
// become a lie; computed, it is right every time the frame is drawn.
func (m appModel) freshness() (label, age string) {
	// Mid-fetch with nothing loaded, the previous age describes nothing that is
	// on screen.
	if m.servedAt.IsZero() || (m.loading && len(m.filtered) == 0) {
		return "", ""
	}
	if m.fromCache {
		label = "cached"
	} else {
		label = "fetched"
	}
	return label, since(m.now().Sub(m.servedAt))
}

// since renders an age coarsely — the exact second stops mattering quickly.
func since(age time.Duration) string {
	switch {
	case age < time.Second:
		return "just now"
	case age < time.Minute:
		return fmt.Sprintf("%ds ago", int(age.Seconds()))
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	}
}

// quota is in the header because it is the resource this tool spends and the
// one the user cannot otherwise see running out.
func (m appModel) quota() string {
	if m.cfg.Quota == nil {
		return ""
	}
	q := m.cfg.Quota()
	if q.Limit == 0 {
		return ""
	}
	text := fmt.Sprintf("%d/%d", q.Remaining, q.Limit)
	if m.quotaLow() {
		return styleWarn.Render(text)
	}
	return styleMuted.Render(text)
}

// quotaLow reports whether the day's allowance is nearly spent.
func (m appModel) quotaLow() bool {
	if m.cfg.Quota == nil {
		return false
	}
	q := m.cfg.Quota()
	return q.Limit > 0 && q.Remaining*quotaWarnRatio <= q.Limit
}

// detailBody is the selected record as text, rendered from the row already in
// hand. The API returns complete records in a list, so this costs no request.
func (m appModel) detailBody() string {
	row := m.selected()
	if row == nil {
		// "No records." is a claim about the account, and while a fetch is in
		// flight it is not one we can make. The list is already saying what is
		// happening; this pane stays quiet rather than contradicting it.
		if m.loading {
			return ""
		}
		return styleMuted.Render("  No records.")
	}

	show := m.current().Detail
	if show == nil {
		return ""
	}
	// The same layout the CLI prints, with color added. Sharing it is what
	// stops `sf invoice view` and this pane describing one record two ways.
	return strings.Join(show(row, m.width, toneStyle), "\n")
}

// syncDetail refills the viewport. Records run to seventeen fields and a third
// of a terminal holds far fewer, so the pane scrolls rather than truncating in
// silence.
func (m *appModel) syncDetail() {
	body := m.detailBody()
	height := m.detailHeight() - 1 // the separator line
	if m.expanded {
		height = max(1, m.height-headerLines-footerLines-1)
	}
	m.detail.Width = m.width
	m.detail.Height = max(1, height)
	m.detail.SetContent(body)
	m.detail.GotoTop()
}

// detailLines is the pane as it appears, separator included.
//
// A record with a headline draws its own rule under it, so this one would be
// the second horizontal line in three — the hint keeps its place and the rule
// is dropped rather than repeated.
func (m appModel) detailLines() []string {
	hint := m.detailScrollHint()
	separator := styleMuted.Render(strings.Repeat("─", max(0, m.width-14))) + " " + hint
	if m.current().Detail != nil && m.selected() != nil {
		separator = strings.Repeat(" ", max(0, m.width-lipgloss.Width(hint)-1)) + hint
	}

	lines := []string{separator}
	body := strings.Split(m.detail.View(), "\n")

	// With rows on screen the notice sits under the selected one, inside the
	// table. Only when there is no row to sit under — an empty account, a
	// standalone create — does it fall back to here.
	if notice := m.noticeLines(); len(notice) > 0 && len(m.filtered) == 0 {
		lines = append(lines, notice...)
		body = body[:max(0, len(body)-len(notice))]
	}
	return append(lines, body...)
}

// noticeLines is whatever the browser needs to say about the selected row: a
// refused write, a question, or a prompt for one value.
//
// All three belong here, directly under the row, because all three are about
// that row. In the footer they were as far from it as the frame allows —
// "Data validation error" on the last line leaves the reader to work out which
// record it meant, and the prompt for a payment amount was not drawn at all,
// so the browser sat waiting while the user typed into nothing.
//
// The space comes off the top of the record rather than being added: the frame
// has to come to exactly the terminal height, so growing here would push the
// footer off the bottom.
func (m appModel) noticeLines() []string {
	line := func(style lipgloss.Style, text string) []string {
		wrapped := wrapText(text, max(10, m.width-3))
		for i, l := range wrapped {
			wrapped[i] = " " + style.Render(l)
		}
		return wrapped
	}

	switch {
	case m.err != "":
		return line(styleErr, m.err)
	case m.mode == modeConfirm:
		return line(styleWarn, m.status)
	case m.mode == modeInput && m.pending != nil:
		return []string{" " + m.input.View()}
	}
	return nil
}

// wrapText breaks a message on spaces to fit a width. The API returns several
// field errors joined into one sentence, which is well past a terminal width.
//
// No line may come back wider than the width asked for, and width is counted
// in runes: the messages arrive in Slovak, and a byte count says "Dátum
// splatnosti nemôže" is over the edge when it is not. A line that really is
// over the edge is worse than a wrong break — the terminal wraps it itself,
// which adds a row the frame did not budget for and pushes the footer off the
// bottom of the screen.
func wrapText(text string, width int) []string {
	if width < 1 {
		width = 1
	}

	var lines []string
	line := ""
	flush := func() {
		if line != "" {
			lines = append(lines, line)
			line = ""
		}
	}

	for _, word := range strings.Fields(text) {
		// A token with no space in it — an identifier, a URL, a base64
		// checksum — has nothing to break on, so break it anyway.
		for runeWidth(word) > width {
			flush()
			runes := []rune(word)
			lines = append(lines, string(runes[:width]))
			word = string(runes[width:])
		}
		switch {
		case line == "":
			line = word
		case runeWidth(line)+1+runeWidth(word) <= width:
			line += " " + word
		default:
			flush()
			line = word
		}
	}
	flush()

	// Three lines is enough to name the fields; the rest is usually the same
	// message in another language.
	if len(lines) > 3 {
		lines = lines[:3:3]
		lines[2] = withEllipsis(lines[2], width)
	}
	return lines
}

func runeWidth(s string) int { return utf8.RuneCountInString(s) }

// withEllipsis marks a line as cut short without letting the mark push it over
// the width. On a very narrow frame the ellipsis is all that is left.
func withEllipsis(line string, width int) string {
	for runeWidth(line)+2 > width && line != "" {
		runes := []rune(line)
		line = strings.TrimRight(string(runes[:len(runes)-1]), " ")
	}
	if line == "" {
		return "…"
	}
	return line + " …"
}

// detailScrollHint says there is more below, which silent truncation did not.
func (m appModel) detailScrollHint() string {
	// Nothing to expand yet, so do not offer it.
	if m.loading && len(m.filtered) == 0 {
		return ""
	}
	if m.detail.TotalLineCount() <= m.detail.Height {
		if m.expanded {
			return styleMuted.Render("esc back")
		}
		return styleMuted.Render("enter expand")
	}
	return styleWarn.Render(fmt.Sprintf("%d%% ↕ ctrl+u/d", int(m.detail.ScrollPercent()*100)))
}

// detailText is the pane as one string, for tests.
func (m appModel) detailText() string { return strings.Join(m.detailLines(), "\n") }

func (m appModel) footer() string {
	line := m.status
	switch {
	// Everything about the selected row is said under it, not here as well.
	// The footer offers the keys that answer it.
	case m.err != "":
		line = styleMuted.Render("esc dismiss · r refresh")
	case m.mode == modeConfirm:
		line = styleKey.Render("y") + styleMuted.Render(" confirm · ") +
			styleKey.Render("n") + styleMuted.Render(" or ") +
			styleKey.Render("esc") + styleMuted.Render(" cancel")
	case m.mode == modeInput && m.pending != nil:
		line = styleKey.Render("enter") + styleMuted.Render(" send · ") +
			styleKey.Render("esc") + styleMuted.Render(" cancel")

	// Only when the list is not already showing one. A fetch with no rows yet
	// puts the indicator in the middle of the list, where the eye is; an
	// action over rows already on screen — a download, say — leaves the list
	// alone, so the footer is the only place left to say something.
	case m.loading && len(m.filtered) > 0:
		line = m.spin.View() + " " + styleMuted.Render(m.status+"…")
	case m.loading:
		line = ""
	default:
		line = styleMuted.Render(line)
	}

	type binding struct{ key, label string }

	// An open record is a mode of its own: the table is gone, so the keys that
	// act on it are not offered.
	if m.expanded {
		return " " + line + "\n" + " " +
			styleKey.Render("esc") + " back" + styleMuted.Render(" · ") +
			styleKey.Render("ctrl+u/d") + " scroll" + styleMuted.Render(" · ") +
			styleKey.Render("q") + " quit"
	}

	// The resource's own actions come first. What survives truncation should be
	// what a user cannot guess: arrows scroll and enter opens in every terminal
	// program ever written, but nothing suggests that "i" adds a line item.
	var bindings []binding
	for _, action := range m.current().Actions {
		if action.Writes && m.cfg.ReadOnly {
			continue
		}
		bindings = append(bindings, binding{action.Key, action.Label})
	}
	bindings = append(bindings,
		binding{"↑/↓", "navigate"}, binding{"/", "filter"},
		binding{"enter", "expand"})
	if len(m.current().Scopes) > 1 {
		bindings = append(bindings, binding{"f", "filter: " + strings.ToLower(m.scopeLabel())})
	}
	if len(m.current().Periods) > 1 {
		bindings = append(bindings, binding{"t", "period: " + strings.ToLower(m.periodLabel())})
	}

	// Fit what the terminal has. Dropping the tail beats wrapping onto a second
	// line, which would shift everything above it.
	//
	// The way out is reserved before anything else competes for the room. An
	// earlier version simply filled left to right, and putting the resource's
	// actions first — so the keys nobody can guess survive — pushed refresh and
	// quit off an 80-column terminal entirely. On the browser's main surface
	// there was then no on-screen way to learn how to get fresh data or leave.
	// r and q are pinned. Refresh is the only way to get fresh data — nothing
	// polls, and the freshness band says how old the screen is without saying
	// how to change that — and quit is the way out. Both appear nowhere else,
	// so truncating either leaves the user with no on-screen route to it.
	always := []binding{{"r", "refresh"}, {"q", "quit"}}
	tail := make([]string, 0, len(always))
	for _, b := range always {
		tail = append(tail, styleKey.Render(b.key)+" "+b.label)
	}
	ellipsis := styleMuted.Render("…")

	const separator = 3 // " · "
	budget := m.width - 1
	for _, entry := range tail {
		budget -= lipgloss.Width(entry) + separator
	}

	rendered := make([]string, 0, len(bindings)+2)
	used := 0
	for i, b := range bindings {
		entry := styleKey.Render(b.key) + " " + b.label
		width := lipgloss.Width(entry) + separator
		// The ellipsis has to fit too, or the one mark that says "there is more"
		// is itself the thing clipped off the end.
		room := budget
		if i < len(bindings)-1 {
			room -= lipgloss.Width(ellipsis) + separator
		}
		if used+width > room && len(rendered) > 0 {
			rendered = append(rendered, ellipsis)
			break
		}
		used += width
		rendered = append(rendered, entry)
	}
	rendered = append(rendered, tail...)

	return " " + line + "\n" + " " + strings.Join(rendered, styleMuted.Render(" · "))
}
