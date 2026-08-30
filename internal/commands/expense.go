package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/output"
	"github.com/xseman/superfaktura-cli/internal/render"
)

var expenseColumns = []render.Column{
	{Header: "ID", Path: "Expense.id"},
	{Header: "Number", Path: "Expense.number"},
	{Header: "Name", Path: "Expense.name", Format: render.Truncate(28)},
	{Header: "Supplier", Path: "Client.name", Format: render.Truncate(24)},
	{Header: "Issued", Path: "Expense.created", Format: render.Date},
	{Header: "Due", Path: "Expense.due", Format: render.Date},
	{Header: "Amount", Path: "Expense.amount", Format: render.Money},
	{Header: "Paid", Path: "Expense.amount_paid", Format: render.Money},
}

var expenseCmd = &cobra.Command{
	Use:   "expense",
	Short: "Manage expenses",
}

func init() {
	rootCmd.AddCommand(expenseCmd)
	expenseCmd.AddCommand(
		expenseListCmd(),
		expenseViewCmd(),
		expenseAddCmd(),
		expenseEditCmd(),
		expenseDeleteCmd(),
		expensePayCmd(),
		expensePaymentCmd(),
		expenseItemCmd(),
		expenseAttachmentCmd(),
		expenseCategoriesCmd(),
		relatedCommand("expense", "/expenses/addRelatedItem", "/expenses/deleteRelatedItem/"),
	)
}

func expenseListCmd() *cobra.Command {
	// Expenses cap per_page at 100, unlike the 200 the other lists allow.
	opts := &listOptions{cap: expensePerPageCap}
	var status, clientRef, expenseType, category string

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List expenses",
		Args:    cobra.NoArgs,
		Example: "  sf expense list --type invoice\n  sf expense list --status 99 --json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			clientID, err := resolveClient(cmd, clientRef)
			if err != nil {
				return err
			}

			extra := client.Params{}
			extra.Set("status", status)
			extra.Set("client_id", clientID)
			extra.Set("type", expenseType)
			extra.Set("expense_category_id", category)

			result, err := fetchList(ctx(cmd), "/expenses/index.json", opts, extra)
			if err != nil {
				return err
			}
			return emitList(result, expenseColumns)
		},
	}

	opts.bind(cmd)
	f := cmd.Flags()
	f.StringVar(&status, "status", "", "Expense status (see 'sf values expense-statuses')")
	f.StringVar(&clientRef, "client", "", "Only expenses for this supplier, by ID or name")
	f.StringVar(&expenseType, "type", "", "Expense type (see 'sf values expense-types')")
	f.StringVar(&category, "category", "", "Expense category ID")
	return cmd
}

func expenseViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "view <id>",
		Aliases: []string{"show"},
		Short:   "Show one expense",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("expense", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/expenses/view/"+id+".json", nil)
			if err != nil {
				return err
			}
			obj, err := decodeObject(raw)
			if err != nil {
				return err
			}
			return emitDetail(raw, obj, "Expense.id", expenseDetail())
		},
	}
}

// expenseWriteFlags are the fields shared by expense add and edit.
type expenseWriteFlags struct {
	// items are the same line-item shape an invoice takes. Bound by `add`
	// alone: /expenses/edit appends them rather than replacing them, exactly as
	// /invoices/edit does, so a flag on edit would read as "these are the
	// items" and quietly double them. See API-DISCREPANCIES.md §B7 and
	// `sf expense item add`.
	items []string

	name, expenseType, variable  string
	created, due, currency       string
	amount, vat, category, notes string
	clientID, documentNo         string
	attachment                   string
	tags                         []string
}

func (e *expenseWriteFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&e.name, "name", "", "Expense name")
	f.StringVar(&e.expenseType, "type", "", "Expense type (see 'sf values expense-types')")
	f.StringVar(&e.variable, "variable", "", "Variable symbol")
	f.StringVar(&e.created, "created", "", "Issue date (YYYY-MM-DD)")
	f.StringVar(&e.due, "due", "", "Due date (YYYY-MM-DD)")
	f.StringVar(&e.currency, "currency", "", "Currency code")
	f.StringVar(&e.amount, "amount", "", "Amount without VAT")
	f.StringVar(&e.vat, "vat", "", "VAT rate as a percentage")
	f.StringVar(&e.category, "category", "", "Expense category ID")
	f.StringVar(&e.clientID, "client", "", "Supplier, by numeric ID or by name")
	f.StringVar(&e.documentNo, "document-no", "", "Supplier document number")
	f.StringVar(&e.notes, "comment", "", "Comment")
	f.StringVar(&e.attachment, "attachment", "",
		"File to attach; base64-encoded for the API (max 4 MB)")
	tagFlag(cmd, &e.tags)
}

