package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/output"
	"github.com/xseman/superfaktura-cli/internal/render"
)

var stockColumns = []render.Column{
	{Header: "ID", Path: "StockItem.id"},
	{Header: "SKU", Path: "StockItem.sku"},
	{Header: "Name", Path: "StockItem.name", Format: render.Truncate(32)},
	{Header: "Unit", Path: "StockItem.unit"},
	{Header: "Price", Path: "StockItem.unit_price", Format: render.Money},
	{Header: "VAT", Path: "StockItem.vat"},
	{Header: "Stock", Path: "StockItem.stock"},
}

var stockFields = []render.Field{
	{Label: "ID", Path: "StockItem.id"},
	{Label: "SKU", Path: "StockItem.sku"},
	{Label: "Name", Path: "StockItem.name"},
	{Label: "Description", Path: "StockItem.description"},
	{Label: "Unit", Path: "StockItem.unit"},
	{Label: "Unit price", Path: "StockItem.unit_price", Format: render.Money},
	{Label: "VAT", Path: "StockItem.vat"},
	{Label: "Purchase price", Path: "StockItem.purchase_unit_price", Format: render.Money},
	{Label: "In stock", Path: "StockItem.stock"},
	{Label: "Watch stock", Path: "StockItem.watch_stock"},
	{Label: "Internal note", Path: "StockItem.internal_comment"},
}

var stockCmd = &cobra.Command{
	Use:   "stock",
	Short: "Manage stock items and movements",
}

func init() {
	rootCmd.AddCommand(stockCmd)
	stockCmd.AddCommand(
		stockListCmd(),
		stockViewCmd(),
		stockAddCmd(),
		stockEditCmd(),
		stockDeleteCmd(),
		stockMovementCmd(),
	)
}

// stockWriteFlags are the fields shared by stock add and edit.
type stockWriteFlags struct {
	name, sku, unit, description string
	unitPrice, vat, purchase     string
	internal                     string
}

func (s *stockWriteFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&s.name, "name", "", "Item name")
	f.StringVar(&s.sku, "sku", "", "Stock keeping unit")
	f.StringVar(&s.unit, "unit", "", "Unit of measure")
	f.StringVar(&s.description, "description", "", "Public description")
	f.StringVar(&s.unitPrice, "price", "", "Unit price without VAT")
	f.StringVar(&s.vat, "vat", "", "VAT rate as a percentage")
	f.StringVar(&s.purchase, "purchase-price", "", "Purchase unit price")
	f.StringVar(&s.internal, "internal-comment", "", "Internal comment")
}

func (s *stockWriteFlags) apply(doc map[string]any) error {
	put(doc, "StockItem", "name", s.name)
	put(doc, "StockItem", "sku", s.sku)
	put(doc, "StockItem", "unit", s.unit)
	put(doc, "StockItem", "description", s.description)
	put(doc, "StockItem", "internal_comment", s.internal)

	for _, pair := range []struct{ flag, field, value string }{
		{"--price", "unit_price", s.unitPrice},
		{"--vat", "vat", s.vat},
		{"--purchase-price", "purchase_unit_price", s.purchase},
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
		put(doc, "StockItem", pair.field, number)
	}
	return nil
}

func stockListCmd() *cobra.Command {
	opts := &listOptions{}

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List stock items",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := fetchList(ctx(cmd), "/stock_items/index.json", opts, nil)
			if err != nil {
				return err
			}
			return emitList(result, stockColumns)
		},
	}

	opts.bind(cmd)
	return cmd
}

func stockViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "view <id>",
		Aliases: []string{"show"},
		Short:   "Show one stock item",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("stock item", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/stock_items/view/"+id, nil)
			if err != nil {
				return err
			}
			obj, err := decodeObject(raw)
			if err != nil {
				return err
			}
			return emitObject(obj, stockFields)
		},
	}
}

