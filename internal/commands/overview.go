package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/render"
	"github.com/xseman/superfaktura-cli/internal/tui"
)

// The overview answers "what needs attention", not "what happened this year".
//
// The web dashboard's figures — invoiced, expensed, balance, the monthly chart
// — are all sums over a period, and the API has no aggregate endpoint and
// returns no totals with a list. Reproducing them means paging every document
// of the year on every glance. What is cheap is the unpaid set: one request per
// resource, with the amounts in the rows, and everything else derived locally.
//
// A cheap overview that lied would be worse than none, so nothing here is
// extrapolated: each figure is the exact sum of records that were counted.

// dueSoonDays is the horizon for "needs attention before it is late".
const dueSoonDays = 7

// metric is one row of the overview.
type metric struct {
	label  string
	count  int
	amount float64
	// note explains where the figure came from, shown in the detail pane.
	note string
}

func overviewResource() tui.Resource {
	return tui.Resource{
		Title:   "Overview",
		Columns: overviewColumns,
		Detail: render.Pairs(nil, []render.Field{
			{Label: "Metric", Path: "Metric.label"},
			{Label: "Records", Path: "Metric.count"},
			{Label: "Amount", Path: "Metric.amount"},
			{Label: "Source", Path: "Metric.note"},
		}),
		Load: loadOverview,
	}
}

var overviewColumns = []render.Column{
	{Header: "", Path: "Metric.label"},
	{Header: "Records", Path: "Metric.count"},
	{Header: "Amount", Path: "Metric.amount"},
}

// loadOverview costs two requests: the unpaid invoices and the unpaid
// expenses. Overdue and due-soon are read off those same rows.
func loadOverview(ctx context.Context, _ int, _ map[string]string) (tui.Page, error) {
	// status 1 is issued and 2 partially paid; the API takes both in one
	// filter, which halves the cost.
	invoices, err := unpaid(ctx, "/invoices/index.json", defaultPerPageCap)
	if err != nil {
		return tui.Page{}, err
	}
	expenses, err := unpaid(ctx, "/expenses/index.json", expensePerPageCap)
	if err != nil {
		return tui.Page{}, err
	}

	today := time.Now().Truncate(24 * time.Hour)
	horizon := today.AddDate(0, 0, dueSoonDays)

	var overdue, soon, later metric
	for _, row := range invoices.rows {
		amount := outstanding(row, "Invoice")
		switch due := dueDate(row, "Invoice"); {
		case due.IsZero():
			later.add(amount)
		case due.Before(today):
			overdue.add(amount)
		case due.Before(horizon):
			soon.add(amount)
		default:
			later.add(amount)
		}
	}

	var owed metric
	for _, row := range expenses.rows {
		owed.add(outstanding(row, "Expense"))
	}

	// Say what the figure covers. A sum over a page presented as a total is
	// the failure this whole design is avoiding.
	counted := func(m metric, loaded, total int) string {
		switch {
		case m.count == 0:
			return "none"
		case total > loaded:
			return fmt.Sprintf("%d of %d loaded — refresh shows more", loaded, total)
		default:
			return fmt.Sprintf("all %d counted", m.count)
		}
	}

	metrics := []metric{
		{label: "Overdue", count: overdue.count, amount: overdue.amount,
			note: "invoices past their due date · " + counted(overdue, len(invoices.rows), invoices.total)},
		{label: fmt.Sprintf("Due within %d days", dueSoonDays), count: soon.count, amount: soon.amount,
			note: "unpaid and due soon · " + counted(soon, len(invoices.rows), invoices.total)},
		{label: "Unpaid, later", count: later.count, amount: later.amount,
			note: "unpaid but not yet pressing · " + counted(later, len(invoices.rows), invoices.total)},
		{label: "Owed on expenses", count: owed.count, amount: owed.amount,
			note: "unpaid expenses · " + counted(owed, len(expenses.rows), expenses.total)},
	}

	items := make([]map[string]any, 0, len(metrics))
	for _, m := range metrics {
		items = append(items, map[string]any{"Metric": map[string]any{
			"label":  m.label,
			"count":  strconv.Itoa(m.count),
			"amount": money(m.amount),
			"note":   m.note,
		}})
	}

	// One page: this is a summary, not a list to walk.
	return tui.Page{Items: items, ItemCount: len(items), PageCount: 1}, nil
}

func (m *metric) add(amount float64) {
	m.count++
	m.amount += amount
}

type unpaidSet struct {
	rows  []map[string]any
	total int
}

// unpaid fetches the records that still owe money, in one request.
//
// If the account has more of them than a page holds, the sums cover what was
// loaded and the note says so rather than presenting a partial figure as a
// total.
func unpaid(ctx context.Context, path string, cap int) (unpaidSet, error) {
	params := client.Params{
		"listinfo": "1",
		"per_page": strconv.Itoa(cap),
		"status":   "1|2",
	}
	raw, err := api.Get(ctx, path, params)
	if err != nil {
		return unpaidSet{}, err
	}
	result, err := decodeList(raw)
	if err != nil {
		return unpaidSet{}, err
	}
	return unpaidSet{rows: result.Items, total: result.ItemCount}, nil
}

// outstanding is what is still owed on a record.
//
// Invoices carry a computed "to_pay"; expenses do not, so there it is the
// amount less whatever has been paid.
func outstanding(row map[string]any, model string) float64 {
	if value := number(render.Get(row, "0.to_pay")); value != 0 {
		return value
	}
	total := number(render.Get(row, model+".amount"))
	return total - number(render.Get(row, model+".amount_paid"))
}

func dueDate(row map[string]any, model string) time.Time {
	text := render.Date(render.Get(row, model+".due"))
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(text))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func number(value any) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(render.Text(value)), 64)
	if err != nil {
		return 0
	}
	return parsed
}

// money formats a figure, leaving zero blank so the eye goes to what is not.
func money(amount float64) string {
	if amount == 0 {
		return ""
	}
	return fmt.Sprintf("%.2f", amount)
}
