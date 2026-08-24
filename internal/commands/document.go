package commands

import (
	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/output"
	"github.com/xseman/superfaktura-cli/internal/render"
)

// What an invoice looks like when you open it.
//
// The field list it replaced described the record; this shows it. The line
// items and the payments were in the row all along — a list response is a
// complete record — so putting them on screen costs nothing and answers the
// question a bag of labeled values could not: what was billed, and what has
// come in against it.
//
//	2026001 · Acme s.r.o.                              787.20 EUR
//	regular · issued 2026-08-01 · due 2026-09-15           UNPAID
//	──────────────────────────────────────────────────────────────
//	 QTY  ITEM                    UNIT    VAT      TOTAL
//	   8  Konzultácie            75.00    23%     738.00
//	   1  Cestovné               40.00    23%      49.20
//	──────────────────────────────────────────────────────────────
//	 PAYMENTS                        Net           640.00
//	  2026-08-01 transfer  152.80    VAT           147.20
//	                                 Total         787.20
//	                                 Paid          300.00
//	                                 To pay        487.20
//	──────────────────────────────────────────────────────────────
//	 Variable 2026001  ·  ID 309101  ·  Delivery 2026-08-01

var invoiceItemColumns = []render.Column{
	// The id leads, because deleting a line needs it and no other human-facing
	// output ever shows one — "add the corrected line, remove the wrong one"
	// was unusable without reaching for --jq.
	{Header: "ID", Path: "id"},
	{Header: "Qty", Path: "quantity", Format: render.Quantity, Right: true},
	{Header: "Item", Path: "name"},
	{Header: "Unit", Path: "unit_price", Format: render.Money, Right: true},
	{Header: "VAT", Path: "tax", Format: render.Percent, Right: true},
	{Header: "Total", Path: "item_price_vat", Format: render.Money, Right: true},
}

// Expense items name their total differently and are usually not named at all
// — SuperFaktura writes an expense's amount as a single unnamed rate line.
var expenseItemColumns = []render.Column{
	// The id leads, because deleting a line needs it and no other human-facing
	// output ever shows one — "add the corrected line, remove the wrong one"
	// was unusable without reaching for --jq.
	{Header: "ID", Path: "id"},
	{Header: "Qty", Path: "quantity", Format: render.Quantity, Right: true},
	{Header: "Item", Path: "name"},
	{Header: "Unit", Path: "unit_price", Format: render.Money, Right: true},
	{Header: "VAT", Path: "tax", Format: render.Percent, Right: true},
	{Header: "Total", Path: "total", Format: render.Money, Right: true},
}

var paymentColumns = []render.Column{
	{Header: "", Path: "created", Format: render.Date},
	{Header: "", Path: "payment_type"},
	{Header: "", Path: "amount", Format: render.Money, Right: true},
}

// invoiceTotals is the money block. Right-aligned, or the decimal points do
// not stack and a column of amounts stops being scannable.
var invoiceTotals = []render.Field{
	{Label: "Net", Path: "Invoice.amount", Format: render.Money, Right: true},
	{Label: "VAT", Path: "Invoice.vat", Format: render.Money, Right: true},
	{Label: "Total", Path: "Invoice.total_amount", Format: render.Money, Right: true},
	{Label: "Paid", Path: "Invoice.amount_paid", Format: render.Money, Right: true},
	{Label: "To pay", Path: "0.to_pay", Format: render.Money, Right: true},
}

var expenseTotals = []render.Field{
	{Label: "Amount", Path: "Expense.amount", Format: render.Money, Right: true},
	{Label: "Paid", Path: "Expense.amount_paid", Format: render.Money, Right: true},
}

// invoiceRefs is the line nobody reads until they need one of them.
var invoiceRefs = []render.Field{
	{Label: "Variable", Path: "Invoice.variable"},
	{Label: "Constant", Path: "Invoice.constant"},
	{Label: "ID", Path: "Invoice.id"},
	{Label: "Delivery", Path: "Invoice.delivery", Format: render.Date},
	{Label: "Comment", Path: "Invoice.comment"},
}

