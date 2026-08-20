package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T, identity string) *Store {
	t.Helper()
	return Open(filepath.Join(t.TempDir(), "cache"), identity)
}

func TestAStoredEntryComesBack(t *testing.T) {
	s := newStore(t, "acct")
	s.Put("/countries", json.RawMessage(`[{"id":191}]`))

	got, ok := s.Get("/countries", time.Hour)
	if !ok {
		t.Fatal("expected a hit")
	}
	if string(got) != `[{"id":191}]` {
		t.Errorf("body = %s", got)
	}
}

func TestAnExpiredEntryIsAMiss(t *testing.T) {
	s := newStore(t, "acct")
	s.Put("/countries", json.RawMessage(`[]`))

	// The TTL is applied at read time, so shortening it takes effect at once
	// rather than after the previous one would have elapsed.
	if _, ok := s.Get("/countries", -time.Second); ok {
		t.Error("an entry past its TTL should be a miss")
	}
	if _, ok := s.Get("/countries", time.Hour); !ok {
		t.Error("the entry should still be readable under a longer TTL")
	}
}

func TestAccountsCannotSeeEachOthersEntries(t *testing.T) {
	// Two profiles on one machine share a directory. A tag list or bank
	// account leaking between them would be a real disclosure, so the account
	// identity is part of every key rather than left to the caller.
	dir := filepath.Join(t.TempDir(), "cache")
	first := Open(dir, "https://moja.superfaktura.sk\x00a@example.com\x001")
	second := Open(dir, "https://moja.superfaktura.sk\x00b@example.com\x002")

	first.Put("/tags/index.json", json.RawMessage(`{"1":"first"}`))

	if _, ok := second.Get("/tags/index.json", time.Hour); ok {
		t.Fatal("the second account read the first account's entry")
	}
	if _, ok := first.Get("/tags/index.json", time.Hour); !ok {
		t.Error("the owning account lost its own entry")
	}
}

func TestTheSameCompanyOnADifferentInstanceIsADifferentAccount(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	sk := Open(dir, "https://moja.superfaktura.sk\x00me@example.com\x001")
	cz := Open(dir, "https://moje.superfaktura.cz\x00me@example.com\x001")

	sk.Put("/bank_accounts/index", json.RawMessage(`{"BankAccounts":[]}`))
	if _, ok := cz.Get("/bank_accounts/index", time.Hour); ok {
		t.Error("the Czech instance read the Slovak instance's entry")
	}
}

func TestNoIdentifyingDetailReachesTheFilesystem(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	s := Open(dir, "https://moja.superfaktura.sk\x00secret@example.com\x0042")
	s.Put("/tags/index.json", json.RawMessage(`{}`))

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "example.com") || strings.Contains(e.Name(), "tags") {
			t.Errorf("filename %q leaks the account or the path", e.Name())
		}
	}
}

func TestEntriesAreNotWorldReadable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	s := Open(dir, "acct")
	s.Put("/tags/index.json", json.RawMessage(`{"1":"Confidential client"}`))

	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Fatal("nothing was written")
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s is %04o, want no group or world access", e.Name(), perm)
		}
	}
}

func TestADisabledStoreNeitherReadsNorWrites(t *testing.T) {
	s := newStore(t, "acct")
	s.Put("/countries", json.RawMessage(`[]`))

	s.Disabled = true
	if _, ok := s.Get("/countries", time.Hour); ok {
		t.Error("--no-cache should not read")
	}
	s.Put("/tags/index.json", json.RawMessage(`{}`))

	s.Disabled = false
	if _, ok := s.Get("/tags/index.json", time.Hour); ok {
		t.Error("--no-cache should not write either")
	}
}

func TestCorruptEntriesAreAMissRatherThanAFailure(t *testing.T) {
	// A truncated or hand-edited file should cost one request, never a failed
	// command.
	dir := filepath.Join(t.TempDir(), "cache")
	s := Open(dir, "acct")
	s.Put("/countries", json.RawMessage(`[]`))

	entries, _ := os.ReadDir(dir)
	path := filepath.Join(dir, entries[0].Name())
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt the entry: %v", err)
	}

	if _, ok := s.Get("/countries", time.Hour); ok {
		t.Error("a corrupt entry should be a miss")
	}
}

func TestClearRemovesEverythingAndToleratesAnAbsentDirectory(t *testing.T) {
	s := newStore(t, "acct")
	s.Put("/countries", json.RawMessage(`[]`))
	s.Put("/tags/index.json", json.RawMessage(`{}`))

	removed, err := s.Clear()
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed %d, want 2", removed)
	}
	if _, ok := s.Get("/countries", time.Hour); ok {
		t.Error("the entry survived Clear")
	}

	// Clearing a cache that was never written is a no-op, not an error.
	fresh := newStore(t, "acct")
	if removed, err := fresh.Clear(); err != nil || removed != 0 {
		t.Errorf("Clear on an empty cache = %d, %v", removed, err)
	}
}

func TestDifferentParametersAreDifferentEntries(t *testing.T) {
	s := newStore(t, "acct")
	s.Put("/countries", json.RawMessage(`"short"`))
	s.Put("/countries/view_full:1", json.RawMessage(`"full"`))

	got, _ := s.Get("/countries", time.Hour)
	if string(got) != `"short"` {
		t.Errorf("body = %s, the two variants collided", got)
	}
}
