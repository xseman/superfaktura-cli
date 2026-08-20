package tui

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe to read while the render loop writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// distinctFrames counts how many different braille glyphs were drawn, which is
// what distinguishes an animation from a single static render.
func distinctFrames(text string) int {
	seen := map[rune]bool{}
	for _, r := range text {
		if r >= '⠀' && r <= '⣿' {
			seen[r] = true
		}
	}
	return len(seen)
}

func TestNothingIsDrawnForAQuickOperation(t *testing.T) {
	// A spinner that flashes for a moment is noise. Anything that finishes
	// inside the grace period should leave no visible trace.
	var buf syncBuffer
	s := Start(&buf, "Loading invoices…")
	s.Stop()

	if got := buf.String(); strings.Contains(got, "Loading invoices…") {
		t.Errorf("the label was drawn for an instant operation: %q", got)
	}
}

func TestASlowOperationIsAnimatedAndThenErased(t *testing.T) {
	var buf syncBuffer
	s := Start(&buf, "Loading invoices…")
	time.Sleep(700 * time.Millisecond)

	during := buf.String()
	if !strings.Contains(during, "Loading invoices…") {
		t.Errorf("the label was never drawn: %q", during)
	}
	if n := distinctFrames(during); n < 2 {
		t.Errorf("only %d distinct frames drawn; it is not animating", n)
	}

	s.Stop()

	// Bubble Tea erases with ESC[2K before restoring the terminal, so the
	// result printed next starts on a clean row.
	tail := buf.String()[len(during):]
	if !strings.Contains(tail, "\x1b[2K") {
		t.Errorf("the line was not erased on stop: %q", tail)
	}
	if strings.Contains(tail, "Loading invoices…") {
		t.Errorf("the label survived the stop: %q", tail)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	// Stop is reached through a deferred call on every request path, and it
	// must not block or panic when it runs twice.
	var buf syncBuffer
	s := Start(&buf, "x")
	s.Stop()
	s.Stop()
}

func TestConcurrentStopsAreSafe(t *testing.T) {
	var buf syncBuffer
	s := Start(&buf, "Loading…")
	time.Sleep(300 * time.Millisecond)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Stop()
		}()
	}
	wg.Wait()
}

func TestStopWaitsForTheFinalRender(t *testing.T) {
	// If Stop returned before the erase was flushed, the result would be
	// printed on top of a half-drawn spinner.
	var buf syncBuffer
	s := Start(&buf, "Loading…")
	time.Sleep(400 * time.Millisecond)
	s.Stop()

	settled := buf.String()
	time.Sleep(200 * time.Millisecond)
	if buf.String() != settled {
		t.Error("output kept arriving after Stop returned")
	}
}
