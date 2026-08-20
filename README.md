# superfaktura-cli

Command-line interface for the [SuperFaktura](https://www.superfaktura.sk) API.

Issue and send invoices, manage clients and expenses, record payments, download
PDFs, and read stock, cash registers, tags and exports — without leaving the
terminal.

```console
$ sf invoice list --status 99
ID    NUMBER   CLIENT       ISSUED      DUE         TOTAL    TO PAY
1042  2026001  Acme s.r.o.  2026-07-01  2026-07-15  1240.00  1240.00
1039  2025214  Beta a.s.    2026-06-02  2026-06-16   380.50   380.50

$ sf invoice list --status 99 --json | jq '[.data[].Invoice.id]'
```

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/xseman/superfaktura-cli/master/scripts/install.sh | bash
```

Or with Go:

```sh
go install github.com/xseman/superfaktura-cli/cmd/sf@latest
```

> **Name collision.** The binary is `sf`, which the Salesforce CLI also claims.
> If you use both, the installer will tell you which one wins on your `PATH`.

## Getting started

Find your credentials in SuperFaktura under **Tools › API access**, then:

```sh
sf auth login
```

The prompt asks for the instance, your email, the API key and an optional
company ID. To script it:

```sh
sf auth login work \
    --api-url https://moja.superfaktura.sk \
    --email you@example.com \
    --api-key YOUR_KEY \
    --company 12345
```

The API key goes into the system keyring when one is available and into a
`0600` file otherwise. Everything else lands in
`~/.config/sf/config.json`.

Verify it works — this makes one real API call:

```sh
sf auth status
```

## Profiles

A profile is one account on one instance: base URL, email, API key, company.
Keep as many as you need — a Slovak account and a Czech one, production and
sandbox.

```sh
sf auth list                        # * marks the default
sf auth switch sandbox
sf --profile sandbox invoice list   # one invocation only
sf auth logout sandbox
```

Resolution order, highest first: command-line flags, then environment
(`SF_PROFILE`, `SF_API_URL`, `SF_EMAIL`, `SF_APIKEY`, `SF_COMPANY_ID`,
`SF_MODULE`), then the selected profile, then defaults.

One SuperFaktura login can own several companies. `sf company list` shows them;
`--company ID` targets one for a single command.

`sf auth status` checks the profile in use. `sf doctor` checks them all —
instance, email, key storage and company for every saved profile, side by side,
because the daily request limit is counted per company and a profile pointing
at the wrong one spends somebody else's allowance without ever failing.

```sh
sf doctor           # reads the config only; sends nothing
sf doctor --live    # also verifies each profile and shows its remaining quota,
                    # at the cost of one request per profile
```

It exits non-zero when a check fails, so `sf doctor || sf auth login` works in
a script.

## Output

Output adapts to where it goes: a table on a terminal, a JSON envelope when
piped or redirected.

| Flag         | Output                                  |
| ------------ | --------------------------------------- |
| *(none)*     | table on a TTY, JSON envelope otherwise |
| `--json`     | JSON envelope, always                   |
| `--styled`   | table, always                           |
| `--quiet`    | the data alone, no envelope             |
| `--ids-only` | one ID per line                         |
| `--count`    | the number of results                   |
| `--jq EXPR`  | filter the JSON with a jq expression    |

The envelope is stable:

```json
{ "ok": true, "data": [...], "meta": { "item_count": 2, "page_count": 1 } }
```

Errors carry a machine-readable code and, where one helps, a hint:

```json
{ "ok": false, "error": "Invoice not found: 999", "code": "not_found" }
```

A rejected write also carries the per-field detail, so a caller can fix the
payload without parsing prose, and an ambiguous reference carries its
candidates:

```json
{ "ok": false, "code": "api_error",
  "fields": { "client_id": ["Klient nepatrí pod túto firmu."] } }

{ "ok": false, "code": "ambiguous",
  "matches": ["Acme s.r.o. (7)", "Acme s.r.o. Trading (8)"] }
```

### Exit codes

| Code | Meaning           | Code | Meaning       |
| ---: | ----------------- | ---: | ------------- |
|  `0` | success           |  `5` | rate limited  |
|  `1` | usage error       |  `6` | network error |
|  `2` | not found         |  `7` | API error     |
|  `3` | not authenticated |  `8` | ambiguous     |
|  `4` | forbidden         |      |               |

Branch on these rather than on message text — SuperFaktura localizes messages
to the account's language.

## Commands

```console
$ sf commands
```

| Group           | What it covers                                            |
| --------------- | --------------------------------------------------------- |
| `invoice`       | create, edit, list, view, delete, PDF, send, pay, related |
| `client`        | CRUD plus `client contact` for contact people             |
| `expense`       | CRUD, payments, attachments, categories, related items    |
| `stock`         | items and movements                                       |
| `cash-register` | registers and their items, receipts                       |
| `tag`           | tags for invoices, expenses and clients                   |
| `bank-account`  | bank accounts shown on documents                          |
| `export`        | bulk PDF/XLS export: create, poll, download               |
| `company`       | which companies these credentials can reach               |
| `bank-moves`    | imported bank statement lines                             |
| `activity`      | a document's activity log                                 |
| `sms`           | SMS payment reminders                                     |
| `values`        | the enumerations the API accepts                          |
| `limits`        | remaining daily and monthly request quota                 |
| `auth`          | credentials and profiles                                  |
| `skill`         | the agent skill embedded in this binary                   |

### Writing records

Every write command takes first-class flags for the common fields:

```sh
sf client create --name 'Acme s.r.o.' --ico 46655034
sf invoice create --client 42 --item 'Consulting:2:500:23' --due 2026-09-01
sf expense add --name 'Hosting' --amount 49 --vat 23 --type invoice
sf invoice pay 1042 --amount 1240 --type transfer
```

`create` and `edit` take the same fields, so nothing has to be set at creation
and corrected afterwards — including the issue date, which is what makes a
back-dated invoice one call rather than two:

```sh
sf invoice create --client 'Acme s.r.o.' \
  --item 'Konzultácie:8:75:23' --item 'Cestovné:1:40:23' \
  --created 2026-07-01 --due 2026-07-15 \
  --constant 0308 --payment-type transfer
```

Dates are more forgiving than the documentation suggests — the server runs them
through `strtotime`, so `--due '+14 days'` and `--due 'next friday'` work as
well as `2026-07-15` or `15.07.2026`. Slashes are refused: they are read in
American month/day order, so `08/09/2026` would silently book the 9th of August.

`--client` and `--tag` take a name as readily as an identifier. A name costs
one lookup and fails rather than guesses:

```sh
sf invoice create --client 'Acme s.r.o.' --item 'Consulting:2:500:23' --tag urgent
```

If several clients match, the command exits `8` and lists the candidates with
their IDs, so the next attempt is unambiguous. Tag *names* matter here beyond
convenience: the API silently ignores them and only stores numeric IDs, so
sending a name directly would look like success and save nothing.

### Seeing what a write would do

`--dry-run` prints the request instead of sending it. Reads still run, so the
plan shows the payload after names have been resolved — which is the part worth
checking:

```console
$ sf --dry-run invoice create --client 'Acme s.r.o.' --item 'Consulting:2:500:23'
POST /invoices/create
Content-Type: application/x-www-form-urlencoded; charset=UTF-8

{
  "Invoice": { "client_id": "7" },
  "InvoiceItem": [ { "name": "Consulting", "quantity": 2, "tax": 23, "unit_price": 500 } ]
}

Not sent (--dry-run).
```

`--item` is `name:quantity:unit_price:tax` and repeats. It belongs to creation
only — changing what is on a document is a separate pair of commands, because
the API **appends** items rather than replacing them:

```sh
sf invoice item add --invoice 309101 --item 'Consulting:2:500:23'
sf invoice item delete 804905 --invoice 309101
sf expense item add --expense 4505 --item 'Hosting:1:49:23'
```

An `--item` flag on `edit` would read as "these are the items" and would in
fact double them. The item's own ID is in the first column of `sf invoice view`,
which is what `item delete` takes.

For anything the flags do not cover, `--data` takes the raw API payload as
inline JSON, `@file`, or `-` for stdin:

```sh
sf invoice create --data @invoice.json
sf invoice create --data '{"Invoice":{"name":"X"},"InvoiceItem":[{"name":"Y","unit_price":10,"tax":23}]}'
```

Flags are layered on top of `--data`, so one stored template can be adjusted
per invocation.

### Filtering lists

Common filters have flags. The rest — SuperFaktura documents twenty-odd per
resource — go through `--filter key=value`, which repeats:

```sh
sf invoice list --type proforma --per-page 50
sf invoice list --filter created=3 --filter created_since=2026-01-01
```

Date-range filters need the matching time-filter constant; `sf values
time-filters` lists them. `--all` follows every page, which on a large account
can consume a lot of quota — `sf limits` shows what is left.

## Browsing

`sf ui` opens a terminal browser over the account.

```console
 Overview  Invoices  Expenses  Clients                                   836/1000

 filter:  Unpaid    period:  This year    fetched just now

 ID      NUMBER   CLIENT           ISSUED      DUE         TOTAL   TO PAY
 309125  2026004  Acme s.r.o.      2026-08-01  2026-08-15  61.50   61.50
 309123  2026002  Acme s.r.o.      2026-08-01  2026-08-15  246.00  246.00

                                                              enter expand
 2026002  ·  Acme s.r.o.                                       246.00 EUR
 regular  ·  due 2026-08-15                                        UNPAID
 ─────────────────────────────────────────────────────────────────────────
 QTY  ITEM          UNIT   VAT   TOTAL
   2  Consulting   100.00  23%  246.00
 ─────────────────────────────────────────────────────────────────────────
                                  Net     200.00
                                  VAT      46.00
                                  Total   246.00
                                  To pay  246.00

 page 1 of 1 · 6 records
 ↑/↓ navigate · / filter · enter expand · r refresh · f filter · t period · …
```

The first tab is an overview of what needs attention; `tab` moves between it
and invoices, expenses and clients. `enter` gives a record the whole screen —
while it is open the tabs are locked, since switching underneath would swap the
account out from behind a detail that still looked like the old one.

### Creating and editing

`n` creates, `e` edits, `d` deletes. Invoices also take `p` for a payment, `s`
to mark sent and `P` for the PDF.

The form is generated from the command's own flags, so it offers exactly what
`sf invoice create --help` does — a flag added to the command appears in the
form the same day.

```
   Client ID or name
   > Acme s.r.o.                        names are resolved, ids accepted

   Line items                           one per line
   2 items · 640.00 + 147.20 VAT = 787.20
   Konzultácie:8:75:23
   Cestovné:1:40:23

   Document type
   ▸ regular invoice                    a fixed set is a list, not a box
     proforma invoice

   Tags
     [✓] urgent                         several, ticked by name
     [ ] archive

     [ Create ]  Cancel
```

Three things make the form safe to open:

- **An edit form shows the record's current values**, so it is clear what is
  about to be overwritten. Only fields whose value you actually change are
  sent — confirming an untouched form writes nothing.
- **A value the API takes from a fixed set is a list**, so a typo is not a
  possible keystroke. An unset field keeps a `— not set —` option and stays
  there.
- **Line items are validated as they are typed** and totalled underneath. The
  total is the arithmetic the server will do, done twice, because a figure that
  is not the one you expected is worth seeing before the invoice exists.

Anything that writes asks first, under the row it names, and a refusal from the
API is reported in the same place rather than at the foot of the screen.
`--dry-run` puts the browser in read-only mode, where those actions are absent
from the footer rather than merely refused.

### What costs a request

Three narrowings sit above the list and are named differently because they cost
differently. `f` cycles a **scope** — all, unpaid, overdue, paid — and `t` a
**period** — all time, this month, this year, last year. Both are server-side
and cost one request. `/` filters **this page**, over rows already loaded, and
costs nothing.

**Opening a record costs no request.** The API returns complete records in a
list — line items, payments, tags and all — so the detail pane is rendered from
the row already in hand. Browsing twenty invoices and inspecting each is one
request here against twenty-one from the command line.

**A page already fetched comes back without a request** for thirty seconds, and
the band above the list always says how old what you are looking at is —
`fetched just now`, `cached 12s ago`. Walking four tabs and back cost ten
requests before and costs three now. `r` always bypasses the cache, and any
write empties it.

**Nothing refreshes on a timer.** A browser polling every two seconds would
spend the daily allowance before lunch, so the remaining quota sits in the
header and turns amber in the last tenth.

The overview costs two requests. The web dashboard's figures are all sums over
a period, and the API has no aggregate endpoint and returns no totals with a
list — reproducing them would mean paging every document of the year on every
glance. What is here is the unpaid set, split into overdue, due soon and later,
and every figure says in the detail pane exactly which records it covers.

## API limits

SuperFaktura enforces a hard quota under its fair-use policy. These are the
service's limits, not the CLI's — nothing here can raise them.

| Limit                 | Value         | What happens                                    |
| --------------------- | ------------- | ----------------------------------------------- |
| Requests per day      | **1 000**     | Every `GET` is blocked once reached             |
| Requests per month    | **30 000**    | Same                                            |
| Emails per hour       | **100**       | `sf invoice send` starts failing                |
| Attachment size       | **4 MB**      | Rejected; checked locally before sending        |
| `per-page`, invoices  | **200**       | Rejected above                                  |
| `per-page`, expenses  | **100**       | Rejected above                                  |
| Recoverable responses | **~3 months** | `sf invoice recover` after that returns nothing |

```sh
sf limits    # what is left today and this month
```

The remaining quota rides on every response, so `sf` warns on stderr
unprompted once the last tenth of the day's allowance is gone.

Two things burn quota faster than they look:

- **`--all` follows every page.** On a few thousand invoices at 200 per page
  that is dozens of requests in one command. Prefer `--per-page` with an
  explicit `--page` when you only need a sample; `meta.page_count` in the JSON
  envelope tells you how many there are.
- **A list already returns complete records.** Following `sf invoice list` with
  a `view` for each row is the single most expensive mistake available, and it
  buys nothing. When you do need details, `sf invoice view 1 2 3` fetches up to
  100 in a single request.

### Caching

Data you edit by hand is cached for a day, and the per-invoice PDF token —
which never changes — for a month. That covers value lists, tags, bank
accounts, the company roster, expense categories, sequences and logos, and
drops `sf invoice pdf` from two requests to one.

**Documents are never cached.** An invoice list served from disk could show a
paid invoice as unpaid, and in accounting that is a worse failure than another
request.

```sh
sf cache path             # where entries live
sf cache clear            # drop them all
sf --no-cache values countries   # bypass for one invocation
```

Entries are keyed per account, so two profiles on one machine never see each
other's data, and no email or path reaches a filename.

### Surviving a lost response

If a create call times out, retrying risks issuing the invoice twice. Pass an
identifier of your own and the API will replay the original response:

```sh
sf invoice create --client 7 --item 'Consulting:1:500:23' --checksum order-4821
# ...connection dies, no response...
sf invoice recover order-4821
```

The checksum is yours to choose (an order number works well), maximum 32
characters.

## For agents

The command surface is available as data, so an agent never has to parse help
text:

```sh
sf commands --json          # the whole tree with flags
sf --agent invoice --help   # one command as JSON
```

The binary also carries a skill describing the output contract and the parts of
the API that bite:

```sh
sf skill install            # writes ~/.claude/skills/superfaktura/SKILL.md
```

## Development

```sh
make build            # ./bin/sf
make test             # unit tests, no API needed
make check            # what CI runs
make surface-snapshot # after an intentional CLI change
```

`SURFACE.txt` is a golden snapshot of every command, flag and positional
argument. A change to the CLI surface fails the build until the snapshot is
regenerated and committed — renaming a flag breaks somebody's script, so it
should be visible in the diff.

End-to-end tests hit a real account and are opt-in twice: they need the `e2e`
build tag and credentials. Copy `.env.test.example` to `.env.test` and point it
at a [sandbox](https://sandbox.superfaktura.sk):

```sh
make test-e2e
```

Without credentials, or against an account the API rejects, those tests skip
rather than fail.
