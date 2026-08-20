package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/xseman/superfaktura-cli/internal/render"
)

// The model is a plain state machine, so it can be driven with messages and
// inspected without a terminal. That matters most for the quota rules: a
// browser that fetched when it should not would be invisible in a screenshot
// and obvious in a bill.

func rows(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var items []map[string]any
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return items
}

// testClock drives the cache without waiting. Every harness shares it, and
// each one resets it, so a test that never touches time sees a fixed instant.
type clock struct{ at time.Time }

func (c *clock) now() time.Time          { return c.at }
func (c *clock) advance(d time.Duration) { c.at = c.at.Add(d) }

var testClock = &clock{at: time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)}

// options wraps a fixed set for the lazy Options signature.
func options(o ...FormOption) func() []FormOption {
	return func() []FormOption { return o }
}

// lastScope records the filters the most recent load was given.
var lastScope atomic.Pointer[map[string]string]

// harness builds a model over two resources and counts every load.
func harness(t *testing.T, readOnly bool) (appModel, *atomic.Int32) {
	t.Helper()
	testClock.at = time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	var loads atomic.Int32

	invoices := rows(t, `[
	  {"Invoice":{"id":"1","invoice_no_formatted":"2026001","token":"t1"},"Client":{"name":"Acme s.r.o."}},
	  {"Invoice":{"id":"2","invoice_no_formatted":"2026002","token":"t2"},"Client":{"name":"Beta a.s."}}]`)
	clients := rows(t, `[{"Client":{"id":"7","name":"Acme s.r.o."}}]`)

	load := func(items []map[string]any) func(context.Context, int, map[string]string) (Page, error) {
		return func(_ context.Context, _ int, scope map[string]string) (Page, error) {
			loads.Add(1)
			lastScope.Store(&scope)
			return Page{Items: items, ItemCount: len(items), PageCount: 2}, nil
		}
	}

	cfg := Config{
		Account:  "test",
		ReadOnly: readOnly,
		Quota:    func() Quota { return Quota{Remaining: 900, Limit: 1000} },
		Resources: []Resource{
			{
				Title: "Invoices",
				Scopes: []Scope{
					{Label: "All"},
					{Label: "Unpaid", Params: map[string]string{"status": "1|2"}},
				},
				Columns: []render.Column{
					{Header: "ID", Path: "Invoice.id"},
					{Header: "Number", Path: "Invoice.invoice_no_formatted"},
					{Header: "Client", Path: "Client.name"},
				},
				Detail: render.Pairs(nil, []render.Field{{Label: "Number", Path: "Invoice.invoice_no_formatted"}}),
				Load:   load(invoices),
				Actions: []Action{
					{Key: "P", Label: "pdf", Run: func(context.Context, map[string]any, map[string]string) (string, bool, error) {
						return "saved", false, nil
					}},
					{Key: "d", Label: "delete", Writes: true,
						Confirm: func(row map[string]any) string {
							return "Delete " + render.Text(render.Get(row, "Invoice.invoice_no_formatted")) + "?"
						},
						Run: func(context.Context, map[string]any, map[string]string) (string, bool, error) {
							return "deleted", true, nil
						}},
				},
			},
			{
				Title:   "Clients",
				Columns: []render.Column{{Header: "ID", Path: "Client.id"}, {Header: "Name", Path: "Client.name"}},
				Detail:  render.Pairs(nil, []render.Field{{Label: "Name", Path: "Client.name"}}),
				Load:    load(clients),
			},
		},
	}

	input := textinput.New()
	m := appModel{
		ctx: context.Background(), cfg: cfg,
		input: input, page: 1,
		cache: newPageCache(testClock.now),
		clock: testClock.now,
	}

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(appModel)
	next, _ = m.Update(loadedMsg{number: 1, page: Page{Items: invoices, ItemCount: 2, PageCount: 2}})
	return next.(appModel), &loads
}

func press(t *testing.T, m appModel, keys ...string) (appModel, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		var next tea.Model
		if len(k) == 1 {
			next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		} else {
			next, cmd = m.Update(tea.KeyMsg{Type: keyTypes[k]})
		}
		m = next.(appModel)
	}
	return m, cmd
}

// runCmd executes a command, unwrapping a batch. tea.Batch does not run its
// members; it returns them for the runtime to schedule, so a test that calls
// the batch directly observes nothing happening.
func runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, sub := range msg {
			runCmd(sub)
		}
	}
}

var keyTypes = map[string]tea.KeyType{
	"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab,
	"down": tea.KeyDown, "up": tea.KeyUp,
	"left": tea.KeyLeft, "right": tea.KeyRight,
}

func TestFilteringNeverFetches(t *testing.T) {
	// The rows are already here. A filter that went to the server would spend
	// quota on something the client can do for free.
	m, loads := harness(t, false)
	before := loads.Load()

	m, _ = press(t, m, "/")
	if m.mode != modeInput {
		t.Fatal("expected the filter prompt")
	}
	m.input.SetValue("beta")
	m, cmd := press(t, m, "enter")

	if cmd != nil {
		t.Error("filtering issued a command; it should be local")
	}
	if loads.Load() != before {
		t.Errorf("%d loads, want none", loads.Load()-before)
	}
	if len(m.filtered) != 1 {
		t.Fatalf("%d rows shown, want 1", len(m.filtered))
	}
	if got := render.Text(render.Get(m.filtered[0], "Client.name")); got != "Beta a.s." {
		t.Errorf("filtered to %q", got)
	}
}

func TestEscapeClearsTheFilterBeforeQuitting(t *testing.T) {
	// Losing the whole session because a filter was still on would be a poor
	// trade for one keystroke.
	m, _ := harness(t, false)
	m.filter = "beta"
	m.applyFilter()

	m, cmd := press(t, m, "esc")
	if cmd != nil {
		t.Error("the first escape should clear the filter, not quit")
	}
	if m.filter != "" || len(m.filtered) != 2 {
		t.Errorf("filter = %q, rows = %d", m.filter, len(m.filtered))
	}
}

func TestSwitchingTabsLoadsTheOtherResource(t *testing.T) {
	m, loads := harness(t, false)
	before := loads.Load()

	m, cmd := press(t, m, "tab")
	if m.resource != 1 || m.current().Title != "Clients" {
		t.Fatalf("resource = %d", m.resource)
	}
	if cmd == nil {
		t.Fatal("switching should fetch the other resource")
	}
	runCmd(cmd) // the load is one member of a batch
	if loads.Load() != before+1 {
		t.Errorf("%d loads, want one", loads.Load()-before)
	}
	if m.page != 1 || m.filter != "" {
		t.Errorf("page = %d, filter = %q — both should reset", m.page, m.filter)
	}
}

func TestADestructiveActionAsksFirst(t *testing.T) {
	m, _ := harness(t, false)

	m, _ = press(t, m, "d")
	if m.mode != modeConfirm {
		t.Fatalf("mode = %v, want a confirmation", m.mode)
	}
	if !strings.Contains(m.status, "2026001") {
		t.Errorf("the question should name the record: %q", m.status)
	}

	// Anything other than y cancels.
	m, _ = press(t, m, "n")
	if m.mode != modeBrowse || m.status != "Canceled" {
		t.Errorf("mode = %v, status = %q", m.mode, m.status)
	}
}

func TestReadOnlyHidesWritesEntirely(t *testing.T) {
	// Not merely refused when pressed: absent from the footer, so the mode is
	// visible rather than a surprise.
	m, _ := harness(t, true)

	if m.actionFor("d") != nil {
		t.Error("a write action was reachable in read-only mode")
	}
	if m.actionFor("P") == nil {
		t.Error("a read action should still work")
	}

	footer := m.footer()
	if strings.Contains(footer, "delete") {
		t.Errorf("the footer still offers delete: %q", footer)
	}
	if !strings.Contains(m.header(), "read-only") {
		t.Error("the header should say so")
	}
}

func TestTheSelectionDrivesTheDetail(t *testing.T) {
	m, _ := harness(t, false)

	if got := render.Text(render.Get(m.selected(), "Invoice.id")); got != "1" {
		t.Fatalf("selected id = %q", got)
	}
	if !strings.Contains(m.detailText(), "2026001") {
		t.Errorf("detail = %q", m.detailText())
	}

	m, _ = press(t, m, "down")
	if got := render.Text(render.Get(m.selected(), "Invoice.id")); got != "2" {
		t.Errorf("after moving down, selected id = %q", got)
	}
	if !strings.Contains(m.detailText(), "2026002") {
		t.Errorf("the detail did not follow the selection: %q", m.detailText())
	}
}

func TestPagingStopsAtTheEnds(t *testing.T) {
	m, loads := harness(t, false)
	before := loads.Load()

	// Page 1 of 2: back is a no-op rather than a wasted request.
	m, cmd := press(t, m, "left")
	if cmd != nil {
		t.Error("paging before the first page should not fetch")
	}

	m, cmd = press(t, m, "right")
	if cmd == nil {
		t.Fatal("paging forward should fetch")
	}
	runCmd(cmd)
	if m.page != 2 || loads.Load() != before+1 {
		t.Errorf("page = %d, loads = %d", m.page, loads.Load()-before)
	}

	m, cmd = press(t, m, "right")
	if cmd != nil {
		t.Error("paging past the last page should not fetch")
	}
}

