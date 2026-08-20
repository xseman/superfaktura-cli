# 006 · Testing

```
                    ┌───────────────────────────────┐
                    │  e2e, live account            │  needs credentials
                    │  build tag: e2e               │  SKIPS without them
                    │  reads, writes, cleans up     │  point at a sandbox
                    ├───────────────────────────────┤
                    │  e2e, local server            │  no credentials
                    │  encoding_test.go — every     │  runs in CI
                    │  write's method + body type   │
                    ├───────────────────────────────┤
                    │  golden frames                │  no credentials
                    │  the binary under a pty,      │  runs in CI
                    │  against a local server       │  Linux only
                    ├───────────────────────────────┤
                    │  unit                         │  no credentials
                    │  client vs httptest,          │  the bulk of it
                    │  TUI model vs messages,       │
                    │  decoders vs live fixtures    │
                    └───────────────────────────────┘
```

## Running

```sh
make check              # fmt-check vet lint tidy-check test-race
go test ./...           # unit only, fuzz corpora included
go test -tags e2e ./e2e/...
make seed               # creates records in a real account and LEAVES them
make fuzz NAME=FuzzWrapText TIME=60s
make security           # vuln + secrets
```

`golangci-lint` must be built with a Go at least as new as the toolchain
installed locally, or it panics while type-checking the standard library. That
is about the toolchain, not this module: `go.mod` targets 1.24, but a
1.25-built linter still cannot read a 1.26 stdlib. `mise install` gets a
matching build.

## Where a test belongs

| Testing                            | Put it in                                        |
| ---------------------------------- | ------------------------------------------------ |
| an API quirk, a response shape     | unit, against `httptest`                         |
| what the client *sends*            | `e2e/encoding_test.go` (local server)            |
| a decoder                          | unit, with a fixture copied from a live response |
| TUI behaviour                      | unit — the model is a plain state machine        |
| what the TUI *looks like*          | `internal/tui/frame_test.go` — a golden frame    |
| that the account really accepts it | `e2e/`, live                                     |

Keep new API quirks in unit tests rather than e2e. E2E costs quota and skips
when credentials are absent, so a quirk covered only there is a quirk that is
usually not covered.

## Fuzzing the hand-written parsers

Anything that takes a string a user typed has a fuzz target next to it, in
`fuzz_test.go` in the package that owns the parser. `make fuzz-list` names them;
`make fuzz NAME=… TIME=…` searches one.

A target asserts a **property**, not "it did not panic" — the panic is only the
cheapest property to violate. What each one pins:

| Target                 | The property                                                    |
| ---------------------- | --------------------------------------------------------------- |
| `FuzzDocumentItemSpec` | an accepted `--item` has a name and JSON-encodable numbers       |
| `FuzzSplitUsage`       | a form field always gets a label, in valid UTF-8                 |
| `FuzzCheckDate`        | nothing with a slash, and no ISO date that is not a real day     |
| `FuzzEscapeSegment`    | no raw `/`, space or control byte, and it decodes back unchanged |
| `FuzzParamsPath`       | a filter value cannot close its segment and start another        |
| `FuzzGet`              | any path against any decoded response, and nothing invented      |
| `FuzzWrapText`         | no line wider than the width it was given                        |
| `FuzzTruncate`         | no cell wider than its column                                    |

Seeds come from values the ordinary tests already use, so a target starts on
real input. Three properties were false when they were first written — amounts
of `NaN`, an empty flag name, and a word with no space to break on; the fixes
are in `parseNumber`, `splitUsage` and `wrapText`.

**A crasher lands as an ordinary test too.** `go test` replays
`testdata/fuzz/…` and the seed corpus on every run, but a named regression test
next to the parser says what the bug *was*, which a corpus file never does.

## The TUI is testable without a terminal

The model is a plain state machine: feed it messages, inspect the result. That
matters most for the quota rules — a browser that fetched when it should not
would be invisible in a screenshot and obvious in a bill. `harness()` counts
every load, and a controllable clock drives the cache without waiting.

```go
m, loads := harness(t, false)
m = switchTab(t, m)          // away
m = switchTab(t, m)          // and back — must not fetch again
if loads.Load()-before != 1 { … }
```

