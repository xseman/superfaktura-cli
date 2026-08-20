// Package output defines what a command prints and what it exits with.
//
// The envelope and the exit-code rubric are the CLI's contract with whatever
// is reading it — a script, a pipeline, an agent — so they live here rather
// than in a third-party package. Changing anything in this file changes that
// contract.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Exit codes. A caller should branch on these rather than on message text,
// which the API localizes to the account's language.
const (
	ExitOK        = 0 // Success
	ExitUsage     = 1 // Bad arguments or flags
	ExitNotFound  = 2 // No such resource
	ExitAuth      = 3 // Not authenticated
	ExitForbidden = 4 // Authenticated but not permitted
	ExitRateLimit = 5 // Quota exhausted
	ExitNetwork   = 6 // Connection, DNS or timeout
	ExitAPI       = 7 // The server reported a failure
	ExitAmbiguous = 8 // More than one match
)

// Error codes as they appear in the JSON envelope.
const (
	CodeUsage     = "usage"
	CodeNotFound  = "not_found"
	CodeAuth      = "auth_required"
	CodeForbidden = "forbidden"
	CodeRateLimit = "rate_limit"
	CodeNetwork   = "network"
	CodeAPI       = "api_error"
	CodeAmbiguous = "ambiguous"
)

var exitCodes = map[string]int{
	CodeUsage:     ExitUsage,
	CodeNotFound:  ExitNotFound,
	CodeAuth:      ExitAuth,
	CodeForbidden: ExitForbidden,
	CodeRateLimit: ExitRateLimit,
	CodeNetwork:   ExitNetwork,
	CodeAPI:       ExitAPI,
	CodeAmbiguous: ExitAmbiguous,
}

// ExitCodeFor maps an envelope code to a process exit code. An unknown code is
// a server-side failure, which is the safest thing to assume.
func ExitCodeFor(code string) int {
	if exit, ok := exitCodes[code]; ok {
		return exit
	}
	return ExitAPI
}

// Error is a failure with enough structure to render and to exit on.
type Error struct {
	Code       string
	Message    string
	Hint       string
	HTTPStatus int
	Retryable  bool
	Cause      error

	// Fields carries per-field validation messages when the API rejects a
	// write. Message flattens the same thing into prose for a person; a caller
	// fixing the payload wants to know which field, not to parse a sentence.
	Fields map[string][]string

	// Matches lists the candidates behind an ambiguous reference, so a caller
	// that asked for a client by name can choose without searching again.
	Matches []string
}

