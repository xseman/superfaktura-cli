package commands

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/output"
	"github.com/xseman/superfaktura-cli/internal/render"
	"github.com/xseman/superfaktura-cli/internal/tui"
)

var invoiceColumns = []render.Column{
	{Header: "ID", Path: "Invoice.id"},
	{Header: "Number", Path: "Invoice.invoice_no_formatted"},
	{Header: "Client", Path: "Client.name", Format: render.Truncate(28)},
	{Header: "Issued", Path: "Invoice.created", Format: render.Date},
	{Header: "Due", Path: "Invoice.due", Format: render.Date},
	{Header: "Total", Path: "0.total", Format: render.Money},
	{Header: "To pay", Path: "0.to_pay", Format: render.Money},
}

var invoiceCmd = &cobra.Command{
	Use:   "invoice",
	Short: "Manage invoices",
}

func init() {
	rootCmd.AddCommand(invoiceCmd)
	invoiceCmd.AddCommand(
		invoiceListCmd(),
		invoiceViewCmd(),
		invoiceCreateCmd(),
		invoiceEditCmd(),
		invoiceDeleteCmd(),
		invoicePDFCmd(),
		invoiceReceiptCmd(),
		invoiceSendCmd(),
		invoicePostCmd(),
		invoiceMarkSentCmd(),
		invoiceUnpayableCmd(),
		invoicePayCmd(),
		invoiceLanguageCmd(),
		invoicePaymentCmd(),
		invoiceItemCmd(),
		invoiceRelatedCmd(),
		invoiceRecoverCmd(),
	)
}

func invoiceListCmd() *cobra.Command {
	opts := &listOptions{}
	var status, clientRef, docType, variable, orderNo, tag string

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List invoices",
		Args:    cobra.NoArgs,
		Example: `  sf invoice list
  sf invoice list --type proforma --per-page 50
  sf invoice list --status 2 --json
  sf invoice list --filter created=3 --filter created_since=2026-01-01`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			clientID, err := resolveClient(cmd, clientRef)
			if err != nil {
				return err
			}

			extra := client.Params{}
			extra.Set("status", status)
			extra.Set("client_id", clientID)
			extra.Set("type", docType)
			extra.Set("variable", variable)
			extra.Set("order_no", orderNo)
			extra.Set("tag", tag)

			result, err := fetchList(ctx(cmd), "/invoices/index.json", opts, extra)
			if err != nil {
				return err
			}
			return emitList(result, invoiceColumns)
		},
	}

	opts.bind(cmd)
	f := cmd.Flags()
	f.StringVar(&status, "status", "", "Invoice status (see 'sf values invoice-statuses')")
	f.StringVar(&clientRef, "client", "", "Only invoices for this client, by ID or name")
	f.StringVar(&docType, "type", "", "Document type, | separated (regular, proforma, cancel, ...)")
	f.StringVar(&variable, "variable", "", "Variable symbol")
	f.StringVar(&orderNo, "order-no", "", "Order number the invoice was created from")
	f.StringVar(&tag, "tag", "", "Tag ID")
	return cmd
}

// maxBatchDetails is the number of invoices the API will describe in one call.
const maxBatchDetails = 100

func invoiceViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "view <id>...",
		Aliases: []string{"show"},
		Short:   "Show one or more invoices",
		Long: `Shows an invoice.

Several identifiers fetch them all in a single request, up to 100 — the
difference between one request and a hundred out of a daily allowance of 1000.`,
		Args:    cobra.MinimumNArgs(1),
		Example: "  sf invoice view 1042\n  sf invoice view 1042 1043 1044 --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			ids := make([]string, 0, len(args))
			for _, arg := range args {
				id, err := requireID("invoice", arg)
				if err != nil {
					return err
				}
				ids = append(ids, id)
			}
			if len(ids) > maxBatchDetails {
				return usageErrorf("at most %d invoices can be fetched at once, got %d",
					maxBatchDetails, len(ids))
			}

			if len(ids) == 1 {
				raw, err := api.Get(ctx(cmd), "/invoices/view/"+ids[0]+".json", nil)
				if err != nil {
					return err
				}
				obj, err := decodeObject(raw)
				if err != nil {
					return err
				}
				return emitDetail(obj, "Invoice.id", invoiceDetail())
			}

			// The batch endpoint answers a map keyed by invoice id, which
			// decodeList already understands.
			raw, err := api.Get(ctx(cmd), "/invoices/getInvoiceDetails/"+strings.Join(ids, ","), nil)
			if err != nil {
				return err
			}
			result, err := decodeList(raw)
			if err != nil {
				return err
			}
			if len(result.Items) == 0 {
				return &output.Error{
					Code:    output.CodeNotFound,
					Message: "no invoices found for " + strings.Join(ids, ", "),
				}
			}
			return emitList(result, invoiceColumns)
		},
	}
}

