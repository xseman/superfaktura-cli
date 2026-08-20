package commands

import (
	"encoding/json"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/render"
)

// valueList is one of the enumerations SuperFaktura documents ("číselníky").
// The short ones are constants in the documentation rather than endpoints, so
// they are carried here; the rest are fetched.
type valueList struct {
	Value       string `json:"value"`
	Description string `json:"description"`
}

var staticValueLists = map[string][]valueList{
	"invoice-types": {
		{"regular", "regular invoice"},
		{"proforma", "proforma invoice"},
		{"cancel", "credit note"},
		{"estimate", "price estimate"},
		{"order", "received order"},
		{"reverse_order", "order"},
		{"delivery", "delivery note"},
		{"draft", "concept"},
	},
	"invoice-statuses": {
		{"1", "issued"},
		{"2", "partially_paid"},
		{"3", "paid"},
		{"99", "overdue"},
	},
	"expense-types": {
		{"invoice", "received invoice"},
		{"bill", "bill"},
		{"contribution", "contribution"},
		{"internal", "internal document"},
		{"nondeductible", "non deductible"},
		{"recieved_credit_note", "received credit note"}, //nolint:misspell // the API's own spelling; "fixing" it breaks the value
	},
	"expense-statuses": {
		{"1", "new"},
		{"2", "partially_paid"},
		{"3", "paid"},
		{"99", "overdue"},
	},
	"export-statuses": {
		{"0", "failed"},
		{"1", "completed"},
		{"2", "in progress"},
		{"3", "scheduled"},
	},
	"payment-types": {
		{"transfer", "wire transfer"},
		{"cash", "cash"},
		{"card", "card"},
		{"credit", "credit card"},
		{"debit", "debit card"},
		{"cod", "cash on delivery"},
		{"inkaso", "encashment"},
		{"accreditation", "mutual credit"},
		{"paypal", "PayPal"},
		{"gopay", "GoPay"},
		{"barion", "Barion"},
		{"besteron", "Besteron (SK only)"},
		{"trustpay", "Trustpay (SK only)"},
		{"viamo", "Viamo"},
		{"other", "other"},
	},
	"delivery-types": {
		{"courier", "courier"},
		{"haulage", "freight transport"},
		{"mail", "mail / post"},
		{"personal", "personal pickup"},
		{"pickup_point", "pickup point"},
	},
	"languages": {
		{"slo", "Slovak"},
		{"cze", "Czech"},
		{"eng", "English"},
		{"deu", "German"},
		{"hun", "Hungarian"},
		{"pol", "Polish"},
		{"hrv", "Croatian"},
		{"ita", "Italian"},
		{"nld", "Dutch"},
		{"rom", "Romanian"},
		{"rus", "Russian"},
		{"slv", "Slovene"},
		{"spa", "Spanish"},
		{"ukr", "Ukrainian"},
	},
	"rounding-types": {
		{"document", "round the whole document"},
		{"item", "round per item"},
		{"item_ext", "retail rounding, recommended for e-shops"},
	},
	// Transcribed from value-lists.md "Time filter constants". The order is
	// not the intuitive one — 9 is this week, not last quarter — so change
	// these only against that table.
	"time-filters": {
		{"0", "all"},
		{"1", "today"},
		{"2", "yesterday"},
		{"3", "custom range, pair with *_since and *_to"},
		{"4", "this month"},
		{"5", "last month"},
		{"6", "this year"},
		{"7", "last year"},
		{"8", "this quarter"},
		{"9", "this week"},
		{"10", "last quarter"},
		{"11", "last hour"},
		{"12", "this hour"},
	},
}

// apiValueLists are the enumerations the account itself defines, so they have
// to be read from the API rather than carried as constants.
//
// Each one arrives in a different shape, so each brings its own decoder. All
// four shapes are documented — the map, the tree and the grouping are on their
// own pages in value-lists.md — and all four were still wrong on the first
// attempt, because the house style of model-wrapped records was assumed rather
// than the specific page read. See API-DISCREPANCIES.md §C.
type apiValueList struct {
	path    string
	decode  func(json.RawMessage) (listResult, error)
	columns []render.Column
}

var apiValueLists = map[string]apiValueList{
	// {"191":"Slovensko","1":"Afganistan",...} — an id-to-name map, like tags.
	// Pass --full for the richer /countries/index/view_full:1 form.
	"countries": {
		path:   "/countries",
		decode: func(raw json.RawMessage) (listResult, error) { return decodeKeyValueList(raw, "Country", "name") },
		columns: []render.Column{
			{Header: "ID", Path: "Country.id"},
			{Header: "Name", Path: "Country.name"},
		},
	},
	// A tree: [{"id":1,"name":"Kancelária","children":[...]}].
	"expense-categories": {
		path:   "/expenses/expense_categories",
		decode: decodeCategoryTree,
		columns: []render.Column{
			{Header: "ID", Path: "ExpenseCategory.id"},
			{Header: "Name", Path: "ExpenseCategory.name"},
			{Header: "Parent", Path: "ExpenseCategory.parent"},
		},
	},
	// Grouped by document type: {"regular":[...],"proforma":[...]}.
	"sequences": {
		path:   "/sequences/index.json",
		decode: decodeSequences,
		columns: []render.Column{
			{Header: "ID", Path: "Sequence.id"},
			{Header: "Type", Path: "Sequence.document_type"},
			{Header: "Name", Path: "Sequence.name"},
			{Header: "Mask", Path: "Sequence.mask"},
			{Header: "Next", Path: "Sequence.sequence_formatted"},
			{Header: "Default", Path: "Sequence.default"},
		},
	},
	"logos": {
		path:   "/users/logo",
		decode: decodeList,
		columns: []render.Column{
			{Header: "ID", Path: "Logo.id"},
			{Header: "File", Path: "Logo.basename"},
			{Header: "Default", Path: "Logo.default"},
		},
	},
}

