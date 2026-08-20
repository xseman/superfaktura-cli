package tui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/xseman/superfaktura-cli/internal/render"
)

// A browser for the account, built around one fact about the API: a list row
// already carries everything its detail view shows. Opening a record therefore
// costs nothing, which is what makes a terminal UI affordable against a
// thousand-request daily allowance.
//
// Nothing here polls. A tool that refreshed every two seconds, as cluster
// browsers do, would spend the whole day's quota before lunch.

// Page is one page of a resource.
type Page struct {
	Items     []map[string]any
	ItemCount int
	PageCount int
}

// FormField is one value an action asks for.
type FormField struct {
	Key         string
	Label       string
	Placeholder string
	// Validate rejects a value before the form is submitted, so the complaint
	// lands under the field being typed into rather than coming back from the
	// server as something else. Optional.
	Validate func(string) error
	// Options turns the field into a list to arrow through instead of a box to
	// type into. For a value the API only accepts from a fixed set, a typo is
	// not a possible input, so it should not be a possible keystroke.
	//
	// It is a function because some lists come from the API. Called when the
	// form opens, not when the browser starts, or every session would spend a
	// request on a dropdown most of them never draw.
	Options func() []FormOption

	// Required blocks the form until the field has a value, and marks it so
	// that is visible before the attempt rather than after it.
	Required bool

	// Multi marks a repeatable flag. With Options it is a checklist; without,
	// a box with one value per line — which is how line items are entered,
	// since huh has no repeating group and a composite value has no single
	// field that fits it.
	Multi bool

	// Note annotates the field as it is typed into, e.g. a running total under
	// a list of line items. Optional.
	Note func(string) string
}

// FormOption is one choice. Label is what the reader sees; Value is what the
// command is given.
type FormOption struct {
	Value string
	Label string
}

// Action is a keystroke that does something to the selected record.
type Action struct {
	Key   string
	Label string

	// Fields are the values to collect first. One plain field is an inline
	// prompt in the footer; several open a form, and so does a single field
	// that brings a validator, a note, options, or is required — the inline
	// input has nowhere to put a complaint or a running total. See needsAForm.
	Fields []FormField

	// Confirm, when set, returns the question to ask before running. An empty
	// return skips the confirmation.
	Confirm func(row map[string]any) string

	// Prefill returns the record's current value for each field key, so an
	// edit form opens showing what it is about to overwrite. Only fields the
	// user changes are sent, so seeding a box does not commit its contents.
	Prefill func(row map[string]any) map[string]string

	// Run performs the action. It returns a line to show and whether the list
	// should be reloaded.
	Run func(ctx context.Context, row map[string]any, values map[string]string) (string, bool, error)

	// Writes marks an action that changes data, so read-only mode can hide it.
	Writes bool

	// Standalone marks an action that needs no selected record, such as
	// creating one.
	Standalone bool

	// Verb names the button that commits the form. "Send" says nothing about
	// what is about to happen; "Update" and "Create" say which of the two it
	// is, which is the only thing a confirm button has to answer. Defaults to
	// "Send".
	Verb string
}

// Scope is a server-side filter offered above the list, such as the paid and
// unpaid tabs the web application puts there. Switching one costs a request,
// unlike the text filter, which only narrows what is already loaded.
type Scope struct {
	Label string
	// Params are merged into the request. An empty map is "everything".
	Params map[string]string
}

// Resource is one tab.
type Resource struct {
	Title   string
	Columns []render.Column
	// Detail renders the selected record. render.Pairs covers the common case;
	// a document composes its own, because it has line items and payments.
	Detail render.Renderer

	// Scopes and Periods are two independent narrowings, cycled by f and t.
	// They are separate because a user wants unpaid AND this month, and
	// folding them into one list would mean an entry per combination.
	Scopes  []Scope
	Periods []Scope
	// Load fetches a page under the given scope.
	Load    func(ctx context.Context, page int, scope map[string]string) (Page, error)
	Actions []Action
}

