package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/render"
)

var cashRegisterColumns = []render.Column{
	{Header: "ID", Path: "CashRegister.id"},
	{Header: "Name", Path: "CashRegister.name"},
	{Header: "Description", Path: "CashRegister.description", Format: render.Truncate(28)},
	{Header: "Currency", Path: "CashRegister.currency"},
	{Header: "Items", Path: "CashRegister.items"},
	{Header: "Total", Path: "CashRegister.total", Format: render.Money},
}

var cashItemColumns = []render.Column{
	{Header: "ID", Path: "CashRegisterItem.id"},
	{Header: "Number", Path: "CashRegisterItem.cash_item_no_formatted"},
	{Header: "Date", Path: "CashRegisterItem.created", Format: render.Date},
	{Header: "Name", Path: "CashRegisterItem.name", Format: render.Truncate(28)},
	{Header: "Amount", Path: "CashRegisterItem.amount", Format: render.Money},
}

func init() {
	cmd := &cobra.Command{
		Use:   "cash-register",
		Short: "Manage cash registers and their items",
	}
	rootCmd.AddCommand(cmd)

	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List cash registers",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := api.Get(ctx(cmd), "/cash_registers/getDetails", nil)
			if err != nil {
				return err
			}
			result, err := decodeList(raw)
			if err != nil {
				return err
			}
			return emitList(result, cashRegisterColumns)
		},
	}

	view := &cobra.Command{
		Use:     "view <id>",
		Aliases: []string{"show"},
		Short:   "Show one cash register",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("cash register", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/cash_registers/view/"+id, nil)
			if err != nil {
				return err
			}
			obj, err := decodeObject(raw)
			if err != nil {
				return err
			}
			return emitObject(obj, []render.Field{
				{Label: "ID", Path: "CashRegister.id"},
				{Label: "Name", Path: "CashRegister.name"},
				{Label: "Description", Path: "CashRegister.description"},
				{Label: "Currency", Path: "CashRegister.currency"},
				{Label: "Items", Path: "CashRegister.items"},
				{Label: "Total", Path: "CashRegister.total", Format: render.Money},
			})
		},
	}

	cmd.AddCommand(list, view, cashItemCmd())
}

func cashItemCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "item",
		Short: "Manage cash register items",
	}

	opts := &listOptions{}
	list := &cobra.Command{
		Use:     "list <cash-register-id>",
		Aliases: []string{"ls"},
		Short:   "List items in a cash register",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("cash register", args[0])
			if err != nil {
				return err
			}
			result, err := fetchList(ctx(cmd), "/cash_register_items/index/"+id, opts, nil)
			if err != nil {
				return err
			}
			return emitList(result, cashItemColumns)
		},
	}
	opts.bind(list)

	var data, registerID, name, amount, itemType, created string
	add := &cobra.Command{
		Use:     "add",
		Aliases: []string{"create"},
		Short:   "Add an item to a cash register",
		Args:    cobra.NoArgs,
		Example: "  sf cash-register item add --register 1 --name 'Coffee' --amount 3.50",
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := readPayload(data)
			if err != nil {
				return err
			}
			put(doc, "CashRegisterItem", "cash_register_id", registerID)
			put(doc, "CashRegisterItem", "name", name)
			put(doc, "CashRegisterItem", "type", itemType)
			put(doc, "CashRegisterItem", "created", created)
			if amount != "" {
				value, err := parseNumber(amount)
				if err != nil {
					return usageErrorf("--amount must be a number, got %q", amount)
				}
				put(doc, "CashRegisterItem", "amount", value)
			}

			if err := requirePayload(doc, "Pass --register, --name and --amount, or --data"); err != nil {
				return err
			}
			raw, err := api.Post(ctx(cmd), "/cash_register_items/add", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "CashRegisterItem", "Cash register item added")
		},
	}
	dataFlag(add, &data)
	add.Flags().StringVar(&registerID, "register", "", "Cash register ID")
	add.Flags().StringVar(&name, "name", "", "Item name")
	add.Flags().StringVar(&amount, "amount", "", "Amount; negative records an expense")
	add.Flags().StringVar(&itemType, "type", "", "Item type")
	add.Flags().StringVar(&created, "date", "", "Item date (YYYY-MM-DD)")

	remove := &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "Delete a cash register item",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("cash register item", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/cash_register_items/delete/"+id, nil)
			if err != nil {
				return err
			}
			return emitWrite(raw, "CashRegisterItem", "Cash register item "+id+" deleted")
		},
	}

	var destination string
	receipt := &cobra.Command{
		Use:   "receipt <id>",
		Short: "Download the receipt for a cash register item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("cash register item", args[0])
			if err != nil {
				return err
			}
			body, _, err := api.Download(ctx(cmd), "/cash_register_items/receipt/"+id, nil)
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
	receipt.Flags().StringVarP(&destination, "output", "o", "", "Write to this file, or - for stdout")

	cmd.AddCommand(list, add, remove, receipt)
	return cmd
}