// documentItemSpec parses the compact item syntax used by --item, on invoices
// and expenses alike: both take the same line-item shape.
//
// Format: name:quantity:unit_price:tax — the three numeric parts are optional.
func documentItemSpec(spec string) (map[string]any, error) {
	parts := strings.Split(spec, ":")
	if parts[0] == "" {
		return nil, &output.Error{
			Code:    output.CodeUsage,
			Message: fmt.Sprintf("--item %q has no name", spec),
			Hint:    "Format: name:quantity:unit_price:tax, e.g. --item 'Consulting:2:500:23'",
		}
	}
	if len(parts) > 4 {
		return nil, &output.Error{
			Code:    output.CodeUsage,
			Message: fmt.Sprintf("--item %q has too many fields", spec),
			Hint:    "Format: name:quantity:unit_price:tax",
		}
	}

	item := map[string]any{"name": parts[0]}
	for i, field := range []string{"quantity", "unit_price", "tax"} {
		if len(parts) <= i+1 || parts[i+1] == "" {
			continue
		}
		value, err := parseNumber(parts[i+1])
		if err != nil {
			return nil, &output.Error{
				Code:    output.CodeUsage,
				Message: fmt.Sprintf("--item %q: %s must be a number, got %q", spec, field, parts[i+1]),
			}
		}
		item[field] = value
	}
	return item, nil
}

// invoiceWriteFlags are the fields shared by invoice create and edit.
//
// They live in one place because they drifted apart when they did not: create
// could set a client and a type that edit could never correct, and edit could
// set an issue date, a delivery date, a constant symbol, a comment and a
// payment type that create could not — so a back-dated invoice had to be made
// today and then edited. Expenses have had this shape all along and never
// developed the gap.
type invoiceWriteFlags struct {
	name        string
	clientRef   string
	docType     string
	created     string
	delivery    string
	due         string
	variable    string
	constant    string
	paymentType string
	comment     string
	tags        []string
}

func (i *invoiceWriteFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&i.name, "name", "", "Invoice name")
	f.StringVar(&i.clientRef, "client", "", "Existing client, by numeric ID or by name")
	f.StringVar(&i.docType, "type", "", "Document type (see 'sf values invoice-types')")
	f.StringVar(&i.created, "created", "", "Issue date (YYYY-MM-DD)")
	f.StringVar(&i.delivery, "delivery", "", "Delivery date (YYYY-MM-DD)")
	f.StringVar(&i.due, "due", "", "Due date (YYYY-MM-DD)")
	f.StringVar(&i.variable, "variable", "", "Variable symbol")
	f.StringVar(&i.constant, "constant", "", "Constant symbol")
	f.StringVar(&i.paymentType, "payment-type", "", "Payment type (see 'sf values payment-types')")
	f.StringVar(&i.comment, "comment", "", "Comment")
	tagFlag(cmd, &i.tags)
}

// apply layers the flags onto a document, resolving the names among them.
func (i *invoiceWriteFlags) apply(cmd *cobra.Command, doc map[string]any) error {
	clientID, err := resolveClient(cmd, i.clientRef)
	if err != nil {
		return err
	}
	tagIDs, err := resolveTags(cmd, i.tags)
	if err != nil {
		return err
	}

	put(doc, "Invoice", "name", i.name)
	put(doc, "Invoice", "client_id", clientID)
	put(doc, "Invoice", "type", i.docType)
	put(doc, "Invoice", "created", i.created)
	put(doc, "Invoice", "delivery", i.delivery)
	put(doc, "Invoice", "due", i.due)
	put(doc, "Invoice", "variable", i.variable)
	put(doc, "Invoice", "constant", i.constant)
	put(doc, "Invoice", "payment_type", i.paymentType)
	put(doc, "Invoice", "comment", i.comment)
	putTags(doc, tagIDs)
	return nil
}

