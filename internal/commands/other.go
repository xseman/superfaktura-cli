package commands

import (
	"fmt"
	"io"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/render"
)

func init() {
	rootCmd.AddCommand(
		companyCmd(),
		smsCmd(),
		bankMovesCmd(),
		activityCmd(),
		limitsCmd(),
		versionCmd(),
	)
}

// companyCmd inspects the companies reachable with the current credentials.
// One SuperFaktura login can own several companies; company_id in the
// authorization header selects which one a request applies to.
func companyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "company",
		Short: "Inspect the companies this account can access",
		Long: `Lists the companies reachable with the current credentials.

Use the ID with --company, SF_COMPANY_ID, or store it in a profile with
'sf auth login --company'.`,
	}

	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List accessible companies",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := cachedGet(cmd, "/users/company_switcher", nil, valueListTTL)
			if err != nil {
				return err
			}
			result, err := decodeListUnder(raw, "companies")
			if err != nil {
				return err
			}
			return emitList(result, []render.Column{
				{Header: "ID", Path: "UserProfile.id"},
				{Header: "Name", Path: "UserProfile.company_name", Format: render.Truncate(32)},
				{Header: "IČO", Path: "UserProfile.ico"},
				{Header: "Country", Path: "UserProfile.country_id"},
			})
		},
	}

	var all bool
	data := &cobra.Command{
		Use:   "data",
		Short: "Show the full company records",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := "/users/getUserCompaniesData"
			if all {
				path += "/1"
			}
			raw, err := cachedGet(cmd, path, nil, valueListTTL)
			if err != nil {
				return err
			}
			result, err := decodeList(raw)
			if err != nil {
				return err
			}
			return emitList(result, []render.Column{
				{Header: "ID", Path: "UserProfile.id"},
				{Header: "Name", Path: "UserProfile.company_name", Format: render.Truncate(32)},
				{Header: "IČO", Path: "UserProfile.ico"},
				{Header: "IČ DPH", Path: "UserProfile.ic_dph"},
			})
		},
	}
	data.Flags().BoolVar(&all, "all", false, "Include companies beyond the active one")

	cmd.AddCommand(list, data)
	return cmd
}

func smsCmd() *cobra.Command {
	var data, invoiceID, phone, message string

	cmd := &cobra.Command{
		Use:     "sms",
		Short:   "Send an SMS payment reminder",
		Args:    cobra.NoArgs,
		Example: "  sf sms --invoice 1042 --phone +421900000000",
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := readPayload(data)
			if err != nil {
				return err
			}
			put(doc, "Sms", "invoice_id", invoiceID)
			put(doc, "Sms", "phone", phone)
			put(doc, "Sms", "text", message)

			if err := requirePayload(doc, "Pass --invoice and --phone, or --data"); err != nil {
				return err
			}
			raw, err := api.Post(ctx(cmd), "/sms/send", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "", "SMS sent")
		},
	}

	dataFlag(cmd, &data)
	cmd.Flags().StringVar(&invoiceID, "invoice", "", "Invoice the reminder is about")
	cmd.Flags().StringVar(&phone, "phone", "", "Recipient phone number")
	cmd.Flags().StringVar(&message, "message", "", "Message text")
	return cmd
}

// bankMovesCmd reads the bank statement lines SuperFaktura has imported.
func bankMovesCmd() *cobra.Command {
	opts := &listOptions{}
	var since, until, when string

	cmd := &cobra.Command{
		Use:     "bank-moves",
		Short:   "List bank account movements",
		Args:    cobra.NoArgs,
		Example: "  sf bank-moves --date 3 --since 2026-01-01 --until 2026-01-31",
		RunE: func(cmd *cobra.Command, _ []string) error {
			extra := client.Params{}
			extra.Set("date", when)
			extra.Set("date_since", since)
			extra.Set("date_to", until)

			result, err := fetchList(ctx(cmd), "/accounts/index.json", opts, extra)
			if err != nil {
				return err
			}
			return emitList(result, []render.Column{
				{Header: "ID", Path: "AccountMove.id"},
				{Header: "Date", Path: "AccountMove.date", Format: render.Date},
				{Header: "Amount", Path: "AccountMove.amount", Format: render.Money},
				{Header: "Currency", Path: "AccountMove.currency"},
				{Header: "Variable", Path: "AccountMove.variable"},
				{Header: "Note", Path: "AccountMove.note", Format: render.Truncate(30)},
			})
		},
	}

	opts.bind(cmd)
	cmd.Flags().StringVar(&when, "date", "", "Time filter constant (see 'sf values time-filters')")
	cmd.Flags().StringVar(&since, "since", "", "Start date, requires --date 3 (YYYY-MM-DD)")
	cmd.Flags().StringVar(&until, "until", "", "End date, requires --date 3 (YYYY-MM-DD)")
	return cmd
}

func activityCmd() *cobra.Command {
	var limit string

	cmd := &cobra.Command{
		Use:     "activity <type> <id>",
		Short:   "Show the activity log of a document",
		Args:    cobra.ExactArgs(2),
		Example: "  sf activity invoice 1042 --limit 20",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("document", args[1])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd),
				"/activity_logs/activity_list/"+args[0]+"/"+id+"/"+limit, nil)
			if err != nil {
				return err
			}
			result, err := decodeList(raw)
			if err != nil {
				return err
			}
			return emitList(result, []render.Column{
				{Header: "Date", Path: "ActivityLog.created"},
				{Header: "User", Path: "ActivityLog.user_email", Format: render.Truncate(28)},
				{Header: "Action", Path: "ActivityLog.action"},
				{Header: "Detail", Path: "ActivityLog.text", Format: render.Truncate(40)},
			})
		},
	}

	cmd.Flags().StringVar(&limit, "limit", "10", "Number of entries to return")
	return cmd
}

// limitsCmd reports the API quota. SuperFaktura enforces hard daily and
// monthly request caps, and reports them on every response — so this makes one
// cheap call and reads the headers off it.
func limitsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "limits",
		Short: "Show remaining API request quota",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := api.Get(ctx(cmd), "/clients/index.json", client.Params{"per_page": "1"}); err != nil {
				// The quota headers accompany failures too, so report them if
				// we got them and surface the error otherwise.
				if !api.Limits().Seen {
					return err
				}
			}

			limits := api.Limits()
			if !limits.Seen {
				return usageErrorf("the API did not report any rate-limit headers")
			}
			return emit(limits, func(w io.Writer) {
				fmt.Fprintf(w, "Daily:    %d of %d remaining, resets %s\n",
					limits.DailyRemaining, limits.DailyLimit, limits.DailyReset)
				fmt.Fprintf(w, "Monthly:  %d of %d remaining, resets %s\n",
					limits.MonthlyRemaining, limits.MonthlyLimit, limits.MonthlyReset)
				if limits.Message != "" {
					fmt.Fprintf(w, "Notice:   %s\n", limits.Message)
				}
			})
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the CLI version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			info := map[string]any{
				"version": version,
				"go":      runtime.Version(),
				"os":      runtime.GOOS,
				"arch":    runtime.GOARCH,
			}
			return emit(info, func(w io.Writer) {
				fmt.Fprintf(w, "sf %s (%s %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			})
		},
	}
}
