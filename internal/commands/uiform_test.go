package commands

import (
	"strings"
	"testing"

	"github.com/xseman/superfaktura-cli/internal/tui"
)

// The form is generated from the command's own flags. If that link breaks, a
// form silently offers fields the command does not accept, or omits ones it
// does — neither of which shows up until somebody tries to save.

func TestFormFieldsComeFromTheCommandFlags(t *testing.T) {
	fields := formFieldsFor("client", "edit")
	if len(fields) == 0 {
		t.Fatal("no fields were generated")
	}

	byKey := map[string]string{}
	for _, f := range fields {
		byKey[f.Key] = f.Label
	}

	// Every string flag the command declares should be offered.
	for _, want := range []string{"name", "ico", "dic", "ic-dph", "address", "city", "email"} {
		if _, ok := byKey[want]; !ok {
			t.Errorf("--%s is missing from the form", want)
		}
	}
	// The label is the flag's own help, so the two cannot describe the field
	// differently.
	if !strings.Contains(byKey["ico"], "IČO") {
		t.Errorf("label for ico = %q", byKey["ico"])
	}
}

func TestUneditableFlagsAreLeftOut(t *testing.T) {
	// --data is a raw payload editor, --help is cobra's.
	for _, key := range []string{"data", "help"} {
		for _, f := range formFieldsFor("client", "edit") {
			if f.Key == key {
				t.Errorf("--%s should not be on the form", key)
			}
		}
	}
}

func TestARepeatableFlagIsAChecklist(t *testing.T) {
	// --tag was left out for want of a field shape. A record can carry several
	// and the API takes only their numeric ids, so ticking names is not a
	// convenience: a name the server does not recognize is dropped in silence.
	var tag *tui.FormField
	for _, f := range formFieldsFor("invoice", "edit") {
		if f.Key == "tag" {
			tag = &f
		}
	}
	if tag == nil {
		t.Fatal("--tag is not on the form")
	}
	if !tag.Multi {
		t.Error("--tag is repeatable and should be a checklist, not one value")
	}
	if tag.Options == nil {
		t.Error("--tag has nothing to tick")
	}

	// A repeatable flag with no list to tick is a box of one value per line
	// instead — which is how line items are entered, huh having no repeating
	// group and a composite value no single field that fits it.
	var item *tui.FormField
	for _, f := range formFieldsFor("invoice", "create") {
		if f.Key == "item" {
			item = &f
		}
	}
	if item == nil {
		t.Fatal("--item is not on the form, so an invoice cannot be created here")
	}
	if !item.Multi || item.Options != nil {
		t.Errorf("--item should be lines, not a checklist: multi=%v options=%v",
			item.Multi, item.Options != nil)
	}
	if item.Validate == nil || item.Note == nil {
		t.Error("--item should validate its lines and total them")
	}
}

func TestItemLinesAreCheckedAndTotalled(t *testing.T) {
	// The arithmetic the server will do, done twice — a total that is not the
	// one you expected is worth seeing before the invoice exists.
	const items = "Konzultácie:8:75:23\nCestovné:1:40:23"

	if err := checkItemLines(items); err != nil {
		t.Errorf("valid items refused: %s", err)
	}
	if got := itemsTotal(items); got != "2 items · 640.00 + 147.20 VAT = 787.20" {
		t.Errorf("total = %q", got)
	}

	// Blank lines are not items.
	if got := itemsTotal("Konzultácie:8:75:23\n\n"); got != "1 items · 600.00 + 138.00 VAT = 738.00" {
		t.Errorf("blank lines counted: %q", got)
	}

	// A bad line is named by its number, while the cursor is still in the box.
	err := checkItemLines("Konzultácie:8:75:23\nCestovné:notanumber:40:23")
	if err == nil {
		t.Fatal("an unparseable line was accepted")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("the complaint does not say which line: %s", err)
	}
	// And the total steps aside rather than showing a figure built from a line
	// the validator is already objecting to.
	if got := itemsTotal("Konzultácie:8:75:23\nCestovné:notanumber:40:23"); got != "" {
		t.Errorf("a total was shown for unparseable input: %q", got)
	}
}

func TestGlobalFlagsAreNotOffered(t *testing.T) {
	// --profile and --company belong to the invocation, not the record.
	for _, f := range formFieldsFor("client", "edit") {
		if f.Key == "profile" || f.Key == "company" || f.Key == "api-url" {
			t.Errorf("--%s is a global flag and should not be on the form", f.Key)
		}
	}
}

func TestTheIdentifyingFieldComesFirst(t *testing.T) {
	// Alphabetical order buried "name" among the addresses.
	for _, path := range [][]string{{"client", "edit"}, {"client", "create"}, {"expense", "add"}} {
		fields := formFieldsFor(path...)
		if len(fields) == 0 {
			t.Fatalf("no fields for %v", path)
		}
		if fields[0].Key != "name" {
			t.Errorf("%v starts with --%s, want --name", path, fields[0].Key)
		}
	}
}

