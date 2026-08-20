package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzWrapText: the API's failure text, laid into the notice line.
//
// The frame has to come to exactly the terminal height, so a line wider than
// the width it was given is not cosmetic: the terminal wraps it itself and the
// footer goes off the bottom. The text is server-supplied and arrives in
// Slovak, so width means runes, and it can contain a token — an identifier, a
// URL, a base64 blob — with no space to break on.
func FuzzWrapText(f *testing.F) {
	seeds := []struct {
		text  string
		width int
	}{
		{"client_id: Klient nepatrí pod túto firmu.; due: Dátum splatnosti " +
			"nemôže byť skôr ako dátum vystavenia; name: Toto pole je povinné; " +
			"variable: Neplatný variabilný symbol; something: else entirely", 60},
		{"", 10},
		{"   ", 10},
		{"Dátum splatnosti nemôže byť skôr ako dátum vystavenia", 20},
		{strings.Repeat("x", 200), 10},
		{"see https://moja.superfaktura.sk/invoices/edit/1042?token=abcdefghijklmnop", 40},
		{"ďžťňáéíóúý", 3},
	}
	for _, seed := range seeds {
		f.Add(seed.text, seed.width)
	}

	f.Fuzz(func(t *testing.T, text string, width int) {
		// The only caller is noticeLines, which floors the width at 10; a
		// narrower one is still worth exercising, an absurd one is not.
		if width < 1 || width > 500 {
			t.Skip("the notice width is a terminal width")
		}
		if !utf8.ValidString(text) {
			t.Skip("the API sends UTF-8")
		}

		lines := wrapText(text, width)

		if len(lines) > 3 {
			t.Fatalf("wrapText(%q, %d) returned %d lines", text, width, len(lines))
		}
		for i, line := range lines {
			if n := utf8.RuneCountInString(line); n > width {
				t.Fatalf("wrapText(%q, %d) line %d is %d wide: %q", text, width, i, n, line)
			}
			if line == "" {
				t.Fatalf("wrapText(%q, %d) returned a blank line %d", text, width, i)
			}
			if line != strings.TrimSpace(line) {
				t.Fatalf("wrapText(%q, %d) line %d is not trimmed: %q", text, width, i, line)
			}
			if strings.ContainsAny(line, "\n\r\t") {
				t.Fatalf("wrapText(%q, %d) line %d carries whitespace that moves the cursor: %q",
					text, width, i, line)
			}
		}

		// Blank in, blank out — a notice with nothing in it must draw nothing
		// rather than an empty highlighted row.
		if strings.TrimSpace(text) == "" && len(lines) != 0 {
			t.Fatalf("wrapText(%q, %d) = %q, want nothing", text, width, lines)
		}

		// Nothing is invented: every rune came from the text, apart from the
		// space wrapText joins words with and the ellipsis it truncates with.
		joined := strings.Join(lines, "")
		for _, r := range joined {
			if r == '…' || r == ' ' {
				continue
			}
			if !strings.ContainsRune(text, r) {
				t.Fatalf("wrapText(%q, %d) produced a rune %q that was not in the text",
					text, width, r)
			}
		}
	})
}