func invoiceCreateCmd() *cobra.Command {
	flags := &invoiceWriteFlags{}
	var data, newClient, checksum string
	var items []string

	cmd := &cobra.Command{
		Use:     "create",
		Aliases: []string{"add"},
		Short:   "Create an invoice",
		Long: `Creates an invoice.

Supply the full payload with --data for complete control, or use the flags for
the common case. Flags are applied on top of --data. With no flags at all on a
terminal, an interactive form collects the essentials.

Pass --checksum with an identifier of your own (an order number, say) to make
the call recoverable: if the response is lost to a timeout, 'sf invoice
recover' returns the original one instead of leaving you to guess whether the
invoice was created.`,
		Args: cobra.NoArgs,
		Example: `  sf invoice create --client 42 --item 'Consulting:2:500:23'
  sf invoice create --client 'Acme s.r.o.' --item 'Consulting:2:500:23' --tag urgent
  sf invoice create --data @invoice.json
  echo '{"Invoice":{"name":"X"}}' | sf invoice create --data -`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := readPayload(data)
			if err != nil {
				return err
			}

			if len(doc) == 0 && !cmd.Flags().Changed("client") && len(items) == 0 {
				if !interactive() {
					return &output.Error{
						Code:    output.CodeUsage,
						Message: "nothing to create",
						Hint:    "Pass --data, or --client with at least one --item",
					}
				}
				if err := promptInvoice(&flags.clientRef, &flags.name, &items); err != nil {
					return err
				}
			}

			if err := flags.apply(cmd, doc); err != nil {
				return err
			}
			if newClient != "" {
				put(doc, "Client", "name", newClient)
			}

			if len(items) > 0 {
				parsed := make([]map[string]any, 0, len(items))
				for _, spec := range items {
					item, err := documentItemSpec(spec)
					if err != nil {
						return err
					}
					parsed = append(parsed, item)
				}
				doc["InvoiceItem"] = parsed
			}

			if checksum != "" {
				if len(checksum) > maxChecksumLength {
					return usageErrorf("--checksum is limited to %d characters, got %d",
						maxChecksumLength, len(checksum))
				}
				// A top-level key alongside the models, not inside Invoice.
				doc["checksum"] = checksum
			}

			if err := requirePayload(doc, "Pass --data, or --client with at least one --item"); err != nil {
				return err
			}

			raw, err := api.Post(ctx(cmd), "/invoices/create", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "Invoice", "Invoice created",
				output.Step{Cmd: "sf invoice view %s", Does: "see it"},
				output.Step{Cmd: "sf invoice pdf %s", Does: "download the PDF"},
				output.Step{Cmd: "sf invoice send %s --to EMAIL", Does: "email it to the client"})
		},
	}

	dataFlag(cmd, &data)
	flags.bind(cmd)
	f := cmd.Flags()
	f.StringVar(&newClient, "new-client", "", "Create the invoice for a new client with this name")
	f.StringArrayVar(&items, "item", nil, "Line item as name:quantity:unit_price:tax (repeatable)")
	f.StringVar(&checksum, "checksum", "",
		"Caller-supplied identifier for recovering the response after a timeout (max 32 chars)")
	return cmd
}

func promptInvoice(clientID, name *string, items *[]string) error {
	var item string
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Client ID").
			Description("Find one with 'sf client list'").
			Value(clientID).
			Validate(required("client ID")),
		huh.NewInput().
			Title("Invoice name").
			Description("Optional — SuperFaktura generates one when empty").
			Value(name),
		huh.NewInput().
			Title("Line item").
			Description("name:quantity:unit_price:tax, e.g. Consulting:2:500:23").
			Value(&item).
			Validate(required("line item")),
	))
	form = form.WithTheme(tui.FormTheme())
	if err := form.Run(); err != nil {
		return &output.Error{Code: output.CodeUsage, Message: err.Error()}
	}
	*items = append(*items, item)
	return nil
}

func invoiceEditCmd() *cobra.Command {
	flags := &invoiceWriteFlags{}
	var data string

	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit an invoice",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("invoice", args[0])
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
			put(doc, "Invoice", "id", id)

			raw, err := api.Post(ctx(cmd), "/invoices/edit", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "Invoice", "Invoice updated")
		},
	}

	dataFlag(cmd, &data)
	flags.bind(cmd)
	return cmd
}

func invoiceDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "Delete an invoice",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("invoice", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/invoices/delete/"+id, nil)
			if err != nil {
				return err
			}
			return emitWrite(raw, "Invoice", "Invoice "+id+" deleted")
		},
	}
}

func invoicePDFCmd() *cobra.Command {
	var destination, language string
	var bysquare, paypal, noSignature bool

	cmd := &cobra.Command{
		Use:   "pdf <id>",
		Short: "Download an invoice as PDF",
		Long: `Downloads the invoice PDF.

The PDF endpoint is addressed by a per-invoice token rather than by the ID
alone, so this fetches the invoice detail first to read that token.`,
		Args:    cobra.ExactArgs(1),
		Example: "  sf invoice pdf 1042 -o faktura.pdf\n  sf invoice pdf 1042 --bysquare | lp",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("invoice", args[0])
			if err != nil {
				return err
			}

			token, err := invoiceToken(cmd, id)
			if err != nil {
				return err
			}

			params := client.Params{"token": token}
			if bysquare {
				params.Set("bysquare", "1")
			}
			if paypal {
				params.Set("paypal", "1")
			}
			if noSignature {
				params.Set("no-signature", "1")
			}
			params.Set("language", language)

			body, _, err := api.Download(ctx(cmd), "/invoices/pdf/"+id, params)
			if err != nil {
				return err
			}

			if destination == "" {
				destination = "invoice-" + id + ".pdf"
			}
			written, err := writeBinary(destination, body)
			if err != nil {
				return err
			}
			if written == "" {
				return nil
			}
			return emitAction(map[string]any{"file": written, "bytes": len(body)},
				fmt.Sprintf("Saved %s (%d bytes)", written, len(body)))
		},
	}

	f := cmd.Flags()
	f.StringVarP(&destination, "output", "o", "", "Write to this file, or - for stdout (default invoice-<id>.pdf)")
	f.StringVar(&language, "language", "", "Document language (see 'sf values languages')")
	f.BoolVar(&bysquare, "bysquare", false, "Include a PAY by square QR code")
	f.BoolVar(&paypal, "paypal", false, "Include a PayPal button")
	f.BoolVar(&noSignature, "no-signature", false, "Hide the signature")
	return cmd
}

// invoiceToken reads the token the PDF and receipt endpoints require.
func invoiceToken(cmd *cobra.Command, id string) (string, error) {
	// Cached: the token is fixed for the life of the invoice, and without this
	// every `sf invoice pdf` costs two requests instead of one.
	raw, err := cachedGet(cmd, "/invoices/view/"+id+".json", nil, tokenTTL)
	if err != nil {
		return "", err
	}
	obj, err := decodeObject(raw)
	if err != nil {
		return "", err
	}
	token := render.Text(render.Get(obj, "Invoice.token"))
	if token == "" {
		return "", &output.Error{
			Code:    output.CodeAPI,
			Message: "invoice " + id + " has no PDF token",
		}
	}
	return token, nil
}

func invoiceReceiptCmd() *cobra.Command {
	var destination string

	cmd := &cobra.Command{
		Use:   "receipt <id>",
		Short: "Download the cash receipt for an invoice",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("invoice", args[0])
			if err != nil {
				return err
			}
			body, _, err := api.Download(ctx(cmd), "/invoices/receipt/"+id, nil)
			if err != nil {
				return err
			}
			if destination == "" {
				destination = "receipt-" + id + ".pdf"
			}
			written, err := writeBinary(destination, body)
			if err != nil || written == "" {
				return err
			}
			return emitAction(map[string]any{"file": written, "bytes": len(body)},
				fmt.Sprintf("Saved %s (%d bytes)", written, len(body)))
		},
	}

	cmd.Flags().StringVarP(&destination, "output", "o", "", "Write to this file, or - for stdout")
	return cmd
}

