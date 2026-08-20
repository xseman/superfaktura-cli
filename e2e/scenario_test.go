//go:build e2e

package e2e

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Whole workflows rather than single commands: the things somebody actually
// does with an invoicing tool, run end to end against a recording server.
//
// These check the sequence of calls and what each one carries. They cannot
// check that the server is happy with any of it — no reachable account allows
// that — but they catch a step that silently sends the wrong thing, which
// single-command tests do not.

// steps returns the recorded requests as "METHOD /path" for readable asserts.
func steps(c *capture) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.requests))
	for _, r := range c.requests {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func assertSequence(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d requests, want %d:\n got %v\nwant %v", len(got), len(want), got, want)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("step %d = %q, want it to start with %q", i+1, got[i], want[i])
		}
	}
}

func TestScenarioBillANewClient(t *testing.T) {
	// The common path: a new customer, an invoice, send it, get paid, file
	// the PDF.
	srv, c := server(t)

	invoke(t, srv, "client", "create", "--name", "Nový Klient s.r.o.", "--ico", "46655034")
	invoke(t, srv, "invoice", "create", "--client", "7",
		"--item", "Konzultácie:8:75:23", "--item", "Doprava:1:40:23",
		"--due", "2026-09-15", "--variable", "2026042")
	invoke(t, srv, "invoice", "send", "1", "--to", "faktury@klient.sk")
	invoke(t, srv, "invoice", "pay", "1", "--amount", "641,20", "--type", "transfer")
	invoke(t, srv, "invoice", "pdf", "1", "-o", filepath.Join(t.TempDir(), "f.pdf"))

	assertSequence(t, steps(c),
		"POST /clients/create",
		"POST /invoices/create",
		"POST /invoices/send",
		"POST /invoice_payments/add",
		// The PDF is two calls: the token lives on the invoice detail.
		"GET /invoices/view/1.json",
		"GET /invoices/pdf/1",
	)

	c.mu.Lock()
	defer c.mu.Unlock()
	items, ok := c.requests[1].Body["InvoiceItem"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("expected two line items, got %v", c.requests[1].Body["InvoiceItem"])
	}
	first, _ := items[0].(map[string]any)
	if first["name"] != "Konzultácie" || first["quantity"] != 8.0 || first["unit_price"] != 75.0 {
		t.Errorf("first item = %v", first)
	}
	if amount := c.requests[3].Body["InvoicePayment"].(map[string]any)["amount"]; amount != 641.20 {
		t.Errorf("payment amount = %v, want 641.2 (comma decimal separator)", amount)
	}
}