func TestQuotaIsShownAndWarnsWhenLow(t *testing.T) {
	m, _ := harness(t, false)
	if !strings.Contains(m.header(), "900/1000") {
		t.Errorf("header = %q", m.header())
	}

	if m.quotaLow() {
		t.Error("900 of 1000 is not low")
	}

	// Assert the decision, not the paint: lipgloss strips color when it is
	// not writing to a terminal, so comparing rendered strings proves nothing.
	m.cfg.Quota = func() Quota { return Quota{Remaining: 40, Limit: 1000} }
	if !m.quotaLow() {
		t.Error("40 of 1000 should warn")
	}
	if !strings.Contains(m.header(), "40/1000") {
		t.Errorf("header = %q", m.header())
	}
}

func TestAnActionThatChangesDataTriggersAReload(t *testing.T) {
	m, loads := harness(t, false)
	before := loads.Load()

	next, _ := m.Update(actedMsg{message: "deleted", reload: true})
	m = next.(appModel)
	if !m.loading {
		t.Error("a reload should be in flight")
	}

	next, cmd := m.Update(actedMsg{message: "saved", reload: false})
	m = next.(appModel)
	if cmd != nil {
		t.Error("an action that changed nothing should not refetch")
	}
	if loads.Load() != before {
		t.Errorf("%d unexpected loads", loads.Load()-before)
	}
}

func TestAFailedLoadIsReportedNotFatal(t *testing.T) {
	m, _ := harness(t, false)

	next, _ := m.Update(loadedMsg{number: 1, err: context.DeadlineExceeded})
	m = next.(appModel)

	if m.err == "" {
		t.Error("the error should be shown")
	}
	if m.loading {
		t.Error("loading should have stopped")
	}
	// The previously loaded rows stay on screen; losing them would make a
	// transient failure look like an empty account.
	if len(m.filtered) != 2 {
		t.Errorf("%d rows survived the failure, want 2", len(m.filtered))
	}
}

func TestSwitchingTabsDropsTheOldRowsImmediately(t *testing.T) {
	// Left in place, the previous resource's rows are painted into the new
	// resource's columns for one frame. That is the flicker.
	m, _ := harness(t, false)
	if len(m.filtered) != 2 {
		t.Fatalf("expected the invoice rows to start with")
	}

	m, _ = press(t, m, "tab")

	if len(m.loaded.Items) != 0 || len(m.filtered) != 0 {
		t.Errorf("%d rows survived the switch; the new tab must start empty",
			len(m.filtered))
	}
	if m.cursor > 0 {
		t.Errorf("cursor = %d, want the top", m.cursor)
	}
}

func TestTheLayoutHeightDoesNotDependOnTheResource(t *testing.T) {
	// Resources have different field counts. A detail pane sized from them
	// made the table and footer jump on every tab.
	m, _ := harness(t, false)

	invoiceDetail, invoiceTable := m.detailHeight(), m.listHeight()
	m, _ = press(t, m, "tab")
	clientDetail, clientTable := m.detailHeight(), m.listHeight()

	if invoiceDetail != clientDetail || invoiceTable != clientTable {
		t.Errorf("layout moved between tabs: detail %d→%d, table %d→%d",
			invoiceDetail, clientDetail, invoiceTable, clientTable)
	}
}

func TestSomethingIsShownWhileLoading(t *testing.T) {
	m, _ := harness(t, false)
	m, _ = press(t, m, "r")

	if !m.loading {
		t.Fatal("a refresh should mark the model as loading")
	}
	if !strings.Contains(m.footer(), "Refreshing") {
		t.Errorf("the footer should say what is happening: %q", m.footer())
	}
}

func TestTheSpinnerStopsTickingWhenIdle(t *testing.T) {
	// A browser that redraws ten times a second while doing nothing is a
	// waste of a terminal.
	m, _ := harness(t, false)
	m.loading = false

	_, cmd := m.Update(m.spin.Tick())
	if cmd != nil {
		t.Error("the spinner kept ticking with nothing in flight")
	}

	m.loading = true
	if _, cmd := m.Update(m.spin.Tick()); cmd == nil {
		t.Error("the spinner should tick while loading")
	}
}

func TestALongRecordScrollsRatherThanBeingCut(t *testing.T) {
	// Seventeen fields against a third of the screen: the overflow used to
	// vanish with no indication and no way to reach it.
	m, _ := harness(t, false)
	many := make([]render.Field, 0, 30)
	for i := range 30 {
		many = append(many, render.Field{
			Label: fmt.Sprintf("Field%02d", i), Path: "Invoice.id",
		})
	}
	m.cfg.Resources[0].Detail = render.Pairs(nil, many)
	m.resize()

	if m.detail.TotalLineCount() <= m.detail.Height {
		t.Fatal("the fixture should overflow the pane")
	}
	if !strings.Contains(m.detailScrollHint(), "↕") {
		t.Errorf("overflow should be advertised: %q", m.detailScrollHint())
	}

	top := m.detail.ScrollPercent()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = next.(appModel)
	if m.detail.ScrollPercent() <= top {
		t.Error("ctrl+d should scroll the detail")
	}
}

func TestExpandingGivesTheRecordTheWholeScreen(t *testing.T) {
	m, _ := harness(t, false)
	compact := m.detailHeight()

	m, _ = press(t, m, "enter")
	if !m.expanded {
		t.Fatal("enter should expand")
	}
	if m.detail.Height <= compact {
		t.Errorf("expanded height %d is no bigger than %d", m.detail.Height, compact)
	}
	if strings.Contains(m.View(), "TO PAY") {
		t.Error("the table should be hidden while expanded")
	}

	// Escape returns rather than quitting.
	m, cmd := press(t, m, "esc")
	if cmd != nil {
		t.Error("escape from an expanded record should not quit")
	}
	if m.expanded {
		t.Error("escape should collapse")
	}
}

func TestOneFieldIsAPromptAndSeveralAreAForm(t *testing.T) {
	// The field count is the whole rule. A separate mechanism for "a form with
	// one field" would earn nothing.
	m, _ := harness(t, false)

	var got map[string]string
	m.cfg.Resources[0].Actions = append(m.cfg.Resources[0].Actions,
		Action{Key: "a", Label: "one", Writes: true,
			Fields: []FormField{{Key: "amount", Label: "amount"}},
			Run: func(_ context.Context, _ map[string]any, values map[string]string) (string, bool, error) {
				got = values
				return "done", false, nil
			}},
		Action{Key: "b", Label: "many", Writes: true,
			Fields: []FormField{{Key: "name", Label: "name"}, {Key: "city", Label: "city"}},
			Run: func(_ context.Context, _ map[string]any, values map[string]string) (string, bool, error) {
				got = values
				return "done", false, nil
			}})

	one, _ := press(t, m, "a")
	if one.mode != modeInput {
		t.Errorf("one field should prompt inline, mode = %v", one.mode)
	}
	one.input.SetValue("42")
	one, cmd := press(t, one, "enter")
	runCmd(cmd)
	if got["amount"] != "42" {
		t.Errorf("values = %v", got)
	}

	many, _ := press(t, m, "b")
	if many.mode != modeForm || many.form == nil {
		t.Errorf("several fields should open a form, mode = %v", many.mode)
	}
}

func TestAbandoningAFormSendsNothing(t *testing.T) {
	called := false
	m, _ := harness(t, false)
	m.cfg.Resources[0].Actions = append(m.cfg.Resources[0].Actions,
		Action{Key: "b", Label: "many", Writes: true,
			Fields: []FormField{{Key: "name", Label: "name"}, {Key: "city", Label: "city"}},
			Run: func(_ context.Context, _ map[string]any, _ map[string]string) (string, bool, error) {
				called = true
				return "", false, nil
			}})

	m, _ = press(t, m, "b")
	m, _ = press(t, m, "esc")

	if called {
		t.Error("escaping a form still sent the write")
	}
	if m.mode != modeBrowse {
		t.Errorf("mode = %v, want browse", m.mode)
	}
}

func TestAStandaloneActionWorksWithNothingSelected(t *testing.T) {
	// Creating a record does not need one to be highlighted, and on an empty
	// account there is nothing to highlight.
	m, _ := harness(t, false)
	m.loaded = Page{}
	m.applyFilter()

	m.cfg.Resources[0].Actions = append(m.cfg.Resources[0].Actions,
		Action{Key: "N", Label: "new", Writes: true, Standalone: true,
			Fields: []FormField{{Key: "name", Label: "name"}, {Key: "city", Label: "city"}},
			Run: func(_ context.Context, _ map[string]any, _ map[string]string) (string, bool, error) {
				return "created", true, nil
			}})

	m, _ = press(t, m, "N")
	if m.mode != modeForm {
		t.Errorf("mode = %v, want a form despite an empty list", m.mode)
	}
	if m.err != "" {
		t.Errorf("err = %q, a standalone action needs no selection", m.err)
	}
}

