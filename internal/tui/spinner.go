// Package tui renders the progress indicator shown while a request is in
// flight, using the same Bubble Tea stack the interactive forms are built on.
package tui

import (
	"io"
	"sync"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// delayFrames is how many ticks pass before anything is drawn. A request that
// finishes quickly should leave no trace: a spinner that flashes for a moment
// is noise, not feedback.
const delayFrames = 2

// Spinner animates a label until it is stopped.
//
// It renders to a side channel — stderr in practice — never to the stream
// carrying results, so `sf invoice list --json | jq` stays parseable. Whether a
// spinner is wanted at all is the caller's decision; this type does not check
// for a terminal.
type Spinner struct {
	program *tea.Program

	mu      sync.Mutex
	stopped bool
	done    chan struct{}
}

type model struct {
	spinner spinner.Model
	label   string
	// ticks counts frames so the first few can be swallowed, and quitting can
	// clear the line by rendering nothing.
	ticks    int
	quitting bool
}

func (m model) Init() tea.Cmd { return m.spinner.Tick }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case spinner.TickMsg:
		m.ticks++
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.QuitMsg:
		m.quitting = true
		return m, nil
	}
	return m, nil
}

func (m model) View() string {
	// An empty view on the last frame leaves the terminal as it was found.
	if m.quitting || m.ticks < delayFrames {
		return ""
	}
	return m.spinner.View() + m.label
}

// Start begins animating and returns the spinner. Stop must be called.
func Start(w io.Writer, label string) *Spinner {
	s := spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("8"))),
	)

	program := tea.NewProgram(
		model{spinner: s, label: label},
		tea.WithOutput(w),
		// No input: this is a progress indicator, not a prompt, and reading
		// stdin would steal keystrokes from whatever runs next.
		tea.WithInput(nil),
		// The CLI installs its own interrupt handling; Bubble Tea must not
		// swallow it or leave the terminal in raw mode on exit.
		tea.WithoutSignalHandler(),
		tea.WithoutCatchPanics(),
	)

	sp := &Spinner{program: program, done: make(chan struct{})}
	go func() {
		defer close(sp.done)
		_, _ = program.Run()
	}()
	return sp
}

// Stop halts the animation and clears the line it drew.
func (s *Spinner) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.mu.Unlock()

	s.program.Quit()
	// Wait for the final render, so the erased line is flushed before the
	// result is printed on top of it.
	<-s.done
}
