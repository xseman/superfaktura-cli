// Package client implements an HTTP client for the SuperFaktura API.
//
// The API is CakePHP-flavored and departs from REST in three ways the rest of
// this package exists to absorb:
//
//  1. Filters travel as path segments (/invoices/index.json/page:2/type:regular),
//     not query strings.
//  2. Writes are form posts carrying a JSON document in a single "data" field.
//     The official examples are explicit: "Don't send pure JSON."
//  3. Failures arrive as 200-or-401 with {"error":1,"message":"..."} in the body,
//     and some paths answer with an HTML error page instead of JSON at all.
package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xseman/superfaktura-cli/internal/output"
)

// DefaultModule identifies this client to the API. Every request must carry a
// module name; SuperFaktura uses it to attribute traffic to an integration.
const DefaultModule = "superfaktura-cli"

// maxErrorBody caps how much of a non-JSON error page we quote back to the user.
const maxErrorBody = 200

// Credentials authenticate a single SuperFaktura company.
type Credentials struct {
	Email     string
	APIKey    string
	Module    string
	CompanyID string
}

// Header renders the SFAPI authorization header. Every value is URL-encoded:
// an email like "hello+world@example.com" must reach the server as
// "hello%2Bworld%40example.com" or the '+' is read as a space.
func (c Credentials) Header() string {
	module := c.Module
	if module == "" {
		module = DefaultModule
	}
	var b strings.Builder
	b.WriteString("SFAPI email=")
	b.WriteString(url.QueryEscape(c.Email))
	b.WriteString("&apikey=")
	b.WriteString(url.QueryEscape(c.APIKey))
	b.WriteString("&module=")
	b.WriteString(url.QueryEscape(module))
	if c.CompanyID != "" {
		b.WriteString("&company_id=")
		b.WriteString(url.QueryEscape(c.CompanyID))
	}
	return b.String()
}

// Client talks to one SuperFaktura instance as one company.
type Client struct {
	BaseURL string
	Creds   Credentials
	HTTP    *http.Client

	// OnRequest, when set, is called before each request and returns a
	// function invoked once the response is in hand. It exists so a caller can
	// show progress without this package needing to know whether it is
	// attached to a terminal.
	OnRequest func(method, path string) func()

	// DryRun stops writes from being sent, returning a *Planned describing
	// what would have gone out. Reads still happen: resolving a client name to
	// an identifier has to work for the plan to be worth reading, and a GET
	// changes nothing.
	DryRun bool

	limits RateLimit
}

// begin runs the progress hook, if one is installed, and returns its
// completion function. The returned function is always safe to call.
func (c *Client) begin(method, path string) func() {
	if c.OnRequest == nil {
		return func() {}
	}
	if done := c.OnRequest(method, path); done != nil {
		return done
	}
	return func() {}
}

// New builds a client with a sane timeout.
func New(baseURL string, creds Credentials) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Creds:   creds,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// RateLimit mirrors the X-RateLimit-* response headers. SuperFaktura enforces
// both a daily and a monthly cap, and reports remaining quota on every call.
type RateLimit struct {
	DailyLimit       int    `json:"daily_limit"`
	DailyRemaining   int    `json:"daily_remaining"`
	DailyReset       string `json:"daily_reset,omitempty"`
	MonthlyLimit     int    `json:"monthly_limit"`
	MonthlyRemaining int    `json:"monthly_remaining"`
	MonthlyReset     string `json:"monthly_reset,omitempty"`
	Message          string `json:"message,omitempty"`
	Seen             bool   `json:"-"`
}

// Limits reports the quota seen on the most recent response.
func (c *Client) Limits() RateLimit { return c.limits }

// Params are CakePHP-style path filters, emitted as sorted "key:value"
// segments so a given filter set always produces the same URL.
type Params map[string]string

// Set records a filter, ignoring empty values so callers can pass unset flags
// straight through.
func (p Params) Set(key, value string) {
	if value != "" {
		p[key] = value
	}
}

// SetInt records a numeric filter, ignoring zero.
func (p Params) SetInt(key string, value int) {
	if value != 0 {
		p[key] = strconv.Itoa(value)
	}
}

func (p Params) path() string {
	if len(p) == 0 {
		return ""
	}
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(p)) {
		b.WriteString("/")
		b.WriteString(k)
		b.WriteString(":")
		b.WriteString(escapeSegment(p[k]))
	}
	return b.String()
}

