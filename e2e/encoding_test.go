//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The request body encoding is the least certain thing in the client. The
// documentation contradicts itself — intro.md says either content type works
// for any POST, the axios example says "Don't send pure JSON", and seven
// endpoints carry their own curl example using a raw JSON body — and the
// sandbox account cannot be used to settle it.
//
// These tests do not prove the server accepts what we send. They pin down what
// we send, so the choice recorded in AGENTS.md cannot drift silently and, when
// a live account is available, there is one place to correct.
//
// They need no credentials, only a local server, so they run in CI.

type capture struct {
	mu       sync.Mutex
	requests []recorded
}

type recorded struct {
	Method      string
	Path        string
	ContentType string
	Body        map[string]any
}

func (c *capture) last() (recorded, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == 0 {
		return recorded{}, false
	}
	return c.requests[len(c.requests)-1], true
}

// server answers every path with something the CLI can decode, and records
// what it was sent.
func server(t *testing.T) (*httptest.Server, *capture) {
	t.Helper()
	c := &capture{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		contentType := strings.Split(r.Header.Get("Content-Type"), ";")[0]

		var body map[string]any
		switch strings.TrimSpace(contentType) {
		case "application/x-www-form-urlencoded":
			if values, err := url.ParseQuery(string(raw)); err == nil {
				_ = json.Unmarshal([]byte(values.Get("data")), &body)
			}
		case "application/json":
			_ = json.Unmarshal(raw, &body)
		}

		c.mu.Lock()
		c.requests = append(c.requests, recorded{
			Method: r.Method, Path: r.URL.EscapedPath(),
			ContentType: strings.TrimSpace(contentType), Body: body,
		})
		c.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseFor(r.URL.EscapedPath()))
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

// responseFor answers the handful of reads the resolvers depend on, and a
// generic write acknowledgement for everything else.
func responseFor(path string) []byte {
	switch {
	// Two clients share a prefix, and one of them matches a search term
	// exactly — the case that distinguishes a deliberate choice from an
	// ambiguous one.
	case strings.HasPrefix(path, "/clients/index.json"):
		return []byte(`{"itemCount":2,"pageCount":1,"items":[
			{"Client":{"id":"7","name":"Acme s.r.o."}},
			{"Client":{"id":"8","name":"Acme s.r.o. Trading"}}]}`)
	case strings.HasPrefix(path, "/tags/index.json"):
		return []byte(`{"11":"urgent","12":"paid"}`)
	case strings.Contains(path, "/getInvoiceDetails/"):
		return []byte(`{"1":{"Invoice":{"id":"1","invoice_no_formatted":"A"}},` +
			`"2":{"Invoice":{"id":"2","invoice_no_formatted":"B"}}}`)
	default:
		// The detail response carries a token so `invoice pdf` can find one.
		return []byte(`{"error":0,"data":{"Invoice":{"id":"1"}},` +
			`"Invoice":{"id":"1","token":"tok"},"items":[],"itemCount":0,"pageCount":0}`)
	}
}

func invoke(t *testing.T, srv *httptest.Server, args ...string) {
	t.Helper()
	binary := filepath.Join(findRepoRoot(), "bin", "sf")
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("%s is missing — run 'make build' first", binary)
	}

	cmd := exec.Command(binary, append(args, "--json")...) //nolint:gosec // G204: arguments come from the test
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"SF_API_URL="+srv.URL,
		"SF_EMAIL=me@example.com",
		"SF_APIKEY=k",
		"SF_COMPANY_ID=1",
		"SF_CONFIG_DIR="+t.TempDir(),
		"SF_NO_KEYRING=1",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sf %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestWriteEndpointsUseTheDocumentedMethodAndEncoding(t *testing.T) {
	const (
		form = "application/x-www-form-urlencoded"
		raw  = "application/json"
	)

	cases := []struct {
		name        string
		args        []string
		method      string
		path        string
		contentType string
	}{
		// Form-encoded: the "data" field, which is what most of the API and
		// both official client libraries use.
		{"invoice create", []string{"invoice", "create", "--client", "1", "--item", "X:1:1:20"},
			"POST", "/invoices/create", form},
		{"invoice edit", []string{"invoice", "edit", "1", "--name", "X"},
			"POST", "/invoices/edit", form},
		{"invoice pay", []string{"invoice", "pay", "1", "--amount", "10"},
			"POST", "/invoice_payments/add/ajax:1/api:1", form},
		{"invoice post", []string{"invoice", "post", "1"},
			"POST", "/invoices/post", form},
		{"invoice related add", []string{"invoice", "related", "add", "--parent", "1", "--child", "2"},
			"POST", "/invoices/addRelatedItem", form},
		{"expense add", []string{"expense", "add", "--name", "X", "--amount", "1"},
			"POST", "/expenses/add", form},
		{"expense edit", []string{"expense", "edit", "1", "--name", "X"},
			"POST", "/expenses/edit", form},
		{"expense pay", []string{"expense", "pay", "1", "--amount", "1"},
			"POST", "/expense_payments/add", form},
		{"contact add", []string{"client", "contact", "add", "1", "--name", "X"},
			"POST", "/contact_people/add/api:1", form},
		{"tag add", []string{"tag", "add", "x"},
			"POST", "/tags/add", form},
		{"bank-account add", []string{"bank-account", "add", "--iban", "SK01"},
			"POST", "/bank_accounts/add", form},
		{"cash item add", []string{"cash-register", "item", "add", "--register", "1", "--name", "X", "--amount", "1"},
			"POST", "/cash_register_items/add", form},
		{"export create", []string{"export", "create", "--invoice", "1", "--pdf"},
			"POST", "/exports", form},
		{"sms", []string{"sms", "--invoice", "1", "--phone", "+421900"},
			"POST", "/sms/send", form},

		// Raw JSON body: these seven endpoints are documented that way.
		{"clients create", []string{"client", "create", "--name", "X"},
			"POST", "/clients/create", raw},
		{"clients edit", []string{"client", "edit", "1", "--name", "X"},
			"POST", "/clients/edit/1", raw},
		{"invoice send", []string{"invoice", "send", "1", "--to", "a@b.c"},
			"POST", "/invoices/send", raw},
		{"invoice mark_as_sent", []string{"invoice", "mark-sent", "1", "--email", "a@b.c"},
			"POST", "/invoices/mark_as_sent", raw},
		{"stock add", []string{"stock", "add", "--name", "X"},
			"POST", "/stock_items/add", raw},
		{"stock movement add", []string{"stock", "movement", "add", "--item", "1", "--quantity", "1"},
			"POST", "/stock_items/addStockMovement", raw},
		// PHP does not populate $_POST for PATCH, so form encoding would be
		// the wrong bet here even without the documentation saying so.
		{"stock edit", []string{"stock", "edit", "1", "--price", "1"},
			"PATCH", "/stock_items/edit/1", raw},

		// Verbs other than POST.
		{"stock delete", []string{"stock", "delete", "1"}, "DELETE", "/stock_items/delete/1", ""},
		{"expense item delete", []string{"expense", "item", "delete", "--id", "1"},
			"DELETE", "/expense_items/delete", form},

		// Writes the API exposes as GET, which is easy to get wrong.
		{"invoice delete", []string{"invoice", "delete", "1"}, "GET", "/invoices/delete/1", ""},
		{"invoice mark_sent", []string{"invoice", "mark-sent", "1"}, "GET", "/invoices/mark_sent/1", ""},
		{"invoice will not be paid", []string{"invoice", "will-not-be-paid", "1"},
			"GET", "/invoices/will_not_be_paid/1", ""},
		{"client delete", []string{"client", "delete", "1"}, "GET", "/clients/delete/1", ""},
		{"tag delete", []string{"tag", "delete", "1"}, "GET", "/tags/delete/1", ""},
		{"payment delete", []string{"invoice", "payment", "delete", "1"},
			"GET", "/invoice_payments/delete/1", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, c := server(t)
			invoke(t, srv, tc.args...)

			got, ok := c.last()
			if !ok {
				t.Fatal("no request was sent")
			}
			if got.Method != tc.method {
				t.Errorf("method = %s, want %s", got.Method, tc.method)
			}
			if !strings.HasPrefix(got.Path, tc.path) {
				t.Errorf("path = %s, want it to start with %s", got.Path, tc.path)
			}
			if tc.contentType != "" && got.ContentType != tc.contentType {
				t.Errorf("content type = %q, want %q", got.ContentType, tc.contentType)
			}
			if tc.contentType != "" && got.Body == nil {
				t.Errorf("the payload did not decode from a %s body", got.ContentType)
			}
		})
	}
}

