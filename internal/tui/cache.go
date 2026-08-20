package tui

import "time"

// A cache for the pages the browser has already fetched.
//
// It exists because the same request comes round again within seconds: walking
// four tabs and back costs ten requests, of which five are for data that was on
// screen moments earlier. Against a thousand-request daily allowance counted per
// company, half of a browsing session is worth reclaiming.
//
// It is deliberately not internal/cache. That one is on disk and outlives the
// process, which is why it refuses to hold documents at all: a stale invoice
// could be served to a script hours later that has no idea it is stale. This
// one is in memory, dies with the session, and can only ever answer the person
// who is watching the screen — so it can hold what the other one must not.
//
// Three things keep it honest:
//
//   - the TTL is short, so nothing here is old enough to make a decision on;
//   - r always bypasses it, so there is a way to demand the truth;
//   - a page served from here says so on screen, because quietly showing an
//     accounting figure from cache is the exact failure the disk cache's
//     narrow boundary exists to avoid.
//
// A write empties the whole thing. See purge.
const pageTTL = 30 * time.Second

// cacheKey identifies a request. It is the same triple a response carries back
// to prove it is still the one being awaited — a page is only interchangeable
// with another fetched under the same resource, page number and scope.
type cacheKey struct {
	resource int
	page     int
	scope    int
	period   int
}

type cacheEntry struct {
	page     Page
	storedAt time.Time
}

type pageCache struct {
	entries map[cacheKey]cacheEntry
	now     func() time.Time
}

func newPageCache(now func() time.Time) *pageCache {
	if now == nil {
		now = time.Now
	}
	return &pageCache{entries: map[cacheKey]cacheEntry{}, now: now}
}

// get returns a page and when it was originally fetched.
func (c *pageCache) get(key cacheKey) (Page, time.Time, bool) {
	if c == nil {
		return Page{}, time.Time{}, false
	}
	entry, ok := c.entries[key]
	if !ok || c.now().Sub(entry.storedAt) > pageTTL {
		return Page{}, time.Time{}, false
	}
	return entry.page, entry.storedAt, true
}

func (c *pageCache) put(key cacheKey, page Page) {
	if c == nil {
		return
	}
	c.entries[key] = cacheEntry{page: page, storedAt: c.now()}
}

// forget drops one entry, which is what an explicit refresh asks for.
func (c *pageCache) forget(key cacheKey) {
	if c == nil {
		return
	}
	delete(c.entries, key)
}

// purge empties the cache after a write.
//
// Everything, not merely the resource that was written: a payment against an
// invoice changes the invoice list and the overview's totals both, and there is
// no accounting for which figures a write touched without knowing more about
// the API than this package should. A handful of re-fetches is a small price
// for never showing a record the user just edited in its previous state.
func (c *pageCache) purge() {
	if c == nil {
		return
	}
	clear(c.entries)
}