func TestScenarioExpenseWithAReceipt(t *testing.T) {
	// A receipt photographed and filed against an expense. The API takes the
	// file as base64 inside the payload rather than as a multipart upload.
	srv, c := server(t)

	receipt := filepath.Join(t.TempDir(), "blocek.pdf")
	content := []byte("%PDF-1.4 receipt content")
	if err := os.WriteFile(receipt, content, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	invoke(t, srv, "expense", "add", "--name", "Tankovanie", "--amount", "62,40",
		"--vat", "23", "--type", "bill", "--attachment", receipt)

	got, _ := c.last()
	expense, ok := got.Body["Expense"].(map[string]any)
	if !ok {
		t.Fatalf("no Expense in the payload: %v", got.Body)
	}

	encoded, _ := expense["attachment"].(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("attachment is not base64: %v", err)
	}
	if string(decoded) != string(content) {
		t.Errorf("attachment decoded to %q, want %q", decoded, content)
	}
	if expense["amount"] != 62.40 {
		t.Errorf("amount = %v, want 62.4", expense["amount"])
	}
}

func TestScenarioAttachmentLimitsAreCheckedLocally(t *testing.T) {
	// A rejected upload costs a request out of a daily allowance of 1000, so
	// the size and type limits are enforced before anything is sent.
	srv, c := server(t)
	dir := t.TempDir()

	oversized := filepath.Join(dir, "big.pdf")
	if err := os.WriteFile(oversized, make([]byte, (4<<20)+1), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	wrongType := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(wrongType, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	for _, path := range []string{oversized, wrongType, filepath.Join(dir, "missing.pdf")} {
		if err := invokeExpectingFailure(t, srv, "expense", "add",
			"--name", "X", "--amount", "1", "--attachment", path); err == nil {
			t.Errorf("%s should have been rejected", filepath.Base(path))
		}
	}

	if n := len(steps(c)); n != 0 {
		t.Errorf("%d requests were sent; all three should have failed locally", n)
	}
}

func TestScenarioMonthEndExport(t *testing.T) {
	// Pull the overdue invoices, queue an export of them, poll, download.
	srv, c := server(t)

	invoke(t, srv, "invoice", "list", "--status", "99", "--per-page", "100")
	invoke(t, srv, "export", "create", "--invoice", "1", "--invoice", "2", "--pdf", "--merge")
	invoke(t, srv, "export", "status", "1")
	invoke(t, srv, "export", "download", "1", "-o", filepath.Join(t.TempDir(), "e.zip"))

	assertSequence(t, steps(c),
		"GET /invoices/index.json",
		"POST /exports",
		"GET /exports/getStatus/1",
		"GET /exports/download_export/1",
	)

	c.mu.Lock()
	defer c.mu.Unlock()
	if !strings.Contains(c.requests[0].Path, "status:99") {
		t.Errorf("the status filter did not reach the path: %s", c.requests[0].Path)
	}
	export := c.requests[1].Body["Export"].(map[string]any)
	if export["msel"] != true {
		t.Error("msel must be set or the API rejects the export")
	}
	ids, _ := c.requests[1].Body["Invoice"].(map[string]any)["ids"].([]any)
	if len(ids) != 2 {
		t.Errorf("invoice ids = %v, want two", ids)
	}
}

func TestScenarioRecoverAfterALostResponse(t *testing.T) {
	// The API can replay the response to a request that carried a checksum,
	// so a create lost to a timeout does not have to be guessed at.
	srv, c := server(t)

	invoke(t, srv, "invoice", "create", "--client", "7",
		"--item", "X:1:100:23", "--checksum", "order-4821")
	invoke(t, srv, "invoice", "recover", "order-4821")

	assertSequence(t, steps(c),
		"POST /invoices/create",
		"GET /api_logs/getResponseByChecksum/order-4821",
	)

	c.mu.Lock()
	defer c.mu.Unlock()
	// The checksum is a top-level key beside the models, not inside Invoice.
	if c.requests[0].Body["checksum"] != "order-4821" {
		t.Errorf("checksum = %v, want it at the top level of the payload",
			c.requests[0].Body["checksum"])
	}
}

func TestScenarioPaginationRespectsThePerResourceCap(t *testing.T) {
	// Expenses cap per_page at 100 where the other lists allow 200. Asking for
	// more is refused locally rather than spent on a request the API rejects.
	srv, c := server(t)

	if err := invokeExpectingFailure(t, srv, "expense", "list", "--per-page", "200"); err == nil {
		t.Error("--per-page 200 should be refused for expenses")
	}
	if n := len(steps(c)); n != 0 {
		t.Errorf("%d requests sent; the cap should be checked before any call", n)
	}

	invoke(t, srv, "invoice", "list", "--per-page", "200")
	if n := len(steps(c)); n != 1 {
		t.Errorf("%d requests, want 1 — invoices do allow 200", n)
	}
}

func TestScenarioCachingSavesRequestsWithoutStalingDocuments(t *testing.T) {
	// Value lists are worth caching; documents are not. An invoice list served
	// from disk could show a paid invoice as unpaid, which is a worse failure
	// than spending one of the day's 1000 requests.
	srv, c := server(t)
	config := t.TempDir()

	repeat := func(args ...string) {
		for range 3 {
			invokeWithConfig(t, srv, config, args...)
		}
	}

	repeat("values", "countries")
	cached := len(steps(c))
	if cached != 1 {
		t.Errorf("%d requests for three identical value-list calls, want 1", cached)
	}

	before := len(steps(c))
	repeat("invoice", "list")
	if live := len(steps(c)) - before; live != 3 {
		t.Errorf("%d requests for three invoice lists, want 3 — documents must not be cached", live)
	}

	before = len(steps(c))
	repeat("tag", "list")
	if n := len(steps(c)) - before; n != 1 {
		t.Errorf("%d requests for three tag lists, want 1", n)
	}

	// --no-cache has to reach the network every time.
	before = len(steps(c))
	for range 2 {
		invokeWithConfig(t, srv, config, "--no-cache", "values", "countries")
	}
	if n := len(steps(c)) - before; n != 2 {
		t.Errorf("%d requests with --no-cache, want 2", n)
	}
}

func TestScenarioPDFTokenIsCachedAcrossInvocations(t *testing.T) {
	// The token is fixed for the life of the invoice, so only the first
	// download should have to fetch the detail to find it.
	srv, c := server(t)
	config := t.TempDir()
	out := t.TempDir()

	invokeWithConfig(t, srv, config, "invoice", "pdf", "1", "-o", filepath.Join(out, "a.pdf"))
	invokeWithConfig(t, srv, config, "invoice", "pdf", "1", "-o", filepath.Join(out, "b.pdf"))

	assertSequence(t, steps(c),
		"GET /invoices/view/1.json",
		"GET /invoices/pdf/1",
		"GET /invoices/pdf/1",
	)
}

func TestScenarioCacheIsNotSharedBetweenProfiles(t *testing.T) {
	// Two accounts on one machine share a cache directory. Tags from one must
	// never surface under the other.
	srv, c := server(t)
	config := t.TempDir()

	invokeAs(t, srv, config, "one@example.com", "1", "tag", "list")
	invokeAs(t, srv, config, "two@example.com", "2", "tag", "list")

	if n := len(steps(c)); n != 2 {
		t.Errorf("%d requests, want 2 — the second account reused the first's cache", n)
	}

	// The same account again does hit the cache.
	invokeAs(t, srv, config, "one@example.com", "1", "tag", "list")
	if n := len(steps(c)); n != 2 {
		t.Errorf("%d requests, want still 2 — the repeat should have been cached", n)
	}
}