// countriesFullColumns describe the richer records --full returns.
var countriesFullColumns = []render.Column{
	{Header: "ID", Path: "Country.id"},
	{Header: "ISO", Path: "Country.iso"},
	{Header: "Name", Path: "Country.name"},
	{Header: "EU", Path: "Country.eu"},
}

// decodeCategoryTree flattens the nested expense category tree, keeping the
// parent name so the hierarchy is still legible in a flat table.
func decodeCategoryTree(raw json.RawMessage) (listResult, error) {
	var roots []map[string]any
	if err := json.Unmarshal(raw, &roots); err != nil {
		return listResult{}, usageErrorf("unexpected expense category response: %s", err)
	}

	var items []map[string]any
	var walk func(nodes []map[string]any, parent string)
	walk = func(nodes []map[string]any, parent string) {
		for _, node := range nodes {
			row := map[string]any{"id": node["id"], "name": node["name"], "parent": parent}
			items = append(items, map[string]any{"ExpenseCategory": row})

			children, _ := node["children"].([]any)
			nested := make([]map[string]any, 0, len(children))
			for _, child := range children {
				if m, ok := child.(map[string]any); ok {
					nested = append(nested, m)
				}
			}
			walk(nested, render.Text(node["name"]))
		}
	}
	walk(roots, "")

	return listResult{Items: items, ItemCount: len(items), PageCount: 1}, nil
}

// decodeSequences flattens the type-to-sequences map, folding the document
// type — which is the map key, not a field — into each record.
func decodeSequences(raw json.RawMessage) (listResult, error) {
	var grouped map[string][]map[string]any
	if err := json.Unmarshal(raw, &grouped); err != nil {
		return listResult{}, usageErrorf("unexpected sequence response: %s", err)
	}

	var items []map[string]any
	for _, documentType := range slices.Sorted(maps.Keys(grouped)) {
		for _, record := range grouped[documentType] {
			row := make(map[string]any, len(record)+1)
			for k, v := range record {
				row[k] = v
			}
			row["document_type"] = documentType
			items = append(items, map[string]any{"Sequence": row})
		}
	}
	return listResult{Items: items, ItemCount: len(items), PageCount: 1}, nil
}

func init() {
	cmd := &cobra.Command{
		Use:   "values [list]",
		Short: "Show the value lists the API accepts",
		Long: `Shows the enumerations ("číselníky") the API accepts.

Run without arguments to see which lists exist. Most are fixed constants;
countries, expense categories, sequences and logos are read from your account.`,
		Args:    cobra.MaximumNArgs(1),
		Example: "  sf values\n  sf values payment-types\n  sf values countries --json",
		RunE:    runValues,
	}
	cmd.Flags().Bool("full", false, "For countries, include the full record")
	rootCmd.AddCommand(cmd)
}

func runValues(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return listValueLists()
	}

	name := args[0]
	if entries, ok := staticValueLists[name]; ok {
		return emit(entries, func(w io.Writer) {
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			for _, entry := range entries {
				_, _ = io.WriteString(tw, entry.Value+"\t"+entry.Description+"\n")
			}
			_ = tw.Flush()
		})
	}

	if spec, ok := apiValueLists[name]; ok {
		path, columns, decode := spec.path, spec.columns, spec.decode

		// --full switches countries to a different endpoint whose records are
		// model-wrapped objects rather than an id-to-name map.
		if full, _ := cmd.Flags().GetBool("full"); full && name == "countries" {
			path = "/countries/index/view_full:1"
			columns = countriesFullColumns
			decode = decodeList
		}

		raw, err := cachedGet(cmd, path, nil, valueListTTL)
		if err != nil {
			return err
		}
		result, err := decode(raw)
		if err != nil {
			return err
		}
		return emitList(result, columns)
	}

	if name == "currencies" {
		return emitAction(map[string]any{"format": "ISO 4217"},
			"Any ISO 4217 code is accepted, e.g. EUR, CZK, USD, GBP.")
	}

	return usageErrorf("unknown value list %q — run 'sf values' to see the available lists", name)
}

func listValueLists() error {
	names := make([]string, 0, len(staticValueLists)+len(apiValueLists)+1)
	for name := range staticValueLists {
		names = append(names, name)
	}
	for name := range apiValueLists {
		names = append(names, name)
	}
	names = append(names, "currencies")
	slices.Sort(names)

	return emit(names, func(w io.Writer) {
		_, _ = io.WriteString(w, strings.Join(names, "\n")+"\n")
	})
}
