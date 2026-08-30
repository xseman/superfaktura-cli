package commands

import (
	"encoding/json"
	"testing"

	"github.com/xseman/superfaktura-cli/internal/render"
)

// These lists are transcribed from the documentation rather than fetched, so a
// typo here silently returns the wrong data — a filter for "this month" that
// quietly means "this year" reads as a working command. Pin the ones where the
// mapping is not guessable.

func TestTimeFilterConstantsMatchTheDocumentedTable(t *testing.T) {
	// value-lists.md, "Time filter constants". The sequence is not intuitive:
	// months come before years, and the week sits at 9 between the quarters.
	want := map[string]string{
		"0": "all", "1": "today", "2": "yesterday",
		"4": "this month", "5": "last month",
		"6": "this year", "7": "last year",
		"8": "this quarter", "9": "this week", "10": "last quarter",
		"11": "last hour", "12": "this hour",
	}

	got := map[string]string{}
	for _, entry := range staticValueLists["time-filters"] {
		got[entry.Value] = entry.Description
	}

	for value, description := range want {
		if got[value] != description {
			t.Errorf("time filter %s = %q, want %q", value, got[value], description)
		}
	}
	if len(got) != 13 {
		t.Errorf("%d constants, want 13 (0 through 12)", len(got))
	}
}

func TestStatusConstantsMatchTheDocumentedTables(t *testing.T) {
	for _, tc := range []struct {
		list string
		want map[string]string
	}{
		{"invoice-statuses", map[string]string{
			"1": "issued", "2": "partially_paid", "3": "paid", "99": "overdue"}},
		{"expense-statuses", map[string]string{
			"1": "new", "2": "partially_paid", "3": "paid", "99": "overdue"}},
		{"export-statuses", map[string]string{
			"0": "failed", "1": "completed", "2": "in progress", "3": "scheduled"}},
	} {
		got := map[string]string{}
		for _, entry := range staticValueLists[tc.list] {
			got[entry.Value] = entry.Description
		}
		for value, description := range tc.want {
			if got[value] != description {
				t.Errorf("%s[%s] = %q, want %q", tc.list, value, got[value], description)
			}
		}
		if len(got) != len(tc.want) {
			t.Errorf("%s has %d entries, want %d", tc.list, len(got), len(tc.want))
		}
	}
}

// The fixtures here are trimmed copies of what the API actually sent.
//
// All three shapes are described in value-lists.md and all three were wrong on
// the first attempt anyway. The categories are the interesting case: the
// documented field table promises an ExpenseCategory wrapper that the example
// response below it does not have, and the live API sides with the example.

func TestCountriesDecodeFromTheIdToNameMap(t *testing.T) {
	// /countries answers {"191":"Slovensko",...}, not a list of records.
	raw := json.RawMessage(`{"191":"Slovensko","1":"Afganistan","14":"Austria"}`)

	got, err := apiValueLists["countries"].decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) != 3 {
		t.Fatalf("got %d countries", len(got.Items))
	}
	// Sorted by identifier, so "1" comes before "14" and "191".
	if id := render.Text(render.Get(got.Items[0], "Country.id")); id != "1" {
		t.Errorf("first id = %q", id)
	}
	if name := render.Text(render.Get(got.Items[0], "Country.name")); name != "Afganistan" {
		t.Errorf("first name = %q", name)
	}
}

func TestExpenseCategoriesFlattenTheTree(t *testing.T) {
	// The categories arrive as a tree, and a child is only identifiable by
	// where it sits, so the parent name has to survive the flattening.
	raw := json.RawMessage(`[
	  {"id":1,"name":"Kancelária","children":[
	    {"id":2,"name":"Nájomné / energie","children":[]},
	    {"id":3,"name":"Kancelárske potreby","children":[]}]},
	  {"id":9,"name":"Doprava","children":[]}]`)

	got, err := decodeCategoryTree(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) != 4 {
		t.Fatalf("got %d categories, want 4 (2 roots + 2 children)", len(got.Items))
	}
	if parent := render.Text(render.Get(got.Items[0], "ExpenseCategory.parent")); parent != "" {
		t.Errorf("a root should have no parent, got %q", parent)
	}
	if parent := render.Text(render.Get(got.Items[1], "ExpenseCategory.parent")); parent != "Kancelária" {
		t.Errorf("child parent = %q", parent)
	}
	if name := render.Text(render.Get(got.Items[3], "ExpenseCategory.name")); name != "Doprava" {
		t.Errorf("last name = %q, the second root should follow its children", name)
	}
}

func TestSequencesFoldTheDocumentTypeIntoEachRecord(t *testing.T) {
	// The type is the map key rather than a field, so flattening has to carry
	// it across or every row looks the same.
	raw := json.RawMessage(`{
	  "regular":[{"id":"18998","name":"Faktúra","mask":"RRRRCCC","default":true}],
	  "proforma":[{"id":"18999","name":"Zálohová","mask":"ZRRRRCCC","default":true}]}`)

	got, err := decodeSequences(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("got %d sequences", len(got.Items))
	}
	// Sorted by type, so proforma precedes regular.
	if kind := render.Text(render.Get(got.Items[0], "Sequence.document_type")); kind != "proforma" {
		t.Errorf("first type = %q", kind)
	}
	if kind := render.Text(render.Get(got.Items[1], "Sequence.document_type")); kind != "regular" {
		t.Errorf("second type = %q", kind)
	}
	if name := render.Text(render.Get(got.Items[1], "Sequence.name")); name != "Faktúra" {
		t.Errorf("the original fields should survive, got name = %q", name)
	}
}
