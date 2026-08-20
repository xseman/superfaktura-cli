package commands

import (
	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/render"
)

var bankColumns = []render.Column{
	{Header: "ID", Path: "BankAccount.id"},
	{Header: "Bank", Path: "BankAccount.bank_name", Format: render.Truncate(24)},
	{Header: "IBAN", Path: "BankAccount.iban"},
	{Header: "SWIFT", Path: "BankAccount.swift"},
	{Header: "Currency", Path: "BankAccount.currency"},
	{Header: "Default", Path: "BankAccount.default"},
}

// bankWriteFlags are the fields shared by bank account add and update.
type bankWriteFlags struct {
	bankName, iban, swift    string
	account, bankCode        string
	currency, country        string
	makeDefault, showOnPrint bool
}

func (b *bankWriteFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&b.bankName, "bank-name", "", "Bank name")
	f.StringVar(&b.iban, "iban", "", "IBAN")
	f.StringVar(&b.swift, "swift", "", "SWIFT / BIC")
	f.StringVar(&b.account, "account", "", "Account number")
	f.StringVar(&b.bankCode, "bank-code", "", "Bank code")
	f.StringVar(&b.currency, "currency", "", "Currency code")
	f.StringVar(&b.country, "country-id", "", "Country ID")
	f.BoolVar(&b.makeDefault, "default", false, "Make this the default account")
	f.BoolVar(&b.showOnPrint, "show", false, "Show this account on documents")
}

func (b *bankWriteFlags) apply(doc map[string]any) {
	put(doc, "BankAccount", "bank_name", b.bankName)
	put(doc, "BankAccount", "iban", b.iban)
	put(doc, "BankAccount", "swift", b.swift)
	put(doc, "BankAccount", "account", b.account)
	put(doc, "BankAccount", "bank_code", b.bankCode)
	put(doc, "BankAccount", "currency", b.currency)
	put(doc, "BankAccount", "country_id", b.country)
	if b.makeDefault {
		put(doc, "BankAccount", "default", 1)
	}
	if b.showOnPrint {
		put(doc, "BankAccount", "show", 1)
	}
}

func init() {
	cmd := &cobra.Command{
		Use:   "bank-account",
		Short: "Manage bank accounts",
	}
	rootCmd.AddCommand(cmd)

	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List bank accounts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := cachedGet(cmd, "/bank_accounts/index", nil, valueListTTL)
			if err != nil {
				return err
			}
			result, err := decodeListUnder(raw, "BankAccounts")
			if err != nil {
				return err
			}
			return emitList(result, bankColumns)
		},
	}

	var addData string
	addFlags := &bankWriteFlags{}
	add := &cobra.Command{
		Use:     "add",
		Aliases: []string{"create"},
		Short:   "Add a bank account",
		Args:    cobra.NoArgs,
		Example: "  sf bank-account add --bank-name Tatrabanka --iban SK0112000000001987426375",
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := readPayload(addData)
			if err != nil {
				return err
			}
			addFlags.apply(doc)
			if err := requirePayload(doc, "Pass --iban and --bank-name, or --data"); err != nil {
				return err
			}
			raw, err := api.Post(ctx(cmd), "/bank_accounts/add", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "BankAccount", "Bank account added")
		},
	}
	dataFlag(add, &addData)
	addFlags.bind(add)

	var updateData string
	updateFlags := &bankWriteFlags{}
	update := &cobra.Command{
		Use:     "update <id>",
		Aliases: []string{"edit"},
		Short:   "Update a bank account",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("bank account", args[0])
			if err != nil {
				return err
			}
			doc, err := readPayload(updateData)
			if err != nil {
				return err
			}
			updateFlags.apply(doc)
			put(doc, "BankAccount", "id", id)

			raw, err := api.Post(ctx(cmd), "/bank_accounts/update/"+id, doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "BankAccount", "Bank account "+id+" updated")
		},
	}
	dataFlag(update, &updateData)
	updateFlags.bind(update)

	remove := &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "Delete a bank account",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("bank account", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Post(ctx(cmd), "/bank_accounts/delete/"+id, map[string]any{})
			if err != nil {
				return err
			}
			return emitWrite(raw, "BankAccount", "Bank account "+id+" deleted")
		},
	}

	cmd.AddCommand(list, add, update, remove)
}
