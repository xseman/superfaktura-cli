// Package cache stores responses that change rarely, to spend fewer of the
// 1000 API requests SuperFaktura allows per day.
//
// Only value lists belong here — countries, tags, bank accounts, the company
// roster, and the per-invoice PDF token. Documents deliberately do not: an
// invoice list served from cache could show a paid invoice as unpaid, and in
// accounting that is a worse failure than another request.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Store is a cache scoped to one account.
type Store struct {
	dir string
	// scope distinguishes accounts. Two profiles on the same machine must
	// never see each other's tags or bank accounts, so it is mixed into every
	// key rather than trusted to the caller.
	scope string
	// Disabled short-circuits every operation, for --no-cache.
	Disabled bool
}

type entry struct {
	// StoredAt is compared against the TTL supplied at read time, so changing
	// a TTL takes effect immediately instead of after the old one expires.
	StoredAt time.Time       `json:"stored_at"`
	Body     json.RawMessage `json:"body"`
}

// Open returns a store under dir, scoped to an account identity. The identity
// is hashed, so no email or key reaches the filesystem.
func Open(dir, identity string) *Store {
	sum := sha256.Sum256([]byte(identity))
	return &Store{dir: dir, scope: hex.EncodeToString(sum[:8])}
}

// Dir reports where entries are written.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(key string) string {
	sum := sha256.Sum256([]byte(s.scope + "\x00" + key))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:16])+".json")
}

// Get returns a cached body if one exists and is younger than ttl.
//
// Any problem reading or decoding is a miss, not an error: a corrupt cache
// should cost one request, never a failed command.
func (s *Store) Get(key string, ttl time.Duration) (json.RawMessage, bool) {
	if s == nil || s.Disabled || s.dir == "" {
		return nil, false
	}

	raw, err := os.ReadFile(s.path(key))
	if err != nil {
		return nil, false
	}
	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, false
	}
	if time.Since(e.StoredAt) > ttl {
		return nil, false
	}
	return e.Body, true
}

// Put stores a body. Failures are silent for the same reason: caching is an
// optimisation, and a command that already has its answer should not fail
// because the disk is full.
func (s *Store) Put(key string, body json.RawMessage) {
	if s == nil || s.Disabled || s.dir == "" {
		return
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return
	}

	encoded, err := json.Marshal(entry{StoredAt: time.Now(), Body: body})
	if err != nil {
		return
	}

	// Write and rename, so a reader never sees a half-written entry.
	tmp, err := os.CreateTemp(s.dir, "entry-*.tmp")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(name, s.path(key))
}

// Clear removes every entry, for all accounts on this machine.
func (s *Store) Clear() (int, error) {
	if s == nil || s.dir == "" {
		return 0, nil
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}

	removed := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, e.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