func TestStockMovementsAreSentAsAList(t *testing.T) {
	// StockLog is an array of movements even when there is one. Sending an
	// object earns a 500 TypeError from the server, which does array work on
	// it — found only by running it against the real API.
	srv, c := server(t)
	invoke(t, srv, "stock", "movement", "add", "--item", "12", "--quantity", "5", "--note", "in")

	got, _ := c.last()
	movements, ok := got.Body["StockLog"].([]any)
	if !ok {
		t.Fatalf("StockLog is %T, want a list: %v", got.Body["StockLog"], got.Body)
	}
	if len(movements) != 1 {
		t.Fatalf("%d movements, want 1", len(movements))
	}
	first, _ := movements[0].(map[string]any)
	if first["stock_item_id"] != "12" || first["quantity"] != float64(5) {
		t.Errorf("movement = %v", first)
	}
}

func TestPayloadsSurviveTheWireIntact(t *testing.T) {
	// An ampersand splits a form field if the encoding is wrong, and Slovak
	// text exercises multi-byte UTF-8 through both encodings.
	const name = "Ľubomír & Škrečok s.r.o. — Žilina"

	t.Run("form encoded", func(t *testing.T) {
		srv, c := server(t)
		invoke(t, srv, "expense", "add", "--name", name, "--amount", "1")

		got, _ := c.last()
		if value := digString(got.Body, "Expense", "name"); value != name {
			t.Errorf("name = %q, want %q", value, name)
		}
	})

	t.Run("json body", func(t *testing.T) {
		srv, c := server(t)
		invoke(t, srv, "client", "create", "--name", name)

		got, _ := c.last()
		if value := digString(got.Body, "Client", "name"); value != name {
			t.Errorf("name = %q, want %q", value, name)
		}
	})
}