func TestTheListWindowFollowsTheCursor(t *testing.T) {
	// With more rows than fit, the selected one has to stay visible or the
	// detail pane describes something off screen.
	m, _ := harness(t, false)
	many := make([]map[string]any, 0, 60)
	for i := range 60 {
		many = append(many, map[string]any{
			"Invoice": map[string]any{"id": fmt.Sprint(i), "invoice_no_formatted": fmt.Sprintf("N%03d", i)},
			"Client":  map[string]any{"name": "Acme"},
		})
	}
	m.loaded = Page{Items: many, ItemCount: 60, PageCount: 1}
	m.applyFilter()

	m.moveCursor(50)
	rendered := strings.Join(m.listLines(12), "\n")
	if !strings.Contains(rendered, "N050") {
		t.Errorf("the cursor row scrolled out of view:\n%s", rendered)
	}
}

func TestTheCursorStopsAtBothEnds(t *testing.T) {
	m, _ := harness(t, false)
	m.moveCursor(-5)
	if m.cursor != 0 {
		t.Errorf("cursor = %d at the top", m.cursor)
	}
	m.moveCursor(99)
	if m.cursor != len(m.filtered)-1 {
		t.Errorf("cursor = %d at the bottom of %d", m.cursor, len(m.filtered))
	}
}

func TestTabsShowWhichIsActiveWithoutDimmingTheRest(t *testing.T) {
	// Copilot's inactive tabs are plain readable text. Dimming them to grey is
	// what the contrast note was about.
	m, _ := harness(t, false)
	header := stripANSI(m.header())

	for _, r := range m.cfg.Resources {
		if !strings.Contains(header, r.Title) {
			t.Errorf("tab %q is missing from %q", r.Title, header)
		}
	}
	// The account name is gone from the header; the tabs start it.
	if strings.Contains(header, "test ") && strings.HasPrefix(strings.TrimSpace(header), "test") {
		t.Errorf("the account label is back in the header: %q", header)
	}
}

func TestFooterKeysAreSeparatedFromTheirLabels(t *testing.T) {
	m, _ := harness(t, false)
	footer := stripANSI(m.footer())

	for _, want := range []string{"↑/↓ navigate", "/ filter", "r refresh", "q quit"} {
		if !strings.Contains(footer, want) {
			t.Errorf("footer is missing %q:\n%s", want, footer)
		}
	}
}

// stripANSI removes styling so a test can assert on the text.
func stripANSI(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == 0x1b {
			for i < len(text) && text[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(text[i])
	}
	return b.String()
}

func TestAColumnHeaderAlwaysHasARuleUnderIt(t *testing.T) {
	// It used to appear only by accident, when the first row happened to be
	// selected and its frame landed there.
	m, _ := harness(t, false)

	for _, cursor := range []int{0, 1} {
		m.cursor = cursor
		lines := m.listLines(20)
		if len(lines) < 2 || !strings.Contains(lines[1], "─") {
			t.Errorf("cursor %d: no rule under the header:\n%s", cursor, strings.Join(lines[:3], "\n"))
		}
	}
}

func TestEveryRowIsOneLineWhicheverIsSelected(t *testing.T) {
	// Framing the cursor made its row three lines tall, so everything below it
	// shifted as the cursor moved and the list appeared to jump.
	m, _ := harness(t, false)

	heights := map[int]int{}
	for cursor := range len(m.filtered) {
		m.cursor = cursor
		lines := m.listLines(20)

		var rows int
		for _, line := range lines {
			if text := strings.TrimSpace(stripANSI(line)); text != "" && strings.Contains(text, "2026") {
				rows++
			}
		}
		heights[cursor] = rows
	}

	for cursor, rows := range heights {
		if rows != len(m.filtered) {
			t.Errorf("cursor %d shows %d rows, want %d — the list changed height",
				cursor, rows, len(m.filtered))
		}
	}
}

func TestTheRowsDoNotMoveWhenTheCursorDoes(t *testing.T) {
	// The same record must stay on the same line, or reading down the list
	// means chasing it. Trailing space is ignored: the selected row is padded
	// to the full width so its inversion spans the list, and that is the one
	// difference between the frames that is meant to be there.
	m, _ := harness(t, false)

	positions := func(cursor int) []string {
		m.cursor = cursor
		var out []string
		for _, line := range m.listLines(20) {
			out = append(out, strings.TrimRight(stripANSI(line), " "))
		}
		return out
	}

	first, second := positions(0), positions(1)
	if len(first) != len(second) {
		t.Fatalf("%d lines against %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("line %d moved when the cursor did:\n  cursor 0: %q\n  cursor 1: %q",
				i, first[i], second[i])
		}
	}
}

func TestTheSelectedRowIsInvertedAcrossTheWholeWidth(t *testing.T) {
	// A highlight that stopped where the text does would read as an artifact
	// rather than a selection, so the row is padded before it is styled.
	//
	// The inversion itself is asserted on the style: lipgloss drops styling
	// when it is not writing to a terminal, so a test can never see the escape.
	if !styleSelected.GetReverse() {
		t.Error("the selected row is not inverted")
	}

	m, _ := harness(t, false)
	m.cursor = 1

	var selected string
	for _, line := range m.listLines(20) {
		if strings.Contains(stripANSI(line), "2026002") {
			selected = stripANSI(line)
		}
	}
	if selected == "" {
		t.Fatal("the selected row is not on screen")
	}
	if width := len([]rune(selected)); width < m.width {
		t.Errorf("the row is %d columns wide, want the full %d so the bar spans the list",
			width, m.width)
	}
}

func TestTheScopeReachesTheRequest(t *testing.T) {
	// A scope is a server-side filter, so it has to arrive as request
	// parameters — otherwise it silently shows everything.
	m, _ := harness(t, false)
	lastScope.Store(nil)

	m, cmd := press(t, m, "f")
	runCmd(cmd)

	if m.scopeLabel() != "Unpaid" {
		t.Fatalf("scope = %q", m.scopeLabel())
	}
	got := lastScope.Load()
	if got == nil || (*got)["status"] != "1|2" {
		t.Errorf("the request was given %v", got)
	}
}

func TestTheScopeCyclesAndCostsARequest(t *testing.T) {
	m, loads := harness(t, false)
	before := loads.Load()

	m, cmd := press(t, m, "f")
	runCmd(cmd)
	if loads.Load() != before+1 {
		t.Errorf("%d loads, want one — a scope is server-side", loads.Load()-before)
	}

	// Two scopes in the fixture, so it wraps.
	m, cmd = press(t, m, "f")
	runCmd(cmd)
	if m.scopeLabel() != "All" {
		t.Errorf("scope = %q, want it to have wrapped", m.scopeLabel())
	}
	if m.page != 1 {
		t.Errorf("page = %d, a new scope starts at the first page", m.page)
	}
}

func TestAResourceWithoutScopesIgnoresTheKey(t *testing.T) {
	m, _ := harness(t, false)
	m, _ = press(t, m, "tab") // Clients has none
	before := m.scope

	m, cmd := press(t, m, "f")
	if cmd != nil {
		t.Error("a resource with no scopes should not fetch on f")
	}
	if m.scope != before {
		t.Errorf("scope moved to %d on a resource that has none", m.scope)
	}
}

func TestBothFiltersShowAboveTheList(t *testing.T) {
	// The scope narrows on the server, the text narrows what is loaded. Both
	// belong where the result appears, which is above the list.
	m, _ := harness(t, false)
	m.filter = "acme"

	line := stripANSI(m.filterLine())
	if !strings.Contains(line, "All") {
		t.Errorf("the scope is missing: %q", line)
	}
	if !strings.Contains(line, "acme") {
		t.Errorf("the text filter is missing: %q", line)
	}
	// Named differently, because one costs a request and the other does not.
	if !strings.Contains(line, "on this page") {
		t.Errorf("the text filter should say it is local: %q", line)
	}
}

func TestTypingAFilterTakesTheLineAboveTheList(t *testing.T) {
	m, _ := harness(t, false)
	m, _ = press(t, m, "/")

	if !strings.Contains(m.filterLine(), m.input.View()) {
		t.Errorf("the prompt is not on the filter line: %q", m.filterLine())
	}
	if strings.Contains(stripANSI(m.footer()), "›") {
		t.Errorf("the prompt is still in the footer: %q", m.footer())
	}
}

func TestTheFilterLineIsAlwaysReserved(t *testing.T) {
	// Appearing and disappearing would shift the list by a row, which is the
	// jump this layout keeps having to fix.
	m, _ := harness(t, false)

	without := len(strings.Split(m.View(), "\n"))
	m.filter = "acme"
	m.applyFilter()
	with := len(strings.Split(m.View(), "\n"))

	if without != with {
		t.Errorf("the view is %d lines without a filter and %d with", without, with)
	}
}

func TestTheViewIsExactlyTheTerminalHeight(t *testing.T) {
	// One line too many and the terminal scrolls, which pushes the header off
	// the top and detaches the detail pane from the highlighted row. One too
	// few and the footer floats. Neither is obvious in a screenshot.
	m, _ := harness(t, false)

	for _, size := range []struct{ w, h int }{{80, 24}, {120, 40}, {100, 30}, {60, 20}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		sized := next.(appModel)

		for _, view := range []struct {
			name  string
			lines []string
		}{
			{"browse", strings.Split(sized.View(), "\n")},
			{"expanded", strings.Split(sized.expandedView(), "\n")},
		} {
			if len(view.lines) != size.h {
				t.Errorf("%dx%d %s: %d lines, want %d",
					size.w, size.h, view.name, len(view.lines), size.h)
			}
		}
	}
}

