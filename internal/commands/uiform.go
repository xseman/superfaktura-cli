package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/output"
	"github.com/xseman/superfaktura-cli/internal/render"
	"github.com/xseman/superfaktura-cli/internal/tui"
)

// Forms in the browser are the CLI commands, driven by hand.
//
// The fields come from the command's own flags and the write goes through its
// own RunE, so a form cannot describe something the command does not accept,
// and adding a flag adds a field. The alternative — a hand-written form per
// resource — is more code that starts correct and drifts.

// skippedFlags never belong on a form: a raw payload editor, cobra's own help,
// and --checksum, which is not a field of the record at all — it exists so a
// create whose response was lost to a timeout can be recovered, and in a
// browser the answer arrives on screen. Tags were here too, for want of a
// field shape; a checklist is that shape.
var skippedFlags = map[string]bool{"data": true, "help": true, "checksum": true}

// subRecordAction offers a command that acts on a part of the selected record —
// its line items — rather than on the record itself.
//
// The parent id is not a form field. It is injected from the highlighted row on
// submit, because a box showing it would be a value the user must not change,
// and Prefill cannot carry it either: a prefilled value that is left alone is
// deliberately dropped, so the id would vanish on its way out.
func subRecordAction(key, label, verb, idPath, parentFlag string, path ...string) tui.Action {
	fields := formFieldsFor(path...)
	// Whatever names the parent goes, having been replaced by the row.
	fields = slices.DeleteFunc(fields, func(f tui.FormField) bool {
		return f.Key == parentFlag
	})

	return tui.Action{
		Key: key, Label: label, Verb: verb, Writes: true, Fields: fields,
		Run: func(ctx context.Context, row map[string]any, values map[string]string) (string, bool, error) {
			id := recordID(row, idPath)
			if id == "" {
				return "", false, usageErrorf("this record has no id")
			}
			values[parentFlag] = id
			message, err := runCommandWithValues(ctx, path, nil, values)
			if err != nil {
				return "", false, err
			}
			return message, true, nil
		},
	}
}

// findCommand walks the tree, e.g. findCommand("client", "edit").
func findCommand(path ...string) *cobra.Command {
	cmd := rootCmd
	for _, name := range path {
		next, _, err := cmd.Find([]string{name})
		if err != nil || next == cmd {
			return nil
		}
		cmd = next
	}
	return cmd
}

// splitUsage turns a flag's help into a field label and the hint that belongs
// inside the empty box.
//
// The whole help string as a label read as prose: "Existing client, by numeric
// ID or by name" above a box is a sentence, not a name, and fourteen of them
// stacked is the wall the form looked like. The part before the first bracket or
// comma is the name somebody already wrote for the field; the rest is the
// explanation, which belongs where it is needed and nowhere else.
//
// A head that is itself a sentence — "Create the invoice for a new client with
// this name" has neither separator — falls back to the flag's own name.
func splitUsage(name, usage string) (label, hint string) {
	label, hint = usage, ""
	for _, sep := range []string{" (", ", ", "; "} {
		if i := strings.Index(label, sep); i > 0 {
			label, hint = label[:i], strings.TrimSpace(label[i+len(sep):])
		}
	}
	label = strings.TrimSpace(label)
	// Trim, drop the bracket the split left behind, then trim again — the
	// bracket can have had a space in front of it.
	hint = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(hint), ")"))

	// Too long to read as a name — measured in runes, because the label is up
	// against a box on screen and "Dátum splatnosti" is shorter than its byte
	// count claims — or, when the help opens with a separator, nothing left to
	// read at all. Either way the flag's own name is a label somebody already
	// wrote for the field.
	const tooLongForALabel = 24
	if utf8.RuneCountInString(label) > tooLongForALabel || label == "" {
		if fallback := capitalize(strings.TrimSpace(strings.ReplaceAll(name, "-", " "))); fallback != "" {
			label, hint = fallback, ""
		}
	}
	// Last resort: a box with no label above it is worse than a long one.
	if label == "" {
		label, hint = strings.TrimSpace(usage), ""
	}
	return label, hint
}

// capitalize raises the first letter, counting runes.
//
// Slicing the first byte instead cut a multi-byte letter in half and put a
// broken rune on the form, and did it by panicking when there was no first
// byte at all. A flag with no usable name has nothing to fall back to, so
// splitUsage keeps the long label rather than leaving the field unlabelled.
func capitalize(s string) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	return string(unicode.ToUpper(runes[0])) + string(runes[1:])
}