// escapeSegment escapes a filter value for a path segment, but leaves the
// comma alone.
//
// url.PathEscape turns "," into "%2C" even though RFC 3986 permits it in a
// path segment unescaped. That matters here: SuperFaktura's base64 convention
// deliberately substitutes "," for "=" *because* the comma is path-safe, so
// percent-encoding it undoes the substitution the documentation asks for and
// leaves the server to un-escape before decoding. Sending the byte the docs
// specify avoids depending on that.
func escapeSegment(value string) string {
	return strings.ReplaceAll(url.PathEscape(value), "%2C", ",")
}

// CacheKey renders the filters as a stable string for use as a cache key. It
// is the unescaped form of path(): a key never travels over the wire, so
// escaping would only make it harder to read in a debug dump.
func (p Params) CacheKey() string {
	if len(p) == 0 {
		return ""
	}
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(p)) {
		b.WriteString("/")
		b.WriteString(k)
		b.WriteString(":")
		b.WriteString(p[k])
	}
	return b.String()
}

// EncodeSearch base64-encodes a search term the way SuperFaktura expects it in
// a path segment: standard base64 with +, / and = swapped for -, _ and ','.
func EncodeSearch(term string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(term))
	return strings.NewReplacer("+", "-", "/", "_", "=", ",").Replace(enc)
}

// Planned describes a write that --dry-run held back. It is an error so that
// it travels the existing return path and cannot be mistaken for a result.
type Planned struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Body        any    `json:"body,omitempty"`
}

func (p *Planned) Error() string {
	return fmt.Sprintf("dry run: %s %s was not sent", p.Method, p.Path)
}

// plan records a withheld write, or returns nil when the request should go out.
func (c *Client) plan(method, path, contentType string, payload any) *Planned {
	if !c.DryRun {
		return nil
	}
	return &Planned{Method: method, Path: path, ContentType: contentType, Body: payload}
}

// Get issues a GET and returns the decoded response body.
func (c *Client) Get(ctx context.Context, path string, params Params) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, path+params.path(), nil)
}

// Post submits payload as the form field "data" holding a JSON document.
func (c *Client) Post(ctx context.Context, path string, payload any) (json.RawMessage, error) {
	if planned := c.plan(http.MethodPost, path, "application/x-www-form-urlencoded; charset=UTF-8", payload); planned != nil {
		return nil, planned
	}
	encoded, err := encodePayload(payload)
	if err != nil {
		return nil, err
	}
	form := url.Values{"data": {string(encoded)}}
	return c.do(ctx, http.MethodPost, path, strings.NewReader(form.Encode()))
}

// Patch submits payload with the PATCH verb and a raw JSON body, which is how
// /stock_items/edit — the only PATCH endpoint — is documented. PHP does not
// populate $_POST for PATCH, so the form encoding used elsewhere is the wrong
// bet here even before the documentation says so.
func (c *Client) Patch(ctx context.Context, path string, payload any) (json.RawMessage, error) {
	if planned := c.plan(http.MethodPatch, path, "application/json", payload); planned != nil {
		return nil, planned
	}
	encoded, err := encodePayload(payload)
	if err != nil {
		return nil, err
	}
	req, err := c.request(ctx, http.MethodPatch, path, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.send(req, path)
}

// Delete issues a DELETE, optionally carrying a payload the way Post does.
func (c *Client) Delete(ctx context.Context, path string, payload any) (json.RawMessage, error) {
	if planned := c.plan(http.MethodDelete, path, "application/x-www-form-urlencoded; charset=UTF-8", payload); planned != nil {
		return nil, planned
	}
	if payload == nil {
		return c.do(ctx, http.MethodDelete, path, nil)
	}
	encoded, err := encodePayload(payload)
	if err != nil {
		return nil, err
	}
	form := url.Values{"data": {string(encoded)}}
	req, err := c.request(ctx, http.MethodDelete, path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	return c.send(req, path)
}

// encodePayload marshals a request body without Go's default HTML escaping.
// The API is not a browser: escaping "&" to "\u0026" in every company name is
// noise at best, and the documented examples show the raw character.
func encodePayload(payload any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, &output.Error{Code: output.CodeUsage, Message: fmt.Sprintf("cannot encode request: %s", err)}
	}
	// Encode appends a newline that no endpoint wants inside a form field.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// PostJSON submits payload as a raw JSON body.
//
// Most write endpoints want the form-encoded "data" field that Post sends, but
// a few — /invoices/send among them — are documented with a bare JSON body and
// reject the form encoding.
func (c *Client) PostJSON(ctx context.Context, path string, payload any) (json.RawMessage, error) {
	if planned := c.plan(http.MethodPost, path, "application/json", payload); planned != nil {
		return nil, planned
	}
	encoded, err := encodePayload(payload)
	if err != nil {
		return nil, err
	}
	req, err := c.request(ctx, http.MethodPost, path, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.send(req, path)
}

// Download fetches a binary body (PDF, attachment) along with its content type.
// It bypasses JSON decoding entirely but still maps transport-level failures.
func (c *Client) Download(ctx context.Context, path string, params Params) ([]byte, string, error) {
	path += params.path()
	req, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, "", err
	}
	defer c.begin(http.MethodGet, path)()

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", output.ErrNetwork(err)
	}
	defer func() { _ = resp.Body.Close() }()
	c.limits = parseRateLimit(resp.Header)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", output.ErrNetwork(err)
	}
	contentType := resp.Header.Get("Content-Type")

	// An error still arrives as JSON even on a download endpoint.
	if isJSON(contentType) {
		if apiErr := decodeError(resp.StatusCode, body, c.limits); apiErr != nil {
			return nil, "", apiErr
		}
	}
	if resp.StatusCode >= 400 {
		return nil, "", nonJSONError(resp.StatusCode, contentType, path, body, c.limits)
	}
	return body, contentType, nil
}

