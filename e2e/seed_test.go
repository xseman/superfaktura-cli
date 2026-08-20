//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Seeds a real account with a set of records and leaves them there, so they
// can be looked at in the web UI.
//
// Doubly opt-in: it needs the e2e tag, credentials, and SF_SEED=1. Everything
// else in this package either cleans up after itself or only reads; this one
// deliberately does not, so it must never fire by accident. Point it at a
// sandbox.
//
//	make seed
func TestSeedSandbox(t *testing.T) {
	if os.Getenv("SF_SEED") == "" {
		t.Skip("set SF_SEED=1 to create records and leave them in place")
	}
	requireAPIAccess(t)

	// A marker that is easy to search for in the UI and obviously not real.
	const marker = "sf-cli test"

	created := map[string]string{}
	record := func(kind, id string) {
		created[kind] = id
		t.Logf("created %-16s id=%s", kind, id)
	}

	// A client to bill.
	clientID := seedWrite(t, "Client",
		"client", "create",
		"--name", marker+" — Acme s.r.o.",
		"--ico", "46655034",
		"--dic", "2023513470",
		"--ic-dph", "SK2023513470",
		"--address", "Pri Suchom mlyne 6",
		"--city", "Bratislava",
		"--zip", "811 04",
		"--email", "fakturacia@example.com",
		"--phone", "+421900000000",
		"--comment", marker)
	requireSeedID(t, "client", clientID)
	record("client", clientID)

	// A contact person on that client.
	if id := seedWrite(t, "ContactPerson",
		"client", "contact", "add", clientID,
		"--name", marker+" — Jana Nováková",
		"--email", "jana@example.com",
		"--phone", "+421900111222"); id != "" {
		record("contact", id)
	}

	// A tag. Re-running the seed must not die here: the API answers 409 for a
	// name that already exists, which on a second run is the expected outcome
	// rather than a failure.
	if out, ok := seedOptional(t, "tag", "add", marker); ok {
		if id := idFrom(out, "Tag"); id != "" {
			record("tag", id)
		}
	} else {
		t.Log("the tag already exists, which is fine on a repeat run")
	}

	// A stock item.
	if id := seedWrite(t, "StockItem",
		"stock", "add",
		"--name", marker+" — Konzultácie",
		"--sku", "SFCLI-001",
		"--unit", "hod",
		"--price", "75",
		"--vat", "23",
		"--description", "Vytvorené automatickým testom sf CLI"); id != "" {
		record("stock item", id)
	}

	// An invoice with two lines, in the currency and VAT rate a Slovak account
	// would actually use.
	invoiceID := seedWrite(t, "Invoice",
		"invoice", "create",
		"--client", clientID,
		"--name", marker+" — faktúra",
		"--item", "Konzultácie:8:75:23",
		"--item", "Cestovné:1:40:23",
		"--due", "2026-09-15",
		"--variable", "20260801",
		"--checksum", "sf-cli-seed-20260801")
	requireSeedID(t, "invoice", invoiceID)
	record("invoice", invoiceID)

	// A partial payment, so the invoice shows as partially paid rather than
	// sitting at zero.
	seedRun(t, "invoice", "pay", invoiceID, "--amount", "300", "--type", "transfer")
	t.Log("recorded a partial payment of 300 on the invoice")

	// The PDF, to confirm the token round-trip works against real data.
	pdf := filepath.Join(t.TempDir(), "seed.pdf")
	seedRun(t, "invoice", "pdf", invoiceID, "-o", pdf, "--bysquare")
	// Check the magic number rather than the size: that distinguishes a real
	// document from an HTML error page, and holds whether the response came
	// from the API or from a test double.
	body, err := os.ReadFile(pdf) //nolint:gosec // G304: a path this test just wrote
	switch {
	case err != nil:
		t.Errorf("no PDF was written: %v", err)
	case !strings.HasPrefix(string(body), "%PDF"):
		t.Errorf("the download is not a PDF, it starts with %q", firstBytes(body, 40))
	default:
		t.Logf("downloaded a %d byte PDF", len(body))
	}

	// A proforma, so both document types exist.
	if id := seedWrite(t, "Invoice",
		"invoice", "create",
		"--client", clientID,
		"--type", "proforma",
		"--name", marker+" — zálohová faktúra",
		"--item", "Záloha:1:500:23"); id != "" {
		record("proforma", id)
	}

	// An expense carrying a real attachment, which is the path that had no
	// coverage at all until now.
	receipt := filepath.Join(t.TempDir(), "blocek.pdf")
	if err := os.WriteFile(receipt, minimalPDF(marker), 0o600); err != nil {
		t.Fatalf("write the receipt fixture: %v", err)
	}
	if id := seedWrite(t, "Expense",
		"expense", "add",
		"--name", marker+" — hosting",
		"--type", "invoice",
		"--amount", "49",
		"--vat", "23",
		"--currency", "EUR",
		"--document-no", "SFCLI-EXP-001",
		"--comment", marker,
		"--attachment", receipt); id != "" {
		record("expense", id)
	}

	t.Log("")
	t.Log("Left in place for inspection in the web UI:")
	for _, kind := range slices.Sorted(maps.Keys(created)) {
		t.Logf("  %-12s %s", kind, created[kind])
	}
	t.Logf("  search the UI for %q", marker)
}