// requiredFields are the fields a form cannot be submitted without.
//
// Not the flags cobra marks required: on the command line --data can stand in
// for all of them, and the browser has no --data. So this is the API's own
// requirement restated for a form that cannot pass a raw payload — an invoice
// needs a client and something to bill for, an expense and a client a name.
var requiredFields = map[string]map[string]bool{
	"invoice create": {"client": true, "item": true},
	"expense add":    {"name": true},
	"client create":  {"name": true},
}

// commandKey names a command the way requiredFields does.
func commandKey(cmd *cobra.Command) string {
	if cmd.Parent() == nil {
		return cmd.Name()
	}
	return cmd.Parent().Name() + " " + cmd.Name()
}

// formFieldsFor turns a command's own flags into form fields.
func formFieldsFor(path ...string) []tui.FormField {
	cmd := findCommand(path...)
	if cmd == nil {
		return nil
	}

	var fields []tui.FormField
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		// A repeatable flag is a checklist when there is a list to tick and a
		// box of one-per-line otherwise. Anything else repeatable has no shape.
		multi := f.Value.Type() == "stringArray"
		if f.Hidden || skippedFlags[f.Name] || (f.Value.Type() != "string" && !multi) {
			return
		}
		label, hint := splitUsage(f.Name, f.Usage)
		field := tui.FormField{
			Key:      f.Name,
			Label:    label,
			Options:  optionsFor(cmd, f.Name),
			Multi:    multi,
			Required: requiredFields[commandKey(cmd)][f.Name],
		}
		if field.Options == nil {
			// A hint belongs in the empty box, not on a line of its own. A list
			// has no box, and its options say more than a sentence could.
			field.Placeholder = hint
		}
		if f.Name == "item" {
			field.Label = "Line items"
			field.Placeholder = "Consulting:8:75:23"
			field.Validate = checkItemLines
			field.Note = itemsTotal
		}
		if isDateFlag(f) {
			// The CLI validates the same input in setup, but the browser calls
			// RunE directly and never passes through it.
			field.Validate = func(value string) error {
				if err := checkDate(value); err != nil {
					return errors.New(err.Message)
				}
				return nil
			}
		}
		fields = append(fields, field)
	})
	// Alphabetical order buries the field that identifies the record. Name
	// first, then the rest as they come.
	slices.SortStableFunc(fields, func(a, b tui.FormField) int {
		if (a.Key == "name") != (b.Key == "name") {
			if a.Key == "name" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	})
	return fields
}

// dateFlagsOf names the flags of a command that take a date.
func dateFlagsOf(path ...string) map[string]bool {
	dates := map[string]bool{}
	if cmd := findCommand(path...); cmd != nil {
		cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if isDateFlag(f) {
				dates[f.Name] = true
			}
		})
	}
	return dates
}

// runCommandWithValues executes a command as if its flags had been given.
//
// Output is diverted while it runs: the command writes an envelope to stdout,
// which Bubble Tea is currently drawing over. The summary is read back out of
// that envelope so the browser can report what happened.
func runCommandWithValues(ctx context.Context, path []string, args []string, values map[string]string) (string, error) {
	cmd := findCommand(path...)
	if cmd == nil {
		return "", usageErrorf("no such command: sf %s", strings.Join(path, " "))
	}

	var captured bytes.Buffer
	restore := divertOutput(&captured)
	defer restore()

	// Flag variables are package-level and bound at startup, so anything set
	// here would leak into the next invocation.
	defer resetFlags(cmd)

	for key, value := range values {
		// A repeatable flag takes one Set per value. pflag's stringArray does
		// not split anything, so setting "525\n526" would make a single tag
		// with a newline in its name.
		if flag := cmd.Flags().Lookup(key); flag != nil && flag.Value.Type() == "stringArray" {
			for _, one := range strings.Split(value, "\n") {
				if one == "" {
					continue
				}
				if err := cmd.Flags().Set(key, one); err != nil {
					return "", usageErrorf("%s: %s", key, err)
				}
			}
			continue
		}
		if err := cmd.Flags().Set(key, value); err != nil {
			return "", usageErrorf("%s: %s", key, err)
		}
	}

	cmd.SetContext(ctx)
	if err := cmd.RunE(cmd, args); err != nil {
		return "", err
	}
	return summaryOf(captured.Bytes()), nil
}

// divertOutput points the writer at a buffer and returns a restore function.
func divertOutput(buf *bytes.Buffer) func() {
	previousWriter, previousOut := outw, out
	outw = buf
	out = output.New(output.Options{Format: output.FormatJSON, Writer: buf})
	return func() { outw, out = previousWriter, previousOut }
}

func resetFlags(cmd *cobra.Command) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Changed {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		}
	})
}

