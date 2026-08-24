# 000 · Architecture

A CLI and a terminal browser for the SuperFaktura accounting API.

## The constraint everything is shaped by

**1000 requests per day, 30 000 per month, counted per `company_id`.** Not per
API key — a wrong company ID does not fail, it spends someone else's allowance.

Every design decision below is downstream of that number.

## The fact everything else exploits

**A list row is a complete record.** `/invoices/index.json` returns the line
items, payments, tags and client — everything the detail endpoint returns. So
opening a record costs nothing, which is the entire reason a TUI is affordable
here.

```
   naive browser                     this browser
   ─────────────                     ────────────
   list          1 request           list       1 request
   open row 1    1 request           open row 1 0
   open row 2    1 request           open row 2 0
   …             …                   …          0
   ─────────────────────             ────────────────────
   20 rows       21 requests         20 rows    1 request
```

## Packages

```
        cmd/sf                    entrypoint, calls commands.Execute
          │
          ▼
   internal/commands   ◄──────────────┐  the cobra tree, one file per resource
          │                           │
          ├──────────────┬────────────┤
          ▼              ▼            │
   internal/client  internal/render   │  HTTP + response shapes │ tables, paths
          │              ▲            │
          ▼              │            │
   internal/output  internal/cache    │  envelope + exit codes  │ disk cache
                                      │
   internal/tui  ────────────────────-┘  the `sf ui` browser + spinner
   internal/config                       profiles and credentials
```

`internal/tui` knows about tables and keystrokes, never about invoices.
`commands/ui.go` passes in the resources, columns and actions — and they are
the *same* values the CLI prints, so the two views cannot drift apart.

| Package             | Holds                                                 |
| ------------------- | ----------------------------------------------------- |
| `cmd/sf`            | entrypoint                                            |
| `internal/client`   | HTTP, URL building, error classification, rate limits |
| `internal/commands` | the command tree, one file per resource               |
| `internal/config`   | profile resolution, keyring, credential precedence    |
| `internal/render`   | tables, detail views, dotted-path lookup              |
| `internal/output`   | the envelope, the error type, the exit-code rubric    |
| `internal/cache`    | disk cache for responses that change rarely           |
| `internal/tui`      | the browser and the progress indicator                |
| `e2e/`              | tests against a real account (build tag `e2e`)        |
| `internal/skills`   | the agent skill, embedded into the binary             |

## Request lifecycle

```
  sf invoice list --unpaid --json
      │
      ▼
  ┌─────────────────┐
  │ cobra parses    │  flags → listOptions
  └────────┬────────┘
           ▼
  ┌─────────────────┐
  │ resolve names   │  --client "Acme" → client_id 7   (costs a lookup)
  └────────┬────────┘
           ▼
  ┌─────────────────┐   hit
  │ cache?          ├────────────► body
  └────────┬────────┘
           │ miss
           ▼
  ┌─────────────────┐
  │ client.Get      │  /invoices/index.json/listinfo:1/status:1|2
  └────────┬────────┘
           ▼
  ┌─────────────────┐   error:1 / HTML / 4xx
  │ classify        ├──────────────────────► *output.Error → exit 1..8
  └────────┬────────┘
           ▼
  ┌─────────────────┐
  │ decode list     │  one of four shapes
  └────────┬────────┘
           ▼
  ┌─────────────────┐
  │ emit            │  table │ JSON envelope │ --quiet │ --ids-only │ --jq
  └─────────────────┘
```

## Where to read next

| If you are                              | Read                                            |
| --------------------------------------- | ----------------------------------------------- |
| Adding or changing a command            | [002 · CLI](002-cli.md)                         |
| Touching HTTP, URLs or error handling   | [001 · API integration](001-api-integration.md) |
| Working on `sf ui`                      | [003 · TUI](003-tui.md)                         |
| Changing what gets cached               | [004 · Caching](004-caching.md)                 |
| Touching credentials or profiles        | [005 · Configuration](005-configuration.md)     |
| Writing tests                           | [006 · Testing](006-testing.md)                 |
| Cutting a release                       | [007 · Releasing](007-releasing.md)             |
| Unsure whether a response shape is real | `API-DISCREPANCIES.md` (local, untracked)       |

## Dependencies

Deliberately small and mainstream. Everything else is ours.

| Dependency                            | For                                              |
| ------------------------------------- | ------------------------------------------------ |
| `spf13/cobra`                         | the command tree                                 |
| `charmbracelet/bubbletea` + `bubbles` | the TUI runtime and the spinner                  |
| `charmbracelet/huh`                   | interactive forms                                |
| `zalando/go-keyring`                  | the system keychain, with a 0600 file fallback   |
| `itchyny/gojq`                        | `--jq`, so a filter works without jq on the PATH |
| `mattn/go-isatty`                     | terminal detection                               |

`internal/output` stays ours on purpose: it is the CLI's contract with its
callers, and a third party's release should not be able to change what a script
sees.
