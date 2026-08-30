package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// This package is the CLI's contract with whatever reads it. These tests are
// less about the code than about pinning the contract: a script branching on
// exit code 3 must keep getting 3.

func TestExitCodeRubric(t *testing.T) {
	for code, want := range map[string]int{
		CodeUsage:     1,
		CodeNotFound:  2,
		CodeAuth:      3,
		CodeForbidden: 4,
		CodeRateLimit: 5,
		CodeNetwork:   6,
		CodeAPI:       7,
		CodeAmbiguous: 8,
	} {
		if got := ExitCodeFor(code); got != want {
			t.Errorf("%s = %d, want %d", code, got, want)
		}
	}
}

func TestAnUnrecognisedCodeIsTreatedAsAServerFailure(t *testing.T) {
	// Guessing "success" for something unmapped would let a broken command
	// pass a CI check.
	if got := ExitCodeFor("something-new"); got != ExitAPI {
		t.Errorf("unknown code = %d, want %d", got, ExitAPI)
	}
}

func TestAsErrorWrapsAPlainError(t *testing.T) {
	plain := errors.New("boom")
	got := AsError(plain)

	if got.Code != CodeAPI || got.Message != "boom" {
		t.Errorf("got %+v", got)
	}
	if !errors.Is(got, plain) {
		t.Error("the cause should stay reachable through errors.Is")
	}
}

func TestAsErrorPassesAStructuredErrorThrough(t *testing.T) {
	original := &Error{Code: CodeNotFound, Message: "Invoice not found"}
	if got := AsError(fmt.Errorf("wrapped: %w", original)); got != original {
		t.Errorf("got %+v, want the original", got)
	}
}

func TestTheSuccessEnvelopeIsStable(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{Format: FormatJSON, Writer: &buf})

	if err := w.OK([]map[string]any{{"Invoice": map[string]any{"id": "1"}}},
		WithSummary("Listed invoices"), WithMeta("item_count", 1)); err != nil {
		t.Fatalf("OK: %v", err)
	}

	var envelope struct {
		OK      bool           `json:"ok"`
		Data    []any          `json:"data"`
		Summary string         `json:"summary"`
		Meta    map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(buf.Bytes(), &envelope); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, buf.String())
	}
	if !envelope.OK || len(envelope.Data) != 1 {
		t.Errorf("envelope = %+v", envelope)
	}
	if envelope.Summary != "Listed invoices" || envelope.Meta["item_count"] != float64(1) {
		t.Errorf("envelope = %+v", envelope)
	}
}

func TestTheErrorEnvelopeIsStable(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{Format: FormatJSON, Writer: &buf})

	if err := w.Err(&Error{
		Code: CodeAuth, Message: "not authenticated", Hint: "run sf auth login",
	}); err != nil {
		t.Fatalf("Err: %v", err)
	}

	var envelope ErrorResponse
	if err := json.Unmarshal(buf.Bytes(), &envelope); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if envelope.OK || envelope.Code != CodeAuth || envelope.Hint == "" {
		t.Errorf("envelope = %+v", envelope)
	}
}

func TestQuietDropsTheEnvelope(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{Format: FormatQuiet, Writer: &buf})
	if err := w.OK(map[string]any{"id": "1"}); err != nil {
		t.Fatalf("OK: %v", err)
	}

	if strings.Contains(buf.String(), `"ok"`) {
		t.Errorf("quiet output should be the data alone: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"id"`) {
		t.Errorf("output = %s", buf.String())
	}
}

func TestCountReportsTheNumberOfResults(t *testing.T) {
	for _, tc := range []struct {
		data any
		want string
	}{
		{nil, "0"},
		{[]any{1, 2, 3}, "3"},
		{[]map[string]any{{}, {}}, "2"},
		{map[string]any{"id": "1"}, "1"},
	} {
		var buf bytes.Buffer
		w := New(Options{Format: FormatCount, Writer: &buf})
		if err := w.OK(tc.data); err != nil {
			t.Fatalf("OK: %v", err)
		}
		if got := strings.TrimSpace(buf.String()); got != tc.want {
			t.Errorf("count for %v = %q, want %q", tc.data, got, tc.want)
		}
	}
}

