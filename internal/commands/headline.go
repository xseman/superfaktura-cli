package commands

import (
	"strings"
	"time"

	"github.com/xseman/superfaktura-cli/internal/render"
)

// The summary above a record: who, how much, by when, and whether it is
// settled. Opening a document is almost always one of those questions, and a
// flat field list made all four take the same effort to find as the token.
//
// Status is derived rather than read. Invoice.status exists but only counts up
// to "partially paid" — overdue is a fact about today, not about the record —
// so it is computed from what is outstanding and when it was due, the same way
// the overview counts it. One rule, two callers.

// documentStatus is the badge and its tone.
func documentStatus(row map[string]any, model string) (string, render.Tone) {
	owed := outstanding(row, model)
	if owed <= 0 {
		return "PAID", render.ToneGood
	}
	if due := dueDate(row, model); !due.IsZero() && due.Before(time.Now().Truncate(24*time.Hour)) {
		return "OVERDUE", render.ToneBad
	}
	return "UNPAID", render.ToneWarn
}

// headlineFor builds the summary shared by invoices and expenses, which differ
// only in what the counterparty is called and where the total lives.
func headlineFor(row map[string]any, model, totalPath string) render.Headline {
	text := func(path string) string { return render.Text(render.Get(row, path)) }

	title := text(model + ".invoice_no_formatted")
	if title == "" {
		title = text(model + ".number")
	}
	if party := text("Client.name"); party != "" {
		title += "  ·  " + party
	}

	amount := render.Money(render.Get(row, totalPath))
	if currency := text(model + ".invoice_currency" + ""); currency != "" {
		amount += " " + currency
	} else if currency := text(model + ".currency"); currency != "" {
		amount += " " + currency
	}

	var parts []string
	if kind := text(model + ".type"); kind != "" {
		parts = append(parts, kind)
	}
	if due := render.Date(render.Get(row, model+".due")); due != "" {
		parts = append(parts, "due "+due)
	}
	if paid := render.Date(render.Get(row, model+".paydate")); paid != "" {
		parts = append(parts, "paid "+paid)
	}

	badge, tone := documentStatus(row, model)
	return render.Headline{
		Title:    title,
		Amount:   strings.TrimSpace(amount),
		Subtitle: strings.Join(parts, "  ·  "),
		Badge:    badge,
		Tone:     tone,
	}
}

func invoiceHeadline(row map[string]any) render.Headline {
	return headlineFor(row, "Invoice", "Invoice.total_amount")
}

func expenseHeadline(row map[string]any) render.Headline {
	return headlineFor(row, "Expense", "Expense.amount")
}