func TestTheBandsAreSeparated(t *testing.T) {
	// Tabs, filter and column headers stacked directly on each other read as
	// one dense block.
	m, _ := harness(t, false)
	lines := strings.Split(m.View(), "\n")

	if strings.TrimSpace(stripANSI(lines[1])) != "" {
		t.Errorf("no gap under the tabs: %q", stripANSI(lines[1]))
	}
	if strings.TrimSpace(stripANSI(lines[3])) != "" {
		t.Errorf("no gap under the filter: %q", stripANSI(lines[3]))
	}
	if !strings.Contains(stripANSI(lines[4]), "ID") {
		t.Errorf("the column headers should follow the gap, got %q", stripANSI(lines[4]))
	}
}

func TestTheFormLandsOnSend(t *testing.T) {
	// Reaching the confirm took a keystroke to open the form and a field or two
	// of typing. Landing on Cancel asks the user to re-affirm a decision made
	// several steps ago; escape is still there for the change of mind.
	m, _ := harness(t, false)
	m.cfg.Resources[0].Actions = append(m.cfg.Resources[0].Actions,
		Action{Key: "b", Label: "many", Writes: true,
			Fields: []FormField{{Key: "name", Label: "name"}, {Key: "city", Label: "city"}},
			Run: func(context.Context, map[string]any, map[string]string) (string, bool, error) {
				return "done", false, nil
			}})

	m, _ = press(t, m, "b")
	if m.formConfirm == nil {
		t.Fatal("no confirm bound")
	}
	if !*m.formConfirm {
		t.Error("the confirm defaults to Cancel")
	}

	// And the two buttons have to be told apart. Both were near-invisible on
	// the base theme, which is worse than an unhelpful default.
	theme := FormTheme()
	on := theme.Focused.FocusedButton.GetBackground()
	off := theme.Focused.BlurredButton.GetBackground()
	if on == off {
		t.Errorf("the selected button is not distinguishable: both %v", on)
	}
}

// loadingModel is a model with a fetch in flight and nothing to show yet —
// the state every tab switch passes through.
func loadingModel(t *testing.T) appModel {
	t.Helper()
	m, _ := harness(t, false)
	m.loading = true
	m.status = "Loading clients"
	m.loaded = Page{}
	m.applyFilter()
	m.syncDetail()
	return m
}

