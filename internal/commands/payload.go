package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/output"
)

// SuperFaktura write payloads are documents keyed by model name, e.g.
//
//	{"Invoice": {...}, "InvoiceItem": [...], "Client": {...}}
//
// Exposing every documented field as a flag would mean hundreds of flags, so
// each write command offers first-class flags for the common fields and
// --data for everything else. Flags are layered on top of --data, which lets a
// stored template be adjusted per invocation.

// dataFlag binds --data to a command.
func dataFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVar(target, "data", "",
		"Raw request payload: inline JSON, @file, or - for stdin")
}

// readPayload parses the --data value. An empty source yields an empty
// document so callers can unconditionally layer flags on top.
func readPayload(source string) (map[string]any, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return map[string]any{}, nil
	}

	var raw []byte
	switch {
	case source == "-":
		body, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, &output.Error{Code: output.CodeUsage, Message: fmt.Sprintf("cannot read stdin: %s", err)}
		}
		raw = body
	case strings.HasPrefix(source, "@"):
		body, err := os.ReadFile(source[1:])
		if err != nil {
			return nil, &output.Error{Code: output.CodeUsage, Message: err.Error()}
		}
		raw = body
	default:
		raw = []byte(source)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, &output.Error{
			Code:    output.CodeUsage,
			Message: fmt.Sprintf("--data is not a JSON object: %s", err),
			Hint:    `Expected something like '{"Invoice":{"name":"..."}}'`,
		}
	}
	return doc, nil
}

// put stores a field under a model, creating the model if needed. Empty
// strings and zero numbers are skipped so unset flags leave the document — and
// therefore the API's own defaults — untouched.
func put(doc map[string]any, model, field string, value any) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return
		}
	case int:
		if v == 0 {
			return
		}
	case float64:
		if v == 0 {
			return
		}
	case nil:
		return
	}

	section, ok := doc[model].(map[string]any)
	if !ok {
		section = map[string]any{}
		doc[model] = section
	}
	section[field] = value
}

// putBool stores a boolean only when it is true, so an unset flag does not
// override an account default with an explicit false.
func putBool(doc map[string]any, model, field string, value bool) { //nolint:unparam // mirrors put; only the export payload uses it today
	if value {
		put(doc, model, field, true)
	}
}

// requirePayload rejects an empty write, which the API would answer with an
// unhelpful validation error.
func requirePayload(doc map[string]any, hint string) error {
	if len(doc) == 0 {
		return &output.Error{
			Code:    output.CodeUsage,
			Message: "nothing to send",
			Hint:    hint,
		}
	}
	return nil
}

// setIfPresent stores a non-empty value directly on a map, for the payloads
// that are a bare list of records rather than the usual model-keyed document.
func setIfPresent(record map[string]any, field, value string) {
	if value != "" {
		record[field] = value
	}
}