// Quota is what the header reports.
type Quota struct {
	Remaining int
	Limit     int
}

// Config wires the application to the account.
type Config struct {
	Account   string
	Resources []Resource
	ReadOnly  bool
	// Quota is read after every request.
	Quota func() Quota
}

// Run starts the browser and blocks until it exits.
func Run(ctx context.Context, cfg Config) error {
	if len(cfg.Resources) == 0 {
		return fmt.Errorf("no resources to browse")
	}

	input := textinput.New()
	input.Prompt = "› "

	m := appModel{
		ctx:     ctx,
		cfg:     cfg,
		input:   input,
		detail:  viewport.New(0, 0),
		spin:    spinner.New(spinner.WithSpinner(spinner.Dot)),
		cache:   newPageCache(nil),
		clock:   time.Now,
		page:    1,
		loading: true,
		status:  "Loading",
	}

	program := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := program.Run()
	return err
}

type mode int

const (
	modeBrowse mode = iota
	modeInput
	modeForm
	modeConfirm
)

type appModel struct {
	// Storing a context in a struct is normally wrong, but tea.Model's
	// Init/Update/View take no parameters, so a command closure has nowhere
	// else to get one. It is the request context for the whole session and is
	// never swapped after construction.
	ctx context.Context
	cfg Config

	cursor int
	input  textinput.Model
	detail viewport.Model
	spin   spinner.Model

	// widths remembers each resource's measured column widths, so a fetch
	// draws its headers where the rows will put them. See rememberWidths.
	widths map[int][]int

	// cache holds pages already fetched this session. See cache.go.
	cache *pageCache
	// servedAt is when the data on screen was fetched, and fromCache whether it
	// came back without a request. Together they drive the freshness marker,
	// which is always on: the band would otherwise be empty on a resource with
	// no filters, and change shape the first time a page came from cache.
	servedAt  time.Time
	fromCache bool
	// clock is time.Now everywhere but the tests.
	clock func() time.Time

	resource int
	scope    int
	period   int
	page     int
	loaded   Page
	// filtered mirrors loaded.Items after the filter, and is what the table
	// shows. Filtering is local to the page and therefore free.
	filtered []map[string]any
	filter   string

	mode       mode
	expanded   bool
	pending    *Action
	form       *huh.Form
	values     map[string]string
	formValues []*string
	// formLists holds the checklists. Only the multi-valued indices are set;
	// the rest stay nil and are read from formValues.
	formLists []*[]string
	// formInitial is what each field held when the form opened, so submission
	// can tell an edit from a value that was merely shown.
	formInitial []string
	// formBlocked says why the form that was just built cannot be completed —
	// a required list with nothing in it. Set by buildForm, read by begin.
	formBlocked string
	formConfirm *bool
	status      string
	err         string
	loading     bool

	width, height int
}

type loadedMsg struct {
	// What was asked for. A response carries it back so one that lands after
	// the user has moved on can be dropped instead of painted into whatever
	// tab is open now — walking the tabs faster than the network answers put
	// one resource's rows under another's columns, which renders as a page of
	// blank cells under a record count that does not match anything on screen.
	resource int
	number   int
	scope    int
	period   int

	// cachedAt is when the page was originally fetched, and is zero for a
	// response that came off the wire just now.
	cachedAt time.Time

	page Page
	err  error
}

// stale reports whether the answer is to a question no longer being asked.
func (m appModel) stale(msg loadedMsg) bool {
	return msg.resource != m.resource || msg.number != m.page ||
		msg.scope != m.scope || msg.period != m.period
}

type actedMsg struct {
	message string
	reload  bool
	err     error
}

func (m appModel) Init() tea.Cmd { return tea.Batch(m.load(), m.spin.Tick) }

func (m appModel) current() Resource { return m.cfg.Resources[m.resource] }

// now is time.Now everywhere but the tests.
func (m appModel) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

// key identifies the request the current view needs.
func (m appModel) key() cacheKey {
	return cacheKey{resource: m.resource, page: m.page, scope: m.scope, period: m.period}
}