func TestTheIndicatorIsWhereTheContentWillBe(t *testing.T) {
	// Not on the status line. The eye is on the region about to change, and a
	// glyph in the last row of the terminal is not where anyone is looking.
	m := loadingModel(t)
	lines := strings.Split(m.View(), "\n")

	list := lines[4 : 4+m.listHeight()]
	var found int
	for _, line := range list {
		if strings.Contains(stripANSI(line), "Loading clients…") {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("the indicator appears %d times in the list area, want once:\n%s",
			found, strings.Join(list, "\n"))
	}

	// And exactly once on the screen — the footer must not repeat it.
	if n := strings.Count(stripANSI(m.View()), "Loading clients…"); n != 1 {
		t.Errorf("the indicator appears %d times on screen, want once", n)
	}
}

func TestNothingClaimsTheAccountIsEmptyWhileLoading(t *testing.T) {
	// Both panes used to answer a question nobody had asked yet: the list said
	// "Nothing here." and the detail said "No records.", when the rows were
	// merely still in flight.
	screen := stripANSI(loadingModel(t).View())

	for _, lie := range []string{"Nothing here.", "No records."} {
		if strings.Contains(screen, lie) {
			t.Errorf("the screen says %q while a fetch is in flight", lie)
		}
	}

	// The detail pane also stops offering to expand a record that is not there
	// yet. The key bar still lists enter, since that legend is static.
	if hint := stripANSI(loadingModel(t).detailLines()[0]); strings.Contains(hint, "expand") {
		t.Errorf("the detail pane offers to expand nothing: %q", strings.TrimSpace(hint))
	}
}

func TestTheLayoutDoesNotMoveWhenTheRowsLand(t *testing.T) {
	// This is what made the old transition read as a flicker: column widths are
	// measured from the rows, so with none loaded the headers rendered cramped
	// and jumped wide a frame later. Reserving the same geometry either way is
	// what lets the indicator appear without an artificial delay.
	m := loadingModel(t)
	before := strings.Split(m.View(), "\n")

	next, _ := m.Update(loadedMsg{number: 1, page: Page{
		Items:     rows(t, `[{"Invoice":{"id":"1","invoice_no_formatted":"2026001"},"Client":{"name":"Acme s.r.o."}}]`),
		ItemCount: 1, PageCount: 1,
	}})
	after := strings.Split(next.(appModel).View(), "\n")

	if len(before) != len(after) {
		t.Fatalf("the frame changed height: %d lines loading, %d loaded", len(before), len(after))
	}
	// The footer is the band furthest from the change, so it is the one that
	// betrays a shift anywhere above it.
	if got, want := stripANSI(after[len(after)-1]), stripANSI(before[len(before)-1]); got != want {
		t.Errorf("the key bar moved:\n loading %q\n loaded  %q", want, got)
	}
}

func TestALateResponseDoesNotLandInTheWrongTab(t *testing.T) {
	// Walking the tabs faster than the network answers used to put one
	// resource's rows under another's columns: every cell blank, because the
	// column paths address a model the rows do not have, under a record count
	// that matched nothing on screen.
	m, _ := harness(t, false)

	// Switch to Clients; the Invoices request is still out.
	m, _ = press(t, m, "tab")
	if m.current().Title != "Clients" {
		t.Fatalf("resource = %q", m.current().Title)
	}

	late := loadedMsg{resource: 0, number: 1, scope: 0, page: Page{
		Items:     rows(t, `[{"Invoice":{"id":"1"},"Client":{"name":"Acme s.r.o."}}]`),
		ItemCount: 1, PageCount: 1,
	}}
	next, _ := m.Update(late)
	m = next.(appModel)

	if len(m.filtered) != 0 {
		t.Errorf("the Invoices response was accepted into Clients: %d rows", len(m.filtered))
	}
	if !m.loading {
		t.Error("the indicator was cleared by a response to a different request")
	}

	// The answer that was actually asked for still lands.
	next, _ = m.Update(loadedMsg{resource: 1, number: 1, scope: 0, page: Page{
		Items:     rows(t, `[{"Client":{"id":"7","name":"Acme s.r.o."}}]`),
		ItemCount: 1, PageCount: 1,
	}})
	if m = next.(appModel); len(m.filtered) != 1 || m.loading {
		t.Errorf("rows = %d, loading = %v", len(m.filtered), m.loading)
	}
}

func TestAStaleResponseIsDroppedAcrossPagesAndScopes(t *testing.T) {
	// The same race on the two other axes: paging and cycling a scope both
	// issue a request without changing the resource.
	m, _ := harness(t, false)

	for _, tc := range []struct {
		name string
		keys []string
		msg  loadedMsg
	}{
		{"page", []string{"n"}, loadedMsg{resource: 0, number: 1, scope: 0}},
		{"scope", []string{"f"}, loadedMsg{resource: 0, number: 1, scope: 0}},
	} {
		moved, _ := press(t, m, tc.keys...)
		tc.msg.page = Page{Items: rows(t, `[{"Invoice":{"id":"99"}}]`), ItemCount: 1, PageCount: 2}

		next, _ := moved.Update(tc.msg)
		if got := next.(appModel); !got.stale(tc.msg) {
			t.Errorf("%s: a response for the previous request was treated as current", tc.name)
		}
	}
}

func TestTheColumnsStayWhileTheRowsLoad(t *testing.T) {
	// The columns belong to the resource, not to the response. Blanking them
	// throws away the one part of the table that never had to wait, and leaves
	// the frame with nothing to say what is being fetched.
	m := loadingModel(t)
	lines := strings.Split(m.View(), "\n")

	header := stripANSI(lines[4])
	for _, want := range []string{"ID", "NUMBER", "CLIENT"} {
		if !strings.Contains(header, want) {
			t.Errorf("column %q is missing while loading: %q", want, header)
		}
	}
	if !strings.Contains(stripANSI(lines[5]), "─") {
		t.Errorf("the rule under the headers is missing: %q", stripANSI(lines[5]))
	}

	// And the indicator sits below them, in the space the rows will occupy.
	var at int
	for i, line := range lines {
		if strings.Contains(stripANSI(line), "Loading") {
			at = i
		}
	}
	if at <= 5 {
		t.Errorf("the indicator is at line %d, above or inside the header block", at)
	}
}

func TestTheHeadersDoNotShiftWhenReturningToATab(t *testing.T) {
	// Widths are measured from the rows, so with none loaded the headers come
	// out cramped and jump sideways the moment data lands. Reusing what the
	// resource measured last time removes that for every visit after the first.
	m, _ := harness(t, false)
	loaded := stripANSI(strings.Split(m.View(), "\n")[4])

	// Away and back, with the rows dropped as a tab switch does.
	m.loading, m.loaded = true, Page{}
	m.applyFilter()
	during := stripANSI(strings.Split(m.View(), "\n")[4])

	if during != loaded {
		t.Errorf("the headers moved between the fetch and the rows:\n during %q\n loaded %q",
			during, loaded)
	}

	// A terminal narrower than the remembered layout must fall back rather than
	// draw columns that no longer fit.
	narrow, _ := m.Update(tea.WindowSizeMsg{Width: 24, Height: 30})
	if got := narrow.(appModel).columnWidths(); m.fits(got) == false && len(got) > 0 {
		t.Log("fell back to measured widths on a narrow terminal, as intended")
	}
}

// prefilled builds a model sitting on an edit form seeded from the record.
func prefilled(t *testing.T, sent *map[string]string) appModel {
	t.Helper()
	m, _ := harness(t, false)
	m.cfg.Resources[0].Actions = append(m.cfg.Resources[0].Actions,
		Action{Key: "e", Label: "edit", Writes: true,
			Fields: []FormField{{Key: "name", Label: "name"}, {Key: "variable", Label: "variable"}},
			Prefill: func(row map[string]any) map[string]string {
				return map[string]string{
					"name":     render.Text(render.Get(row, "Client.name")),
					"variable": render.Text(render.Get(row, "Invoice.invoice_no_formatted")),
				}
			},
			Run: func(_ context.Context, _ map[string]any, values map[string]string) (string, bool, error) {
				*sent = values
				return "saved", true, nil
			}})
	m, _ = press(t, m, "e")
	if m.mode != modeForm {
		t.Fatalf("mode = %v, want a form", m.mode)
	}
	return m
}

func TestAnEditFormOpensShowingTheRecord(t *testing.T) {
	// Opening a blank form gives no way to tell what is about to be overwritten.
	var sent map[string]string
	m := prefilled(t, &sent)

	if got := *m.formValues[0]; got != "Acme s.r.o." {
		t.Errorf("name field = %q, want the record's value", got)
	}
	if got := *m.formValues[1]; got != "2026001" {
		t.Errorf("variable field = %q, want the record's value", got)
	}
}

func TestConfirmingAnUnchangedFormWritesNothing(t *testing.T) {
	// This is what makes pre-filling safe. A form that submitted every box
	// would write back values nobody touched, and a value shown one way and
	// written another would rewrite a record by merely being opened.
	var sent map[string]string
	m := prefilled(t, &sent)

	m.form.State = huh.StateCompleted
	next, cmd := m.updateForm(struct{ tea.Msg }{})
	runCmd(cmd)

	if sent != nil {
		t.Errorf("an untouched form sent %v", sent)
	}
	if got := next.(appModel).status; got != "Nothing to send" {
		t.Errorf("status = %q", got)
	}
}

func TestOnlyTheEditedFieldIsSent(t *testing.T) {
	var sent map[string]string
	m := prefilled(t, &sent)

	*m.formValues[0] = "Acme Trading s.r.o." // changed
	// formValues[1] left at the value it opened with.

	m.form.State = huh.StateCompleted
	_, cmd := m.updateForm(struct{ tea.Msg }{})
	runCmd(cmd)

	if len(sent) != 1 || sent["name"] != "Acme Trading s.r.o." {
		t.Errorf("sent %v, want only the changed name", sent)
	}
}

// The cache is worth roughly half a browsing session — walking four tabs and
// back costs ten requests, five of them for rows that were just on screen. What
// makes that safe is bounded here: a short life, an explicit way out, and no
// survival across a write.

// switchTab presses tab and runs whatever it asked for, so the load actually
// happens. press alone returns only the last command of a sequence.
func switchTab(t *testing.T, m appModel) appModel {
	t.Helper()
	m, cmd := press(t, m, "tab")
	runCmd(cmd)
	return m
}

func TestReturningToATabCostsNoRequest(t *testing.T) {
	m, loads := harness(t, false)
	before := loads.Load()

	// Away to Clients, which has never been fetched.
	m = switchTab(t, m)
	if m.current().Title != "Clients" {
		t.Fatalf("resource = %q", m.current().Title)
	}
	if got := loads.Load() - before; got != 1 {
		t.Fatalf("switching away made %d loads, want one", got)
	}

	// And back to Invoices, fetched moments ago.
	m = switchTab(t, m)
	if m.current().Title != "Invoices" {
		t.Fatalf("resource = %q", m.current().Title)
	}
	if got := loads.Load() - before; got != 1 {
		t.Errorf("%d loads in total, want one — the return should be free", got)
	}
}

func TestAPageOlderThanTheTTLIsFetchedAgain(t *testing.T) {
	m, loads := harness(t, false)
	m = switchTab(t, m)
	before := loads.Load()

	testClock.advance(pageTTL + time.Second)

	switchTab(t, m) // back to Invoices, whose entry has expired
	if loads.Load() == before {
		t.Error("a page older than the TTL was served from cache")
	}
}

func TestRefreshAlwaysGoesToTheServer(t *testing.T) {
	// The way to demand the truth, and what makes any TTL acceptable.
	m, loads := harness(t, false)
	before := loads.Load()

	_, cmd := press(t, m, "r")
	runCmd(cmd)

	if loads.Load() != before+1 {
		t.Errorf("r made %d loads, want one", loads.Load()-before)
	}
}

func TestAWriteEmptiesTheWholeCache(t *testing.T) {
	// Not merely the resource written: a payment against an invoice moves the
	// overview's totals too, and this package cannot know which figures a write
	// touched.
	m, _ := harness(t, false)
	m.cache.put(cacheKey{resource: 1, page: 1}, Page{ItemCount: 9})

	next, _ := m.Update(actedMsg{message: "saved", reload: true})
	m = next.(appModel)

	if _, _, ok := m.cache.get(cacheKey{resource: 1, page: 1}); ok {
		t.Error("another resource's page survived a write")
	}
	if _, _, ok := m.cache.get(m.key()); ok {
		t.Error("the written resource's page survived")
	}
}

func TestTheAgeOfWhatIsOnScreenIsAlwaysShown(t *testing.T) {
	// Not only after a cache hit. A band that was blank until the first cached
	// page, then grew a marker, changed shape under the reader — and a resource
	// with no filters had nothing in it at all.
	m, _ := harness(t, false)
	if got := stripANSI(m.filterLine()); !strings.Contains(got, "fetched just now") {
		t.Errorf("a freshly fetched page says %q", got)
	}

	testClock.advance(90 * time.Second)
	if got := stripANSI(m.filterLine()); !strings.Contains(got, "fetched 1m ago") {
		t.Errorf("after 90s the line says %q — the age is computed at render time", got)
	}

	// A cached page is marked apart, because that is what says the quota did
	// not move.
	fetched := testClock.at
	testClock.advance(12 * time.Second)
	next, _ := m.Update(loadedMsg{
		number: 1, cachedAt: fetched,
		page: Page{Items: m.filtered, ItemCount: 2, PageCount: 1},
	})
	if got := stripANSI(next.(appModel).filterLine()); !strings.Contains(got, "cached 12s ago") {
		t.Errorf("filter line = %q, want the page marked as cached", got)
	}
}

func TestAResourceWithNoFiltersStillFillsTheBand(t *testing.T) {
	// The Overview has neither a scope nor a period, and the band is reserved
	// either way — so it read as an unexplained gap above the table.
	m, _ := harness(t, false)
	m.cfg.Resources[0].Scopes = nil
	m.cfg.Resources[0].Periods = nil

	if got := strings.TrimSpace(stripANSI(m.filterLine())); got == "" {
		t.Error("the band is empty on a resource with no filters")
	}
}

func TestTheAgeIsNotClaimedMidFetch(t *testing.T) {
	// With nothing loaded the previous age describes nothing on screen.
	m := loadingModel(t)
	if label, age := m.freshness(); label != "" || age != "" {
		t.Errorf("mid-fetch the band claims %q %q", label, age)
	}
}

func TestAFailedFetchIsNotCached(t *testing.T) {
	m, _ := harness(t, false)
	m.cache.purge()

	next, _ := m.Update(loadedMsg{number: 1, err: context.DeadlineExceeded})
	if _, _, ok := next.(appModel).cache.get(m.key()); ok {
		t.Error("an error response was cached")
	}
}

func TestTheTwoNarrowingsAreIndependent(t *testing.T) {
	// A user wants unpaid AND this month. Folding them into one list would
	// need an entry per combination; two axes need one entry each.
	m, _ := harness(t, false)
	m.cfg.Resources[0].Periods = []Scope{
		{Label: "All time", Params: map[string]string{"created": "0"}},
		{Label: "This month", Params: map[string]string{"created": "4"}},
	}

	m, cmd := press(t, m, "f") // Unpaid
	runCmd(cmd)
	m, cmd = press(t, m, "t") // ...and this month
	runCmd(cmd)

	got := *lastScope.Load()
	if got["status"] != "1|2" || got["created"] != "4" {
		t.Errorf("params = %v, want both narrowings applied", got)
	}
	if m.scopeLabel() != "Unpaid" || m.periodLabel() != "This month" {
		t.Errorf("labels = %q / %q", m.scopeLabel(), m.periodLabel())
	}
}

func TestCyclingAnAxisDoesNotRewriteItsDefinition(t *testing.T) {
	// The Scope values are package-level. Merging one axis into the other's
	// map would edit the resource's own definition for the rest of the run.
	m, _ := harness(t, false)
	m.cfg.Resources[0].Periods = []Scope{{Label: "This month", Params: map[string]string{"created": "4"}}}

	m, cmd := press(t, m, "f")
	runCmd(cmd)

	if got := m.cfg.Resources[0].Scopes[1].Params; len(got) != 1 || got["status"] != "1|2" {
		t.Errorf("the scope definition was modified: %v", got)
	}
	if got := m.cfg.Resources[0].Periods[0].Params; len(got) != 1 {
		t.Errorf("the period definition was modified: %v", got)
	}
}

func TestTheCacheTellsThePeriodsApart(t *testing.T) {
	// Same resource, same page, same status — a different period is a
	// different question and must not be answered from the other's entry.
	m, loads := harness(t, false)
	m.cfg.Resources[0].Periods = []Scope{
		{Label: "All time", Params: map[string]string{"created": "0"}},
		{Label: "This month", Params: map[string]string{"created": "4"}},
	}
	before := loads.Load()

	m, cmd := press(t, m, "t")
	runCmd(cmd)
	if loads.Load() != before+1 {
		t.Fatalf("switching period made %d loads, want one", loads.Load()-before)
	}
	arrived, _ := m.Update(loadedMsg{number: 1, period: 1, page: Page{ItemCount: 1, PageCount: 1}})
	m = arrived.(appModel)

	// Back to the first period, which is still cached from startup.
	m, cmd = press(t, m, "t")
	runCmd(cmd)
	if got := loads.Load() - before; got != 1 {
		t.Errorf("%d loads, want one — returning to a cached period should be free", got)
	}
}

// noticeRow returns how many lines below the highlighted row the text sits.
// Zero means the line directly beneath it.
func noticeRow(t *testing.T, m appModel, text string) int {
	t.Helper()
	lines := strings.Split(m.View(), "\n")

	// Line 4 is the column header, 5 the rule, 6 the first row.
	const firstRow = 6
	for i, line := range lines {
		if strings.Contains(stripANSI(line), text) {
			return i - (firstRow + m.cursor) - 1
		}
	}
	t.Fatalf("%q is nowhere on screen:\n%s", text, stripANSI(m.View()))
	return -1
}

func TestAFailedWriteIsReportedAgainstItsRow(t *testing.T) {
	// "Data validation error" on the last line of the screen leaves the reader
	// to work out which record it was about. After an edit there is exactly
	// one, directly above.
	m, _ := harness(t, false)
	before := len(strings.Split(m.View(), "\n"))

	next, _ := m.Update(actedMsg{err: errors.New("Data validation error.")})
	m = next.(appModel)
	lines := strings.Split(m.View(), "\n")

	if len(lines) != before {
		t.Errorf("the frame changed height when the error appeared: %d → %d", before, len(lines))
	}

	// Directly beneath the highlighted row, not at the foot of the table: the
	// list is padded to a fixed height, so down there it can be twenty blank
	// lines from the record it names.
	if below := noticeRow(t, m, "Data validation error."); below != 0 {
		t.Errorf("the failure is %d lines below the row it is about, want 0", below)
	}
	if strings.Contains(stripANSI(lines[len(lines)-2]), "Data validation error.") {
		t.Error("the footer repeats the failure")
	}
}

func TestTheFailureGoesAwayWhenItStopsApplying(t *testing.T) {
	m, _ := harness(t, false)
	next, _ := m.Update(actedMsg{err: errors.New("Data validation error.")})
	m = next.(appModel)

	moved, _ := press(t, m, "down")
	if moved.err != "" {
		t.Error("the failure followed the cursor to another record")
	}

	next, _ = m.Update(actedMsg{err: errors.New("Data validation error.")})
	dismissed, cmd := press(t, next.(appModel), "esc")
	if dismissed.err != "" {
		t.Error("escape did not dismiss the failure")
	}
	if cmd != nil {
		t.Error("escape quit the browser instead of dismissing the failure")
	}
}

func TestALongFailureIsWrappedAndCapped(t *testing.T) {
	// The API joins several field errors into one sentence, well past a
	// terminal width — and the tail is usually the same message again.
	long := "client_id: Klient nepatrí pod túto firmu.; due: Dátum splatnosti " +
		"nemôže byť skôr ako dátum vystavenia; name: Toto pole je povinné; " +
		"variable: Neplatný variabilný symbol; something: else entirely"

	if got := wrapText(long, 60); len(got) > 3 {
		t.Errorf("%d lines, want at most 3", len(got))
	}
	// Width is what the terminal draws, so runes — a byte count calls this
	// Slovak sentence wider than it is, and the slack that hid was real.
	for _, line := range wrapText(long, 60) {
		if n := utf8.RuneCountInString(line); n > 60 {
			t.Errorf("line is %d wide: %q", n, line)
		}
	}
}

func TestAWordWiderThanTheFrameIsBrokenAnyway(t *testing.T) {
	// A URL or an identifier in a failure message has no space to wrap on.
	// Left whole it runs past the edge, the terminal wraps it itself, and the
	// frame is a row taller than the height it was drawn for — so the footer
	// goes off the bottom of the screen.
	for _, tc := range []struct {
		text  string
		width int
	}{
		{"see https://moja.superfaktura.sk/invoices/edit/1042?token=abcdefgh", 40},
		{strings.Repeat("x", 200), 10},
		{"ďžťňáéíóúý", 3}, // multi-byte, and narrower than its byte count
		{"a b c d e f g h i j k l m n o p q r s t u v w x y z", 2},
	} {
		for i, line := range wrapText(tc.text, tc.width) {
			if n := utf8.RuneCountInString(line); n > tc.width {
				t.Errorf("wrapText(%q, %d) line %d is %d wide: %q",
					tc.text, tc.width, i, n, line)
			}
		}
	}
}

func TestTabsAreLockedWhileARecordIsOpen(t *testing.T) {
	// The expanded view fills the screen with one record. Switching underneath
	// it would swap the account out from behind a detail that still looked like
	// the old one.
	m, _ := harness(t, false)
	m, _ = press(t, m, "enter")
	if !m.expanded {
		t.Fatal("enter did not open the record")
	}

	locked, cmd := press(t, m, "tab")
	if locked.resource != 0 {
		t.Errorf("tab switched to resource %d while a record was open", locked.resource)
	}
	if cmd != nil {
		t.Error("tab issued a request while a record was open")
	}

	// Escape closes it, and then tabs work again.
	closed, _ := press(t, locked, "esc")
	if closed.expanded {
		t.Fatal("esc did not close the record")
	}
	if switched, _ := press(t, closed, "tab"); switched.resource != 1 {
		t.Error("tabs did not come back after closing the record")
	}
}

func TestTheOpenRecordOffersOnlyItsOwnKeys(t *testing.T) {
	m, _ := harness(t, false)
	m, _ = press(t, m, "enter")

	footer := stripANSI(m.footer())
	for _, gone := range []string{"navigate", "filter", "expand", "refresh"} {
		if strings.Contains(footer, gone) {
			t.Errorf("the key bar still offers %q with the table hidden: %q", gone, footer)
		}
	}
	if !strings.Contains(footer, "esc back") {
		t.Errorf("the way out is not offered: %q", footer)
	}
}

func TestEverythingAboutARowIsSaidUnderIt(t *testing.T) {
	// A question in the footer is as far from the row it is about as the frame
	// allows, and the prompt for a single value was not drawn at all — the
	// browser sat waiting while the user typed into nothing.
	m, _ := harness(t, false)
	m.cfg.Resources[0].Actions = append(m.cfg.Resources[0].Actions,
		Action{Key: "p", Label: "payment", Writes: true,
			Fields: []FormField{{Key: "amount", Label: "amount"}},
			Run: func(context.Context, map[string]any, map[string]string) (string, bool, error) {
				return "paid", true, nil
			}})

	for _, tc := range []struct {
		name, key, want string
	}{
		{"a question", "d", "Delete 2026001?"},
		{"a prompt", "p", "amount"},
	} {
		asked, _ := press(t, m, tc.key)
		if below := noticeRow(t, asked, tc.want); below != 0 {
			t.Errorf("%s: %d lines below the row it is about, want 0", tc.name, below)
		}
		if len(strings.Split(asked.View(), "\n")) != len(strings.Split(m.View(), "\n")) {
			t.Errorf("%s: the frame changed height", tc.name)
		}
	}
}

func TestTheNoticeFollowsTheCursor(t *testing.T) {
	// It names one record, so it has to sit against that record wherever the
	// cursor is — not at a fixed place in the frame.
	m, _ := harness(t, false)

	for _, at := range []int{0, 1} {
		moved := m
		for range at {
			moved, _ = press(t, moved, "down")
		}
		asked, _ := press(t, moved, "d")
		if asked.cursor != at {
			t.Fatalf("cursor = %d, want %d", asked.cursor, at)
		}
		if below := noticeRow(t, asked, "(y/n)"); below != 0 {
			t.Errorf("cursor on row %d: the question is %d lines off", at, below)
		}
	}
}

func TestAnEnumeratedFieldIsAListNotABox(t *testing.T) {
	// A value the API only accepts from a fixed set should not be a value you
	// can mistype.
	var sent map[string]string
	m, _ := harness(t, false)
	m.cfg.Resources[0].Actions = append(m.cfg.Resources[0].Actions,
		Action{Key: "e", Label: "edit", Writes: true,
			Fields: []FormField{
				{Key: "name", Label: "name"},
				{Key: "type", Label: "type", Options: options(
					FormOption{Value: "regular", Label: "regular invoice"},
					FormOption{Value: "proforma", Label: "proforma invoice"},
				)},
			},
			Prefill: func(map[string]any) map[string]string {
				return map[string]string{"name": "X", "type": "proforma"}
			},
			Run: func(_ context.Context, _ map[string]any, v map[string]string) (string, bool, error) {
				sent = v
				return "saved", true, nil
			}})

	m, _ = press(t, m, "e")
	if got := *m.formValues[1]; got != "proforma" {
		t.Errorf("the record's own value is not selected: %q", got)
	}

	// Untouched, it sends nothing — the same rule that makes pre-filling safe.
	m.form.State = huh.StateCompleted
	next, cmd := m.updateForm(struct{ tea.Msg }{})
	runCmd(cmd)
	if sent != nil {
		t.Errorf("an untouched form sent %v", sent)
	}
	_ = next
}

func TestASelectKeepsAValueItDoesNotKnow(t *testing.T) {
	// A record carrying something the constant list does not name would
	// otherwise land on the first option, and confirming would write a change
	// nobody asked for. An unset field is the same problem with an empty value.
	for _, tc := range []struct{ current, want string }{
		{"something-else", "something-else"},
		{"", "— not set —"},
	} {
		value := tc.current
		field := selectField(FormField{Key: "type", Options: options(
			FormOption{Value: "regular", Label: "regular invoice"},
		)}, &value)

		if got := field.GetValue(); got != tc.current {
			t.Errorf("%q: selection moved to %v", tc.current, got)
		}
		if !strings.Contains(stripANSI(field.View()), tc.want) {
			t.Errorf("%q: %q is not offered:\n%s", tc.current, tc.want, stripANSI(field.View()))
		}
	}
}

func TestAChecklistSendsEveryTickedValue(t *testing.T) {
	// A record can carry several tags. They travel through the single-string
	// values map joined by newlines, which the command splits back into one
	// Set per value — a comma could appear in a tag name and a newline cannot.
	var sent map[string]string
	m, _ := harness(t, false)
	m.cfg.Resources[0].Actions = append(m.cfg.Resources[0].Actions,
		Action{Key: "e", Label: "edit", Verb: "Update", Writes: true,
			Fields: []FormField{
				{Key: "name", Label: "name"},
				{Key: "tag", Label: "Tags", Multi: true, Options: options(
					FormOption{Value: "525", Label: "sf-cli test"},
					FormOption{Value: "526", Label: "urgent"},
					FormOption{Value: "527", Label: "archive"},
				)},
			},
			Prefill: func(map[string]any) map[string]string {
				return map[string]string{"name": "X", "tag": "525"}
			},
			Run: func(_ context.Context, _ map[string]any, v map[string]string) (string, bool, error) {
				sent = v
				return "saved", true, nil
			}})

	m, _ = press(t, m, "e")
	if got := *m.formLists[1]; len(got) != 1 || got[0] != "525" {
		t.Errorf("the record's own tags are not ticked: %v", got)
	}

	// Untouched, nothing is sent — the same rule that makes pre-filling safe.
	unchanged := m
	unchanged.form.State = huh.StateCompleted
	if _, cmd := unchanged.updateForm(struct{ tea.Msg }{}); true {
		runCmd(cmd)
	}
	if sent != nil {
		t.Errorf("an untouched checklist sent %v", sent)
	}

	// Tick another and both go.
	*m.formLists[1] = []string{"525", "527"}
	m.form.State = huh.StateCompleted
	_, cmd := m.updateForm(struct{ tea.Msg }{})
	runCmd(cmd)
	if sent["tag"] != "525\n527" {
		t.Errorf("sent %q, want both ticked ids", sent["tag"])
	}
}

func TestTheConfirmButtonNamesWhatItDoes(t *testing.T) {
	// "Send?" says nothing about which of the two things is about to happen.
	m, _ := harness(t, false)
	for _, tc := range []struct{ verb, want string }{
		{"Update", "Update"},
		{"Create", "Create"},
		{"", "Send"}, // the default, for actions that are neither
	} {
		action := Action{Key: "x", Verb: tc.verb,
			Fields: []FormField{{Key: "a", Label: "a"}, {Key: "b", Label: "b"}}}
		form := m.buildForm(&action)
		form.Init()
		if got := stripANSI(form.View()); !strings.Contains(got, tc.want) {
			t.Errorf("verb %q: no %q button:\n%s", tc.verb, tc.want, got)
		}
	}
}

func TestTheArrowMovesNotTheList(t *testing.T) {
	// Setting a Height on a huh select makes it recompute the viewport with
	// YOffset = selected, which pins the highlighted option to the top and
	// scrolls the list underneath a stationary arrow. The cursor stops being a
	// cursor. This is the test that fails if anyone puts Height back.
	m, _ := harness(t, false)
	action := Action{Key: "e", Fields: []FormField{
		{Key: "t", Label: "t", Options: options(
			FormOption{Value: "a", Label: "alpha"},
			FormOption{Value: "b", Label: "beta"},
			FormOption{Value: "c", Label: "gamma"},
			FormOption{Value: "d", Label: "delta"},
		)},
	}}
	form := m.buildForm(&action)
	form.Init()

	at := func(f *huh.Form) int {
		for i, line := range strings.Split(stripANSI(f.View()), "\n") {
			if strings.Contains(line, "▸") {
				return i
			}
		}
		return -1
	}

	start := at(form)
	if start < 0 {
		t.Fatalf("no arrow on screen:\n%s", stripANSI(form.View()))
	}
	for step := 1; step <= 3; step++ {
		next, _ := form.Update(tea.KeyMsg{Type: tea.KeyDown})
		form = next.(*huh.Form)
		if got := at(form); got != start+step {
			t.Fatalf("after %d downs the arrow is on line %d, want %d — the list moved instead",
				step, got, start+step)
		}
	}
}

func TestTheConfirmIsButtonsAlone(t *testing.T) {
	// The buttons say Update and Cancel. A line above them asking "Update?" is
	// the same word again as a question.
	m, _ := harness(t, false)
	action := Action{Key: "e", Verb: "Update",
		Fields: []FormField{{Key: "a", Label: "a"}, {Key: "b", Label: "b"}}}
	form := m.buildForm(&action)
	form.Init()

	view := stripANSI(form.View())
	if strings.Contains(view, "Update?") {
		t.Errorf("the confirm still carries a title:\n%s", view)
	}
	if !strings.Contains(view, "Update") || !strings.Contains(view, "Cancel") {
		t.Errorf("the buttons are missing:\n%s", view)
	}
}

func TestLineItemsAreABoxOfLinesWithARunningTotal(t *testing.T) {
	// huh has no repeating group and a line item is four values, so the whole
	// list is typed one per line. The total is computed as it is typed, which
	// huh does through a command rather than inline — so the message loop has
	// to run for it to appear.
	m, _ := harness(t, false)
	action := Action{Key: "n", Verb: "Create", Fields: []FormField{
		{Key: "item", Label: "Line items", Multi: true,
			Note: func(text string) string {
				return fmt.Sprintf("%d lines", len(strings.Split(strings.TrimSpace(text), "\n")))
			}},
	}, Prefill: func(map[string]any) map[string]string {
		return map[string]string{"item": "Konzultácie:8:75:23\nCestovné:1:40:23"}
	}}

	form := m.buildForm(&action)
	drive(t, form, form.Init())

	view := stripANSI(form.View())
	for _, want := range []string{"Line items", "Konzultácie:8:75:23", "Cestovné:1:40:23", "2 lines"} {
		if !strings.Contains(view, want) {
			t.Errorf("%q is missing:\n%s", want, view)
		}
	}
}

// drive runs a command and feeds whatever it produces back into the form, so
// asynchronous work — huh computes a description in a command — completes.
func drive(t *testing.T, form *huh.Form, cmd tea.Cmd) {
	t.Helper()
	for range 8 {
		if cmd == nil {
			return
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				drive(t, form, sub)
			}
			return
		}
		next, following := form.Update(msg)
		form, cmd = next.(*huh.Form), following
	}
}