// summaryOf reads the one-line result out of a captured envelope.
func summaryOf(raw []byte) string {
	var envelope struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Summary != "" {
		return envelope.Summary
	}
	return "Done"
}

// flagOptions names the value list a flag chooses from.
//
// Only the lists carried as constants are here. The API-backed ones — expense
// categories, sequences, countries — would each cost a request when the form
// opens, and a browser whose whole argument is that opening a record is free
// should not spend one to draw a dropdown.
var flagOptions = map[string]string{
	"type":          "", // resolved per command below
	"payment-type":  "payment-types",
	"delivery-type": "delivery-types",
	"language":      "languages",
	"rounding":      "rounding-types",
}

// optionsFor returns the choices a flag offers, or nil for a free-text field.
//
// `--type` means a different set on each resource, so it is resolved from the
// command rather than the flag name — the one place the flag name is not
// enough on its own.
func optionsFor(cmd *cobra.Command, flag string) func() []tui.FormOption {
	// Tags are read when the form opens and cached for a day, so the cost is
	// one request per session at worst — and picking a tag by name is not a
	// convenience: the API takes only numeric ids and drops a name without a
	// word.
	if flag == "tag" {
		return func() []tui.FormOption { return tagOptions(cmd) }
	}

	// The client roster, for --client on either document. This one is not
	// cached: a client created minutes ago in the web application has to be
	// selectable, and a day-old list would not have it.
	//
	// It costs nothing extra. Typing a name already spends a request —
	// resolveClient searches for it — so the list costs the same one and
	// answers with every name at once instead of an ambiguity error.
	if flag == "client" {
		return func() []tui.FormOption { return clientOptions(cmd) }
	}

	list := flagOptions[flag]
	if flag == "type" {
		switch cmd.Parent().Name() {
		case "invoice":
			list = "invoice-types"
		case "expense":
			list = "expense-types"
		default:
			return nil
		}
	}
	if list == "" {
		return nil
	}

	values := staticValueLists[list]
	options := make([]tui.FormOption, 0, len(values))
	for _, v := range values {
		label := v.Description
		if label == "" {
			label = v.Value
		}
		options = append(options, tui.FormOption{Value: v.Value, Label: label})
	}
	return func() []tui.FormOption { return options }
}

// tagOptions lists the account's tags by name, valued by id.
//
// A failure returns nothing rather than an error: the checklist is then empty
// and the tags are left alone, which is the same as not touching the field.
// Making a whole edit form unopenable because a tag list did not load would be
// a poor trade.
func tagOptions(cmd *cobra.Command) []tui.FormOption {
	raw, err := cachedGet(cmd, "/tags/index.json", nil, valueListTTL)
	if err != nil {
		return nil
	}
	result, err := decodeKeyValueList(raw, "Tag", "name")
	if err != nil {
		return nil
	}
	options := make([]tui.FormOption, 0, len(result.Items))
	for _, item := range result.Items {
		options = append(options, tui.FormOption{
			Value: render.Text(render.Get(item, "Tag.id")),
			Label: render.Text(render.Get(item, "Tag.name")),
		})
	}
	return options
}

// clientOptions lists the account's clients by name, valued by id.
//
// Capped at one page. An account with more clients than that cannot be served
// by a list at all, and the field still takes a typed name or id, which
// resolveClient looks up server-side — so the cap degrades to today's behavior
// rather than to a dead end.
func clientOptions(cmd *cobra.Command) []tui.FormOption {
	params := client.Params{
		"listinfo": "1",
		"per_page": strconv.Itoa(defaultPerPageCap),
		"sort":     "name",
	}
	raw, err := api.Get(ctx(cmd), "/clients/index.json", params)
	if err != nil {
		return nil
	}
	result, err := decodeList(raw)
	if err != nil {
		return nil
	}

	options := make([]tui.FormOption, 0, len(result.Items))
	for _, item := range result.Items {
		id := render.Text(render.Get(item, "Client.id"))
		name := render.Text(render.Get(item, "Client.name"))
		if id == "" {
			continue
		}
		if name == "" {
			name = id
		}
		options = append(options, tui.FormOption{Value: id, Label: name})
	}
	return options
}

// fieldPaths maps a flag name to the record key it corresponds to, where the
// convention does not hold.
//
// The convention is that a flag matches the API field it writes, with dashes
// for underscores — --ic-dph writes ic_dph. It holds nearly everywhere because
// both names come from the same place. Only where a flag was named for what it
// means rather than for the field it sets does it need an entry here.
//
// A missing entry costs an empty box, never a wrong one: a key that does not
// resolve simply has no value to show.
var fieldPaths = map[string]string{
	"due-days":   "due_date",
	"country-id": "country_id",
}