// nonJSONError describes a response that was not the JSON we asked for. An
// HTML error page is never worth quoting back — it is hundreds of bytes of
// markup that say less than the status and path already do.
func nonJSONError(status int, contentType, path string, body []byte, limits RateLimit) error {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "unknown"
	}
	message := snippet(body)
	if mediaType == "text/html" || mediaType == "unknown" {
		message = fmt.Sprintf("server returned %s instead of JSON for %s", mediaType, path)
		if status < 400 {
			// A 200 carrying HTML means the path was not recognized as an API
			// route at all, which is a not-found in everything but the status.
			return &output.Error{
				Code:    output.CodeNotFound,
				Message: message,
				Hint:    "This endpoint may not exist on this SuperFaktura instance",
			}
		}
	}
	return httpError(status, message, limits)
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if c.BaseURL == "" {
		return nil, &output.Error{
			Code:    output.CodeUsage,
			Message: "no API URL configured",
			Hint:    "Run 'sf auth login' or pass --api-url",
		}
	}
	if c.Creds.Email == "" || c.Creds.APIKey == "" {
		return nil, &output.Error{
			Code:    output.CodeAuth,
			Message: "not authenticated",
			Hint:    "Run 'sf auth login' or set SF_EMAIL and SF_APIKEY",
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, &output.Error{Code: output.CodeUsage, Message: err.Error()}
	}
	req.Header.Set("Authorization", c.Creds.Header())
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	}
	return req, nil
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (json.RawMessage, error) {
	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	return c.send(req, path)
}

func (c *Client) send(req *http.Request, path string) (json.RawMessage, error) {
	defer c.begin(req.Method, path)()

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, output.ErrNetwork(err)
	}
	defer func() { _ = resp.Body.Close() }()
	c.limits = parseRateLimit(resp.Header)

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, output.ErrNetwork(err)
	}

	// Some paths answer with an HTML error page rather than JSON.
	if contentType := resp.Header.Get("Content-Type"); !isJSON(contentType) {
		return nil, nonJSONError(resp.StatusCode, contentType, path, raw, c.limits)
	}
	if apiErr := decodeError(resp.StatusCode, raw, c.limits); apiErr != nil {
		return nil, apiErr
	}
	if resp.StatusCode >= 400 {
		return nil, httpError(resp.StatusCode, snippet(raw), c.limits)
	}
	return json.RawMessage(raw), nil
}

// errorEnvelope is the failure shape SuperFaktura returns. The "error" field is
// a number on most endpoints but a string on a few, so it is decoded loosely.
type errorEnvelope struct {
	Error   any    `json:"error"`
	Message string `json:"message"`
	// error_message is sometimes a string and sometimes a map of field errors.
	ErrorMessage json.RawMessage `json:"error_message"`
}

// decodeError returns a mapped error when the body reports one, nil otherwise.
//
// Status codes alone are not enough to classify a failure here: the sandbox
// answers "Musíte mať platné prémiové členstvo" with 401, and a successful
// list has no "error" key at all. So the body decides, and the status only
// refines the classification.
func decodeError(status int, raw []byte, limits RateLimit) error {
	var env errorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		// A JSON array (some list endpoints) cannot carry an error envelope.
		return nil
	}
	if !isErrorFlag(env.Error) {
		return nil
	}

	fields := fieldMap(env.ErrorMessage)

	message := env.Message
	if message == "" {
		message = fieldErrors(env.ErrorMessage)
	}
	if message == "" {
		message = "request failed"
	}

	err := httpError(status, message, limits)
	// Keep the per-field detail alongside the prose: a caller fixing the
	// payload needs to know which field, not to parse a sentence.
	var apiErr *output.Error
	if errors.As(err, &apiErr) && len(fields) > 0 {
		apiErr.Fields = fields
	}
	return err
}