func TestAnActionOnAPagingKeyStillFires(t *testing.T) {
	// Paging answers to n and p as well as the arrows, and both cases used to
	// return before actions were dispatched. Payment survived only because
	// somebody added a special case for p; create, bound to n, was unreachable
	// from the keyboard while looking perfectly wired.
	for _, key := range []string{"n", "p"} {
		fired := false
		m, _ := harness(t, false)
		m.loaded.PageCount = 5 // so paging would have something to do
		m.cfg.Resources[0].Actions = append(m.cfg.Resources[0].Actions,
			Action{Key: key, Label: "act", Standalone: true,
				Run: func(context.Context, map[string]any, map[string]string) (string, bool, error) {
					fired = true
					return "done", false, nil
				}})

		_, cmd := press(t, m, key)
		runCmd(cmd)
		if !fired {
			t.Errorf("an action on %q was swallowed by paging", key)
		}
	}

	// The arrows still page, so paging is not lost.
	m, _ := harness(t, false)
	m.loaded.PageCount = 5
	paged, _ := press(t, m, "right")
	if paged.page != 2 {
		t.Errorf("right no longer pages: page = %d", paged.page)
	}
}

func TestTheBrowserKeepsItsOwnKeys(t *testing.T) {
	// The other half of the rule: an action must not be able to take over
	// navigation or the filters.
	for _, key := range []string{"/", "r", "f", "t", "tab", "enter", "esc", "q", "down"} {
		if !reservedKeys[key] {
			t.Errorf("%q is browser chrome and should be reserved", key)
		}
	}

	m, _ := harness(t, false)
	m.cfg.Resources[0].Actions = append(m.cfg.Resources[0].Actions,
		Action{Key: "/", Label: "hijack", Standalone: true,
			Run: func(context.Context, map[string]any, map[string]string) (string, bool, error) {
				t.Error("an action took over the filter key")
				return "", false, nil
			}})

	if filtered, _ := press(t, m, "/"); filtered.mode != modeInput {
		t.Errorf("mode = %v, want the filter prompt", filtered.mode)
	}
}

