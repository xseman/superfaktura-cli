package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/render"
)

var exportFields = []render.Field{
	{Label: "ID", Path: "Export.id"},
	{Label: "Type", Path: "Export.type"},
	{Label: "Status", Path: "Export.status", Format: exportStatus},
	{Label: "Progress", Path: "Export.progress"},
	{Label: "Total", Path: "Export.count_total"},
	{Label: "Completed", Path: "Export.count_completed"},
}

// exportStatus names the numeric status the API returns.
func exportStatus(v any) string {
	names := map[string]string{
		"0": "failed",
		"1": "completed",
		"2": "in progress",
		"3": "scheduled",
	}
	code := render.Text(v)
	if name, ok := names[code]; ok {
		return fmt.Sprintf("%s (%s)", name, code)
	}
	return code
}

func init() {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export invoices in bulk",
		Long: `Exports several invoices at once as PDF or XLS.

An export runs asynchronously: create it, poll 'sf export status', then
download once it reports completed.`,
	}
	rootCmd.AddCommand(cmd)

	var data string
	var ids []string
	var asPDF, asXLS, merge, onlyMerge bool
	var hideSignature, hidePaymentInfo, sortByClient, sortByDate bool

	create := &cobra.Command{
		Use:     "create",
		Aliases: []string{"add"},
		Short:   "Queue an export",
		Args:    cobra.NoArgs,
		Example: "  sf export create --invoice 1 --invoice 2 --pdf --merge",
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := readPayload(data)
			if err != nil {
				return err
			}

			if len(ids) > 0 {
				numeric := make([]int, 0, len(ids))
				for _, id := range ids {
					value, err := strconv.Atoi(id)
					if err != nil {
						return usageErrorf("--invoice must be a number, got %q", id)
					}
					numeric = append(numeric, value)
				}
				put(doc, "Invoice", "ids", numeric)
			}

			// msel marks the request as a multiselect export; the API rejects
			// the call without it.
			put(doc, "Export", "msel", true)
			putBool(doc, "Export", "invoices_pdf", asPDF)
			putBool(doc, "Export", "invoices_xls", asXLS)
			putBool(doc, "Export", "merge_pdf", merge)
			putBool(doc, "Export", "only_merge", onlyMerge)
			putBool(doc, "Export", "hide_signature", hideSignature)
			putBool(doc, "Export", "hide_pdf_payment_info", hidePaymentInfo)
			putBool(doc, "Export", "pdf_sort_client", sortByClient)
			putBool(doc, "Export", "pdf_sort_date", sortByDate)

			if !asPDF && !asXLS {
				return usageErrorf("choose an output format: --pdf or --xls")
			}

			raw, err := api.Post(ctx(cmd), "/exports", doc)
			if err != nil {
				return err
			}
			obj, err := decodeObject(raw)
			if err != nil {
				return err
			}
			return emitObject(obj, exportFields)
		},
	}
	dataFlag(create, &data)
	f := create.Flags()
	f.StringArrayVar(&ids, "invoice", nil, "Invoice ID to include (repeatable)")
	f.BoolVar(&asPDF, "pdf", false, "Export as PDF")
	f.BoolVar(&asXLS, "xls", false, "Export as XLS")
	f.BoolVar(&merge, "merge", false, "Also produce a merged PDF")
	f.BoolVar(&onlyMerge, "only-merge", false, "Produce only the merged PDF")
	f.BoolVar(&hideSignature, "hide-signature", false, "Hide the signature")
	f.BoolVar(&hidePaymentInfo, "hide-payment-info", false, "Hide payment information")
	f.BoolVar(&sortByClient, "sort-client", false, "Sort documents by client")
	f.BoolVar(&sortByDate, "sort-date", false, "Sort documents by date")

	status := &cobra.Command{
		Use:   "status <id>",
		Short: "Check the progress of an export",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("export", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/exports/getStatus/"+id, nil)
			if err != nil {
				return err
			}
			obj, err := decodeObject(raw)
			if err != nil {
				return err
			}
			return emitObject(obj, exportFields)
		},
	}

	var destination string
	download := &cobra.Command{
		Use:   "download <id>",
		Short: "Download a completed export",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("export", args[0])
			if err != nil {
				return err
			}
			body, _, err := api.Download(ctx(cmd), "/exports/download_export/"+id, nil)
			if err != nil {
				return err
			}
			if destination == "" {
				destination = "export-" + id + ".zip"
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

	cmd.AddCommand(create, status, download)
}
