package tui

import (
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// FormTheme dresses huh in this application's palette.
//
// Exported because the browser is not the only place huh runs: `auth login`,
// `client create` and `invoice create` prompt from the CLI, and on the stock
// theme they came out fuchsia — a different application dropped into the
// middle of this one. Only the accent and one grey are used: the focused field
// is accented, everything else is the terminal's own foreground.
func FormTheme() *huh.Theme {
	t := huh.ThemeBase()

	accent := lipgloss.NewStyle().Foreground(colAccent)
	dim := lipgloss.NewStyle().Foreground(colDim)

	t.Focused.Base = t.Focused.Base.BorderForeground(colAccent)
	t.Focused.Title = accent.Bold(true)
	t.Focused.Description = dim
	// Keep the strings the base theme set. Assigning a bare style here — which
	// this did — throws away the "> " and "[ ] " those styles carry, and the
	// cursor and the checkboxes disappear with them.
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(colAccent).SetString("▸ ")
	t.Focused.MultiSelectSelector = t.Focused.MultiSelectSelector.Foreground(colAccent).SetString("▸ ")
	t.Focused.SelectedPrefix = t.Focused.SelectedPrefix.Foreground(colAccent).SetString("[✓] ")
	t.Focused.UnselectedPrefix = t.Focused.UnselectedPrefix.Foreground(colDim).SetString("[ ] ")
	t.Focused.SelectedOption = accent
	t.Focused.UnselectedOption = lipgloss.NewStyle()
	// Deliberately empty, unlike the selectors, where a bare style silently
	// dropped the string they carry. The complaint is rendered under the label
	// by complaintFor, so a marker on the label would only repeat it — and the
	// base theme's " *" is what marks a field *required* here.
	t.Focused.ErrorIndicator = lipgloss.NewStyle().SetString("")
	// The message carries its own text, so it wants no string of its own.
	t.Focused.ErrorMessage = lipgloss.NewStyle().Foreground(colBad)
	t.Focused.TextInput.Prompt = accent
	t.Focused.TextInput.Cursor = accent
	t.Focused.TextInput.Placeholder = dim
	t.Focused.TextInput.Text = lipgloss.NewStyle()

	// A blurred field recedes but stays readable. Dimming it to near-invisible
	// is the contrast problem this palette exists to avoid.
	t.Blurred.Base = t.Blurred.Base.BorderForeground(colRule)
	t.Blurred.Title = dim
	t.Blurred.Description = dim
	t.Blurred.TextInput.Prompt = dim
	t.Blurred.TextInput.Placeholder = dim
	t.Blurred.TextInput.Text = lipgloss.NewStyle()
	t.Blurred.SelectedOption = dim
	t.Blurred.UnselectedOption = dim
	// A blurred list keeps its checkboxes — which options are ticked is the
	// content of the field, not decoration on the focused one.
	t.Blurred.SelectedPrefix = t.Blurred.SelectedPrefix.Foreground(colDim).SetString("[✓] ")
	t.Blurred.UnselectedPrefix = t.Blurred.UnselectedPrefix.Foreground(colDim).SetString("[ ] ")

	// The base theme's buttons are a pale grey on a pale grey, which on this
	// palette came out as two barely-visible smudges with no telling which one
	// was selected. Use the tab treatment instead: the active one filled, the
	// other plain readable text.
	t.Focused.FocusedButton = lipgloss.NewStyle().
		Bold(true).Foreground(colPaper).Background(colAccent).Padding(0, 2)
	t.Focused.BlurredButton = lipgloss.NewStyle().Padding(0, 2)
	t.Blurred.FocusedButton = t.Focused.FocusedButton
	t.Blurred.BlurredButton = t.Focused.BlurredButton

	t.Help.ShortKey = lipgloss.NewStyle().Bold(true)
	t.Help.ShortDesc = dim
	t.Help.ShortSeparator = dim
	t.Help.FullKey = lipgloss.NewStyle().Bold(true)
	t.Help.FullDesc = dim
	t.Help.FullSeparator = dim

	return t
}
