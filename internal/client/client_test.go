package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/xseman/superfaktura-cli/internal/output"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return New(server.URL, Credentials{
		Email:     "hello+world@example.com",
		APIKey:    "secret",
		CompanyID: "42",
	})
}

func TestCredentialsHeaderURLEncodesEveryValue(t *testing.T) {
	got := Credentials{
		Email:     "hello+world@example.com",
		APIKey:    "abc123",
		Module:    "sf-cli 0.1.0",
		CompanyID: "42",
	}.Header()

	want := "SFAPI email=hello%2Bworld%40example.com&apikey=abc123&module=sf-cli+0.1.0&company_id=42"
	if got != want {
		t.Errorf("header\n got %q\nwant %q", got, want)
	}
}

func TestCredentialsHeaderOmitsEmptyCompanyAndDefaultsModule(t *testing.T) {
	got := Credentials{Email: "a@b.c", APIKey: "k"}.Header()

	if strings.Contains(got, "company_id") {
		t.Errorf("header should omit company_id when unset: %q", got)
	}
	if !strings.Contains(got, "module="+DefaultModule) {
		t.Errorf("header should default the module: %q", got)
	}
}

func TestParamsRenderAsSortedPathSegments(t *testing.T) {
	p := Params{}
	p.Set("type", "regular")
	p.SetInt("page", 2)
	p.Set("listinfo", "1")
	p.Set("skipped", "")
	p.SetInt("also_skipped", 0)

	// Sorted so the same filter set always produces the same URL.
	want := "/listinfo:1/page:2/type:regular"
	if got := p.path(); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestParamsEscapeValues(t *testing.T) {
	p := Params{"type": "regular|proforma"}
	if got := p.path(); got != "/type:regular%7Cproforma" {
		t.Errorf("path = %q, want the pipe escaped", got)
	}
}

func TestEncodeSearchReplacesBase64SpecialCharacters(t *testing.T) {
	// "??>" encodes to "Pz8+" in standard base64; the '+' must become '-'.
	if got := EncodeSearch("??>"); got != "Pz8-" {
		t.Errorf("EncodeSearch = %q, want %q", got, "Pz8-")
	}
	// "?" encodes to "Pw==" — both '=' become ','.
	if got := EncodeSearch("?"); got != "Pw,," {
		t.Errorf("EncodeSearch = %q, want %q", got, "Pw,,")
	}
}

func TestGetSendsAuthorizationAndParams(t *testing.T) {
	var gotPath, gotAuth string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	})

	if _, err := c.Get(context.Background(), "/invoices/index.json", Params{"page": "2"}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotPath != "/invoices/index.json/page:2" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "SFAPI email=") {
		t.Errorf("authorization = %q", gotAuth)
	}
}

func TestPostSendsJSONInsideTheDataFormField(t *testing.T) {
	var contentType, body string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		raw := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(raw)
		body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":0}`))
	})

	payload := map[string]any{"Client": map[string]any{"name": "test & company a.s."}}
	if _, err := c.Post(context.Background(), "/clients/create", payload); err != nil {
		t.Fatalf("Post: %v", err)
	}

	if !strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		t.Errorf("content type = %q", contentType)
	}
	values, err := url.ParseQuery(body)
	if err != nil {
		t.Fatalf("body is not form encoded: %v", err)
	}
	// The ampersand must survive form encoding rather than splitting the field.
	if got := values.Get("data"); got != `{"Client":{"name":"test & company a.s."}}` {
		t.Errorf("data = %q", got)
	}
}

func TestErrorEnvelopeOnHTTP200IsStillAnError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":1,"message":"Chyba"}`))
	})

	_, err := c.Get(context.Background(), "/invoices/index.json", nil)
	var apiErr *output.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected an *output.Error, got %v", err)
	}
	if apiErr.Message != "Chyba" {
		t.Errorf("message = %q", apiErr.Message)
	}
	if apiErr.Code != output.CodeAPI {
		t.Errorf("code = %q, want %q", apiErr.Code, output.CodeAPI)
	}
}

func TestUnauthorizedMapsToTheAuthExitCode(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":1,"message":"Musíte mať platné prémiové členstvo"}`))
	})

	_, err := c.Get(context.Background(), "/clients/index.json", nil)
	var apiErr *output.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected an *output.Error, got %v", err)
	}
	if apiErr.ExitCode() != output.ExitAuth {
		t.Errorf("exit code = %d, want %d", apiErr.ExitCode(), output.ExitAuth)
	}
}