func TestAmpersandsAreNotEscapedIntoUnicodeSequences(t *testing.T) {
	// A company name is not HTML, and "&" in a piped result is noise a
	// downstream reader has to undo.
	var buf bytes.Buffer
	w := New(Options{Format: FormatJSON, Writer: &buf})
	if err := w.OK(map[string]any{"name": "Acme & Partners"}); err != nil {
		t.Fatalf("OK: %v", err)
	}

	if strings.Contains(buf.String(), `\u0026`) {
		t.Errorf("the ampersand was escaped: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "Acme & Partners") {
		t.Errorf("output = %s", buf.String())
	}
}

func TestNormalizeDataKeepsLargeIdentifiersExact(t *testing.T) {
	// Decoding into float64 would render a long identifier in scientific
	// notation, which no API would accept back.
	raw := json.RawMessage(`{"id":9007199254740993}`)
	encoded, err := json.Marshal(NormalizeData(raw))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), "9007199254740993") {
		t.Errorf("identifier was rounded: %s", encoded)
	}
}

func TestNormalizeDataAcceptsEveryShapeCommandsProduce(t *testing.T) {
	for _, data := range []any{
		nil,
		"text",
		true,
		map[string]any{"a": 1},
		[]map[string]any{{"a": 1}},
		[]any{1, "two"},
		json.RawMessage(`{"a":1}`),
		struct {
			Name string `json:"name"`
		}{Name: "x"},
	} {
		if _, err := json.Marshal(NormalizeData(data)); err != nil {
			t.Errorf("NormalizeData(%T) produced something unmarshalable: %v", data, err)
		}
	}
}

func TestAutoFormatFallsBackToJSONOffATerminal(t *testing.T) {
	// A bytes.Buffer is not a terminal, and neither is a pipe.
	w := New(Options{Format: FormatAuto, Writer: &bytes.Buffer{}})
	if got := w.EffectiveFormat(); got != FormatJSON {
		t.Errorf("format = %v, want JSON", got)
	}
}

func TestNetworkAndRateLimitErrorsCarryTheirExitCodes(t *testing.T) {
	if got := ErrNetwork(errors.New("dial tcp: refused")).ExitCode(); got != ExitNetwork {
		t.Errorf("network exit = %d, want %d", got, ExitNetwork)
	}
	limited := ErrRateLimit(30)
	if limited.ExitCode() != ExitRateLimit || !strings.Contains(limited.Hint, "30 seconds") {
		t.Errorf("rate limit = %+v", limited)
	}
	if !ErrRateLimit(0).Retryable {
		t.Error("a rate limit is retryable")
	}
}

func TestNextStepsRideTheEnvelope(t *testing.T) {
	// A write tells the user what happened and then stops, leaving them to work
	// out what the record is called and which command touches it next. The
	// command that just ran knows both.
	var buf bytes.Buffer
	w := New(Options{Format: FormatJSON, Writer: &buf})
	if err := w.OK(map[string]any{"id": "7"},
		WithSummary("Invoice created"),
		WithNext(Step{Cmd: "sf invoice view 7", Does: "see it"}),
		WithNext(Step{Cmd: "sf invoice pdf 7", Does: "download the PDF"}),
	); err != nil {
		t.Fatalf("OK: %v", err)
	}

	var got Response
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Next) != 2 {
		t.Fatalf("%d steps, want 2 — repeated WithNext appends", len(got.Next))
	}
	if got.Next[0].Cmd != "sf invoice view 7" || got.Next[0].Does != "see it" {
		t.Errorf("first step = %+v", got.Next[0])
	}

	// Absent when there is nothing to suggest: omitempty, so a read's envelope
	// is unchanged.
	buf.Reset()
	if err := w.OK(map[string]any{"id": "7"}); err != nil {
		t.Fatalf("OK: %v", err)
	}
	if bytes.Contains(buf.Bytes(), []byte(`"next"`)) {
		t.Errorf("a plain result carries an empty next key: %s", buf.String())
	}
}

// The API's own key order is the output contract: raw bytes must reach the
// consumer verbatim. A round-trip through Go maps re-sorts every object and
// floods a consumer's diffs with reshuffles that changed nothing.
func TestRawDataKeepsKeyOrder(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{Format: FormatJSON, Writer: &buf})

	raw := json.RawMessage(`{"zebra":1,"alpha":2}`)
	if err := w.OK([]json.RawMessage{raw}); err != nil {
		t.Fatalf("OK: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "zebra") || strings.Index(out, "zebra") > strings.Index(out, "alpha") {
		t.Fatalf("key order was reshuffled:\n%s", out)
	}
}
