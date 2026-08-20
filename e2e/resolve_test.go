//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The API deals in identifiers and callers deal in names. These check the
// translation, which is the difference between "invoice Acme for 500" being one
// command and being a search the caller has to interpret.

// output runs the binary and returns stdout plus the exit code, without
// failing on a non-zero exit — several of these expect one.
func outputOf(t *testing.T, srv *httptest.Server, args ...string) (string, int) {
	t.Helper()
	binary := filepath.Join(findRepoRoot(), "bin", "sf")

	cmd := exec.Command(binary, append(args, "--json")...) //nolint:gosec // G204: arguments come from the test
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"SF_API_URL="+srv.URL,
		"SF_EMAIL=me@example.com",
		"SF_APIKEY=k",
		"SF_CONFIG_DIR="+t.TempDir(),
		"SF_NO_KEYRING=1",
	)
	out, _ := cmd.Output()
	return string(out), cmd.ProcessState.ExitCode()
}

func TestANumericClientPassesStraightThrough(t *testing.T) {
	// No lookup, so no request wasted on something the caller already knew.
	srv, c := server(t)
	invoke(t, srv, "invoice", "create", "--client", "42", "--item", "X:1:1:23")

	got, _ := c.last()
	section, _ := got.Body["Invoice"].(map[string]any)
	if section["client_id"] != "42" {
		t.Errorf("client_id = %v", section["client_id"])
	}
	if n := len(steps(c)); n != 1 {
		t.Errorf("%d requests, want 1 — a numeric id needs no lookup", n)
	}
}

func TestAnExactNameBeatsALongerMatch(t *testing.T) {
	// The server returns both "Acme s.r.o." and "Acme s.r.o. Trading". Asking
	// for the former by its full name is a deliberate choice, and must not be
	// reported as ambiguous just because the other one also matched.
	srv, c := server(t)
	invoke(t, srv, "invoice", "create", "--client", "Acme s.r.o.", "--item", "X:1:1:23")

	got, _ := c.last()
	section, _ := got.Body["Invoice"].(map[string]any)
	if section["client_id"] != "7" {
		t.Errorf("client_id = %v, want the exact match", section["client_id"])
	}
}

func TestAnAmbiguousNameFailsWithTheCandidates(t *testing.T) {
	// Guessing here would put an invoice on the wrong company's account.
	srv, _ := server(t)
	out, code := outputOf(t, srv, "invoice", "create", "--client", "Acme", "--item", "X:1:1:23")

	if code != 8 {
		t.Errorf("exit = %d, want 8 (ambiguous)", code)
	}

	var envelope struct {
		Code    string   `json:"code"`
		Matches []string `json:"matches"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("output is not JSON: %s", out)
	}
	if envelope.Code != "ambiguous" {
		t.Errorf("code = %q", envelope.Code)
	}
	if len(envelope.Matches) != 2 {
		t.Fatalf("matches = %v, want both candidates", envelope.Matches)
	}
	// Each candidate has to carry its identifier or the caller cannot act.
	for _, match := range envelope.Matches {
		if !strings.Contains(match, "(") {
			t.Errorf("candidate %q has no identifier", match)
		}
	}
}

func TestTagNamesBecomeIdentifiers(t *testing.T) {
	// The API silently ignores tag names, so sending one looks like success
	// and saves nothing. Resolving is not a convenience, it is correctness.
	srv, c := server(t)
	invoke(t, srv, "invoice", "create", "--client", "42",
		"--item", "X:1:1:23", "--tag", "urgent", "--tag", "12")

	got, _ := c.last()
	section, ok := got.Body["Tag"].(map[string]any)
	if !ok {
		t.Fatalf("no Tag section: %v", got.Body)
	}
	ids, _ := section["Tag"].([]any)
	if len(ids) != 2 || ids[0] != float64(11) || ids[1] != float64(12) {
		t.Errorf("tag ids = %v, want [11 12] as numbers", ids)
	}
}

func TestAnUnknownTagIsRefusedBeforeTheWrite(t *testing.T) {
	srv, c := server(t)
	out, code := outputOf(t, srv, "invoice", "create", "--client", "42",
		"--item", "X:1:1:23", "--tag", "nosuchtag")

	if code != 2 {
		t.Errorf("exit = %d, want 2 (not found)", code)
	}
	if !strings.Contains(out, "nosuchtag") {
		t.Errorf("the message should name the tag: %s", out)
	}
	for _, step := range steps(c) {
		if strings.HasPrefix(step, "POST") {
			t.Errorf("a write went out despite the bad tag: %s", step)
		}
	}
}

func TestDryRunShowsThePlanAndSendsNothing(t *testing.T) {
	srv, c := server(t)
	out, code := outputOf(t, srv, "--dry-run", "invoice", "create",
		"--client", "Acme s.r.o.", "--item", "X:2:50:23", "--tag", "urgent")

	if code != 0 {
		t.Errorf("exit = %d, want 0 — nothing went wrong", code)
	}

	var plan struct {
		Data struct {
			Method      string         `json:"method"`
			Path        string         `json:"path"`
			ContentType string         `json:"content_type"`
			Body        map[string]any `json:"body"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("output is not JSON: %s", out)
	}
	if plan.Data.Method != "POST" || !strings.Contains(plan.Data.Path, "/invoices/create") {
		t.Errorf("plan = %+v", plan.Data)
	}

	// The plan has to show the resolved payload, not the raw flags, or it
	// cannot be reviewed for the thing most likely to be wrong.
	invoice, _ := plan.Data.Body["Invoice"].(map[string]any)
	if invoice["client_id"] != "7" {
		t.Errorf("the plan should show the resolved client, got %v", invoice["client_id"])
	}
	if _, ok := plan.Data.Body["Tag"]; !ok {
		t.Error("the plan should show the resolved tags")
	}

	for _, step := range steps(c) {
		if !strings.HasPrefix(step, "GET") {
			t.Errorf("a non-read went out during a dry run: %s", step)
		}
	}
}

func TestSeveralInvoicesAreFetchedInOneRequest(t *testing.T) {
	// A hundred detail calls against a thousand-request daily allowance is the
	// difference this endpoint exists to make.
	srv, c := server(t)
	invoke(t, srv, "invoice", "view", "1", "2")

	requests := steps(c)
	if len(requests) != 1 {
		t.Fatalf("%d requests, want 1: %v", len(requests), requests)
	}
	if !strings.Contains(requests[0], "/invoices/getInvoiceDetails/1,2") {
		t.Errorf("request = %s", requests[0])
	}
}

func TestASingleInvoiceStillUsesTheDetailEndpoint(t *testing.T) {
	// The detail response is richer than a batch row, so one identifier should
	// not silently downgrade to the summary.
	srv, c := server(t)
	invoke(t, srv, "invoice", "view", "1")

	requests := steps(c)
	if len(requests) != 1 || !strings.Contains(requests[0], "/invoices/view/1.json") {
		t.Errorf("requests = %v", requests)
	}
}

func TestTooManyInvoicesAreRefusedBeforeTheRequest(t *testing.T) {
	srv, c := server(t)
	ids := make([]string, 101)
	for i := range ids {
		ids[i] = "1"
	}
	_, code := outputOf(t, srv, append([]string{"invoice", "view"}, ids...)...)

	if code != 1 {
		t.Errorf("exit = %d, want 1 (usage)", code)
	}
	if n := len(steps(c)); n != 0 {
		t.Errorf("%d requests sent; the cap should be checked first", n)
	}
}