func invoiceSendCmd() *cobra.Command {
	var data, to, subject, body, pdfLanguage string
	var cc, bcc []string

	cmd := &cobra.Command{
		Use:     "send <id>",
		Short:   "Email an invoice to a recipient",
		Long:    "Emails the invoice. SuperFaktura limits this to 100 emails per hour.",
		Args:    cobra.ExactArgs(1),
		Example: "  sf invoice send 1042 --to client@example.com --subject 'Faktúra 2026001'",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("invoice", args[0])
			if err != nil {
				return err
			}
			doc, err := readPayload(data)
			if err != nil {
				return err
			}
			put(doc, "Email", "invoice_id", id)
			put(doc, "Email", "to", to)
			put(doc, "Email", "subject", subject)
			put(doc, "Email", "body", body)
			put(doc, "Email", "pdf_language", pdfLanguage)
			if len(cc) > 0 {
				put(doc, "Email", "cc", cc)
			}
			if len(bcc) > 0 {
				put(doc, "Email", "bcc", bcc)
			}

			if render.Text(render.Get(doc, "Email.to")) == "" {
				return &output.Error{
					Code:    output.CodeUsage,
					Message: "--to is required",
				}
			}

			// This endpoint is documented with a raw JSON body, unlike the
			// form-encoded writes elsewhere in the API.
			raw, err := api.PostJSON(ctx(cmd), "/invoices/send", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "", "Invoice sent")
		},
	}

	dataFlag(cmd, &data)
	f := cmd.Flags()
	f.StringVar(&to, "to", "", "Recipient email address (required)")
	f.StringVar(&subject, "subject", "", "Email subject")
	f.StringVar(&body, "body", "", "Email body")
	f.StringVar(&pdfLanguage, "pdf-language", "", "Language of the attached PDF")
	f.StringArrayVar(&cc, "cc", nil, "Carbon copy recipient (repeatable)")
	f.StringArrayVar(&bcc, "bcc", nil, "Blind carbon copy recipient (repeatable)")
	return cmd
}

func invoicePostCmd() *cobra.Command {
	var data string

	cmd := &cobra.Command{
		Use:   "post <id>",
		Short: "Send an invoice by postal mail",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("invoice", args[0])
			if err != nil {
				return err
			}
			doc, err := readPayload(data)
			if err != nil {
				return err
			}
			put(doc, "Post", "invoice_id", id)

			raw, err := api.Post(ctx(cmd), "/invoices/post", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "", "Invoice queued for postal delivery")
		},
	}

	dataFlag(cmd, &data)
	return cmd
}

