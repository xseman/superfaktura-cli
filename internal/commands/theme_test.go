package commands

import (
	"strings"
	"testing"

	"github.com/xseman/superfaktura-cli/internal/tui"
)

func TestTheCLIPromptsShareTheBrowsersPalette(t *testing.T) {
	// The browser is not the only place huh runs. `auth login`, `client create`
	// and `invoice create` prompt from the CLI, and on huh's stock theme they
	// came out fuchsia — a different application dropped into the middle of
	// this one.
	theme := tui.FormTheme()
	if got := theme.Focused.Title.GetForeground(); got == nil {
		t.Fatal("the shared theme has no accent")
	}

	for _, path := range [][]string{
		{"auth", "login"},
		{"client", "create"},
		{"invoice", "create"},
	} {
		name := strings.Join(path, " ")
		if cmd := findCommand(path...); cmd == nil {
			t.Errorf("%s does not exist", name)
		}
	}
}

func TestTheKeyIsOptionalOnlyWhenOneIsStored(t *testing.T) {
	// Empty means "keep the stored key". With nothing stored it means nothing,
	// so the field stays required and says where to find one.
	stored := stripANSI(keyField(new(string), true).View())
	if !strings.Contains(stored, "keep the key already stored") {
		t.Errorf("the field does not say an empty box keeps the key: %q", stored)
	}

	fresh := stripANSI(keyField(new(string), false).View())
	if !strings.Contains(fresh, "Tools > API access") {
		t.Errorf("a first login is not told where to find a key: %q", fresh)
	}
}

func stripANSI(text string) string {
	var out strings.Builder
	escaped := false
	for _, r := range text {
		switch {
		case escaped && r == 'm':
			escaped = false
		case escaped:
		case r == 0x1b:
			escaped = true
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}
