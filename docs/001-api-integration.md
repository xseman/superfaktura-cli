# 001 · API integration

`internal/client`. SuperFaktura is CakePHP-shaped and breaks four REST
expectations. **Each is handled in exactly one place — do not re-handle it per
command.**

## 1 · Filters are path segments

Not query strings.

```
  https://moja.superfaktura.sk/invoices/index.json/listinfo:1/page:2/type:regular
  └──────────── base ────────────┘└── path ──┘└──────── filters ────────┘
                                                  key:value, sorted
```

`client.Params` renders them sorted, so the same filter set always produces the
same URL — which is what makes it usable as a cache key.

**The comma stays unescaped.** `url.PathEscape` turns `,` into `%2C`, but the
base64 search convention substitutes `,` for `=` *because* a comma is path-safe.
Escaping it undoes the substitution the docs ask for. `escapeSegment` restores
it; `/`, `|` and spaces stay escaped.

`|` separates multiple values: `status:1|2` returns the union. Documented for
`type`, `delivery_type`, `ignore` and `payment_type` — and it works for `status`
too, which is undocumented, verified against the live API, and load-bearing for
the overview.

## 2 · Writes: form-encoded or raw JSON, per endpoint

The documentation contradicts itself. `intro.md` says either content type works;
the axios example says "Don't send pure JSON"; seven endpoints carry their own
curl example using raw JSON. **The rule adopted is: follow each endpoint's own
example.**

```
                          a write
                             │
             ┌───────────────┴───────────────┐
             │ does this endpoint's own doc  │
             │ example use a raw JSON body?  │
             └───────────────┬───────────────┘
                 yes         │        no
          ┌──────────────────┘└──────────────────┐
          ▼                                      ▼
  client.PostJSON                          client.Post
  Content-Type: application/json           application/x-www-form-urlencoded
  body: {"Invoice":{…}}                    body: data=<url-encoded JSON>

  /clients/create                          everything else
  /clients/edit/{id}
  /invoices/send                           client.Delete → DELETE
  /invoices/mark_as_sent                     /stock_items/delete
  /stock_items/add                           /expense_items/delete
  /stock_items/addStockMovement
  /stock_items/edit/{id} → client.Patch    Some writes are GET:
    (the only PATCH; PHP does not            /invoices/delete/{id}
     populate $_POST for PATCH, so           /invoices/mark_sent/{id}
     form encoding cannot work here)         /clients/delete/{id}
```

`e2e/encoding_test.go` asserts the method and content type of **every** write
against a local server, so this choice cannot drift silently. If a write starts
failing with a validation error that makes no sense, flip `Post`/`PostJSON` for
that endpoint and update the test.

Payloads are encoded with HTML escaping off — the API is not a browser, and
`&` in every company name is noise.

## 3 · Failures do not use status codes consistently

```
                        response
                            │
                            ▼
              ┌──────── is it JSON? ────────┐
           no │                             │ yes
              ▼                             ▼
      nonJSONError()              ┌── "error" key present ──┐
      HTML error page          no │                         │ yes
      → api_error / auth          ▼                         ▼
                              success                  decodeError()
                              (a list has no      ┌───────────────────┐
                               "error" key)       │ status refines it │
                                                  │  401 → auth       │
                                                  │  403 → forbidden  │
                                                  │  429 → rate_limit │
                                                  │  200 → api_error  │
                                                  └───────────────────┘
```

**The body decides; the status only refines.** Failures arrive as HTTP **200**
carrying `{"error":1,"message":…}`, the sandbox answers a subscription problem
with **401**, and `/tags/add` uses proper 201/403/409. All three are real.

Decoded loosely on purpose: `error` is a number on most endpoints and a string
on a few; `error_message` is sometimes a string and sometimes a map of per-field
errors, which lands in `Error.Fields` so a caller knows *which* field.

## 4 · Responses nest under model names

An invoice number is at `Invoice.invoice_no_formatted`, its client at
`Client.name`. Columns address values by dotted path (`render.Get`) rather than
by struct field, so the CLI never has to model every response type.

## List shapes

Four, all handled in `commands/helpers.go`:

| Shape           | Example                            | Decoder              |
| --------------- | ---------------------------------- | -------------------- |
| `{"items":[…]}` | any `index.json` with `listinfo:1` | `decodeList`         |
| bare array      | `/cash_registers/getDetails`       | `decodeList`         |
| named key       | `/bank_accounts/index`             | `decodeListUnder`    |
| id→name map     | `/tags/index.json` → `{"1":"abc"}` | `decodeKeyValueList` |

## Dates

Every date field goes through PHP's `strtotime`, so far more than `YYYY-MM-DD`
works. Verified live:

```
  2026-08-15    15.08.2026    31.12.2026    today    +14 days    next friday
```

For an invoicing tool `+14 days` is the common case, so the CLI passes almost
everything through untouched. **One shape is refused**, in `commands/dates.go`:

```
   --due 08/09/2026
        │
        └─► strtotime reads slashes in american month/day order,
            so a European meaning 8 September silently books 9 August.
            No error, no way to notice. Refused locally instead.
```

Everything else either works or fails loudly: an expression `strtotime` cannot
parse becomes the epoch and trips a later check, reporting "due date is before
the issue date" — misleading, but not silent.

The check is hooked once at the root over any flag whose usage mentions
`YYYY-MM-DD`, so a new date flag is covered the day it is added. The browser's
forms call `RunE` directly and never pass through it, so they validate in the
form instead — which puts the complaint under the field being typed into.

## Rate limits

Parsed from `X-RateLimit-*` on every response and surfaced by `sf auth status`,
by `sf doctor --live` for every profile at once, and in the TUI header.

```
  X-RateLimit-DailyLimit: 1000        ← per company_id, not per key
  X-RateLimit-DailyRemaining: 876
  X-RateLimit-MonthlyLimit: 30000
```

## Dry run

`client.DryRun` stops writes in the client and returns a `*Planned`, which
`Execute` renders before exiting zero. It travels as an *error* only so it cannot
be mistaken for a result on the way up.

**Reads still go out.** A plan showing `--client "Acme"` rather than the resolved
`client_id` would hide the part most worth checking.

## Before you trust a shape

The vendor docs and the live API disagree in more places than this file names.
Do not trust a response shape you have not seen on the wire: check it against a
real response, absorb the difference in one place, and pin it with a test.
