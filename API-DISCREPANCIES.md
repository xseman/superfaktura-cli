# API discrepancies

Where `superfaktura/docs` and the live API disagree, and where this CLI got it
wrong. Every entry says what the documentation claims, what actually happens,
where the code absorbs it, and what stops it regressing.

The documentation clone is `.reference/superfaktura-docs`. Line numbers refer
to that copy and are a hint, not an anchor — the section headings are stable.

Everything below was checked against a live **sandbox** company. A sandbox is
not production; where that matters it is said so.

**Status markers**

| | meaning |
| --- | --- |
| ✅ | confirmed against the live API |
| 📌 | not confirmed server-side; the CLI's behaviour is pinned by a test so it cannot drift silently |
| ⚠️ | open question |

---

## A. The documentation contradicts itself

### A1 · Which content type a write should use ⚠️📌

**Docs.** `intro.md` §"POST data format" (l. 37) is unambiguous: *"POST requests
support two Content-Type options"* — `application/json` **or**
`application/x-www-form-urlencoded`. But `examples/axios.js` (l. 55, 64) and
`examples/requests.py` (l. 73, 83) both carry the comment *"Don't send pure
JSON."* — and one of the endpoints they warn about is `/clients/create`, whose
own curl example in `clients.md` uses a raw JSON body.

So three sources say three things: both work / never use JSON / this endpoint
uses JSON.

**What the CLI does.** Follows *each endpoint's own curl example*, on the
grounds that it is the most specific evidence available. Seven endpoints get a
raw JSON body; everything else gets the form encoding:

| raw JSON (`client.PostJSON`) | why |
| --- | --- |
| `/clients/create`, `/clients/edit/{id}` | own curl example |
| `/invoices/send`, `/invoices/mark_as_sent` | own curl example |
| `/stock_items/add`, `/stock_items/addStockMovement` | own curl example |
| `/stock_items/edit/{id}` (`client.Patch`) | the only PATCH — PHP does not populate `$_POST` for PATCH, so form encoding cannot work regardless of the docs |

**Where.** `internal/client/client.go` — `Post`, `PostJSON`, `Patch`, `Delete`.

**Pinned by.** `e2e/encoding_test.go` asserts the method and content type of
every write against a local recording server (49 cases). It does not prove the
server accepts them — it proves the choice recorded here cannot change by
accident.

**Confirmed live** for the writes the sandbox run exercised: client create,
invoice create with line items, invoice payment, expense create with a base64
attachment, tag add, stock item add. The remainder is 📌.

*If a write ever fails with a validation error that makes no sense, flip
`Post`/`PostJSON` for that endpoint and update the test.*

---

### A2 · `/expense/view` vs `/expenses/view` ✅

**Docs.** `expenses.md` l. 592 gives the URL as `/expense/view/{ID}.json`
(singular). Six lines later the curl example for the same section calls
`/expenses/view/1.json` (plural).

**Live.** The plural exists. The singular does not.

**Where.** `internal/commands/expense.go` uses the plural throughout.

**How it was settled.** A route prober walked all 72 paths the CLI can issue,
with a deliberate negative control (a path known not to exist) to prove the
prober could tell the difference.

---

### A3 · Expense categories: the field table and the example disagree ✅

**Docs.** `value-lists.md` §"Expense categories" (l. 300+) documents each array
element as having an **`ExpenseCategory` object** plus a `children` array, and
describes `lft`/`rght` for Modified Preorder Tree Traversal. The example
response immediately below shows **flat** objects instead —
`{"id": 2, "name": "Nájomné / energie", "children": [], "icon": …}` — with no
`ExpenseCategory` wrapper and no `lft`/`rght` at all.

**Live.** Matches the example, not the table: flat objects, nested `children`.

**Where.** `internal/commands/values.go` — `decodeCategoryTree` flattens the
tree and carries the parent name across, since a child row is otherwise
unidentifiable.

