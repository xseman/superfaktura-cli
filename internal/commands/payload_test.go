package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadPayloadAcceptsInlineJSONFileAndStdin(t *testing.T) {
	want := map[string]any{"Invoice": map[string]any{"name": "X"}}

	got, err := readPayload(`{"Invoice":{"name":"X"}}`)
	if err != nil {
		t.Fatalf("inline: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("inline = %v", got)
	}

	path := filepath.Join(t.TempDir(), "invoice.json")
	if err := os.WriteFile(path, []byte(`{"Invoice":{"name":"X"}}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	got, err = readPayload("@" + path)
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("file = %v", got)
	}
}

func TestReadPayloadReturnsAnEmptyDocumentForNoInput(t *testing.T) {
	got, err := readPayload("   ")
	if err != nil {
		t.Fatalf("readPayload: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected an empty document, got %v", got)
	}
}

func TestReadPayloadRejectsMalformedInputWithAUsefulHint(t *testing.T) {
	for _, source := range []string{"not json", "[1,2,3]", `{"unterminated":`} {
		if _, err := readPayload(source); err == nil {
			t.Errorf("readPayload(%q) should have failed", source)
		}
	}

	if _, err := readPayload("@/nonexistent/path.json"); err == nil {
		t.Error("a missing file should be an error")
	}
}

func TestPutCreatesModelSectionsAndSkipsUnsetValues(t *testing.T) {
	doc := map[string]any{}
	put(doc, "Invoice", "name", "Faktúra")
	put(doc, "Invoice", "due", "")
	put(doc, "Invoice", "client_id", 0)
	put(doc, "Invoice", "amount", 12.5)
	put(doc, "Invoice", "nothing", nil)
	put(doc, "Client", "name", "Acme")

	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"Client":{"name":"Acme"},"Invoice":{"amount":12.5,"name":"Faktúra"}}`
	if string(encoded) != want {
		t.Errorf("document\n got %s\nwant %s", encoded, want)
	}
}

func TestPutLayersOnTopOfAnExistingPayload(t *testing.T) {
	doc, err := readPayload(`{"Invoice":{"name":"from file","variable":"123"}}`)
	if err != nil {
		t.Fatalf("readPayload: %v", err)
	}
	put(doc, "Invoice", "name", "from flag")

	section := doc["Invoice"].(map[string]any)
	if section["name"] != "from flag" {
		t.Errorf("the flag should win, got %v", section["name"])
	}
	if section["variable"] != "123" {
		t.Errorf("untouched fields should survive, got %v", section["variable"])
	}
}

func TestPutBoolOnlyWritesTrue(t *testing.T) {
	doc := map[string]any{}
	putBool(doc, "Export", "invoices_pdf", true)
	putBool(doc, "Export", "invoices_xls", false)

	section := doc["Export"].(map[string]any)
	if section["invoices_pdf"] != true {
		t.Error("true should be written")
	}
	if _, present := section["invoices_xls"]; present {
		t.Error("false should be omitted so account defaults survive")
	}
}

func TestRequirePayloadRejectsAnEmptyWrite(t *testing.T) {
	if err := requirePayload(map[string]any{}, "pass --name"); err == nil {
		t.Error("an empty document should be rejected")
	}
	if err := requirePayload(map[string]any{"Invoice": map[string]any{}}, ""); err != nil {
		t.Errorf("a populated document should pass, got %v", err)
	}
}
