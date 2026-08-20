# 004 · Caching

Two caches. They are not the same thing and the boundary between them is the
whole design.

```
 ┌──────────────────────────────┐   ┌──────────────────────────────┐
 │ internal/cache               │   │ internal/tui/cache.go        │
 │ ──────────────               │   │ ─────────────────────        │
 │ on disk                      │   │ in memory                    │
 │ outlives the process         │   │ dies with the session        │
 │ read by any later invocation │   │ only ever answers the person │
 │                              │   │ watching the screen          │
 │ TTL: 1 day / 1 month         │   │ TTL: 30 seconds              │
 │                              │   │                              │
 │ HOLDS                        │   │ HOLDS                        │
 │  value lists, tags,          │   │  any page the browser        │
 │  bank accounts, companies,   │   │  fetched — invoices and      │
 │  expense categories,         │   │  expenses included           │
 │  sequences, logos,           │   │                              │
 │  per-invoice PDF token       │   │                              │
 │                              │   │                              │
 │ NEVER HOLDS                  │   │ SAFE BECAUSE                 │
 │  invoices, expenses,         │   │  30s is too short to decide  │
 │  clients — a stale invoice   │   │  on · r always bypasses ·    │
 │  could reach a script hours  │   │  a cached page says so ·     │
 │  later with no way to know   │   │  a write purges all of it    │
 └──────────────────────────────┘   └──────────────────────────────┘
```

The disk cache refuses documents *because* it is on disk. The session cache can
hold what the other must not, precisely because it cannot outlive the person
looking at it.

## Disk cache — `internal/cache`

**Do not widen the boundary casually.** A cached invoice list could report a
paid invoice as unpaid, which costs more than the request it saved.

```go
cachedGet(cmd, path, params, ttl)   // cached
api.Get(ctx, path, params)          // not cached
```

Entries are keyed by `path + params.CacheKey()`, and the store is scoped to
`baseURL + email + companyID`, hashed — so profiles cannot read each other and
no identifying detail reaches a filename.

The TTL is supplied at **read** time, not write time, so changing a TTL takes
effect immediately instead of after the old one expires.

**Failures are silent by design.** A corrupt or unwritable entry costs one
request, never a failed command.

```sh
sf cache clear      # delete every entry
sf --no-cache …     # bypass for one invocation
```

## Session cache — `internal/tui/cache.go`

Walking four tabs and back cost ten requests, five of them for rows that had
been on screen moments earlier. Measured against the sandbox, the same eight
keystrokes now cost three.

The key is what a response already carries to prove it is still awaited —
a page is only interchangeable with one fetched under the same four:

```
   cacheKey{ resource, page, scope, period }
```

### Lifetime

```
  t+0s   tab → Invoices        MISS  → request      ┐
  t+3s   tab → Clients         MISS  → request      │
  t+6s   tab → Invoices        HIT   → 0 requests   │ 30s
  t+9s   press r               BYPASS→ request      │ window
  t+12s  tab → Clients         HIT   → 0 requests   │
  ───────────────────────────────────────────────   ┘
  t+40s  tab → Invoices        EXPIRED → request

  any write, at any point:
         ████ purge everything ████
```

### The three things that keep it honest

1. **30 seconds is too short a window to make a decision inside.**
2. **`r` always bypasses it** — the way to demand the truth. It drops only the
   current view, not everything fetched.
3. **The age of what is on screen is always shown**, above the list:
   `fetched just now`, `cached 12s ago`. Always, not only on a cache hit — a
   band that stayed blank until the first cached page and then grew a marker
   changed shape under the reader, and a resource with no filters (the
   Overview) had nothing in that band at all.

   "cached" and "fetched" are the same fact from the reader's side — how far
   behind the server this might be — but the distinction says whether the quota
   moved. The age is computed at render time, not stored: nothing here polls,
   so a stored string would be frozen at the last keystroke and slowly become a
   lie.

Quietly showing an accounting figure from cache is the exact failure the disk
cache avoids by refusing to hold documents at all. This one holds them and
admits it instead.

### A write purges all of it

Not just the resource written. A payment against an invoice moves the overview's
totals too, and this package cannot know which figures a write touched without
knowing more about the API than it should. A handful of re-fetches beats ever
showing a record in the state it was in before the user edited it.

Error responses are never cached.

The client roster the create form lists is **not** cached either, and that is
the same reasoning from the other side: a client created minutes ago in the web
application has to be selectable, and a day-old list would not have it. It costs
nothing extra — typing a name already spends a request, since `resolveClient`
searches for it, so the list costs the same one and answers with every name at
once instead of an ambiguity error.