**Pinned by.** `TestExpenseCategoriesFlattenTheTree` in
`internal/commands/values_test.go`, whose fixture is a trimmed copy of a real
response.

---

## B. Behaviour the documentation does not describe

### B1 · `|` works as a multi-value separator for `status` ✅

**Docs.** `invoice.md` §"Invoice list" explicitly permits <code>|</code> for
`type` (l. 2132), `delivery_type` (l. 2147), `ignore` (l. 2148) and
`payment_type` (l. 2157). The `status` row (l. 2159) says nothing about it.

**Live.** It works, and returns the union:

```
--filter status=1     → 3 invoices
--filter status=2     → 1 invoice
--filter status=1|2   → 4 invoices
```

**Why it matters.** The overview's whole cost model rests on this. "Unpaid"
means issued *or* partially paid; without `|` that is two requests per resource
instead of one, on a budget capped per day.

**Where.** `internal/commands/overview.go` — `unpaid()` sends `status: "1|2"`.

⚠️ Undocumented behaviour holding up a load-bearing feature. If it ever stops
working the overview silently under-reports rather than failing, because a
narrower result set still looks like a valid answer.

---

### B2 · The rate limit is counted per company, not per key ✅

**Docs.** `intro.md` §"Limit headers" (l. 127) documents the `X-RateLimit-*`
headers and their values (1000/day, 30000/month). It does not say what the
counter is scoped to.

**Live.** The counters move independently for each `company_id`. Requests made
with no company, or with a company the key cannot reach, are counted somewhere
the caller cannot see.

**Consequence.** A misconfigured `company_id` does not fail loudly — it spends
a different company's allowance. This cost a whole day's quota on a company ID
with one digit too many before it was noticed.

**Where.** `internal/client/client.go` parses the headers; `sf auth status`
surfaces them.

---

### B3 · `getResponseByChecksum` nests the replayed response under `data` ✅

**Docs.** `faq.md` §"How to avoid duplicate invoices" (l. 146) explains the
mechanism and shows the request. **No response example is given** — only the
prose *"Response contains original response"*.

**Live.** The models sit one level down, under `data`, rather than at the top
level like a fresh detail response.

**Where.** `internal/commands/invoice.go` — `sf invoice recover` unwraps `data`
before rendering, so the same field list works for both.

---

### B4 · List rows are complete records ✅

**Docs.** Nowhere stated. The list and detail endpoints are documented
separately, which reads as though a list gives a summary.

**Live.** A row from `/invoices/index.json` already contains the line items,
payments, tags and client — everything the detail endpoint returns.

**Consequence.** This is the single most useful undocumented fact about the
API, and the TUI is built on it: opening a record costs **zero** requests.
Browsing 20 invoices costs 1 request in the TUI against 21 in a naive
list-then-fetch design.

**Where.** `internal/tui/app.go` renders the detail pane straight from the row
in hand; no load is issued on selection.

---

### B5 · HTTP status codes are not consistent across endpoints ✅

**Docs.** `tags.md` documents real status codes for `/tags/add` — 201 created,
403 insufficient privileges, 409 duplicate. That is a normal REST contract, and
reading it first suggests the API behaves this way generally.

**Live.** Elsewhere it does not. Failures arrive as HTTP **200** carrying
`{"error": 1, "message": …}`, and the sandbox answers *"Musíte mať platné
prémiové členstvo"* with **401** — an authorization code for what is a
subscription problem. Some paths return an HTML error page and no JSON at all.
A successful list has no `error` key whatsoever.

**Where.** `internal/client/client.go` — `decodeError` lets the **body** decide
and uses the status only to refine the classification. `nonJSONError` handles
the HTML pages, which are never worth quoting back to the user.

**Also loose:** the `error` field is a number on most endpoints and a string on
a few, and `error_message` is sometimes a string and sometimes a map of
per-field errors. Both are decoded permissively (`isErrorFlag`, `fieldMap`).

