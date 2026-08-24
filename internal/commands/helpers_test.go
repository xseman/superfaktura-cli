package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/xseman/superfaktura-cli/internal/output"
	"github.com/xseman/superfaktura-cli/internal/render"
)

func TestDecodeListHandlesTheListinfoEnvelope(t *testing.T) {
	raw := json.RawMessage(`{"filtered":false,"itemCount":2,"pageCount":1,
		"items":[{"Invoice":{"id":"1"}},{"Invoice":{"id":"2"}}]}`)

	got, err := decodeList(raw)
	if err != nil {
		t.Fatalf("decodeList: %v", err)
	}
	if len(got.Items) != 2 || got.ItemCount != 2 || got.PageCount != 1 {
		t.Fatalf("got %+v", got)
	}
	if id := render.Text(render.Get(got.Items[1], "Invoice.id")); id != "2" {
		t.Errorf("second item id = %q", id)
	}
}

func TestDecodeListHandlesABareArray(t *testing.T) {
	// /cash_registers/getDetails answers with a plain array.
	raw := json.RawMessage(`[{"CashRegister":{"id":"2"}},{"CashRegister":{"id":"1"}}]`)

	got, err := decodeList(raw)
	if err != nil {
		t.Fatalf("decodeList: %v", err)
	}
	if len(got.Items) != 2 || got.ItemCount != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestDecodeListUnderPullsANamedCollection(t *testing.T) {
	// /bank_accounts/index wraps its list in a named key.
	raw := json.RawMessage(`{"BankAccounts":[{"BankAccount":{"id":"1"}}],"error":0}`)

	got, err := decodeListUnder(raw, "BankAccounts")
	if err != nil {
		t.Fatalf("decodeListUnder: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("got %+v", got)
	}
	if id := render.Text(render.Get(got.Items[0], "BankAccount.id")); id != "1" {
		t.Errorf("id = %q", id)
	}
}

func TestDecodeListUnderTreatsAMissingKeyAsEmpty(t *testing.T) {
	got, err := decodeListUnder(json.RawMessage(`{"error":0}`), "BankAccounts")
	if err != nil {
		t.Fatalf("decodeListUnder: %v", err)
	}
	if len(got.Items) != 0 {
		t.Errorf("got %+v, want no items", got)
	}
}

func TestDecodeKeyValueListTurnsTheTagMapIntoRows(t *testing.T) {
	// /tags/index.json answers {"1":"abc"}.
	raw := json.RawMessage(`{"2":"beta","1":"abc"}`)

	got, err := decodeKeyValueList(raw, "Tag", "name")
	if err != nil {
		t.Fatalf("decodeKeyValueList: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("got %+v", got)
	}
	// Sorted by ID so output is stable between runs.
	if id := render.Text(render.Get(got.Items[0], "Tag.id")); id != "1" {
		t.Errorf("first id = %q", id)
	}
	if name := render.Text(render.Get(got.Items[1], "Tag.name")); name != "beta" {
		t.Errorf("second name = %q", name)
	}
}

func TestDecodeKeyValueListTreatsTheEmptyArrayAsNoTags(t *testing.T) {
	got, err := decodeKeyValueList(json.RawMessage(`[]`), "Tag", "name")
	if err != nil {
		t.Fatalf("decodeKeyValueList: %v", err)
	}
	if len(got.Items) != 0 {
		t.Errorf("got %+v, want no items", got)
	}
}

func TestListOptionsRejectAnOversizedPageAndBadDirection(t *testing.T) {
	if _, err := (&listOptions{perPage: 500}).params(); err == nil {
		t.Error("per_page above the documented cap should be rejected")
	}
	if _, err := (&listOptions{direction: "sideways"}).params(); err == nil {
		t.Error("an invalid direction should be rejected")
	}
	if _, err := (&listOptions{direction: "ASC"}).params(); err != nil {
		t.Errorf("ASC should be accepted, got %v", err)
	}
}

func TestListOptionsBase64EncodeTheSearchTerm(t *testing.T) {
	params, err := (&listOptions{search: "Acme s.r.o."}).params()
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	if params["search"] == "Acme s.r.o." {
		t.Error("the search term should be base64 encoded for the path")
	}
	if params["listinfo"] != "1" {
		t.Error("listinfo should always be requested so counts come back")
	}
}

func TestFilterFlagsBecomeAPIParameters(t *testing.T) {
	params, err := (&listOptions{filters: []string{"created=3", "created_since=2026-01-01"}}).params()
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	if params["created"] != "3" || params["created_since"] != "2026-01-01" {
		t.Errorf("params = %v", params)
	}

	if _, err := (&listOptions{filters: []string{"missing-equals"}}).params(); err == nil {
		t.Error("a filter without = should be rejected")
	}
}

func TestRequireIDRejectsNonNumericIdentifiers(t *testing.T) {
	if _, err := requireID("invoice", "abc"); err == nil {
		t.Error("expected an error")
	} else if output.AsError(err).ExitCode() != output.ExitUsage {
		t.Errorf("exit code = %d, want %d", output.AsError(err).ExitCode(), output.ExitUsage)
	}

	if got, err := requireID("invoice", "1042"); err != nil || got != "1042" {
		t.Errorf("requireID = %q, %v", got, err)
	}
}

func TestParseNumberAcceptsACommaDecimalSeparator(t *testing.T) {
	got, err := parseNumber("1240,50")
	if err != nil {
		t.Fatalf("parseNumber: %v", err)
	}
	if got != 1240.50 {
		t.Errorf("parseNumber = %v", got)
	}
	if _, err := parseNumber("lots"); err == nil {
		t.Error("expected an error for a non-number")
	}
}

func TestDocumentItemSpecParsesTheCompactSyntax(t *testing.T) {
	item, err := documentItemSpec("Consulting:2:500:23")
	if err != nil {
		t.Fatalf("documentItemSpec: %v", err)
	}
	if item["name"] != "Consulting" || item["quantity"] != 2.0 ||
		item["unit_price"] != 500.0 || item["tax"] != 23.0 {
		t.Errorf("item = %v", item)
	}

	// Everything after the name is optional.
	item, err = documentItemSpec("Consulting")
	if err != nil {
		t.Fatalf("documentItemSpec: %v", err)
	}
	if len(item) != 1 {
		t.Errorf("item = %v, want only the name", item)
	}
}

func TestDocumentItemSpecRejectsUnusableInput(t *testing.T) {
	for _, spec := range []string{"", ":2:500", "Name:two", "a:1:2:3:4"} {
		if _, err := documentItemSpec(spec); err == nil {
			t.Errorf("documentItemSpec(%q) should have failed", spec)
		}
	}
}

func TestParseNumberRejectsTheNumbersJSONCannotSend(t *testing.T) {
	// strconv reads "NaN" and "Inf" as numbers without an error, so they got
	// as far as json.Marshal, which refuses the entire payload with
	// "unsupported value: NaN" — a message that never names the flag that
	// carried it. Found by FuzzDocumentItemSpec.
	for _, value := range []string{"NaN", "nan", "Inf", "+inf", "-Infinity", "infinity"} {
		if got, err := parseNumber(value); err == nil {
			t.Errorf("parseNumber(%q) = %v, want an error", value, got)
		}
	}

	for _, spec := range []string{"Consulting:NaN", "Consulting:1:Inf", "Consulting:1:2:-inf"} {
		item, err := documentItemSpec(spec)
		if err == nil {
			t.Errorf("documentItemSpec(%q) = %v, want an error", spec, item)
			continue
		}
	}

	// And a finite amount still gets through, including a negative one: a
	// credit note's lines are negative.
	if got, err := parseNumber("-19.5"); err != nil || got != -19.5 {
		t.Errorf("parseNumber(-19.5) = %v, %v", got, err)
	}
}

func TestWriteIDsReadsTheNestedIdentifier(t *testing.T) {
	// The shared output writer expects a top-level "id"; SuperFaktura records
	// nest it under the model name, which is what writeIDs exists to handle.
	var buf bytes.Buffer
	outw = &buf
	t.Cleanup(func() { outw = os.Stdout })

	rows := []map[string]any{
		{"Invoice": map[string]any{"id": "1042"}},
		{"Invoice": map[string]any{"id": "1039"}},
		{"Invoice": map[string]any{"name": "no id here"}},
	}
	if err := writeIDs(rows, "Invoice.id"); err != nil {
		t.Fatalf("writeIDs: %v", err)
	}
	if got := buf.String(); got != "1042\n1039\n" {
		t.Errorf("output = %q", got)
	}
}

func TestAHumanSeesTheNextStepsToo(t *testing.T) {
	// The human branch of emit used to return before it looked at the options,
	// so anything carried there was invisible to a person — while the comment
	// above emit promised that "exit codes, breadcrumbs and --jq behave
	// identically everywhere". This is that promise made true.
	var buf bytes.Buffer
	printNextSteps(&buf,
		output.WithSummary("Invoice created"),
		output.WithNext(
			output.Step{Cmd: "sf invoice view 7", Does: "see it"},
			output.Step{Cmd: "sf invoice pdf 7", Does: "download the PDF"}))

	got := buf.String()
	for _, want := range []string{"Next:", "sf invoice view 7", "see it", "sf invoice pdf 7"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is missing:\n%s", want, got)
		}
	}
	// The commands are column-aligned, so the descriptions line up and the
	// block reads as a list rather than a paragraph.
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines, want a heading and two steps:\n%s", len(lines), got)
	}
	if strings.Index(lines[1], "see it") != strings.Index(lines[2], "download") {
		t.Errorf("the descriptions are not aligned:\n%s", got)
	}

	// Nothing to suggest, nothing printed — a read must not grow a blank block.
	buf.Reset()
	printNextSteps(&buf, output.WithSummary("Invoice created"))
	if buf.Len() != 0 {
		t.Errorf("printed %q with no steps", buf.String())
	}
}

// Raw must stay aligned with Items — emitList only trusts it when the lengths
// match, and a misalignment would silently emit the wrong records.
func TestDecodeListKeepsRawAligned(t *testing.T) {
	raw := json.RawMessage(`{"itemCount":2,"pageCount":1,"items":[
		{"Invoice":{"id":"2","zebra":1,"alpha":2}},
		{"Invoice":{"id":"1"}}]}`)
	result, err := decodeList(raw)
	if err != nil {
		t.Fatalf("decodeList: %v", err)
	}
	if len(result.Raw) != len(result.Items) {
		t.Fatalf("raw misaligned: %d raw for %d items", len(result.Raw), len(result.Items))
	}
	// The bytes are the originals: key order survives.
	first := string(result.Raw[0])
	if strings.Index(first, "zebra") > strings.Index(first, "alpha") {
		t.Fatalf("raw item was reshuffled: %s", first)
	}
	// The synthesized keyed fallback has no faithful bytes and must say so.
	keyed, err := decodeList(json.RawMessage(`{"7":{"name":"x"}}`))
	if err != nil {
		t.Fatalf("decodeList keyed: %v", err)
	}
	if len(keyed.Raw) != 0 {
		t.Fatalf("keyed fallback claimed raw fidelity: %v", keyed.Raw)
	}
}