func invoiceMarkSentCmd() *cobra.Command {
	var email, subject, message string

	cmd := &cobra.Command{
		Use:   "mark-sent <id>",
		Short: "Record an invoice as sent without emailing it",
		Long: `Records the invoice as sent.

With --email the API logs a specific recipient (mark_as_sent); without it the
invoice is simply flagged as sent (mark_sent).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("invoice", args[0])
			if err != nil {
				return err
			}
			if email == "" {
				raw, err := api.Get(ctx(cmd), "/invoices/mark_sent/"+id, nil)
				if err != nil {
					return err
				}
				return emitWrite(raw, "Invoice", "Invoice "+id+" marked as sent")
			}

			doc := map[string]any{}
			put(doc, "InvoiceEmail", "invoice_id", id)
			put(doc, "InvoiceEmail", "email", email)
			put(doc, "InvoiceEmail", "subject", subject)
			put(doc, "InvoiceEmail", "message", message)

			raw, err := api.PostJSON(ctx(cmd), "/invoices/mark_as_sent", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "Invoice", "Invoice "+id+" marked as sent to "+email)
		},
	}

	f := cmd.Flags()
	f.StringVar(&email, "email", "", "Recipient to record")
	f.StringVar(&subject, "subject", "", "Subject to record")
	f.StringVar(&message, "message", "", "Message to record")
	return cmd
}

func invoiceUnpayableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "will-not-be-paid <id>",
		Short: `Mark an invoice as "will not be paid"`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("invoice", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/invoices/will_not_be_paid/"+id, nil)
			if err != nil {
				return err
			}
			return emitWrite(raw, "Invoice", "Invoice "+id+" marked as will not be paid")
		},
	}
}

func invoicePayCmd() *cobra.Command {
	var data, amount, currency, paymentType, date, documentNo, cashRegister string

	cmd := &cobra.Command{
		Use:   "pay <id>",
		Short: "Record a payment against an invoice",
		Long: `Records a payment.

Only regular, proforma and cancel invoices can be paid. Omitting --amount pays
the invoice in full.`,
		Args:    cobra.ExactArgs(1),
		Example: "  sf invoice pay 1042 --amount 1240 --type transfer --date 2026-08-01",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("invoice", args[0])
			if err != nil {
				return err
			}
			doc, err := readPayload(data)
			if err != nil {
				return err
			}
			put(doc, "InvoicePayment", "invoice_id", id)
			put(doc, "InvoicePayment", "currency", currency)
			put(doc, "InvoicePayment", "payment_type", paymentType)
			put(doc, "InvoicePayment", "date", date)
			put(doc, "InvoicePayment", "document_no", documentNo)
			put(doc, "InvoicePayment", "cash_register_id", cashRegister)
			if amount != "" {
				value, err := parseNumber(amount)
				if err != nil {
					return &output.Error{
						Code:    output.CodeUsage,
						Message: fmt.Sprintf("--amount must be a number, got %q", amount),
					}
				}
				put(doc, "InvoicePayment", "amount", value)
			}

			raw, err := api.Post(ctx(cmd), "/invoice_payments/add/ajax:1/api:1", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "InvoicePayment", "Payment recorded for invoice "+id)
		},
	}

	dataFlag(cmd, &data)
	f := cmd.Flags()
	f.StringVar(&amount, "amount", "", "Amount paid (defaults to the full total)")
	f.StringVar(&currency, "currency", "", "Currency code (see 'sf values currencies')")
	f.StringVar(&paymentType, "type", "", "Payment type (see 'sf values payment-types')")
	f.StringVar(&date, "date", "", "Payment date (YYYY-MM-DD)")
	f.StringVar(&documentNo, "document-no", "", "Document number")
	f.StringVar(&cashRegister, "cash-register", "", "Cash register ID")
	return cmd
}

func invoiceLanguageCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "language <id> <lang>",
		Short:   "Set the document language of an invoice",
		Args:    cobra.ExactArgs(2),
		Example: "  sf invoice language 1042 eng",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("invoice", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/invoices/setinvoicelanguage/"+id,
				client.Params{"lang": args[1]})
			if err != nil {
				return err
			}
			return emitWrite(raw, "Invoice", "Invoice "+id+" language set to "+args[1])
		},
	}
}

func invoicePaymentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "payment",
		Short: "Manage invoice payments",
	}
	cmd.AddCommand(&cobra.Command{
		Use:     "delete <payment-id>",
		Aliases: []string{"rm"},
		Short:   "Delete a payment",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("payment", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/invoice_payments/delete/"+id, nil)
			if err != nil {
				return err
			}
			return emitWrite(raw, "", "Payment "+id+" deleted")
		},
	})
	return cmd
}

func invoiceItemCmd() *cobra.Command {
	var invoiceID string

	remove := &cobra.Command{
		Use:     "delete <item-id>",
		Aliases: []string{"rm"},
		Short:   "Delete a line item",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			itemID, err := requireID("item", args[0])
			if err != nil {
				return err
			}
			if invoiceID == "" {
				return &output.Error{Code: output.CodeUsage, Message: "--invoice is required"}
			}
			raw, err := api.Get(ctx(cmd), "/invoice_items/delete/"+itemID,
				client.Params{"invoice_id": invoiceID})
			if err != nil {
				return err
			}
			return emitWrite(raw, "", "Item "+itemID+" deleted")
		},
	}
	remove.Flags().StringVar(&invoiceID, "invoice", "", "Invoice ID the item belongs to (required)")

	var addTo string
	var specs []string
	add := &cobra.Command{
		Use:   "add",
		Short: "Add line items to an existing invoice",
		Long: `Appends line items to an invoice.

There is no endpoint for adding one item: /invoices/edit is the only way in,
and sending it an InvoiceItem array appends to what is already there rather
than replacing it. That is measured, not assumed — see API-DISCREPANCIES.md.

Which is why this is 'item add' and not an --item flag on 'invoice edit': the
flag would read as "these are the items" and would in fact duplicate them.
Remove what is wrong with 'sf invoice item delete'.`,
		Args:    cobra.NoArgs,
		Example: "  sf invoice item add --invoice 309101 --item 'Consulting:2:500:23'",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Before requireID, which would otherwise report an unset flag as
			// "must be a number, got \"\"" — the wrong problem entirely.
			if addTo == "" {
				return &output.Error{Code: output.CodeUsage, Message: "--invoice is required"}
			}
			id, err := requireID("invoice", addTo)
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

			doc := map[string]any{"InvoiceItem": items}
			put(doc, "Invoice", "id", id)
			raw, err := api.Post(ctx(cmd), "/invoices/edit", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "Invoice",
				fmt.Sprintf("Added %d %s to invoice %s", len(items), plural(len(items), "item", "items"), id))
		},
	}
	add.Flags().StringVar(&addTo, "invoice", "", "Invoice ID to add to (required)")
	add.Flags().StringArrayVar(&specs, "item", nil,
		"Line item as name:quantity:unit_price:tax (repeatable)")

	var listOf string
	list := &cobra.Command{
		Use:   "list",
		Short: "List an invoice's line items with their IDs",
		Long: `Lists the line items on an invoice.

Costs one request: the items travel inside the invoice record, so there is
nothing else to fetch. The ID column is the one 'sf invoice item delete' takes.`,
		Args:    cobra.NoArgs,
		Example: "  sf invoice item list --invoice 309101",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listOf == "" {
				return &output.Error{Code: output.CodeUsage, Message: "--invoice is required"}
			}
			id, err := requireID("invoice", listOf)
			if err != nil {
				return err
			}
			return itemList(cmd, "/invoices/view/"+id+".json", "InvoiceItem", invoiceItemColumns,
				output.Step{
					Cmd:  "sf invoice item delete ITEM-ID --invoice " + id,
					Does: "remove one of these lines",
				},
				output.Step{
					Cmd:  "sf invoice item add --invoice " + id + " --item 'ITEM:QTY:PRICE:VAT'",
					Does: "add another",
				})
		},
	}
	list.Flags().StringVar(&listOf, "invoice", "", "Invoice ID to list (required)")

	cmd := &cobra.Command{Use: "item", Short: "Manage invoice line items"}
	cmd.AddCommand(list, add, remove)
	return cmd
}

func invoiceRelatedCmd() *cobra.Command {
	return relatedCommand("invoice", "/invoices/addRelatedItem", "/invoices/deleteRelatedItem/")
}

// relatedCommand builds the shared "related" subtree. Invoices and expenses
// expose the same two operations against different paths.
func relatedCommand(kind, addPath, deletePath string) *cobra.Command {
	var data, parentID, childID, relationType string

	add := &cobra.Command{
		Use:   "add",
		Short: "Link a related document",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := readPayload(data)
			if err != nil {
				return err
			}
			put(doc, "Relation", "parent_id", parentID)
			put(doc, "Relation", "child_id", childID)
			put(doc, "Relation", "type", relationType)

			if err := requirePayload(doc, "Pass --parent and --child, or --data"); err != nil {
				return err
			}
			raw, err := api.Post(ctx(cmd), addPath, doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "", "Related document linked")
		},
	}
	dataFlag(add, &data)
	add.Flags().StringVar(&parentID, "parent", "", "Parent document ID")
	add.Flags().StringVar(&childID, "child", "", "Child document ID")
	add.Flags().StringVar(&relationType, "type", "", "Relation type")

	remove := &cobra.Command{
		Use:     "delete <relation-id>",
		Aliases: []string{"rm"},
		Short:   "Unlink a related document",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("relation", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Post(ctx(cmd), deletePath+id, map[string]any{})
			if err != nil {
				return err
			}
			return emitWrite(raw, "", "Relation "+id+" removed")
		},
	}

	cmd := &cobra.Command{
		Use:   "related",
		Short: "Manage documents related to this " + kind,
	}
	cmd.AddCommand(add, remove)
	return cmd
}

// maxChecksumLength is the ceiling the API documents for a caller-supplied
// checksum.
const maxChecksumLength = 32

func invoiceRecoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recover <checksum>",
		Short: "Fetch the original response for an earlier --checksum",
		Long: `Returns the response the API produced for an earlier request that carried
this checksum.

Use it when a create call did not come back — a timeout, a dropped connection —
and you need to know whether the invoice exists before retrying. Responses are
available for about three months.`,
		Args: cobra.ExactArgs(1),
		Example: "  sf invoice create --client 7 --item 'X:1:100:23' --checksum order-4821\n" +
			"  sf invoice recover order-4821",
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := api.Get(ctx(cmd), "/api_logs/getResponseByChecksum/"+args[0], nil)
			if err != nil {
				return err
			}
			obj, err := decodeObject(raw)
			if err != nil {
				return err
			}
			// The replayed body is the original *response*, so the models sit
			// one level down under "data" rather than at the top like a fresh
			// detail. Unwrap so the same field list renders it.
			if inner, ok := obj["data"].(map[string]any); ok {
				obj = inner
			}
			return emitDetail(obj, "Invoice.id", invoiceDetail())
		},
	}
}