---

### B6 · Date fields accept anything PHP's `strtotime` accepts ✅

**Docs.** Every date field is documented as `YYYY-MM-DD` and nothing else.

**Live.** The server is PHP and puts the value through `strtotime`, so relative
English expressions work — verified against the sandbox on 2026-08-02:

| sent as `--due` | stored as |
| --- | --- |
| `next friday` | `2026-08-07` |
| `+14 days` | `2026-08-16` |
| `2026-08-15` | `2026-08-15` |

**Useful, and a trap.** For an invoicing tool `+14 days` is the common case, so
this is worth knowing. But an input `strtotime` *cannot* parse becomes the Unix
epoch, which then fails a different validation and reports a misleading error:

```
  --due "nxt fridey"     →  "Dátum splatnosti nemôže byť skôr ako dátum
  --due "15/08/2026"     →   vystavenia"   (due date is before the issue date)
```

Nothing is corrupted — the write is rejected — but the message describes the
consequence rather than the cause, and `15/08/2026` is the mistake a European
typist makes first.

**Where.** Nowhere yet: the CLI passes date flags through untouched and does not
validate them. That is deliberate for now — validating strictly to `YYYY-MM-DD`
would reject `+14 days`, which works.

### B7 · `/invoices/edit` appends line items, it does not replace them ✅

**Docs.** Nowhere stated. The Edit invoice section says only *"Optional: Same as
for Add invoice"*, and `InvoiceItem` appears in that section's **response** —
never in a request example. There is no endpoint for adding or changing a single
item: the whole documentation exposes one,
`/invoice_items/delete/{ITEM_ID}/invoice_id:{ID}`.

**Live.** Measured on invoice 311011:

```
   items before edit   [AAA 10]
   edit sends          [BBB 20]
   items after edit    [AAA 10, BBB 20]
```

The shape of the API had argued for this: a separate delete endpoint would serve
no purpose if sending the array replaced it.

**Consequence.** An `--item` flag on `sf invoice edit` would read as "these are
the items" and would in fact **duplicate** them, so it does not exist. The
operation the API offers is *append*, and it carries that name —
`sf invoice item add`. With `sf invoice item delete` beside it, an invoice's
contents are editable: add the corrected line, remove the wrong one.

**Where.** `internal/commands/invoice.go` — `invoiceItemCmd`.

---

### B8 · "Not found" is a 404 on one endpoint and a 200 on another ✅

**Docs.** Nowhere stated.

**Live.** The same user mistake — naming an invoice that does not exist — is
answered two different ways:

```
  GET  /invoices/view/99999999.json
       HTTP 404  {"error":1,"message":"Invoice not found"}

  POST /invoices/edit   Invoice.id = 99999999
       HTTP 200  {"error":1,"message":"Invoice ID not found."}
```

**Consequence.** `sf invoice view 99999999` exits **2** (`not_found`) and
`sf invoice item add --invoice 99999999` exits **7** (`api_error`), for what the
user did wrong in exactly the same way. `httpError` maps 404 to `not_found`; the
200 falls to the default arm, which is `api_error`.

**Not fixed, deliberately.** Distinguishing them means matching the message
text, and this API's messages are not reliably English — the same catalogue
records it answering a subscription problem with *"Musíte mať platné prémiové
členstvo"*. A CLI that decided its exit code by grepping for "not found" would
be right in one language and wrong in the next. `error` is `1` in both bodies,
so there is no structured signal to use instead.

If SuperFaktura ever gives its failures distinct numeric codes, this becomes a
two-line fix in `httpError`.

---

### B9 · The comma in a path filter must not be percent-encoded 📌

**Docs.** The base64 search convention substitutes `,` for `=` — precisely
*because* a comma is path-safe in RFC 3986. The docs do not spell out the
consequence.

**Trap.** Go's `url.PathEscape` turns `,` into `%2C`, which undoes the
substitution the convention asks for and leaves the server to un-escape before
decoding.