What it cannot say is what any of that *looked like*. Every visual bug this
browser has had — a select that offered no options, a key the pager swallowed
before the action saw it, labels that were whole sentences, a theme style that
dropped the string it carried — was found by driving a pty by hand and reading
the screen, and none of them showed up in a model test.

## The golden frame

`internal/tui/frame_test.go` is that session, written down. It builds the
binary, points it at an `httptest` server serving canned records, drives it
through a pseudo-terminal and replays the output onto a grid, then compares each
frame with a file in `internal/tui/testdata/`.

```sh
go test ./internal/tui -run TestTheBrowserDrawsItsFrames   # check
make frames-snapshot                                       # rewrite (-update)
```

`-update` rewrites every golden. Read the diff before committing it: the golden
*is* the layout, so a change to it is a change to what people see.

```
 6 | 309127  2026001  Acme s.r.o.   2024-03-01  787.20  487.20|   ← the screen
 7 | Delete invoice 2026001? (y/n)|                                 under the row
…
 6   0-100 reverse                                                ← the appearance
```

Two things about the format. Each line is bracketed and numbered, so trailing
blanks and the exact number of lines are both visible — the frame coming to
*exactly* the terminal height is the layout rule most easily broken. And under
the text is every styled run, because the selected row being padded to full
width before it is inverted is invisible in characters alone: without it the bar
stops where the text does, and the plain screen is identical.

Determinism is the whole point, so:

- **No credentials.** A local server answers with fixed records, and the
  `X-RateLimit-*` headers it sets are where the header's `876/1000` comes from.
- **The fixtures are dated in the past.** A badge and the overview's buckets are
  computed against today, so an invoice due next week would change the golden
  the moment the week turned.
- **The environment is built from scratch**, not inherited: `TERM` decides how
  every colour in the frame is rendered.
- **Ages are normalised.** "fetched 2s ago" is rewritten to "fetched just now" —
  the label is left alone, since `cached` against `fetched` is the fact worth
  pinning and only the number moves.

The pty is opened with the kernel's own ioctls rather than a dependency, which
is three calls and Linux-only; elsewhere the test skips. Everything else in it
is portable.

## E2E rules

- **They skip, never fail**, when credentials are missing or the account cannot
  reach the API. A blocked sandbox must read as "not run", not "broken".
- Credentials come from `.env.test` (gitignored) or the environment, with real
  environment variables winning so CI can override.
- Write tests create real records and clean up after themselves. `make seed` is
  the deliberate exception — it leaves them, needs `SF_SEED=1` on top of the
  build tag and credentials, and must never fire by accident.
- Three commands are never run automatically because they have real-world side
  effects: `invoice send` (email), `invoice post` (postage), `sms`.

## The surface snapshot

`SURFACE.txt` records every command, flag and positional argument. `make check`
fails until it matches — so a flag cannot be renamed without somebody noticing.

```sh
make surface-snapshot   # regenerate, commit in the same change
```

## Security scans

`make security` is `vuln` + `secrets`. Both **skip cleanly when the tool is not
installed**, so a fresh checkout never fails on a missing binary — and neither
is in `make check`.

- `vuln` runs govulncheck, which reports only advisories whose vulnerable
  *symbols* this module actually reaches. It stays out of `check` because it
  fetches the vulnerability database from `vuln.go.dev` on every run, and
  `check` has to work offline. Run it before a release.
- `secrets` runs gitleaks over the tree and its history. This CLI's whole job
  is to hold an API key, so a key pasted into a fixture or a commit message is
  the failure worth scanning for. It uses gitleaks' own rules; a
  `.gitleaks.toml` is picked up if one is added.

### The one advisory that is not being fixed

`make vuln` reports **GO-2026-5024** — an integer overflow in
`NewNTUnicodeString`, `golang.org/x/sys` — and will keep reporting it.

It is not called, it is Windows-only, and the module is an indirect dependency
nothing here imports. The fix is `x/sys@v0.44.0`, and that version **requires Go
1.25**, which would drag the `go` directive up with it. CI pins **1.24**
(`.github/workflows/ci.yml`), so taking the bump means raising the floor on who
can build this, in exchange for a vulnerability no code path reaches.

Revisit when the toolchain moves for a reason of its own.