// prefillFrom reads a record's current values for a set of form fields.
func prefillFrom(model string, fields []tui.FormField, dateFlags map[string]bool) func(map[string]any) map[string]string {
	return func(row map[string]any) map[string]string {
		if row == nil {
			return nil
		}
		values := make(map[string]string, len(fields))
		for _, f := range fields {
			key := fieldPaths[f.Key]
			if key == "" {
				key = strings.ReplaceAll(f.Key, "-", "_")
			}
			// The stored value, not a rendered one. What is written back has to
			// be what the API gave us, or opening a form and saving it would
			// reformat fields the user never looked at.
			//
			// Dates are the exception, and only because trimming them changes
			// nothing: the API returns "2026-08-02 00:00:00" for a field it
			// documents and accepts as YYYY-MM-DD, so the time is noise in a
			// box labeled for the date alone.
			// Tags are a list on the record, not a value at a path.
			if f.Key == "tag" {
				if joined := currentTags(row); joined != "" {
					values[f.Key] = joined
				}
				continue
			}

			value := render.Text(render.Get(row, model+"."+key))
			if dateFlags[f.Key] {
				value = render.Date(value)
			}
			if value != "" {
				values[f.Key] = value
			}
		}
		return values
	}
}

// checkItemLines rejects a line the command would reject anyway, while the
// cursor is still in the box rather than after the write comes back.
func checkItemLines(text string) error {
	for n, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if _, err := documentItemSpec(strings.TrimSpace(line)); err != nil {
			return fmt.Errorf("line %d: %s", n+1, output.AsError(err).Message)
		}
	}
	return nil
}

// itemsTotal is what the lines add up to, shown under them as they are typed.
//
// It is the arithmetic the server will do, done twice — which is the point: a
// total that does not match the one you expected is worth seeing before the
// invoice exists, not after.
func itemsTotal(text string) string {
	var net, vat float64
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		item, err := documentItemSpec(strings.TrimSpace(line))
		if err != nil {
			return "" // the validator is already saying what is wrong
		}
		count++

		quantity, price := 1.0, 0.0
		if v, ok := item["quantity"].(float64); ok {
			quantity = v
		}
		if v, ok := item["unit_price"].(float64); ok {
			price = v
		}
		amount := quantity * price
		net += amount
		if rate, ok := item["tax"].(float64); ok {
			vat += amount * rate / 100
		}
	}
	if count == 0 {
		return ""
	}
	return fmt.Sprintf("%d items · %.2f + %.2f VAT = %.2f", count, net, vat, net+vat)
}

// currentTags is the record's tag ids, in the form the checklist expects.
func currentTags(row map[string]any) string {
	ids := make([]string, 0, 4)
	for _, tag := range render.List(row, "Tag") {
		if id := render.Text(render.Get(tag, "Tag.id")); id != "" {
			ids = append(ids, id)
		}
	}
	return strings.Join(ids, "\n")
}

// editAction opens a form for a resource's edit command.
//
// The form shows the record's current values, so it is clear what is about to
// be overwritten. Only fields whose value the user changes are sent — the same
// thing the command does with unset flags — so opening a form and confirming it
// unchanged writes nothing.
func editAction(resource, idPath string, path ...string) tui.Action {
	fields := formFieldsFor(path...)
	model, _, _ := strings.Cut(idPath, ".")
	return tui.Action{
		Key: "e", Label: "edit", Verb: "Update", Writes: true, Fields: fields,
		Prefill: prefillFrom(model, fields, dateFlagsOf(path...)),
		Run: func(ctx context.Context, row map[string]any, values map[string]string) (string, bool, error) {
			id := recordID(row, idPath)
			if id == "" {
				return "", false, usageErrorf("this %s has no id", resource)
			}
			message, err := runCommandWithValues(ctx, path, []string{id}, values)
			if err != nil {
				return "", false, err
			}
			return message, true, nil
		},
	}
}

// createAction opens a form for a resource's create command.
func createAction(resource string, path ...string) tui.Action {
	fields := formFieldsFor(path...)
	return tui.Action{
		Key: "n", Label: "new " + resource, Verb: "Create",
		Writes: true, Standalone: true, Fields: fields,
		Run: func(ctx context.Context, _ map[string]any, values map[string]string) (string, bool, error) {
			message, err := runCommandWithValues(ctx, path, nil, values)
			if err != nil {
				return "", false, err
			}
			return message, true, nil
		},
	}
}

func recordID(row map[string]any, path string) string {
	if row == nil {
		return ""
	}
	return render.Text(render.Get(row, path))
}