// load fetches the current page. It is the only thing that spends quota.
//
// A page already fetched this session comes back without a request. The reply
// still travels as a loadedMsg so the rest of the flow cannot tell the
// difference — including the guard that drops answers to superseded questions.
func (m appModel) load() tea.Cmd {
	key := m.key()
	if page, at, ok := m.cache.get(key); ok {
		return func() tea.Msg {
			return loadedMsg{
				resource: key.resource, number: key.page, scope: key.scope,
				period: key.period, cachedAt: at, page: page,
			}
		}
	}

	resource := m.current()
	params := m.scopeParams()
	return func() tea.Msg {
		p, err := resource.Load(m.ctx, key.page, params)
		return loadedMsg{
			resource: key.resource, number: key.page, scope: key.scope,
			period: key.period, page: p, err: err,
		}
	}
}

// scopeParams are the filters both active narrowings add to a request.
//
// A fresh map every time: the Scope values are package-level and merging into
// one of them would quietly rewrite the resource's own definition.
func (m appModel) scopeParams() map[string]string {
	params := map[string]string{}
	for _, axis := range []struct {
		scopes []Scope
		at     int
	}{
		{m.current().Scopes, m.scope},
		{m.current().Periods, m.period},
	} {
		if axis.at < 0 || axis.at >= len(axis.scopes) {
			continue
		}
		maps.Copy(params, axis.scopes[axis.at].Params)
	}
	if len(params) == 0 {
		return nil
	}
	return params
}

// periodLabel names the active period for the filter line.
func (m appModel) periodLabel() string {
	periods := m.current().Periods
	if m.period < 0 || m.period >= len(periods) {
		return ""
	}
	return periods[m.period].Label
}

