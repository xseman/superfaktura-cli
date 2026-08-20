---
name: superfaktura
description: Drive SuperFaktura (Slovak/Czech/Austrian invoicing) from the terminal with the `sf` CLI — issue and send invoices, manage clients and expenses, record payments, download PDFs, and read stock, cash registers, tags and exports. Use whenever the task involves SuperFaktura, superfaktura.sk/.cz/.at, or issuing an invoice against that account.
---

# SuperFaktura CLI

`sf` is a command-line client for the SuperFaktura API. Prefer it over raw
`curl`: it handles the SFAPI authorization header, the CakePHP-style path
filters, and the `data=` form encoding that the API requires.

## Before anything else

Check that credentials resolve. This is one cheap API call and it tells you
which company you are about to write to:

```sh
sf auth status --json
```

`authenticated: false` means stop and tell the user — do not guess at
credentials. Setup is `sf auth login` (interactive) or:

```sh
sf auth login <profile> --api-url https://moja.superfaktura.sk \
    --email EMAIL --api-key KEY --company COMPANY_ID
```

When the account looks wrong rather than absent — the writes are landing
somewhere unexpected, or the quota is draining — `sf doctor --json` reports
every saved profile's instance, company and credential storage. It sends
nothing to the API unless you add `--live`, which costs one request per
profile.

## Output contract

Every command emits one JSON document when not on a terminal:

```json
{ "ok": true, "data": ..., "meta": { "item_count": 2, "page_count": 1 } }
```

Failures emit `{"ok": false, "error": "...", "code": "...", "hint": "..."}`.

Exit codes: `0` ok, `1` usage, `2` not found, `3` auth, `4` forbidden,
`5` rate limited, `6` network, `7` API error, `8` ambiguous. Branch on these
rather than on message text — messages are localized to the account language.

Add `--json` to be explicit, `--quiet` for bare data, `--ids-only` for one ID
per line, `--count` for a number.

`sf ui` is a terminal browser for a person. It needs a terminal and is not for
you — use the commands, which emit JSON.

## Discovering commands

```sh
sf commands --json          # the whole tree with flags
sf --agent invoice --help   # one command as JSON
```

Do not guess flags. The catalog is authoritative.

## Common tasks

Issue an invoice for an existing client:

```sh
sf invoice create --client 42 --item 'Consulting:2:500:23' --due 2026-09-01
```

`--client` and `--tag` accept a name as well as an ID, so you do not have to
search first. If the name is ambiguous the command exits `8` and lists the
candidates with their IDs in `matches` — pick one and retry, do not guess.

Before any write you are unsure about, ask the CLI what it would do:

```sh
sf --dry-run invoice create --client 'Acme s.r.o.' --item 'Consulting:2:500:23'
```

Reads still run, so the plan shows the payload with names already resolved.
Show it to the user when the write is significant.

`--item` is `name:quantity:unit_price:tax`, repeatable. Anything the flags do
not cover goes through `--data`, which takes the raw API payload:

```sh
sf invoice create --data '{"Invoice":{"name":"X"},"InvoiceItem":[{"name":"Y","unit_price":10,"tax":23}]}'
sf invoice create --data @invoice.json
```

Flags are layered on top of `--data`, so a stored template can be adjusted per
invocation.

Send it and record the payment:

```sh
sf invoice send 1042 --to client@example.com
sf invoice pay 1042 --amount 1240 --type transfer --date 2026-08-01
sf invoice pdf 1042 -o faktura.pdf
```

Find things:

```sh
sf client list --search 'Acme' --json
sf invoice list --status 99 --json          # 99 is overdue
sf expense list --type invoice --per-page 50
```

## Things that will trip you up

- **Statuses and types are numeric or coded.** Run `sf values` for the list of
  enumerations and `sf values invoice-statuses` for one of them. Never invent a
  code.
- **Filters not exposed as flags** go through `--filter key=value`, repeatable:
  `sf invoice list --filter created=3 --filter created_since=2026-01-01`. Date
  ranges need the matching `created:3`-style constant, see
  `sf values time-filters`.
- **The quota is small and hard.** 1000 requests per day and 30 000 per month,
  shared across everything using this account. **Once the daily limit is
  reached every GET is blocked for the rest of the day**, so an agent that
  burns it has broken the user's invoicing until midnight. `sf limits` reports
  what is left. Never poll in a loop, and prefer one list call with a filter
  over many detail calls — a list already returns complete records.
- **`--all` follows every page.** On a large account that is dozens of requests
  from one command. Without it you get one page and `meta.page_count` tells you
  how many exist. Ask before running `--all` on an account you have not sized.
- **`sf invoice pdf` costs two requests the first time** — the PDF is addressed
  by a token that lives on the invoice detail — and one thereafter, because the
  token is cached.
- **`sf invoice view` takes several IDs** and fetches up to 100 in one request.
  Never loop `view` over a list.
- **A rejected write reports `fields`** in the envelope, naming what the API
  objected to. Read that instead of the prose message before retrying, and do
  not retry unchanged.
- **Other limits:** 100 emails/hour (`invoice send`), 4 MB attachments,
  `--per-page` maxes at 200 for invoices but 100 for expenses.
- **A create that times out may still have succeeded.** Pass
  `--checksum <your-id>` when creating an invoice, then `sf invoice recover
  <your-id>` returns the original response instead of leaving you to guess.
  Never blindly retry a create — you will issue the invoice twice.
- **Writes are real.** Creating, editing, paying and deleting all take effect
  immediately and appear in the user's accounting. Confirm destructive or
  outward-facing actions — `invoice send`, `invoice delete`, `client delete`,
  `sms` — with the user before running them.
- **One account can own several companies.** `sf company list` shows them;
  `--company ID` picks one for a single invocation.
- **Value lists are cached for a day**, the PDF token for a month. Documents
  never are, so a list is always live. Pass `--no-cache` if a tag or bank
  account was just changed elsewhere and you need to see it now.
- **Money is a string in responses.** `"12.000000"` and `"12.00"` are the same
  amount. Parse, do not compare textually.