func (e *expenseWriteFlags) apply(cmd *cobra.Command, doc map[string]any) error {
	clientID, err := resolveClient(cmd, e.clientID)
	if err != nil {
		return err
	}
	e.clientID = clientID

	if len(e.items) > 0 {
		lines := make([]map[string]any, 0, len(e.items))
		for _, spec := range e.items {
			item, err := documentItemSpec(spec)
			if err != nil {
				return err
			}
			lines = append(lines, item)
		}
		doc["ExpenseItem"] = lines
	}

	put(doc, "Expense", "name", e.name)
	put(doc, "Expense", "type", e.expenseType)
	put(doc, "Expense", "variable", e.variable)
	put(doc, "Expense", "created", e.created)
	put(doc, "Expense", "due", e.due)
	put(doc, "Expense", "currency", e.currency)
	put(doc, "Expense", "expense_category_id", e.category)
	put(doc, "Expense", "client_id", e.clientID)
	put(doc, "Expense", "document_number", e.documentNo)
	put(doc, "Expense", "comment", e.notes)

	for _, pair := range []struct{ flag, field, value string }{
		{"--amount", "amount", e.amount},
		{"--vat", "vat", e.vat},
	} {
		if pair.value == "" {
			continue
		}
		number, err := parseNumber(pair.value)
		if err != nil {
			return &output.Error{
				Code:    output.CodeUsage,
				Message: fmt.Sprintf("%s must be a number, got %q", pair.flag, pair.value),
			}
		}
		put(doc, "Expense", pair.field, number)
	}

	if e.attachment != "" {
		encoded, err := readAttachment(e.attachment)
		if err != nil {
			return err
		}
		put(doc, "Expense", "attachment", encoded)
	}

	tagIDs, err := resolveTags(cmd, e.tags)
	if err != nil {
		return err
	}
	putTags(doc, tagIDs)
	return nil
}

func expenseAddCmd() *cobra.Command {
	var data string
	flags := &expenseWriteFlags{}

	cmd := &cobra.Command{
		Use:     "add",
		Aliases: []string{"create"},
		Short:   "Add an expense",
		Args:    cobra.NoArgs,
		Example: "  sf expense add --name 'Hosting' --amount 49 --vat 23 --type invoice\n" +
			"  sf expense add --name 'Taxi' --amount 12 --attachment blocek.pdf",
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := readPayload(data)
			if err != nil {
				return err
			}
			if err := flags.apply(cmd, doc); err != nil {
				return err
			}
			if err := requirePayload(doc, "Pass --name and --amount, or --data"); err != nil {
				return err
			}
			raw, err := api.Post(ctx(cmd), "/expenses/add", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "Expense", "Expense added")
		},
	}

	dataFlag(cmd, &data)
	flags.bind(cmd)
	// Creation only. /expenses/edit appends line items rather than replacing
	// them, so the same flag on edit would read as "these are the items" and
	// double them instead. `sf expense item add` is the append, named for it.
	cmd.Flags().StringArrayVar(&flags.items, "item", nil,
		"Line item as name:quantity:unit_price:tax (repeatable)")
	return cmd
}

func expenseEditCmd() *cobra.Command {
	var data string
	flags := &expenseWriteFlags{}

	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit an expense",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("expense", args[0])
			if err != nil {
				return err
			}
			doc, err := readPayload(data)
			if err != nil {
				return err
			}
			if err := flags.apply(cmd, doc); err != nil {
				return err
			}
			put(doc, "Expense", "id", id)

			raw, err := api.Post(ctx(cmd), "/expenses/edit", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "Expense", "Expense "+id+" updated")
		},
	}

	dataFlag(cmd, &data)
	flags.bind(cmd)
	return cmd
}

func expenseDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "Delete an expense",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("expense", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/expenses/delete/"+id, nil)
			if err != nil {
				return err
			}
			return emitWrite(raw, "Expense", "Expense "+id+" deleted")
		},
	}
}

func expensePayCmd() *cobra.Command {
	var data, amount, currency, paymentType, date, cashRegister string

	cmd := &cobra.Command{
		Use:     "pay <id>",
		Short:   "Record a payment against an expense",
		Args:    cobra.ExactArgs(1),
		Example: "  sf expense pay 88 --amount 49 --type transfer",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("expense", args[0])
			if err != nil {
				return err
			}
			doc, err := readPayload(data)
			if err != nil {
				return err
			}
			put(doc, "ExpensePayment", "expense_id", id)
			put(doc, "ExpensePayment", "currency", currency)
			put(doc, "ExpensePayment", "payment_type", paymentType)
			put(doc, "ExpensePayment", "created", date)
			put(doc, "ExpensePayment", "cash_register_id", cashRegister)
			if amount != "" {
				value, err := parseNumber(amount)
				if err != nil {
					return &output.Error{
						Code:    output.CodeUsage,
						Message: fmt.Sprintf("--amount must be a number, got %q", amount),
					}
				}
				put(doc, "ExpensePayment", "amount", value)
			}

			raw, err := api.Post(ctx(cmd), "/expense_payments/add", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "ExpensePayment", "Payment recorded for expense "+id)
		},
	}

	dataFlag(cmd, &data)
	f := cmd.Flags()
	f.StringVar(&amount, "amount", "", "Amount paid")
	f.StringVar(&currency, "currency", "", "Currency code")
	f.StringVar(&paymentType, "type", "", "Payment type (see 'sf values payment-types')")
	f.StringVar(&date, "date", "", "Payment date (YYYY-MM-DD)")
	f.StringVar(&cashRegister, "cash-register", "", "Cash register ID")
	return cmd
}

func expensePaymentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "payment", Short: "Manage expense payments"}
	cmd.AddCommand(&cobra.Command{
		Use:     "delete <payment-id>",
		Aliases: []string{"rm"},
		Short:   "Delete an expense payment",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("payment", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/expense_payments/delete/"+id, nil)
			if err != nil {
				return err
			}
			return emitWrite(raw, "", "Payment "+id+" deleted")
		},
	})
	return cmd
}