// scopeLabel names the active scope for the filter line.
func (m appModel) scopeLabel() string {
	scopes := m.current().Scopes
	if m.scope < 0 || m.scope >= len(scopes) {
		return ""
	}
	return scopes[m.scope].Label
}

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// A form takes every message, not only keys. huh advances between fields
	// with messages of its own, and anything that falls through to the bottom
	// of this function is handed to the table instead — which is why the form
	// sat on its first field and would not move.
	if m.mode == modeForm {
		if _, resize := msg.(tea.WindowSizeMsg); !resize {
			return m.updateForm(msg)
		}
	}

	switch msg := msg.(type) {
	case spinner.TickMsg:
		// Keep ticking only while something is in flight, so an idle browser
		// is not redrawing the screen ten times a second for nothing.
		if !m.loading {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil

	case loadedMsg:
		// Drop it without clearing the indicator: the request it belongs to is
		// not the one still in flight, and turning the spinner off here would
		// leave the screen looking settled while the real answer is pending.
		if m.stale(msg) {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		m.loaded = msg.page
		m.fromCache = !msg.cachedAt.IsZero()
		if m.fromCache {
			m.servedAt = msg.cachedAt
		} else {
			m.servedAt = m.now()
			m.cache.put(m.key(), msg.page)
		}
		// resize, not applyFilter: column widths are measured from the rows,
		// and the first resize happened before there were any.
		m.resize()
		m.rememberWidths()
		m.status = m.summary()
		return m, nil

	case actedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		m.status = msg.message
		if msg.reload {
			// The write may have moved figures this cache cannot reason about,
			// so none of it survives. See pageCache.purge.
			m.cache.purge()
			m.loading = true
			return m, tea.Batch(m.load(), m.spin.Tick)
		}
		return m, nil

	case tea.KeyMsg:
		switch m.mode {
		case modeInput:
			return m.updateInput(msg)
		case modeConfirm:
			return m.updateConfirm(msg)
		default:
			return m.updateBrowse(msg)
		}
	}

	return m, nil
}

// reservedKeys belong to the browser itself and can never be claimed by a
// resource's action.
//
// Everything else reaches actions first. Paging answers to n and p as well as
// the arrows, and those cases used to return before the dispatch below — so an
// action bound to either letter was swallowed. Payment worked only because
// somebody had noticed and added a special case for p; create, on n, did not
// exist as far as the keyboard was concerned. One rule instead of a special
// case per collision.
var reservedKeys = map[string]bool{
	"up": true, "down": true, "left": true, "right": true,
	"home": true, "end": true, "pgup": true, "pgdown": true,
	"ctrl+u": true, "ctrl+d": true, "ctrl+c": true,
	"k": true, "j": true, "g": true, "G": true,
	"shift+up": true, "shift+down": true,
	"enter": true, "esc": true, "q": true,
	"tab": true, "shift+tab": true,
	"f": true, "t": true, "r": true, "/": true,
}

func (m appModel) updateBrowse(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !reservedKeys[msg.String()] {
		if action := m.actionFor(msg.String()); action != nil {
			return m.begin(action)
		}
	}

	switch msg.String() {
	case "enter":
		// A record runs to seventeen fields; a third of the screen holds far
		// fewer. Expanding gives it the whole terminal.
		m.expanded = !m.expanded
		m.resize()
		return m, nil

	case "ctrl+d", "ctrl+u", "pgdown", "pgup":
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd

	case "q", "ctrl+c", "esc":
		// Dismissing a failure comes first. Losing the session because a write
		// was rejected would be a poor trade for one keystroke.
		if m.err != "" {
			m.err = ""
			m.status = m.summary()
			return m, nil
		}
		if m.expanded {
			m.expanded = false
			m.resize()
			return m, nil
		}
		if m.filter != "" {
			m.filter = ""
			m.applyFilter()
			m.status = m.summary()
			return m, nil
		}
		return m, tea.Quit

	case "tab", "shift+tab":
		// Not while a record is open. The expanded view fills the screen with
		// one record, so switching underneath it would swap the whole account
		// out from behind a detail that still looked like the old one — and the
		// key that gets you back, esc, is right there.
		if m.expanded {
			return m, nil
		}
		step := 1
		if msg.String() == "shift+tab" {
			step = len(m.cfg.Resources) - 1
		}
		m.resource = (m.resource + step) % len(m.cfg.Resources)
		m.page, m.scope, m.period, m.filter, m.loading = 1, 0, 0, "", true

		// Drop the previous resource's rows before redrawing. Left in place
		// they are briefly painted into the new resource's columns, which is
		// the flicker: wrong data, wrong widths, for one frame.
		m.loaded = Page{}
		m.status = "Loading " + strings.ToLower(m.current().Title)
		m.resize()
		return m, tea.Batch(m.load(), m.spin.Tick)

	case "f":
		// Cycling a scope is a request, unlike the text filter. That is why
		// the two sit on the same line but are named differently.
		if scopes := m.current().Scopes; len(scopes) > 1 {
			m.scope = (m.scope + 1) % len(scopes)
			m.page, m.loading = 1, true
			m.status = "Loading " + strings.ToLower(m.scopeLabel())
			return m, tea.Batch(m.load(), m.spin.Tick)
		}
		return m, nil

	case "t":
		// The other axis. Costs a request exactly as f does.
		if periods := m.current().Periods; len(periods) > 1 {
			m.period = (m.period + 1) % len(periods)
			m.page, m.loading = 1, true
			m.status = "Loading " + strings.ToLower(m.periodLabel())
			return m, tea.Batch(m.load(), m.spin.Tick)
		}
		return m, nil

	case "r":
		// The way to demand the truth. Only this view: r means "I do not trust
		// what I am looking at", not "throw away everything I have fetched".
		m.cache.forget(m.key())
		m.loading = true
		m.status = "Refreshing"
		return m, tea.Batch(m.load(), m.spin.Tick)

	case "n", "right":
		if m.page < m.loaded.PageCount {
			m.page++
			m.loading = true
			return m, tea.Batch(m.load(), m.spin.Tick)
		}
		return m, nil

	case "p", "left":
		if m.page > 1 {
			m.page--
			m.loading = true
			return m, tea.Batch(m.load(), m.spin.Tick)
		}
		return m, nil

	case "/":
		m.mode = modeInput
		m.pending = nil
		m.input.Placeholder = "filter this page"
		m.input.SetValue(m.filter)
		m.input.Focus()
		return m, textinput.Blink
	}

	switch msg.String() {
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "home", "g":
		m.moveCursor(-len(m.filtered))
	case "end", "G":
		m.moveCursor(len(m.filtered))
	case "shift+up":
		m.moveCursor(-10)
	case "shift+down":
		m.moveCursor(10)
	}
	return m, nil
}

// begin starts an action, asking for whatever it wants first.
func (m appModel) begin(action *Action) (tea.Model, tea.Cmd) {
	row := m.selected()
	if row == nil && !action.Standalone {
		m.err = "nothing selected"
		return m, nil
	}

	m.pending = action
	m.values = map[string]string{}

	switch len(action.Fields) {
	case 0:
		return m.askConfirmOrRun(row)

	case 1:
		// One field is a prompt in the footer — but only when a prompt can
		// actually carry it. The inline input has nowhere to show a validator's
		// complaint, a running total, or a list of options, so a field that
		// brings any of those gets the form regardless of the count.
		if !needsAForm(action.Fields[0]) {
			m.mode = modeInput
			m.input.Placeholder = action.Fields[0].Label
			m.input.SetValue("")
			m.input.Focus()
			return m, textinput.Blink
		}
		fallthrough

	default:
		m.formBlocked = ""
		form := m.buildForm(action)
		if m.formBlocked != "" {
			m.err = m.formBlocked
			m.pending, m.form = nil, nil
			return m, nil
		}
		m.mode = modeForm
		m.form = form
		return m, m.form.Init()
	}
}

// needsAForm reports whether a field is more than a line of text can hold.
func needsAForm(f FormField) bool {
	return f.Validate != nil || f.Note != nil || f.Options != nil || f.Multi || f.Required
}

// askConfirmOrRun asks the question the action carries, or runs it.
func (m appModel) askConfirmOrRun(row map[string]any) (tea.Model, tea.Cmd) {
	if m.pending.Confirm != nil && row != nil {
		if question := m.pending.Confirm(row); question != "" {
			m.mode = modeConfirm
			m.status = question + " (y/n)"
			return m, nil
		}
	}
	return m.run()
}

// buildForm turns the field list into a huh form bound to string pointers.
//
// An edit form opens showing the record's current values, so it is clear what
// is about to be overwritten and a small change does not mean retyping the
// whole field.
//
// What keeps that safe is that the initial contents are remembered and only
// fields whose value actually changed are sent. A form that submitted every
// box would write back fields nobody touched — and the value on screen is not
// always the value that should be written, so a round trip through the form
// could rewrite a record by merely opening it.
func (m *appModel) buildForm(action *Action) *huh.Form {
	stored := make([]*string, len(action.Fields))
	lists := make([]*[]string, len(action.Fields))
	fields := make([]huh.Field, 0, len(action.Fields)+1)

	var current map[string]string
	if action.Prefill != nil {
		current = action.Prefill(m.selected())
	}
	m.formInitial = make([]string, len(action.Fields))

	for i, spec := range action.Fields {
		value := new(string)
		*value = current[spec.Key]
		stored[i] = value
		m.formInitial[i] = *value

		if spec.Required {
			// The marker is styled on its own so the label keeps the weight
			// every other label has: the asterisk carries the difference, and it
			// survives a terminal with no color at all.
			//
			// The complaint is named before the marker is appended, because huh
			// prints it at the foot of the form — on a form long enough to
			// scroll, "required" alone does not say which of three fields it is
			// about.
			spec.Validate = requireValue(spec.Validate)
			spec.Label += styleRequired.Render(" *")
		}

		if spec.Multi {
			if spec.Options != nil {
				chosen := new([]string)
				*chosen = splitList(*value)
				lists[i] = chosen
				fields = append(fields, checklistField(spec, chosen))
				continue
			}
			fields = append(fields, linesField(spec, value))
			continue
		}
		if spec.Options != nil {
			if len(spec.Options()) == 0 && spec.Required {
				// A required list with nothing in it is a form that cannot be
				// completed. Saying so now beats letting it be filled in and
				// refused — an invoice needs a client, and if the account has
				// none there is nothing this form can do about it.
				m.formBlocked = fmt.Sprintf(
					"%s is required and there is nothing to choose from", spec.Label)
			}
			fields = append(fields, selectField(spec, value))
			continue
		}

		input := huh.NewInput().Key(spec.Key).Title(spec.Label).Value(value)
		if spec.Placeholder != "" {
			input = input.Placeholder(spec.Placeholder)
		}
		if spec.Validate != nil {
			input = input.Validate(spec.Validate).
				DescriptionFunc(complaintFor(spec.Validate, func() string { return *value }), value)
		}
		fields = append(fields, input)
	}

	// Default to the affirmative. Getting here already took a keystroke to open
	// the form and a field or two of typing; landing on Cancel means one more
	// press to undo a decision made several steps ago. Escape still abandons.
	confirmed := new(bool)
	*confirmed = true
	verb := action.Verb
	if verb == "" {
		verb = "Send"
	}
	// No title. The buttons say Update and Cancel; a line above them asking
	// "Update?" is the same word again as a question.
	fields = append(fields, huh.NewConfirm().
		Affirmative(verb).Negative("Cancel").Value(confirmed))

	m.formValues = stored
	m.formLists = lists
	m.formConfirm = confirmed
	return huh.NewForm(huh.NewGroup(fields...).WithShowErrors(false)).
		WithTheme(FormTheme()).
		WithShowHelp(true).
		WithWidth(min(72, max(40, m.width-4))).
		WithHeight(max(8, m.height-4))
}

// selectField builds a list field, with the record's current value selected.
//
// The value is kept as an option even when it is not one of the known ones.
// Without that, a record carrying something the constant list does not name
// would silently land on the first option, and confirming the form would write
// a change nobody asked for. The same reasoning covers an unset field: it gets
// a "not set" option and stays there, so an untouched form still sends nothing.
func selectField(spec FormField, value *string) huh.Field {
	options := spec.Options()
	if !slices.ContainsFunc(options, func(o FormOption) bool { return o.Value == *value }) {
		label := *value
		if label == "" {
			label = "— not set —"
			if spec.Required {
				// Required means it has to be chosen, so the empty entry is an
				// instruction rather than a state to leave it in.
				label = "— choose one —"
			}
		}
		options = append([]FormOption{{Value: *value, Label: label}}, options...)
	}

	choices := make([]huh.Option[string], 0, len(options))
	for _, o := range options {
		choices = append(choices, huh.NewOption(o.Label, o.Value))
	}

	// No Height, deliberately. Setting one makes huh recompute the viewport with
	// YOffset = selected on every message, which pins the highlighted option to
	// the top and scrolls the list underneath a stationary arrow — the cursor
	// stops being a cursor. Unset, the viewport is sized to the whole list and
	// the arrow moves down it.
	//
	// Search needs nothing here: "/" inside a focused select starts filtering by
	// itself. Calling Filtering(true) does not enable that, it starts the field
	// already filtering — which is why an earlier attempt at it left a bare "/"
	// where every label should have been.
	field := huh.NewSelect[string]().
		Key(spec.Key).
		Title(spec.Label).
		Options(choices...).
		Value(value)
	if spec.Validate != nil {
		field = field.Validate(spec.Validate).
			DescriptionFunc(complaintFor(spec.Validate, func() string { return *value }), value)
	}
	return field
}

// checklistField builds a multi-select, with the record's current values
// ticked. Unlike a single select there is nothing to guard: a value the list
// does not name simply is not offered, and leaving the boxes alone sends the
// same set back, which the change check then discards.
func checklistField(spec FormField, chosen *[]string) huh.Field {
	options := spec.Options()
	choices := make([]huh.Option[string], 0, len(options))
	for _, o := range options {
		choices = append(choices, huh.NewOption(o.Label, o.Value).Selected(slices.Contains(*chosen, o.Value)))
	}
	field := huh.NewMultiSelect[string]().
		Key(spec.Key).
		Title(spec.Label).
		Options(choices...).
		Value(chosen)
	if spec.Validate != nil {
		// The shared checks take the joined form a multi-valued field travels
		// as, so a required checklist is empty on the same terms as a required
		// box.
		check := spec.Validate
		field = field.Validate(func(chosen []string) error {
			return check(strings.Join(chosen, "\n"))
		}).DescriptionFunc(
			complaintFor(check, func() string { return strings.Join(*chosen, "\n") }), chosen)
	}
	return field
}

// linesField is a box holding one value per line.
//
// Line items are the reason it exists: a repeatable flag whose every value is
// itself four fields, which huh has no shape for. Typing them is a compromise —
// but the whole list is visible at once and any line can be corrected, which a
// group asked once per item cannot offer.
func linesField(spec FormField, value *string) huh.Field {
	text := huh.NewText().
		Key(spec.Key).
		Title(spec.Label).
		Lines(4).
		Value(value)
	if spec.Placeholder != "" {
		text = text.Placeholder(spec.Placeholder)
	}
	if spec.Validate != nil {
		text = text.Validate(spec.Validate).
			DescriptionFunc(complaintFor(spec.Validate, func() string { return *value }), value)
	}
	if spec.Note != nil {
		// Recomputed as the value changes, so the total follows the typing
		// rather than describing what was there when the form opened.
		note := spec.Note
		text = text.DescriptionFunc(func() string { return note(*value) }, value)
	}
	return text
}

// complaintFor is the field's own error line, shown between its label and its
// box.
//
// huh prints errors in the group's footer, which on a form long enough to scroll
// is nowhere near the field they are about. A field's description is the only
// slot it has next to itself, so that is where the complaint goes, and huh's
// footer is switched off rather than saying it twice.
//
// It costs a line per validated field even when there is nothing wrong — huh
// reserves the description whether it renders anything or not. Five lines on the
// invoice create form, which is the price of every complaint being where the
// field is.
func complaintFor(check func(string) error, read func() string) func() string {
	return func() string {
		if check == nil {
			return ""
		}
		if err := check(read()); err != nil {
			return styleErr.Render(err.Error())
		}
		return ""
	}
}

// requireValue refuses an empty field, and otherwise defers to whatever check
// the field already had.
//
// huh will not let a group complete while a field errors, so this is what turns
// "the write came back rejected" into "the button does not go yet". The message
// does not name the field: complaintFor puts it directly under the label.
func requireValue(next func(string) error) func(string) error {
	return func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("required")
		}
		if next != nil {
			return next(value)
		}
		return nil
	}
}

