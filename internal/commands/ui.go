package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/output"
	"github.com/xseman/superfaktura-cli/internal/render"
	"github.com/xseman/superfaktura-cli/internal/tui"
)

// The browser is wired here rather than in the tui package: that package knows
// about tables and keystrokes, this one knows about invoices. The column and
// field definitions are the same ones the CLI prints, so the two views cannot
// drift apart.

func init() {
	var perPage int

	cmd := &cobra.Command{
		Use:     "ui",
		Aliases: []string{"browse"},
		Short:   "Browse the account in a terminal UI",
		Long: `Opens a terminal browser over invoices, clients and expenses.

Tab switches between them, / filters the loaded page, and the detail of the
selected record is always shown. Opening a record costs no request: the API
returns complete records in a list, so the detail is already here.

Nothing refreshes on a timer. Press r when you want current data — a tool that
polled would spend a thousand-request daily allowance in minutes.`,
		Args:    cobra.NoArgs,
		Example: "  sf ui\n  sf ui --per-page 50",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !isatty.IsTerminal(os.Stdout.Fd()) {
				return &output.Error{
					Code:    output.CodeUsage,
					Message: "the browser needs a terminal",
					Hint:    "For scripts and pipes use the individual commands, which emit JSON",
				}
			}

			// The request spinner writes to stderr, which Bubble Tea is now
			// drawing over. The browser shows its own loading state.
			api.OnRequest = nil

			return tui.Run(ctx(cmd), tui.Config{
				Account:   browserAccount(),
				ReadOnly:  api.DryRun,
				Resources: browserResources(perPage),
				Quota: func() tui.Quota {
					limits := api.Limits()
					return tui.Quota{Remaining: limits.DailyRemaining, Limit: limits.DailyLimit}
				},
			})
		},
	}

	cmd.Flags().IntVar(&perPage, "per-page", 25, "Records to load per page")
	rootCmd.AddCommand(cmd)
}

func browserAccount() string {
	if settings.Profile != "" {
		return settings.Profile
	}
	if settings.CompanyID != "" {
		return "company " + settings.CompanyID
	}
	return settings.Email
}

// browserResources describes the three tabs, reusing the CLI's own columns and
// fields so a record looks the same in both.
func browserResources(perPage int) []tui.Resource {
	return []tui.Resource{
		overviewResource(),
		{
			Title:   "Invoices",
			Columns: invoiceColumns,
			Detail:  invoiceDetail(),
			Scopes:  documentScopes,
			Periods: documentPeriods,
			Load:    pageLoader("/invoices/index.json", perPage, defaultPerPageCap),
			Actions: []tui.Action{
				{
					Key: "p", Label: "payment", Writes: true,
					Fields: []tui.FormField{{Key: "amount", Label: "amount (empty pays in full)"}},
					Run: func(c context.Context, row map[string]any, values map[string]string) (string, bool, error) {
						id := render.Text(render.Get(row, "Invoice.id"))
						doc := map[string]any{}
						put(doc, "InvoicePayment", "invoice_id", id)
						if input := values["amount"]; input != "" {
							amount, err := parseNumber(input)
							if err != nil {
								return "", false, usageErrorf("%q is not an amount", input)
							}
							put(doc, "InvoicePayment", "amount", amount)
						}
						if _, err := api.Post(c, "/invoice_payments/add/ajax:1/api:1", doc); err != nil {
							return "", false, err
						}
						return "Payment recorded on " + invoiceLabel(row), true, nil
					},
				},
				{
					Key: "s", Label: "mark sent", Writes: true,
					Confirm: func(row map[string]any) string {
						return "Mark " + invoiceLabel(row) + " as sent?"
					},
					Run: func(c context.Context, row map[string]any, _ map[string]string) (string, bool, error) {
						id := render.Text(render.Get(row, "Invoice.id"))
						if _, err := api.Get(c, "/invoices/mark_sent/"+id, nil); err != nil {
							return "", false, err
						}
						return invoiceLabel(row) + " marked as sent", true, nil
					},
				},
				{
					Key: "P", Label: "pdf",
					Run: func(c context.Context, row map[string]any, _ map[string]string) (string, bool, error) {
						id := render.Text(render.Get(row, "Invoice.id"))
						// The token is on the row already, so this is one
						// request rather than the usual two.
						token := render.Text(render.Get(row, "Invoice.token"))
						if token == "" {
							return "", false, usageErrorf("this invoice has no PDF token")
						}
						body, _, err := api.Download(c, "/invoices/pdf/"+id, client.Params{"token": token})
						if err != nil {
							return "", false, err
						}
						name := filepath.Join(".", "invoice-"+id+".pdf")
						if err := os.WriteFile(name, body, 0o600); err != nil {
							return "", false, err
						}
						return fmt.Sprintf("Saved %s (%d bytes)", name, len(body)), false, nil
					},
				},
				createAction("invoice", "invoice", "create"),
				editAction("invoice", "Invoice.id", "invoice", "edit"),
				// The browser was the one surface where an invoice's contents
				// could not be changed, while the CLI could. The ids to delete
				// by are in the detail pane's item table already.
				subRecordAction("i", "add item", "Add", "Invoice.id", "invoice",
					"invoice", "item", "add"),
				deleteAction("invoice", "Invoice.id", "/invoices/delete/", invoiceLabel),
			},
		},
		{
			Title:   "Expenses",
			Columns: expenseColumns,
			Detail:  expenseDetail(),
			Scopes:  documentScopes,
			Periods: documentPeriods,
			Load:    pageLoader("/expenses/index.json", perPage, expensePerPageCap),
			Actions: []tui.Action{
				{
					Key: "p", Label: "payment", Writes: true,
					Fields: []tui.FormField{{Key: "amount", Label: "amount"}},
					Run: func(c context.Context, row map[string]any, values map[string]string) (string, bool, error) {
						id := render.Text(render.Get(row, "Expense.id"))
						doc := map[string]any{}
						put(doc, "ExpensePayment", "expense_id", id)
						if input := values["amount"]; input != "" {
							amount, err := parseNumber(input)
							if err != nil {
								return "", false, usageErrorf("%q is not an amount", input)
							}
							put(doc, "ExpensePayment", "amount", amount)
						}
						if _, err := api.Post(c, "/expense_payments/add", doc); err != nil {
							return "", false, err
						}
						return "Payment recorded on expense " + id, true, nil
					},
				},
				createAction("expense", "expense", "add"),
				subRecordAction("i", "add item", "Add", "Expense.id", "expense",
					"expense", "item", "add"),
				editAction("expense", "Expense.id", "expense", "edit"),
				deleteAction("expense", "Expense.id", "/expenses/delete/", func(row map[string]any) string {
					return "expense " + render.Text(render.Get(row, "Expense.number"))
				}),
			},
		},
		{
			Title:   "Clients",
			Columns: clientColumns,
			Detail:  render.Pairs(nil, clientFields),
			Load:    pageLoader("/clients/index.json", perPage, defaultPerPageCap),
			Actions: []tui.Action{
				createAction("client", "client", "create"),
				editAction("client", "Client.id", "client", "edit"),
				deleteAction("client", "Client.id", "/clients/delete/", func(row map[string]any) string {
					return render.Text(render.Get(row, "Client.name"))
				}),
			},
		},
	}
}