func TestFieldValidationErrorsAreFlattened(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":1,"error_message":{"Invoice":{"name":["is required"]}}}`))
	})

	_, err := c.Post(context.Background(), "/invoices/create", map[string]any{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := output.AsError(err).Message; got != "Invoice: name is required" {
		t.Errorf("message = %q", got)
	}
}

func TestSuccessfulListIsNotMistakenForAnError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"itemCount":1,"items":[{"Invoice":{"id":"1"}}]}`))
	})

	raw, err := c.Get(context.Background(), "/invoices/index.json", nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(string(raw), `"itemCount":1`) {
		t.Errorf("body = %s", raw)
	}
}

func TestHTMLResponseIsNotQuotedBackAtTheUser(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		_, _ = w.Write([]byte("<!DOCTYPE html><html><head><title>Chyba</title></head></html>"))
	})

	_, err := c.Get(context.Background(), "/currencies", nil)
	apiErr := output.AsError(err)
	if strings.Contains(apiErr.Message, "<html") {
		t.Errorf("message should not contain markup: %q", apiErr.Message)
	}
	if apiErr.Code != output.CodeNotFound {
		t.Errorf("code = %q, want %q", apiErr.Code, output.CodeNotFound)
	}
}

func TestRateLimitHeadersAreRecorded(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("X-RateLimit-DailyLimit", "1000")
		h.Set("X-RateLimit-DailyRemaining", "876")
		h.Set("X-RateLimit-DailyReset", "02.08.2026 00:00:00")
		h.Set("X-RateLimit-MonthlyLimit", "31000")
		h.Set("X-RateLimit-MonthlyRemaining", "13995")
		_, _ = w.Write([]byte(`{"items":[]}`))
	})

	if _, err := c.Get(context.Background(), "/invoices/index.json", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}

	limits := c.Limits()
	if !limits.Seen {
		t.Fatal("limits should be marked as seen")
	}
	if limits.DailyRemaining != 876 || limits.DailyLimit != 1000 {
		t.Errorf("daily = %d/%d", limits.DailyRemaining, limits.DailyLimit)
	}
	if limits.MonthlyRemaining != 13995 {
		t.Errorf("monthly remaining = %d", limits.MonthlyRemaining)
	}
}

func TestQuotaExhaustionIsReportedAsRateLimited(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("X-RateLimit-Message", "You have exceeded a daily limit of 1000 API requests.")
		h.Set("X-RateLimit-DailyReset", "02.08.2026 00:00:00")
		_, _ = w.Write([]byte(`{"error":1,"message":"limit"}`))
	})

	_, err := c.Get(context.Background(), "/invoices/index.json", nil)
	apiErr := output.AsError(err)
	if apiErr.ExitCode() != output.ExitRateLimit {
		t.Errorf("exit code = %d, want %d", apiErr.ExitCode(), output.ExitRateLimit)
	}
	if !strings.Contains(apiErr.Message, "exceeded a daily limit") {
		t.Errorf("message = %q", apiErr.Message)
	}
}

func TestMissingCredentialsFailBeforeAnyRequest(t *testing.T) {
	reached := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer server.Close()

	c := New(server.URL, Credentials{})
	_, err := c.Get(context.Background(), "/invoices/index.json", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if reached {
		t.Error("the request should not have been sent")
	}
	if output.AsError(err).ExitCode() != output.ExitAuth {
		t.Errorf("exit code = %d, want %d", output.AsError(err).ExitCode(), output.ExitAuth)
	}
}

func TestDownloadReturnsBytesAndSurfacesJSONErrors(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/404") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":1,"message":"not found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4"))
	})

	body, contentType, err := c.Download(context.Background(), "/invoices/pdf/1", Params{"token": "abc"})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(body) != "%PDF-1.4" || contentType != "application/pdf" {
		t.Errorf("body = %q, contentType = %q", body, contentType)
	}

	if _, _, err := c.Download(context.Background(), "/invoices/pdf/404", nil); err == nil {
		t.Error("expected an error for the 404 case")
	} else if output.AsError(err).ExitCode() != output.ExitNotFound {
		t.Errorf("exit code = %d, want %d", output.AsError(err).ExitCode(), output.ExitNotFound)
	}
}

func TestSearchTermReachesThePathAsTheDocumentedBytes(t *testing.T) {
	// The base64 convention swaps '=' for ',' because a comma is path-safe.
	// url.PathEscape escapes it anyway, which would put "%2C" on the wire and
	// leave the server to un-escape before reversing the substitution.
	p := Params{"search": EncodeSearch("Acme s.r.o.")}

	got := p.path()
	if strings.Contains(got, "%2C") {
		t.Errorf("path = %q, the comma should not be percent-encoded", got)
	}
	if got != "/search:QWNtZSBzLnIuby4," {
		t.Errorf("path = %q", got)
	}
}

func TestCharactersThatWouldBreakParsingAreStillEscaped(t *testing.T) {
	// A slash would end the segment, a space is not legal in a URI, and a pipe
	// — which the API itself uses to separate multiple values — is outside the
	// RFC 3986 character set. Those must stay escaped even though the comma
	// does not.
	p := Params{"a": "x/y", "c": "regular|proforma", "d": "a b"}

	got := p.path()
	for _, want := range []string{"%2F", "%7C", "%20"} {
		if !strings.Contains(got, want) {
			t.Errorf("path = %q, expected it to contain %s", got, want)
		}
	}
}

func TestAColonInsideAValueIsLeftAlone(t *testing.T) {
	// A colon is legal in a path segment and CakePHP splits a named parameter
	// on the first one, so "b:x:y" reads back as b = "x:y". Escaping it would
	// also work; not escaping keeps the URL closer to what the docs show.
	if got := (Params{"b": "x:y"}).path(); got != "/b:x:y" {
		t.Errorf("path = %q", got)
	}
}

func TestDryRunWithholdsEveryWriteVerb(t *testing.T) {
	// A write that reaches the server has already changed somebody's
	// accounting, so --dry-run has to stop it in the client rather than
	// anywhere further out.
	reached := false
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":0}`))
	})
	c.DryRun = true

	payload := map[string]any{"Invoice": map[string]any{"name": "X"}}
	for _, tc := range []struct {
		name       string
		call       func() (json.RawMessage, error)
		wantMethod string
		wantType   string
	}{
		{"Post", func() (json.RawMessage, error) {
			return c.Post(context.Background(), "/invoices/create", payload)
		}, "POST", "application/x-www-form-urlencoded; charset=UTF-8"},
		{"PostJSON", func() (json.RawMessage, error) {
			return c.PostJSON(context.Background(), "/clients/create", payload)
		}, "POST", "application/json"},
		{"Patch", func() (json.RawMessage, error) {
			return c.Patch(context.Background(), "/stock_items/edit/1", payload)
		}, "PATCH", "application/json"},
		{"Delete", func() (json.RawMessage, error) {
			return c.Delete(context.Background(), "/stock_items/delete/1", nil)
		}, "DELETE", "application/x-www-form-urlencoded; charset=UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.call()

			var planned *Planned
			if !errors.As(err, &planned) {
				t.Fatalf("expected a *Planned, got %v", err)
			}
			if planned.Method != tc.wantMethod {
				t.Errorf("method = %s, want %s", planned.Method, tc.wantMethod)
			}
			if planned.ContentType != tc.wantType {
				t.Errorf("content type = %q, want %q", planned.ContentType, tc.wantType)
			}
		})
	}

	if reached {
		t.Error("a request reached the server during a dry run")
	}
}