func TestARequiredFieldBlocksEveryKindOfInput(t *testing.T) {
	// The validator has to reach the field whatever shape it took. It did not:
	// only huh.Input was given it, so a required *select* was unenforced — the
	// client on the create form could be left empty and the write went out to
	// be refused by the server instead.
	m, _ := harness(t, false)
	for _, spec := range []FormField{
		{Key: "text", Label: "Text", Required: true},
		{Key: "list", Label: "List", Required: true, Options: options(
			FormOption{Value: "a", Label: "alpha"})},
		{Key: "lines", Label: "Lines", Required: true, Multi: true},
		// A required checklist is deliberately absent: nothing needs one — tags
		// are never compulsory — and huh's multi-select does not re-evaluate a
		// DescriptionFunc the way the other three do, so its complaint would
		// have nowhere to appear. Requiring one would need that solved first.
	} {
		action := Action{Key: "x", Verb: "Create", Fields: []FormField{spec}}
		form := m.buildForm(&action)
		drive(t, form, form.Init())

		// huh checks a field when you try to leave it, not when it appears — so
		// the attempt has to be made before there is anything to catch.
		next, cmd := form.Update(tea.KeyMsg{Type: tea.KeyEnter})
		form = next.(*huh.Form)
		drive(t, form, cmd)

		if errs := form.Errors(); len(errs) == 0 {
			t.Errorf("%s: an empty required field raised nothing", spec.Label)
			continue
		}

		// And it is said under that field's own label, not at the foot of the
		// form, where on anything long enough to scroll it is nowhere near the
		// field it is about.
		lines := strings.Split(stripANSI(form.View()), "\n")
		at := -1
		for i, line := range lines {
			if strings.Contains(line, spec.Label+" *") {
				at = i
			}
		}
		if at < 0 {
			t.Errorf("%s: the label is not on screen", spec.Label)
			continue
		}
		if at+1 >= len(lines) || !strings.Contains(lines[at+1], "required") {
			t.Errorf("%s: the complaint is not under the label:\n%s",
				spec.Label, strings.Join(lines[max(0, at-1):min(len(lines), at+4)], "\n"))
		}
	}
}