// documentScopes mirror the tabs the web application puts above a document
// list. Invoices and expenses share the status codes, so they share these.
// documentPeriods narrow by issue date, using the API's own time-filter
// constants (see 'sf values time-filters') rather than a date range, which
// would need two more inputs to say what one constant says.
//
// "All time" is first and sends created:0 explicitly. The documentation gives
// the parameter a default of 6 — this year — so sending nothing leaves the
// window up to the server. Being explicit can only ever show more than before,
// and hiding older invoices is the failure worth avoiding.
var documentPeriods = []tui.Scope{
	{Label: "All time", Params: map[string]string{"created": "0"}},
	{Label: "This month", Params: map[string]string{"created": "4"}},
	{Label: "This year", Params: map[string]string{"created": "6"}},
	{Label: "Last year", Params: map[string]string{"created": "7"}},
}

var documentScopes = []tui.Scope{
	{Label: "All"},
	{Label: "Unpaid", Params: map[string]string{"status": "1|2"}},
	{Label: "Overdue", Params: map[string]string{"status": "99"}},
	{Label: "Paid", Params: map[string]string{"status": "3"}},
}

// pageLoader fetches one page of a resource, capped to what the resource
// allows.
func pageLoader(path string, perPage, cap int) func(context.Context, int, map[string]string) (tui.Page, error) {
	if perPage > cap {
		perPage = cap
	}
	return func(c context.Context, page int, scope map[string]string) (tui.Page, error) {
		opts := &listOptions{page: page, perPage: perPage, cap: cap}
		extra := client.Params{}
		for key, value := range scope {
			extra.Set(key, value)
		}
		result, err := fetchList(c, path, opts, extra)
		if err != nil {
			return tui.Page{}, err
		}
		return tui.Page{
			Items:     result.Items,
			ItemCount: result.ItemCount,
			PageCount: result.PageCount,
		}, nil
	}
}

// deleteAction is the same shape for every resource, and always confirms:
// these records are somebody's accounting.
func deleteAction(kind, idPath, path string, label func(map[string]any) string) tui.Action {
	return tui.Action{
		Key: "d", Label: "delete", Writes: true,
		Confirm: func(row map[string]any) string {
			return "Delete " + label(row) + "?"
		},
		Run: func(c context.Context, row map[string]any, _ map[string]string) (string, bool, error) {
			id := render.Text(render.Get(row, idPath))
			if id == "" {
				return "", false, usageErrorf("this %s has no id", kind)
			}
			if _, err := api.Get(c, path+id, nil); err != nil {
				return "", false, err
			}
			return "Deleted " + label(row), true, nil
		},
	}
}

func invoiceLabel(row map[string]any) string {
	if number := render.Text(render.Get(row, "Invoice.invoice_no_formatted")); number != "" {
		return "invoice " + number
	}
	return "invoice " + render.Text(render.Get(row, "Invoice.id"))
}