func (e *Error) Error() string {
	if e.Hint != "" {
		return e.Message + ": " + e.Hint
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

// ExitCode is the process status this failure should produce.
func (e *Error) ExitCode() int { return ExitCodeFor(e.Code) }

// AsError converts any error into an *Error, so callers never have to test
// whether they already hold one.
func AsError(err error) *Error {
	if err == nil {
		return &Error{Code: CodeAPI, Message: "unknown error"}
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeAPI, Message: err.Error(), Cause: err}
}

// ErrNetwork reports a transport failure. The cause carries the detail, which
// is usually the only actionable part.
func ErrNetwork(cause error) *Error {
	return &Error{
		Code:      CodeNetwork,
		Message:   "Network error",
		Hint:      cause.Error(),
		Retryable: true,
		Cause:     cause,
	}
}

// ErrRateLimit reports an exhausted quota.
func ErrRateLimit(retryAfterSeconds int) *Error {
	hint := "Try again later"
	if retryAfterSeconds > 0 {
		hint = fmt.Sprintf("Try again in %d seconds", retryAfterSeconds)
	}
	return &Error{
		Code:       CodeRateLimit,
		Message:    "Rate limited",
		Hint:       hint,
		HTTPStatus: 429,
		Retryable:  true,
	}
}

// Response is the success envelope.
type Response struct {
	OK      bool           `json:"ok"`
	Data    any            `json:"data,omitempty"`
	Summary string         `json:"summary,omitempty"`
	Next    []Step         `json:"next,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
}

// Step is a command worth running after this one.
//
// A write tells the user what happened and then stops, leaving them to work out
// what the record they just made is called and which command touches it next.
// The command that just ran knows both.
//
// It is data, not prose, so a script can read it and a person can copy it.
type Step struct {
	Cmd  string `json:"cmd"`
	Does string `json:"does"`
}

// ErrorResponse is the failure envelope.
type ErrorResponse struct {
	OK      bool                `json:"ok"`
	Error   string              `json:"error"`
	Code    string              `json:"code"`
	Hint    string              `json:"hint,omitempty"`
	Fields  map[string][]string `json:"fields,omitempty"`
	Matches []string            `json:"matches,omitempty"`
}

// ResponseOption decorates a success envelope.
type ResponseOption func(*Response)

// WithSummary adds a one-line description of what happened.
func WithSummary(text string) ResponseOption {
	return func(r *Response) { r.Summary = text }
}

// WithNext suggests commands to run after this one. Repeated calls append.
func WithNext(steps ...Step) ResponseOption {
	return func(r *Response) { r.Next = append(r.Next, steps...) }
}

// WithMeta adds a metadata field, such as a total the caller needs in order to
// page correctly.
func WithMeta(key string, value any) ResponseOption {
	return func(r *Response) {
		if r.Meta == nil {
			r.Meta = map[string]any{}
		}
		r.Meta[key] = value
	}
}

// Format selects how a result is rendered.
type Format int

const (
	FormatAuto   Format = iota // Terminal: styled. Anything else: JSON.
	FormatJSON                 // The full envelope
	FormatQuiet                // The data alone
	FormatStyled               // Tables, rendered by the caller
	FormatIDs                  // One identifier per line
	FormatCount                // The number of results
)

// Options configure a Writer.
type Options struct {
	Format  Format
	Writer  io.Writer
	Verbose bool
}

// Writer renders results in the selected format.
type Writer struct{ opts Options }

// New builds a writer, defaulting to stdout.
func New(opts Options) *Writer {
	if opts.Writer == nil {
		opts.Writer = os.Stdout
	}
	return &Writer{opts: opts}
}

// EffectiveFormat resolves FormatAuto against the destination. Output that is
// not going to a terminal is going to a program, and a program wants JSON.
func (w *Writer) EffectiveFormat() Format {
	if w.opts.Format != FormatAuto {
		return w.opts.Format
	}
	if file, ok := w.opts.Writer.(*os.File); ok {
		if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return FormatStyled
		}
	}
	return FormatJSON
}

// OK writes a success result.
func (w *Writer) OK(data any, opts ...ResponseOption) error {
	response := &Response{OK: true, Data: NormalizeData(data)}
	for _, opt := range opts {
		opt(response)
	}

	switch w.EffectiveFormat() {
	case FormatQuiet:
		return w.encode(response.Data)
	case FormatCount:
		return w.writeCount(response.Data)
	case FormatIDs:
		return w.writeIDs(response.Data)
	default:
		// FormatStyled reaches here only when a command has no table to render,
		// in which case the envelope is better than nothing.
		return w.encode(response)
	}
}

// Err writes a failure result.
func (w *Writer) Err(err error) error {
	e := AsError(err)
	return w.encode(&ErrorResponse{
		OK:      false,
		Error:   e.Message,
		Code:    e.Code,
		Hint:    e.Hint,
		Fields:  e.Fields,
		Matches: e.Matches,
	})
}

func (w *Writer) encode(v any) error {
	encoder := json.NewEncoder(w.opts.Writer)
	encoder.SetIndent("", "  ")
	// The API is not a browser; escaping "&" in a company name helps nobody.
	encoder.SetEscapeHTML(false)
	return encoder.Encode(v)
}

func (w *Writer) writeCount(data any) error {
	count := 1
	switch typed := data.(type) {
	case nil:
		count = 0
	case []any:
		count = len(typed)
	case []map[string]any:
		count = len(typed)
	}
	_, err := fmt.Fprintln(w.opts.Writer, count)
	return err
}

// writeIDs is the fallback for records that do carry a top-level identifier.
// SuperFaktura nests its own under a model name, so commands override this;
// see writeIDs in the commands package.
func (w *Writer) writeIDs(data any) error {
	rows, ok := data.([]any)
	if !ok {
		if single, isMap := data.(map[string]any); isMap {
			rows = []any{single}
		} else {
			return nil
		}
	}
	for _, row := range rows {
		record, ok := row.(map[string]any)
		if !ok {
			continue
		}
		if id, present := record["id"]; present {
			if _, err := fmt.Fprintln(w.opts.Writer, strings.TrimSpace(fmt.Sprint(id))); err != nil {
				return err
			}
		}
	}
	return nil
}

// NormalizeData round-trips a value through JSON so that typed structs, raw
// messages and maps all reach the renderer as plain maps and slices. Numbers
// are preserved exactly rather than being widened to float64, which would turn
// an invoice ID into 1.042e+03.
func NormalizeData(data any) any {
	switch typed := data.(type) {
	case nil:
		return nil
	case json.RawMessage:
		return decodeAny(typed)
	case []byte:
		return decodeAny(typed)
	case map[string]any, []map[string]any, []any, string, bool:
		return data
	}

	encoded, err := json.Marshal(data)
	if err != nil {
		return data
	}
	return decodeAny(encoded)
}

func decodeAny(raw []byte) any {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return string(raw)
	}
	return value
}