// isErrorFlag reports whether the "error" field signals a failure. Absent is
// nil, success is 0 or "0".
func isErrorFlag(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case float64:
		return t != 0
	case string:
		return t != "" && t != "0"
	case bool:
		return t
	default:
		return false
	}
}

// fieldErrors flattens the per-field validation map SuperFaktura returns when
// a write is rejected, e.g. {"Invoice":{"name":["Name is required"]}}.
func fieldErrors(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}

	var nested map[string]any
	if json.Unmarshal(raw, &nested) != nil {
		return ""
	}
	var parts []string
	for _, key := range slices.Sorted(maps.Keys(nested)) {
		parts = append(parts, key+": "+flatten(nested[key]))
	}
	return strings.Join(parts, "; ")
}

// fieldMap extracts the per-field validation messages the API returns for a
// rejected write, e.g. {"Invoice":{"name":["Pole je povinné"]}} becomes
// {"Invoice.name": ["Pole je povinné"]}.
func fieldMap(raw json.RawMessage) map[string][]string {
	if len(raw) == 0 {
		return nil
	}
	var nested map[string]any
	if json.Unmarshal(raw, &nested) != nil {
		return nil
	}

	fields := map[string][]string{}
	var walk func(prefix string, value any)
	walk = func(prefix string, value any) {
		switch t := value.(type) {
		case map[string]any:
			for _, key := range slices.Sorted(maps.Keys(t)) {
				next := key
				if prefix != "" {
					next = prefix + "." + key
				}
				walk(next, t[key])
			}
		case []any:
			for _, item := range t {
				fields[prefix] = append(fields[prefix], flatten(item))
			}
		default:
			fields[prefix] = append(fields[prefix], flatten(t))
		}
	}
	walk("", nested)

	if len(fields) == 0 {
		return nil
	}
	return fields
}

func flatten(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, flatten(item))
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		parts := make([]string, 0, len(t))
		for _, key := range slices.Sorted(maps.Keys(t)) {
			parts = append(parts, key+" "+flatten(t[key]))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(t)
	}
}

// httpError maps a status and message onto the shared error taxonomy, which in
// turn drives the process exit code.
func httpError(status int, message string, limits RateLimit) error {
	switch {
	case status == 401:
		return &output.Error{
			Code:       output.CodeAuth,
			Message:    message,
			Hint:       "Check credentials with 'sf auth status', or re-run 'sf auth login'",
			HTTPStatus: status,
		}
	case status == 403:
		return &output.Error{Code: output.CodeForbidden, Message: message, HTTPStatus: status}
	case status == 404:
		return &output.Error{Code: output.CodeNotFound, Message: message, HTTPStatus: status}
	case status == 429 || limits.Message != "":
		e := output.ErrRateLimit(0)
		e.Message = firstNonEmpty(limits.Message, message)
		e.Hint = fmt.Sprintf("Daily quota resets %s", limits.DailyReset)
		return e
	case status >= 500:
		return &output.Error{
			Code:       output.CodeAPI,
			Message:    message,
			HTTPStatus: status,
			Retryable:  true,
			Hint:       "The server returned a temporary error. Try again in a moment.",
		}
	default:
		return &output.Error{Code: output.CodeAPI, Message: message, HTTPStatus: status}
	}
}

func parseRateLimit(h http.Header) RateLimit {
	limits := RateLimit{
		DailyLimit:       atoi(h.Get("X-RateLimit-DailyLimit")),
		DailyRemaining:   atoi(h.Get("X-RateLimit-DailyRemaining")),
		DailyReset:       h.Get("X-RateLimit-DailyReset"),
		MonthlyLimit:     atoi(h.Get("X-RateLimit-MonthlyLimit")),
		MonthlyRemaining: atoi(h.Get("X-RateLimit-MonthlyRemaining")),
		MonthlyReset:     h.Get("X-RateLimit-MonthlyReset"),
		Message:          h.Get("X-RateLimit-Message"),
	}
	limits.Seen = limits.DailyLimit > 0 || limits.MonthlyLimit > 0 || limits.Message != ""
	return limits
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func isJSON(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func snippet(body []byte) string {
	text := strings.TrimSpace(string(bytes.ReplaceAll(body, []byte("\n"), []byte(" "))))
	if text == "" {
		return "empty response"
	}
	if len(text) > maxErrorBody {
		return text[:maxErrorBody] + "…"
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
