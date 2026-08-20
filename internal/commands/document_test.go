package commands

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/xseman/superfaktura-cli/internal/render"
)

// A document view shows what was billed and what came in against it. Both were
// in the row all along; the field list that came before described the record
// without ever showing it.

func decodeRow(t *testing.T, raw string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return obj
}

// A trimmed copy of a real invoice: two named items and one payment.
const invoiceRow = `{
  "Invoice": {"id":"309101","invoice_no_formatted":"2026001","type":"regular",
    "created":"2026-08-01","due":"2026-09-15","variable":"20260801",
    "amount":"640.00","vat":"147.20","total_amount":"787.20","amount_paid":"300.00",
    "invoice_currency":"EUR"},
  "Client": {"name":"Acme s.r.o."},
  "0": {"to_pay":"487.200000"},
  "InvoiceItem": [
    {"id":"804902","name":"Konzultácie","quantity":"8.00000","unit_price":75,"tax":23,"item_price_vat":738},
    {"id":"804904","name":"Cestovné","quantity":"1.00000","unit_price":40,"tax":23,"item_price_vat":49.2}],
  "InvoicePayment": [{"created":"2026-08-01 00:00:00","payment_type":"transfer","amount":"152.80"}]
}`

func TestAnInvoiceShowsWhatWasBilled(t *testing.T) {
	out := strings.Join(invoiceDetail()(decodeRow(t, invoiceRow), 66, nil), "\n")

	for _, want := range []string{
		"Konzultácie", "Cestovné", // the items
		"738.00", "49.20", // their totals
		"transfer", "152.80", // the payment
		"787.20", "487.20", // total and outstanding
		"2026001", "Acme s.r.o.", "UNPAID", // the headline
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing:\n%s", want, out)
		}
	}
}

func TestAmountsStackTheirDecimalPoints(t *testing.T) {
	// A column of money left-aligned is not scannable, which is most of why
	// the flat field list read as a wall.
	out := invoiceDetail()(decodeRow(t, invoiceRow), 66, nil)

	// Only the totals block: a label, then an amount at the end of the line.
	// The headline's amount is right-aligned to the frame edge instead, and
	// the item table's VAT column header is not an amount at all.
	total := regexp.MustCompile(`(Net|VAT|Total|Paid|To pay) +\d+\.\d{2}$`)
	var at []int
	for _, line := range out {
		if total.MatchString(strings.TrimRight(line, " ")) {
			at = append(at, len(strings.TrimRight(line, " ")))
		}
	}
	if len(at) < 5 {
		t.Fatalf("found %d totals, want 5:\n%s", len(at), strings.Join(out, "\n"))
	}
	for _, end := range at[1:] {
		if end != at[0] {
			t.Errorf("decimal points do not stack: ends at %v", at)
			break
		}
	}
}

func TestAnUnnamedRateLineIsNotTabulated(t *testing.T) {
	// SuperFaktura writes an expense's amount as one unnamed rate line. A
	// table of a single blank row restating the total below it is noise.
	row := decodeRow(t, `{
	  "Expense": {"id":"4504","number":"2026001","type":"invoice","due":"2026-08-01",
	    "amount":"49.00","amount_paid":"0.00","currency":"EUR"},
	  "ExpenseItem": [{"quantity":"1.00000","unit_price":"49.0000","tax":"0.00","total":"49.0000"}],
	  "ExpensePayment": []}`)

	out := strings.Join(expenseDetail()(row, 66, nil), "\n")
	if strings.Contains(out, "QTY") {
		t.Errorf("an unnamed rate line was tabulated:\n%s", out)
	}
	if !strings.Contains(out, "49.00") {
		t.Errorf("the amount is missing:\n%s", out)
	}

	// One that is named still gets its table.
	named := decodeRow(t, `{
	  "Expense": {"id":"1","amount":"10.00"},
	  "ExpenseItem": [{"name":"Hosting","quantity":"1.00000","total":"10.0000"}]}`)
	if !strings.Contains(strings.Join(expenseDetail()(named, 66, nil), "\n"), "Hosting") {
		t.Error("a named expense item was hidden")
	}
}

func TestADocumentWithNothingOnItStillRenders(t *testing.T) {
	// No items, no payments — an invoice created a moment ago. The blocks that
	// have nothing to say are skipped rather than printed empty.
	row := decodeRow(t, `{"Invoice":{"id":"1","invoice_no_formatted":"2026009","amount":"0.00"}}`)
	out := invoiceDetail()(row, 66, nil)

	if len(out) == 0 {
		t.Fatal("nothing rendered")
	}
	for _, line := range out {
		if strings.Contains(line, "QTY") || strings.Contains(line, "PAYMENTS") {
			t.Errorf("an empty block was printed: %q", line)
		}
	}
}

func TestItemListTakesTheItemsFromTheRecord(t *testing.T) {
	// The items travel inside the parent, so listing them costs the one request
	// that fetches it and no more. This exists because `item delete` takes an
	// item id and, until the id column, no human-facing output showed one.
	obj := decodeRow(t, invoiceRow)

	items := render.List(obj, "InvoiceItem")
	if len(items) != 2 {
		t.Fatalf("%d items, want 2", len(items))
	}
	if got := render.Text(render.Get(items[0], "id")); got == "" {
		t.Error("the item carries no id, so the list cannot serve a delete")
	}

	// The id leads the columns the list renders with, since that is the value
	// the user has come for.
	if invoiceItemColumns[0].Header != "ID" {
		t.Errorf("first column is %q, want the id", invoiceItemColumns[0].Header)
	}
	if expenseItemColumns[0].Header != "ID" {
		t.Errorf("expense first column is %q, want the id", expenseItemColumns[0].Header)
	}
}