func TestDryRunStillAllowsReads(t *testing.T) {
	// Resolving a client name to an identifier is a read, and the plan is
	// worthless without it. A GET changes nothing, so it goes out.
	reached := false
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	c.DryRun = true

	if _, err := c.Get(context.Background(), "/clients/index.json", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reached {
		t.Error("a read should still be sent during a dry run")
	}
}

func TestValidationErrorsKeepTheirPerFieldDetail(t *testing.T) {
	// The prose message is for a person. A caller fixing the payload needs to
	// know which field, without parsing a sentence.
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":1,"error_message":{"Invoice":{"name":["Pole je povinné"],` +
			`"due":["Neplatný dátum","Musí byť v budúcnosti"]}}}`))
	})

	_, err := c.Post(context.Background(), "/invoices/create", map[string]any{})
	apiErr := output.AsError(err)

	if len(apiErr.Fields) != 2 {
		t.Fatalf("fields = %v, want two entries", apiErr.Fields)
	}
	if got := apiErr.Fields["Invoice.name"]; len(got) != 1 || got[0] != "Pole je povinné" {
		t.Errorf("Invoice.name = %v", got)
	}
	if got := apiErr.Fields["Invoice.due"]; len(got) != 2 {
		t.Errorf("Invoice.due = %v, want both messages", got)
	}
	// The prose form still has to be there for a person reading a terminal.
	if apiErr.Message == "" {
		t.Error("the flattened message went missing")
	}
}

func TestAFlatErrorMessageProducesNoFieldMap(t *testing.T) {
	// error_message is a plain string on most failures, and inventing a field
	// map from it would be worse than having none.
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":1,"message":"Doklad neexistuje","error_message":"Doklad neexistuje"}`))
	})

	_, err := c.Get(context.Background(), "/invoices/view/1.json", nil)
	if fields := output.AsError(err).Fields; fields != nil {
		t.Errorf("fields = %v, want none", fields)
	}
}