var expenseRefs = []render.Field{
	{Label: "Variable", Path: "Expense.variable"},
	{Label: "Document", Path: "Expense.document_number"},
	{Label: "ID", Path: "Expense.id"},
	{Label: "Delivery", Path: "Expense.delivery", Format: render.Date},
	{Label: "Comment", Path: "Expense.comment"},
}

// itemList fetches a document and emits its line items as a table.
//
// It costs one request and no more: the items travel inside the parent record,
// so there is nothing else to ask for. It exists because `item delete` takes an
// item id, and until the id column was added no human-facing output showed one
// — a user had to know to reach for --jq.
func itemList(cmd *cobra.Command, path, key string, cols []render.Column, next ...output.Step) error {
	raw, err := api.Get(ctx(cmd), path, nil)
	if err != nil {
		return err
	}
	obj, err := decodeObject(raw)
	if err != nil {
		return err
	}

	items := render.List(obj, key)
	return emitList(listResult{
		Items:     items,
		ItemCount: len(items),
		PageCount: 1,
	}, cols, next...)
}

// document composes the layout shared by invoices and expenses, which differ
// only in which keys hold their items, payments and totals.
type document struct {
	headline    func(map[string]any) render.Headline
	items       string
	itemColumns []render.Column
	payments    string
	totals      []render.Field
	refs        []render.Field
}

// namedItems returns the line items worth tabulating.
//
// An expense's amount comes back as one unnamed rate line, and a table of a
// single blank row restating the total below it is noise. An expense that does
// carry named items still gets them.
func namedItems(obj map[string]any, key string) []map[string]any {
	rows := render.List(obj, key)
	for _, row := range rows {
		if render.Text(render.Get(row, "name")) != "" {
			return rows
		}
	}
	return nil
}

func (d document) render(obj map[string]any, width int, style render.Styler) []string {
	if width <= 0 {
		width = 66
	}

	lines := append([]string{}, headlineOf(d.headline, obj, width, style)...)

	if items := render.Rows(d.itemColumns, namedItems(obj, d.items), style); len(items) > 0 {
		lines = append(lines, items...)
		lines = append(lines, render.Rule(width, style))
	}

	// The payments and the totals sit side by side: they are the same
	// question from two directions, and stacking them pushed the total off
	// the bottom of the pane on any record with more than one payment.
	totals := render.Values(d.totals, obj, style)
	var payments []string
	if paid := render.List(obj, d.payments); len(paid) > 0 {
		payments = append([]string{render.Caption("payments", style)},
			render.Rows(paymentColumns, paid, style)...)
	}
	if len(payments) > 0 || len(totals) > 0 {
		lines = append(lines, render.SideBySide(payments, totals, max(width/2, 30))...)
		lines = append(lines, render.Rule(width, style))
	}

	if refs := render.Inline(d.refs, obj, style); refs != "" {
		lines = append(lines, refs)
	}
	return lines
}

// headlineOf renders the summary block, or nothing when there is no record.
func headlineOf(build func(map[string]any) render.Headline, obj map[string]any, width int, style render.Styler) []string {
	if build == nil {
		return nil
	}
	// DetailLines with no fields is the headline and its rule alone.
	return render.DetailLines(build(obj), nil, obj, width, style)
}

func invoiceDetail() render.Renderer {
	d := document{
		headline:    invoiceHeadline,
		items:       "InvoiceItem",
		itemColumns: invoiceItemColumns,
		payments:    "InvoicePayment",
		totals:      invoiceTotals,
		refs:        invoiceRefs,
	}
	return d.render
}

func expenseDetail() render.Renderer {
	d := document{
		headline:    expenseHeadline,
		items:       "ExpenseItem",
		itemColumns: expenseItemColumns,
		payments:    "ExpensePayment",
		totals:      expenseTotals,
		refs:        expenseRefs,
	}
	return d.render
}