func TestPDFReadsTheTokenBeforeDownloading(t *testing.T) {
	// The PDF endpoint is addressed by a per-invoice token, so this is two
	// requests: fetch the detail, then download using the token it carries.
	srv, c := server(t)
	invoke(t, srv, "invoice", "pdf", "1", "--bysquare", "-o", "-")

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) != 2 {
		t.Fatalf("%d requests, want 2: %+v", len(c.requests), c.requests)
	}
	if !strings.Contains(c.requests[0].Path, "/invoices/view/1.json") {
		t.Errorf("first request = %s", c.requests[0].Path)
	}
	if !strings.Contains(c.requests[1].Path, "token:tok") {
		t.Errorf("second request = %s, expected the token from the detail", c.requests[1].Path)
	}
	if !strings.Contains(c.requests[1].Path, "bysquare:1") {
		t.Errorf("second request = %s, expected the bysquare flag", c.requests[1].Path)
	}
}

func digString(body map[string]any, model, field string) string {
	section, ok := body[model].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := section[field].(string)
	return value
}

// invokeExpectingFailure runs the binary and returns the error rather than
// failing the test, for cases where a non-zero exit is the expected outcome.
func invokeExpectingFailure(t *testing.T, srv *httptest.Server, args ...string) error {
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
	return cmd.Run()
}

// invokeWithConfig runs the binary against a shared config directory, so the
// cache written by one invocation is visible to the next.
func invokeWithConfig(t *testing.T, srv *httptest.Server, configDir string, args ...string) {
	t.Helper()
	invokeAs(t, srv, configDir, "me@example.com", "1", args...)
}

// invokeAs runs the binary as a specific account.
func invokeAs(t *testing.T, srv *httptest.Server, configDir, email, company string, args ...string) {
	t.Helper()
	binary := filepath.Join(findRepoRoot(), "bin", "sf")

	cmd := exec.Command(binary, append(args, "--json")...) //nolint:gosec // G204: arguments come from the test
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"SF_API_URL="+srv.URL,
		"SF_EMAIL="+email,
		"SF_APIKEY=k",
		"SF_COMPANY_ID="+company,
		"SF_CONFIG_DIR="+configDir,
		"SF_NO_KEYRING=1",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sf %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}