// seedOptional runs a command whose failure is tolerable, reporting whether it
// succeeded rather than ending the run.
func seedOptional(t *testing.T, args ...string) (string, bool) {
	t.Helper()
	res := run(t, args...)
	if res.ExitCode != 0 {
		t.Logf("sf %s: %s", strings.Join(args, " "), strings.TrimSpace(res.Stdout))
		return "", false
	}
	return res.Stdout, true
}

// seedWrite runs a create and returns the new identifier.
func seedWrite(t *testing.T, model string, args ...string) string {
	t.Helper()
	out := seedRun(t, args...)
	if id := idFrom(out, model); id != "" {
		return id
	}

	// Several endpoints answer without an identifier. That is worth noting but
	// not worth failing on: the record was still created.
	t.Logf("note: no %s id in the response to 'sf %s'", model, strings.Join(args, " "))
	return ""
}

// idFrom digs the new identifier out of a response envelope.
func idFrom(out, model string) string {
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		return ""
	}

	// The identifier sits under data.<Model>.id on most endpoints and
	// data.id on a few.
	if section, ok := envelope.Data[model].(map[string]any); ok {
		if id := fmt.Sprint(section["id"]); id != "" && id != "<nil>" {
			return id
		}
	}
	if nested, ok := envelope.Data["data"].(map[string]any); ok {
		if section, ok := nested[model].(map[string]any); ok {
			if id := fmt.Sprint(section["id"]); id != "" && id != "<nil>" {
				return id
			}
		}
		if id := fmt.Sprint(nested["id"]); id != "" && id != "<nil>" {
			return id
		}
	}

	// Some endpoints report it at the top level under their own name, such as
	// /tags/add answering {"error":0,"tag_id":"1"}.
	for key, value := range envelope.Data {
		if strings.HasSuffix(key, "_id") {
			if id := fmt.Sprint(value); id != "" && id != "<nil>" {
				return id
			}
		}
	}
	return ""
}

// requireID fails the seed when an identifier the rest of it depends on is
// missing, rather than carrying on and creating orphans.
func requireSeedID(t *testing.T, kind, id string) string {
	t.Helper()
	if id == "" {
		t.Fatalf("cannot continue without a %s id", kind)
	}
	return id
}

// seedRun runs the binary against the configured account and returns stdout.
func seedRun(t *testing.T, args ...string) string {
	t.Helper()
	res := run(t, args...)
	if res.ExitCode != 0 {
		t.Fatalf("sf %s failed with exit %d:\n%s%s",
			strings.Join(args, " "), res.ExitCode, res.Stdout, res.Stderr)
	}
	return res.Stdout
}

// minimalPDF returns the smallest thing the API will accept as a PDF
// attachment, so the seed does not depend on a checked-in binary fixture.
func minimalPDF(title string) []byte {
	return []byte("%PDF-1.4\n" +
		"1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
		"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
		"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 50]>>endobj\n" +
		"trailer<</Root 1 0 R>>\n" +
		"% " + title + "\n%%EOF\n")
}

func firstBytes(body []byte, n int) string {
	if len(body) > n {
		return string(body[:n]) + "…"
	}
	return string(body)
}
