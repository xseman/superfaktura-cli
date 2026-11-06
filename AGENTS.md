# Development context

A CLI and terminal browser for the SuperFaktura API.

Full documentation is in **[docs/](docs/)**. This file is the short list of
things that are easy to break and expensive to notice — read the relevant doc
before changing anything it covers, rather than inferring the design from the
code. Keep each fact in one place: when behaviour changes, update the doc that
owns it rather than restating it elsewhere — duplicated knowledge in this repo
has already drifted into false claims once.

| Doc                                                  | Covers                                                           |
| ---------------------------------------------------- | ---------------------------------------------------------------- |
| [000 · Architecture](docs/000-architecture.md)       | packages, request lifecycle, dependencies                        |
| [001 · API integration](docs/001-api-integration.md) | URLs, write encoding, error classification, rate limits, dry run |
| [002 · CLI](docs/002-cli.md)                         | adding a command, output contract, exit codes, name resolution   |
| [003 · TUI](docs/003-tui.md)                         | the browser: layout, message loop, forms, load rules             |
| [004 · Caching](docs/004-caching.md)                 | the disk cache, the session cache, the boundary                  |
| [005 · Configuration](docs/005-configuration.md)     | profiles, credential precedence, secrets                         |
| [006 · Testing](docs/006-testing.md)                 | what goes where, e2e rules, the surface snapshot                 |
| [007 · Releasing](docs/007-releasing.md)             | version bumping, the release workflow, the binaries              |
| API-DISCREPANCIES.md (local, untracked)              | every place the docs and the live API disagree                   |

## Invariants

Breaking one of these produces a bug that looks like something else.

**API** — [001](docs/001-api-integration.md)

- The API's four departures from REST are each absorbed in exactly one place.
  Do not re-handle them per command.
- Which writes send raw JSON and which send `data=<json>` is decided per
  endpoint and pinned by `e2e/encoding_test.go`. Do not "tidy" it into one rule.
- The body decides whether a response is an error; the status only refines it.
  A failure can arrive as HTTP 200.
- Before trusting a response shape you have not seen on the wire, check
  `API-DISCREPANCIES.md` (kept locally, untracked) — and add to it when you
  find a new one.

**Quota** — the constraint the whole design serves

- 1000 requests/day, counted **per `company_id`**, not per key. A wrong company
  ID does not fail; it spends someone else's allowance.
- List rows are complete records. Anything that fetches a detail the list
  already contains turns a browse into a request per keystroke.

**Output** — [002](docs/002-cli.md)

- One invocation emits exactly one document. Never print directly; go through
  `emit`, or `--json`, `--quiet`, `--ids-only`, `--count` and `--jq` break.
- Return `*output.Error` with the right `Code` — that is what sets the exit
  code.
- Name the model at the `emitWrite` call site. Guessing it alphabetically made
  every invoice create report its client's ID.
- Machine output carries the API's bytes verbatim — never re-marshal a
  response through Go maps on the way out. A map re-sorts every key and a
  consumer diffing two versions drowns in reshuffles that changed nothing;
  that flood happened once.
- The command surface is pinned locally: `make surface-snapshot` writes
  `SURFACE.txt` (untracked), and the test diffs against it while it exists —
  read what moved, because a rename breaks somebody's script.

**TUI** — [003](docs/003-tui.md)

- Nothing polls. Only `r`, a tab switch or a page change fetches.
- The view must come to exactly the terminal height.
- A form takes every message, not only keys.
- A response must say which request it answers, or a slow reply lands in
  whatever tab is now open.
- Edit forms send only fields whose value actually changed.
- The golden frames must match. `go test ./internal/tui -run
  TestTheBrowserDrawsItsFrames -update`, committed in the same change — and read
  the diff, because it is the layout.

**Caching** — [004](docs/004-caching.md)

- The disk cache never holds documents. The session cache may, because it dies
  with the process. Do not merge them.
- Any write purges the session cache entirely.

## Checks

```sh
make check   # fmt-check vet lint tidy-check test-race
```

`golangci-lint` needs to be built with a Go at least as new as the local
toolchain or it panics while type-checking the standard library — `go.mod`
targets 1.24, but a 1.25-built linter still cannot read a 1.26 stdlib. Use a
current release binary, or build one with your own toolchain:
`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`.

## What the real API confirmed

Verified against a live sandbox company: reads, writes, an invoice with line
items, a payment, a PDF download, an expense carrying a base64 attachment, and
recovery of a response by checksum. The form-versus-JSON split is confirmed
working for every write that run exercised; the rest is pinned by tests but
unconfirmed server-side.