func TestAFormThatCannotBeCompletedIsNotOpened(t *testing.T) {
	// An invoice needs a client. On an account with none there is nothing the
	// form can do about it, so saying so beats letting it be filled in and
	// refused — the select would offer only its own "choose one" placeholder.
	m, _ := harness(t, false)
	m.cfg.Resources[0].Actions = append(m.cfg.Resources[0].Actions,
		Action{Key: "N", Label: "new", Verb: "Create", Writes: true, Standalone: true,
			Fields: []FormField{
				{Key: "client", Label: "Existing client", Required: true,
					Options: func() []FormOption { return nil }},
				{Key: "name", Label: "Name"},
			},
			Run: func(context.Context, map[string]any, map[string]string) (string, bool, error) {
				t.Error("a form that cannot be completed was submitted")
				return "", false, nil
			}})

	next, _ := press(t, m, "N")
	if next.mode == modeForm {
		t.Error("the form opened even though its required list is empty")
	}
	if !strings.Contains(next.err, "nothing to choose from") {
		t.Errorf("err = %q, want it to say why", next.err)
	}

	// A list with something in it opens as usual.
	m.cfg.Resources[0].Actions[len(m.cfg.Resources[0].Actions)-1].Fields[0].Options =
		options(FormOption{Value: "7", Label: "Acme s.r.o."})
	if opened, _ := press(t, m, "N"); opened.mode != modeForm {
		t.Errorf("mode = %v, want a form once there is a client to pick", opened.mode)
	}
}

func TestARequiredFieldIsMarkedBeforeTheAttempt(t *testing.T) {
	m, _ := harness(t, false)
	action := Action{Key: "x", Verb: "Create", Fields: []FormField{
		{Key: "a", Label: "Needed", Required: true},
		{Key: "b", Label: "Optional"},
	}}
	form := m.buildForm(&action)
	drive(t, form, form.Init())
	view := stripANSI(form.View())

	if !strings.Contains(view, "Needed *") {
		t.Errorf("a required field is not marked:\n%s", view)
	}
	if strings.Contains(view, "Optional *") {
		t.Errorf("an optional field was marked:\n%s", view)
	}
}

func TestARichSingleFieldGetsAFormNotAPrompt(t *testing.T) {
	// One field is an inline prompt in the footer — but the inline input has
	// nowhere to show a validator's complaint, a running total or a list of
	// options. The line-item field carries all three, and an action that
	// collects only that would have lost them silently.
	for _, tc := range []struct {
		name  string
		field FormField
		form  bool
	}{
		{"plain", FormField{Key: "amount", Label: "Amount"}, false},
		{"validated", FormField{Key: "due", Label: "Due", Validate: func(string) error { return nil }}, true},
		{"annotated", FormField{Key: "item", Label: "Items", Note: func(string) string { return "" }}, true},
		{"listed", FormField{Key: "type", Label: "Type", Options: options()}, true},
		{"required", FormField{Key: "name", Label: "Name", Required: true}, true},
		{"multi", FormField{Key: "item", Label: "Items", Multi: true}, true},
	} {
		// A fresh harness each time: the resources are a slice, so appending to
		// a copy of the model still writes into the shared backing array and
		// every case after the first would find the first case's action.
		local, _ := harness(t, false)
		local.cfg.Resources[0].Actions = append(local.cfg.Resources[0].Actions,
			Action{Key: "z", Label: "act", Writes: true, Fields: []FormField{tc.field},
				Run: func(context.Context, map[string]any, map[string]string) (string, bool, error) {
					return "done", false, nil
				}})

		opened, _ := press(t, local, "z")
		gotForm := opened.mode == modeForm
		if gotForm != tc.form {
			what := map[bool]string{true: "a form", false: "an inline prompt"}
			t.Errorf("%s field opened %s, want %s", tc.name, what[gotForm], what[tc.form])
		}
	}
}

func TestTheWayOutSurvivesEveryWidth(t *testing.T) {
	// The key bar truncates, and an earlier version filled it left to right.
	// Putting the resource's actions first — so the keys nobody can guess
	// survive — pushed refresh and quit off an 80-column terminal, which is the
	// most common width there is. Nothing polls, so r is the only route to
	// fresh data, and neither key appears anywhere else on screen.
	m, _ := harness(t, false)
	m.cfg.Resources[0].Actions = append(m.cfg.Resources[0].Actions,
		Action{Key: "a", Label: "an action with a long label"},
		Action{Key: "b", Label: "another long one"},
		Action{Key: "c", Label: "a third to overflow with"})

	for _, width := range []int{120, 100, 80, 60, 50, 40} {
		sized, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		bar := stripANSI(strings.Split(sized.(appModel).footer(), "\n")[1])

		for _, must := range []string{"r refresh", "q quit"} {
			if !strings.Contains(bar, must) {
				t.Errorf("%d cols: %q is gone:\n  %s", width, must, bar)
			}
		}
		// Runes, not bytes: the separators and the ellipsis are multi-byte, and
		// a byte count would call a bar that fits three cells too wide.
		if got := utf8.RuneCountInString(bar); got > width {
			t.Errorf("%d cols: the bar is %d wide:\n  %s", width, got, bar)
		}
		// The mark that says "there is more" must not itself be what gets cut.
		if strings.HasSuffix(strings.TrimSpace(bar), "·") {
			t.Errorf("%d cols: the bar ends on a dangling separator:\n  %s", width, bar)
		}
	}
}