func stockAddCmd() *cobra.Command {
	var data string
	flags := &stockWriteFlags{}

	cmd := &cobra.Command{
		Use:     "add",
		Aliases: []string{"create"},
		Short:   "Add a stock item",
		Args:    cobra.NoArgs,
		Example: "  sf stock add --name 'Item B' --sku itemb1241 --price 19.95 --vat 20",
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := readPayload(data)
			if err != nil {
				return err
			}
			if err := flags.apply(doc); err != nil {
				return err
			}
			if err := requirePayload(doc, "Pass --name, or --data"); err != nil {
				return err
			}
			raw, err := api.PostJSON(ctx(cmd), "/stock_items/add", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "StockItem", "Stock item added")
		},
	}

	dataFlag(cmd, &data)
	flags.bind(cmd)
	return cmd
}

func stockEditCmd() *cobra.Command {
	var data string
	flags := &stockWriteFlags{}

	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit a stock item",
		Long:  "Edits a stock item. This endpoint uses PATCH, unlike most writes in the API.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("stock item", args[0])
			if err != nil {
				return err
			}
			doc, err := readPayload(data)
			if err != nil {
				return err
			}
			if err := flags.apply(doc); err != nil {
				return err
			}
			if err := requirePayload(doc, "Pass at least one field to change, or --data"); err != nil {
				return err
			}
			raw, err := api.Patch(ctx(cmd), "/stock_items/edit/"+id, doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "StockItem", "Stock item "+id+" updated")
		},
	}

	dataFlag(cmd, &data)
	flags.bind(cmd)
	return cmd
}

func stockDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "Delete a stock item",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("stock item", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Delete(ctx(cmd), "/stock_items/delete/"+id, nil)
			if err != nil {
				return err
			}
			return emitWrite(raw, "StockItem", "Stock item "+id+" deleted")
		},
	}
}

func stockMovementCmd() *cobra.Command {
	var data, itemID, sku, quantity, note string

	add := &cobra.Command{
		Use:     "add",
		Short:   "Record a stock movement",
		Args:    cobra.NoArgs,
		Example: "  sf stock movement add --item 12 --quantity -3 --note 'Sold at market'",
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := readPayload(data)
			if err != nil {
				return err
			}

			// StockLog is an array of movements, not one object. Sending an
			// object earns a 500 TypeError: the server does array work on it.
			movement := map[string]any{}
			setIfPresent(movement, "stock_item_id", itemID)
			setIfPresent(movement, "sku", sku)
			setIfPresent(movement, "note", note)
			if quantity != "" {
				value, err := parseNumber(quantity)
				if err != nil {
					return &output.Error{
						Code:    output.CodeUsage,
						Message: fmt.Sprintf("--quantity must be a number, got %q", quantity),
					}
				}
				movement["quantity"] = value
			}
			if len(movement) > 0 {
				doc["StockLog"] = []map[string]any{movement}
			}

			if err := requirePayload(doc, "Pass --item (or --sku) and --quantity, or --data"); err != nil {
				return err
			}
			raw, err := api.PostJSON(ctx(cmd), "/stock_items/addStockMovement", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "StockLog", "Stock movement recorded")
		},
	}
	dataFlag(add, &data)
	add.Flags().StringVar(&itemID, "item", "", "Stock item ID")
	add.Flags().StringVar(&sku, "sku", "", "Stock keeping unit, as an alternative to --item")
	add.Flags().StringVar(&quantity, "quantity", "", "Quantity to add; negative removes")
	add.Flags().StringVar(&note, "note", "", "Movement note")

	list := &cobra.Command{
		Use:     "list <item-id>",
		Aliases: []string{"ls"},
		Short:   "List movements of a stock item",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("stock item", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/stock_items/movements/"+id, nil)
			if err != nil {
				return err
			}
			result, err := decodeList(raw)
			if err != nil {
				return err
			}
			return emitList(result, []render.Column{
				{Header: "ID", Path: "StockLog.id"},
				{Header: "Date", Path: "StockLog.created", Format: render.Date},
				{Header: "Quantity", Path: "StockLog.quantity"},
				{Header: "Stock", Path: "StockLog.stock"},
				{Header: "Note", Path: "StockLog.note", Format: render.Truncate(32)},
			})
		},
	}

	cmd := &cobra.Command{Use: "movement", Short: "Record and inspect stock movements"}
	cmd.AddCommand(add, list)
	return cmd
}