// splitList is how a multi-valued field travels through the single-string
// values map the actions share. A newline cannot appear in an identifier or a
// tag name, which a comma can.
func splitList(joined string) []string {
	if joined == "" {
		return nil
	}
	return strings.Split(joined, "\n")
}

func (m appModel) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeBrowse
		m.input.Blur()
		m.pending = nil
		return m, nil

	case "enter":
		value := strings.TrimSpace(m.input.Value())
		m.mode = modeBrowse
		m.input.Blur()

		// No pending action means the input was the filter.
		if m.pending == nil {
			m.filter = value
			m.applyFilter()
			m.status = m.summary()
			return m, nil
		}

		m.values[m.pending.Fields[0].Key] = value
		return m.askConfirmOrRun(m.selected())
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// updateForm hands keys to huh until it finishes or is abandoned.
func (m appModel) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.form == nil {
		m.mode = modeBrowse
		return m, nil
	}

	// Escape means "back" everywhere else here, and huh reserves it for moving
	// between fields. Abandoning a half-typed write is the more useful
	// meaning, and ctrl+c would quit the whole browser.
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
		m.mode = modeBrowse
		m.form, m.pending = nil, nil
		m.status = "Canceled"
		return m, nil
	}

	form, cmd := m.form.Update(msg)
	if updated, ok := form.(*huh.Form); ok {
		m.form = updated
	}

	switch m.form.State {
	case huh.StateAborted:
		m.mode = modeBrowse
		m.form, m.pending = nil, nil
		m.status = "Canceled"
		return m, nil

	case huh.StateCompleted:
		m.mode = modeBrowse
		if m.formConfirm == nil || !*m.formConfirm {
			m.form, m.pending = nil, nil
			m.status = "Canceled"
			return m, nil
		}
		// Only fields the user actually changed are sent. A pre-filled form
		// that submitted every box would write back values nobody touched.
		for i, spec := range m.pending.Fields {
			value := strings.TrimSpace(*m.formValues[i])
			if list := m.formLists[i]; list != nil {
				value = strings.Join(*list, "\n")
			}
			if value == "" || value == strings.TrimSpace(m.formInitial[i]) {
				continue
			}
			m.values[spec.Key] = value
		}
		if len(m.values) == 0 {
			m.form, m.pending = nil, nil
			m.status = "Nothing to send"
			return m, nil
		}
		return m.run()
	}
	return m, cmd
}