**Where.** `internal/client/client.go` — `escapeSegment` restores the comma.
`/`, `|` and spaces stay escaped.

**Pinned by.** `internal/client/client_test.go` (l. 344).

---

## C. The documentation was right; this CLI guessed wrong

These are not API defects. They are recorded because each one cost a debugging
session, and every one of them was an assumption applied *before* reading the
endpoint's own page. The lesson generalises: **read the specific page, do not
extrapolate the house style.**

| # | Assumption | What the docs actually say | Symptom |
| --- | --- | --- | --- |
| C1 | Every payload nests under its model, so `/tags/add` takes `{"Tag":{"name":…}}` | `tags.md` l. 13 shows `data={"name":"abc"}` — **flat**. The response's `tag_id` is top-level too | `"Chýbajúce údaje"` |
| C2 | `/stock_items/addStockMovement` takes one movement object | `stock.md` l. 568 shows `"StockLog":[ … ]` — an **array**, even for one | HTTP 500 TypeError; the server does array work on whatever it is given |
| C3 | `/countries` returns model-wrapped records | `value-lists.md` l. 24 says *"in `id: country_name` format"* and shows the map | decode error |
| C4 | `/sequences/index.json` returns a flat list | l. 755 says *"categorized by document type"* — the type is the map **key**, not a field | decode error |
| C5 | Time-filter constants transcribed from memory | The table at l. 944 is correct and unambiguous: **4** = this month, **6** = this year, **9** = this week, **11** = last hour | wrong filter silently applied — no error at all, which is why this is the worst of the five |

C1–C4 are pinned by fixtures in `internal/commands/values_test.go` and
`helpers_test.go`; C5 by `TestValueListsMatchTheDocumentation`.

> Three code comments and one AGENTS.md section previously claimed these shapes
> were undocumented. They were not. The comments have been corrected — see the
> commit that introduced this file.

---

## D. Recorded as findings, but not actually discrepancies

Kept so nobody re-investigates them.

- **`/currencies` does not exist.** Correct, and not a defect: `value-lists.md`
  §"Currencies" (l. 122) is a **constant table** of ISO-4217 codes, never an
  endpoint. A route prober tried a path the documentation never claimed. This
  is why `sf values currencies` prints the constants instead of calling the
  API.
- **`per_page` caps differ by resource.** Invoices allow 200
  (`invoice.md` l. 2130), expenses only 100 (`expenses.md` l. 1087). Both are
  documented; they just differ, which is easy to miss. Handled by
  `defaultPerPageCap` / `expensePerPageCap` in `internal/commands/helpers.go`.
- **Responses nest under model names** (`Invoice.invoice_no_formatted`,
  `Client.name`). Thoroughly documented; listed here only because it shapes the
  whole rendering layer (`render.Get` addresses values by dotted path).
- **Checksum is limited to 32 characters.** Documented at `faq.md` l. 136 and
  enforced client-side (`maxChecksumLength`), so the request is not wasted.

---

## E. Open

- **E1 · The form-versus-JSON split is unconfirmed for endpoints the sandbox
  run did not exercise.** See A1. Three writes are deliberately never run
  automatically because they have real-world side effects: `invoice send`
  (sends email), `invoice post` (buys postage), `sms` (sends an SMS).
- **E2 · `status:1|2` is undocumented.** See B1. Worth re-checking if the
  overview's numbers ever look low.
- **E3 · Everything here is sandbox behaviour.** Production may differ,
  particularly around the 401-for-subscription case in B5, which is plausibly a
  sandbox-specific entitlement check.

---

## Adding to this file

One entry per discrepancy, in the section that matches what kind it is. Say
what the docs claim *with a reference*, what actually happens *with evidence*,
where the code absorbs it, and what pins it. An entry with no test reference
should say 📌 or ⚠️ so it is obvious the claim rests on a single observation.
