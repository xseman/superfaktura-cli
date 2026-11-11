package commands

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/output"
)

// The replies invoice.md documents for the two Peppol endpoints, verbatim. The
// sandbox company has no Peppol, so neither has been seen on the wire: these
// tests pin what the documentation promises, not what a server was seen to do.
const (
	peppolQueued = `{
  "error": 0,
  "error_message": "",
  "data": {
    "invoice_id": 50103893,
    "operation_id": "018f4471-67f8-7c12-a3b4-123456789abc",
    "status": "queued"
  }
}`
	peppolAccepted = `{
  "error": 0,
  "error_message": "",
  "data": {
    "invoice_id": 50103893,
    "operation_id": "018f4471-67f8-7c12-a3b4-123456789abc",
    "status": "accepted",
    "outbound_document_id": 501,
    "provider_document_id": "flowis-document-1",
    "provider_status": "accepted",
    "failure_code": null,
    "created_at": "2026-10-28T09:15:00+01:00",
    "completed_at": "2026-10-28T09:15:02+01:00"
  }
}`
)

// runPeppol runs one peppol subcommand against a server answering with the
// documented replies, and returns what a person at a terminal would read.
func runPeppol(t *testing.T, args ...string) string {
	t.Helper()

	return runPeppolAs(t, output.FormatStyled, args...)
}

func runPeppolAs(t *testing.T, format output.Format, args ...string) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/invoices/sendPeppol" {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(peppolQueued))
			return
		}
		_, _ = w.Write([]byte(peppolAccepted))
	}))
	t.Cleanup(server.Close)

	var buf bytes.Buffer
	previousOut, previousWriter, previousAPI := out, outw, api
	t.Cleanup(func() { out, outw, api = previousOut, previousWriter, previousAPI })
	outw = &buf
	out = output.New(output.Options{Format: format, Writer: &buf})
	api = client.New(server.URL, client.Credentials{Email: "a@example.com", APIKey: "k"})

	cmd := invoicePeppolCmd()
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sf invoice peppol %s: %v", strings.Join(args, " "), err)
	}

	return buf.String()
}

func TestAQueuedPeppolSendShowsItsOperationAndWhereToLookNext(t *testing.T) {
	got := runPeppol(t, "send", "50103893")

	for _, want := range []string{
		"Invoice 50103893 queued for Peppol delivery",
		"Operation: 018f4471-67f8-7c12-a3b4-123456789abc", // what a webhook is matched by
		"sf invoice peppol status 50103893",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is missing:\n%s", want, got)
		}
	}
}

func TestPeppolStatusShowsTheRecordedState(t *testing.T) {
	got := runPeppol(t, "status", "50103893")

	for _, want := range []string{
		"50103893", "accepted", "018f4471-67f8-7c12-a3b4-123456789abc",
		"501", "flowis-document-1", "2026-10-28T09:15:02+01:00",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is missing:\n%s", want, got)
		}
	}
	// Once as the status, once as the provider's: the reply carries both.
	if count := strings.Count(got, "accepted"); count != 2 {
		t.Errorf("\"accepted\" appears %d times, want 2:\n%s", count, got)
	}
	// failure_code is null on a send that went through: no empty Failure row.
	if strings.Contains(got, "Failure") {
		t.Errorf("an empty failure code was given a row:\n%s", got)
	}
}

func TestAPeppolSendPrintsTheInvoiceIDForPiping(t *testing.T) {
	// The invoice id, not the operation's: it is what `peppol status` takes.
	got := runPeppolAs(t, output.FormatIDs, "send", "50103893")

	if got != "50103893\n" {
		t.Errorf("output = %q, want the invoice id alone", got)
	}
}

func TestAWebhookSecretNeedsAWebhookURLWhereverItComesFrom(t *testing.T) {
	for _, args := range [][]string{
		{"send", "1", "--webhook-secret", "s"},
		{"send", "1", "--data", `{"webhook_secret":"s"}`},
	} {
		cmd := invoicePeppolCmd()
		cmd.SetArgs(args)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true

		err := cmd.Execute()

		if output.AsError(err).Code != output.CodeUsage {
			t.Errorf("%v: err = %v, want a usage error", args, err)
		}
	}
}