func (m appModel) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		return m.run()
	default:
		m.mode = modeBrowse
		m.pending = nil
		m.status = "Canceled"
		return m, nil
	}
}

func (m appModel) run() (tea.Model, tea.Cmd) {
	action, row, values := m.pending, m.selected(), m.values
	m.mode = modeBrowse
	m.pending, m.form = nil, nil
	if action == nil || (row == nil && !action.Standalone) {
		return m, nil
	}

	m.loading = true
	m.status = action.Label
	ctx := m.ctx
	return m, tea.Batch(m.spin.Tick, func() tea.Msg {
		message, reload, err := action.Run(ctx, row, values)
		return actedMsg{message: message, reload: reload, err: err}
	})
}

// actionFor finds the action bound to a key, skipping writes in read-only mode.
func (m appModel) actionFor(key string) *Action {
	for i, action := range m.current().Actions {
		if action.Key != key {
			continue
		}
		if action.Writes && m.cfg.ReadOnly {
			return nil
		}
		return &m.current().Actions[i]
	}
	return nil
}

func (m appModel) selected() map[string]any {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return nil
	}
	return m.filtered[m.cursor]
}

// applyFilter narrows the loaded page by substring, across every rendered cell.
// It never issues a request: the rows are already here.
func (m *appModel) applyFilter() {
	resource := m.current()
	needle := strings.ToLower(m.filter)

	m.filtered = m.filtered[:0]
	for _, item := range m.loaded.Items {
		if needle == "" || strings.Contains(strings.ToLower(rowText(resource.Columns, item)), needle) {
			m.filtered = append(m.filtered, item)
		}
	}

	m.cursor = max(0, min(m.cursor, len(m.filtered)-1))
}

func rowText(cols []render.Column, item map[string]any) string {
	return strings.Join(cellsOf(cols, item), " ")
}

func cellsOf(cols []render.Column, item map[string]any) []string {
	cells := make([]string, len(cols))
	for i, c := range cols {
		value := render.Get(item, c.Path)
		if c.Format != nil {
			cells[i] = c.Format(value)
		} else {
			cells[i] = render.Text(value)
		}
	}
	return cells
}

func (m appModel) summary() string {
	if m.filter != "" {
		// Say "on this page": the filter is local, and on a large account it
		// searches the loaded rows rather than the account.
		return fmt.Sprintf("%d of %d on this page · filter %q",
			len(m.filtered), len(m.loaded.Items), m.filter)
	}
	return fmt.Sprintf("page %d of %d · %d records", m.page, max(1, m.loaded.PageCount), m.loaded.ItemCount)
}