func expenseItemCmd() *cobra.Command {
	var itemIDs []string

	remove := &cobra.Command{
		Use:     "delete [item-id]...",
		Aliases: []string{"rm"},
		Short:   "Delete expense line items",
		Long: `Deletes line items from an expense.

Takes ids either as arguments or as repeated --id. The positional form is what
lets the output of 'sf expense item list --ids-only' pipe straight in, the same
way it does for invoices.`,
		Args:    cobra.ArbitraryArgs,
		Example: "  sf expense item delete 12 13\n  sf expense item delete --id 12 --id 13",
		RunE: func(cmd *cobra.Command, args []string) error {
			itemIDs = append(itemIDs, args...)
			if len(itemIDs) == 0 {
				return &output.Error{
					Code:    output.CodeUsage,
					Message: "pass at least one item id",
					Hint:    "Find them with 'sf expense item list --expense <id>'",
				}
			}
			raw, err := api.Delete(ctx(cmd), "/expense_items/delete",
				map[string]any{"ExpenseItem": map[string]any{"id": itemIDs}})
			if err != nil {
				return err
			}
			return emitWrite(raw, "", fmt.Sprintf("Deleted %d expense %s", len(itemIDs), plural(len(itemIDs), "item", "items")))
		},
	}
	remove.Flags().StringArrayVar(&itemIDs, "id", nil, "Expense item ID (repeatable)")

	var addTo string
	var specs []string
	add := &cobra.Command{
		Use:   "add",
		Short: "Add line items to an existing expense",
		Long: `Appends line items to an expense.

There is no endpoint for adding one item: /expenses/edit is the only way in,
and sending it an ExpenseItem array appends to what is already there rather
than replacing it — measured, see API-DISCREPANCIES.md.

Which is why this is 'item add' and not an --item flag on 'expense edit': the
flag would read as "these are the items" and would in fact double them. Remove
what is wrong with 'sf expense item delete'.`,
		Args:    cobra.NoArgs,
		Example: "  sf expense item add --expense 4505 --item 'Hosting:1:49:23'",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if addTo == "" {
				return &output.Error{Code: output.CodeUsage, Message: "--expense is required"}
			}
			id, err := requireID("expense", addTo)
			if err != nil {
				return err
			}
			if len(specs) == 0 {
				return usageErrorf("pass at least one --item")
			}

			items := make([]map[string]any, 0, len(specs))
			for _, spec := range specs {
				item, err := documentItemSpec(spec)
				if err != nil {
					return err
				}
				items = append(items, item)
			}

			doc := map[string]any{"ExpenseItem": items}
			put(doc, "Expense", "id", id)
			raw, err := api.Post(ctx(cmd), "/expenses/edit", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "Expense",
				fmt.Sprintf("Added %d %s to expense %s", len(items), plural(len(items), "item", "items"), id))
		},
	}
	add.Flags().StringVar(&addTo, "expense", "", "Expense ID to add to (required)")
	add.Flags().StringArrayVar(&specs, "item", nil,
		"Line item as name:quantity:unit_price:tax (repeatable)")

	var listOf string
	list := &cobra.Command{
		Use:   "list",
		Short: "List an expense's line items with their IDs",
		Long: `Lists the line items on an expense.

Costs one request: the items travel inside the expense record, so there is
nothing else to fetch. The ID column is the one 'sf expense item delete' takes.`,
		Args:    cobra.NoArgs,
		Example: "  sf expense item list --expense 4505",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listOf == "" {
				return &output.Error{Code: output.CodeUsage, Message: "--expense is required"}
			}
			id, err := requireID("expense", listOf)
			if err != nil {
				return err
			}
			return itemList(cmd, "/expenses/view/"+id+".json", "ExpenseItem", expenseItemColumns,
				output.Step{
					Cmd:  "sf expense item delete ITEM-ID",
					Does: "remove one of these lines",
				},
				output.Step{
					Cmd:  "sf expense item add --expense " + id + " --item 'ITEM:QTY:PRICE:VAT'",
					Does: "add another",
				})
		},
	}
	list.Flags().StringVar(&listOf, "expense", "", "Expense ID to list (required)")

	cmd := &cobra.Command{Use: "item", Short: "Manage expense line items"}
	cmd.AddCommand(list, add, remove)
	return cmd
}

func expenseAttachmentCmd() *cobra.Command {
	var destination string

	download := &cobra.Command{
		Use:     "download <expense-id> <attachment-id>",
		Short:   "Download an expense attachment",
		Args:    cobra.ExactArgs(2),
		Example: "  sf expense attachment download 88 3 -o receipt.pdf",
		RunE: func(cmd *cobra.Command, args []string) error {
			expenseID, err := requireID("expense", args[0])
			if err != nil {
				return err
			}
			attachmentID, err := requireID("attachment", args[1])
			if err != nil {
				return err
			}

			body, _, err := api.Download(ctx(cmd),
				"/expenses/downloadAttachment/"+expenseID+"/"+attachmentID, nil)
			if err != nil {
				return err
			}
			if destination == "" {
				destination = "expense-" + expenseID + "-attachment-" + attachmentID
			}
			written, err := writeBinary(destination, body)
			if err != nil || written == "" {
				return err
			}
			return emitAction(map[string]any{"file": written, "bytes": len(body)},
				fmt.Sprintf("Saved %s (%d bytes)", written, len(body)))
		},
	}
	download.Flags().StringVarP(&destination, "output", "o", "", "Write to this file, or - for stdout")

	cmd := &cobra.Command{Use: "attachment", Short: "Work with expense attachments"}
	cmd.AddCommand(download)
	return cmd
}

func expenseCategoriesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "categories",
		Short: "List expense categories",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := cachedGet(cmd, "/expenses/expense_categories", nil, valueListTTL)
			if err != nil {
				return err
			}
			// The API answers with a tree, not a list.
			result, err := decodeCategoryTree(raw)
			if err != nil {
				return err
			}
			return emitList(result, apiValueLists["expense-categories"].columns)
		},
	}
}