func TestEveryResourceTheBrowserOffersHasAForm(t *testing.T) {
	for _, path := range [][]string{
		{"client", "edit"}, {"client", "create"},
		{"expense", "edit"}, {"expense", "add"},
		{"invoice", "edit"},
	} {
		if len(formFieldsFor(path...)) == 0 {
			t.Errorf("sf %s generated an empty form", strings.Join(path, " "))
		}
	}
}

func TestAnUnknownCommandYieldsNoForm(t *testing.T) {
	if fields := formFieldsFor("nonexistent", "edit"); fields != nil {
		t.Errorf("got %d fields for a command that does not exist", len(fields))
	}
}

func TestPrefillReadsTheRecordsOwnValues(t *testing.T) {
	// The flag name is the API field name with dashes for underscores, because
	// both come from the same place. Only a flag named for what it means rather
	// than for the field it sets needs an entry in fieldPaths.
	fields := []tui.FormField{
		{Key: "name"}, {Key: "ic-dph"}, {Key: "due-days"}, {Key: "country-id"},
		{Key: "nonesuch"},
	}
	row := map[string]any{"Client": map[string]any{
		"name":       "Acme s.r.o.",
		"ic_dph":     "SK2020202020",
		"due_date":   "14",
		"country_id": "191",
	}}

	got := prefillFrom("Client", fields, nil)(row)

	for key, want := range map[string]string{
		"name": "Acme s.r.o.", "ic-dph": "SK2020202020",
		"due-days": "14", "country-id": "191",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	// A flag with no matching field leaves an empty box rather than a wrong one.
	if _, ok := got["nonesuch"]; ok {
		t.Errorf("an unresolvable flag was given a value: %q", got["nonesuch"])
	}
}

func TestPrefillOnNoRecordIsEmpty(t *testing.T) {
	// Create actions are standalone: there is no row to read.
	if got := prefillFrom("Client", []tui.FormField{{Key: "name"}}, nil)(nil); len(got) != 0 {
		t.Errorf("got %v, want nothing", got)
	}
}

func TestDatesAreCheckedForTheOneShapeThatLiesQuietly(t *testing.T) {
	// The server takes anything strtotime takes, so almost everything must be
	// let through. Slashes are the exception: they are read in american
	// month/day order, so 08/09/2026 books the 9th of August without an error.
	for _, ok := range []string{
		"", "2026-08-15", "15.08.2026", "31.12.2026",
		"today", "tomorrow", "+14 days", "next friday",
	} {
		if err := checkDate(ok); err != nil {
			t.Errorf("%q was refused: %s", ok, err.Message)
		}
	}
	for _, bad := range []string{"15/08/2026", "08/09/2026", "2026-13-01", "2026-02-30"} {
		if err := checkDate(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestOnlyDateFlagsGetTheDateCheck(t *testing.T) {
	// --date on a list command is a time-filter constant, not a date. The
	// usage string is the signal precisely so the two cannot be confused.
	fields := formFieldsFor("invoice", "edit")
	byKey := map[string]tui.FormField{}
	for _, f := range fields {
		byKey[f.Key] = f
	}

	if byKey["due"].Validate == nil {
		t.Error("--due takes a date and was not validated")
	}
	if got := byKey["due"].Validate("15/08/2026"); got == nil {
		t.Error("the form accepted a slashed date")
	}
	if got := byKey["due"].Validate("+14 days"); got != nil {
		t.Errorf("the form refused a relative date: %s", got)
	}
	if byKey["name"].Validate != nil {
		t.Error("a non-date flag was given a date validator")
	}
}

// countOptions is how many choices a field offers, zero for free text.
func countOptions(f tui.FormField) int {
	if f.Options == nil {
		return 0
	}
	return len(f.Options())
}

func TestEnumeratedFlagsBecomeLists(t *testing.T) {
	byKey := func(path ...string) map[string]tui.FormField {
		out := map[string]tui.FormField{}
		for _, f := range formFieldsFor(path...) {
			out[f.Key] = f
		}
		return out
	}

	invoice := byKey("invoice", "edit")
	if got := countOptions(invoice["type"]); got != 8 {
		t.Errorf("invoice --type offers %d options, want the 8 documented types", got)
	}
	if got := countOptions(invoice["payment-type"]); got != 15 {
		t.Errorf("--payment-type offers %d options", got)
	}
	// --type means a different set on each resource, which the flag name alone
	// cannot tell you.
	if got := countOptions(byKey("expense", "edit")["type"]); got != 6 {
		t.Errorf("expense --type offers %d options, want the 6 expense types", got)
	}

	// Free text stays free text.
	for _, key := range []string{"name", "variable", "due", "comment"} {
		if countOptions(invoice[key]) != 0 {
			t.Errorf("--%s was turned into a list", key)
		}
	}

	// The list is on screen, so the label stops pointing at a command that
	// prints it.
	if strings.Contains(invoice["type"].Label, "see ") {
		t.Errorf("label still refers the reader elsewhere: %q", invoice["type"].Label)
	}
}

func TestInvoiceEditMatchesExpenseEdit(t *testing.T) {
	// It offered three fields, so a client or a document type chosen at
	// creation could never be corrected — create accepted both and edit did not.
	have := map[string]bool{}
	for _, f := range formFieldsFor("invoice", "edit") {
		have[f.Key] = true
	}
	for _, want := range []string{
		"name", "client", "type", "created", "delivery", "due",
		"variable", "constant", "payment-type", "comment",
	} {
		if !have[want] {
			t.Errorf("invoice edit cannot change --%s", want)
		}
	}
}

func TestADatePrefillLosesTheTimeTheAPIAddsToIt(t *testing.T) {
	// The API returns "2026-08-02 00:00:00" for a field it documents and
	// accepts as YYYY-MM-DD. In a box labeled for the date alone, the time is
	// noise the user would have to delete before typing.
	fields := []tui.FormField{{Key: "created"}, {Key: "name"}}
	row := map[string]any{"Invoice": map[string]any{
		"created": "2026-08-02 00:00:00",
		"name":    "Faktúra 2026005",
	}}

	got := prefillFrom("Invoice", fields, map[string]bool{"created": true})(row)
	if got["created"] != "2026-08-02" {
		t.Errorf("created = %q, want the date alone", got["created"])
	}
	if got["name"] != "Faktúra 2026005" {
		t.Errorf("a non-date field was reformatted: %q", got["name"])
	}
}

func TestAFlagsHelpBecomesALabelAndAHint(t *testing.T) {
	// The whole help string as a label read as prose. "Existing client, by
	// numeric ID or by name" above a box is a sentence, not a name, and
	// fourteen of them stacked is what made the form a wall.
	for _, tc := range []struct{ name, usage, label, hint string }{
		{"created", "Issue date (YYYY-MM-DD)", "Issue date", "YYYY-MM-DD"},
		{"client", "Existing client, by numeric ID or by name", "Existing client", "by numeric ID or by name"},
		{"type", "Document type (see 'sf values invoice-types')", "Document type", "see 'sf values invoice-types'"},
		{"comment", "Comment", "Comment", ""},
		{"constant", "Constant symbol", "Constant symbol", ""},
		// No separator and too long to be a label: fall back to the flag name.
		{"new-client", "Create the invoice for a new client with this name", "New client", ""},
	} {
		label, hint := splitUsage(tc.name, tc.usage)
		if label != tc.label || hint != tc.hint {
			t.Errorf("--%s → %q / %q, want %q / %q", tc.name, label, hint, tc.label, tc.hint)
		}
	}
}

func TestTheLabelFallbackSurvivesANameItCannotSlice(t *testing.T) {
	// The fallback used to raise the first *byte* of the flag name, which
	// halved a multi-byte letter and panicked outright when there was no byte
	// at all. Found by FuzzSplitUsage.
	tooLong := "Create the invoice for a new client with this name"

	if label, hint := splitUsage("dátum-splatnosti", tooLong); label != "Dátum splatnosti" || hint != "" {
		t.Errorf("multi-byte name → %q / %q", label, hint)
	}
	// Nothing to fall back to, so the long help is still better than a field
	// with no label above it.
	for _, name := range []string{"", " ", "-", " - "} {
		if label, _ := splitUsage(name, tooLong); label != tooLong {
			t.Errorf("splitUsage(%q, …) → %q, want the usage kept", name, label)
		}
	}
}

func TestTheFormKnowsWhatItCannotBeSubmittedWithout(t *testing.T) {
	// Not the flags cobra marks required — on the command line --data stands in
	// for all of them, and the browser has no --data.
	required := func(path ...string) map[string]bool {
		out := map[string]bool{}
		for _, f := range formFieldsFor(path...) {
			if f.Required {
				out[f.Key] = true
			}
		}
		return out
	}

	if got := required("invoice", "create"); !got["client"] || !got["item"] || len(got) != 2 {
		t.Errorf("invoice create requires %v, want client and item", got)
	}
	// Editing is correcting, so nothing is compulsory: an untouched form is a
	// no-op by design.
	if got := required("invoice", "edit"); len(got) != 0 {
		t.Errorf("invoice edit requires %v, want nothing", got)
	}
	if got := required("expense", "add"); !got["name"] || len(got) != 1 {
		t.Errorf("expense add requires %v, want name", got)
	}
}

func TestTheClientFieldIsARosterToSearch(t *testing.T) {
	// Typing a name already costs a request — resolveClient searches for it —
	// so listing every client costs the same one and answers with all the names
	// at once instead of an ambiguity error.
	var client *tui.FormField
	for _, f := range formFieldsFor("invoice", "create") {
		if f.Key == "client" {
			client = &f
		}
	}
	if client == nil {
		t.Fatal("--client is not on the form")
	}
	if client.Options == nil {
		t.Error("--client should offer the roster, not a bare box")
	}
	if client.Multi {
		t.Error("--client takes one client")
	}
	// --new-client names one that does not exist yet, so it stays free text.
	for _, f := range formFieldsFor("invoice", "create") {
		if f.Key == "new-client" && f.Options != nil {
			t.Error("--new-client cannot be picked from a list of what exists")
		}
	}
}
