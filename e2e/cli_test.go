//go:build e2e

package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

// These tests need no API access — they exercise the CLI's own behavior.

func TestVersionReportsABuild(t *testing.T) {
	res := run(t, "version")
	if res.ExitCode != 0 {
		t.Fatalf("exit %d: %s", res.ExitCode, res.Stderr)
	}
	env := res.decode(t)
	if !env.OK {
		t.Errorf("envelope = %+v", env)
	}
}

func TestUnknownCommandExitsWithTheUsageCode(t *testing.T) {
	res := run(t, "nonexistent-command")
	if res.ExitCode != 1 {
		t.Errorf("exit = %d, want 1 (usage)", res.ExitCode)
	}
}

func TestConflictingFormatFlagsAreRejected(t *testing.T) {
	res := run(t, "--count", "--ids-only", "invoice", "list")
	if res.ExitCode != 1 {
		t.Errorf("exit = %d, want 1 (usage)", res.ExitCode)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "cannot be combined") {
		t.Errorf("output = %s%s", res.Stdout, res.Stderr)
	}
}

func TestNonNumericIDIsRejectedBeforeAnyRequest(t *testing.T) {
	res := run(t, "invoice", "view", "not-a-number")
	if res.ExitCode != 1 {
		t.Errorf("exit = %d, want 1 (usage)", res.ExitCode)
	}
}

func TestAgentHelpIsValidJSON(t *testing.T) {
	res := run(t, "--agent", "invoice", "--help")
	var info struct {
		Name        string           `json:"name"`
		Subcommands []map[string]any `json:"subcommands"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &info); err != nil {
		t.Fatalf("agent help is not JSON: %v\n%s", err, res.Stdout)
	}
	if info.Name != "sf invoice" || len(info.Subcommands) == 0 {
		t.Errorf("info = %+v", info)
	}
}

func TestStaticValueListsWorkOffline(t *testing.T) {
	res := run(t, "values", "payment-types")
	if res.ExitCode != 0 {
		t.Fatalf("exit %d: %s", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "transfer") {
		t.Errorf("output = %s", res.Stdout)
	}
}

// From here on the tests talk to the API.

func TestAuthStatusReportsTheResolvedAccount(t *testing.T) {
	res := run(t, "auth", "status")
	env := res.decode(t)

	var status struct {
		BaseURL string `json:"base_url"`
		Email   string `json:"email"`
	}
	if err := json.Unmarshal(env.Data, &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Email != load(t).Email {
		t.Errorf("email = %q, want %q", status.Email, load(t).Email)
	}
}

func TestLimitsReportsTheQuota(t *testing.T) {
	requireAPIAccess(t)

	res := run(t, "limits")
	if res.ExitCode != 0 {
		t.Fatalf("exit %d: %s", res.ExitCode, res.Stderr)
	}
	var limits struct {
		DailyLimit int `json:"daily_limit"`
	}
	if err := json.Unmarshal(res.decode(t).Data, &limits); err != nil {
		t.Fatalf("decode limits: %v", err)
	}
	if limits.DailyLimit == 0 {
		t.Error("the API should report a daily limit")
	}
}

func TestInvoiceListReturnsAnEnvelopeWithCounts(t *testing.T) {
	requireAPIAccess(t)

	res := run(t, "invoice", "list", "--per-page", "2")
	if res.ExitCode != 0 {
		t.Fatalf("exit %d: %s", res.ExitCode, res.Stderr)
	}
	env := res.decode(t)
	if !env.OK {
		t.Fatalf("envelope = %+v", env)
	}
	if _, ok := env.Meta["item_count"]; !ok {
		t.Errorf("meta should carry item_count, got %v", env.Meta)
	}

	var items []map[string]any
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("data is not a list: %v", err)
	}
	if len(items) > 2 {
		t.Errorf("--per-page 2 returned %d items", len(items))
	}
}

func TestClientLifecycle(t *testing.T) {
	requireAPIAccess(t)

	created := run(t, "client", "create", "--name", "sf-cli e2e", "--city", "Bratislava")
	if created.ExitCode != 0 {
		t.Fatalf("create: exit %d: %s%s", created.ExitCode, created.Stdout, created.Stderr)
	}

	id := extractID(t, created.decode(t).Data, "Client")
	if id == "" {
		t.Fatalf("no client ID in the response: %s", created.Stdout)
	}
	t.Cleanup(func() {
		if res := run(t, "client", "delete", id); res.ExitCode != 0 {
			t.Logf("cleanup of client %s failed: %s%s", id, res.Stdout, res.Stderr)
		}
	})

	viewed := run(t, "client", "view", id)
	if viewed.ExitCode != 0 {
		t.Fatalf("view: exit %d: %s%s", viewed.ExitCode, viewed.Stdout, viewed.Stderr)
	}
	if !strings.Contains(viewed.Stdout, "sf-cli e2e") {
		t.Errorf("view did not return the created client: %s", viewed.Stdout)
	}

	edited := run(t, "client", "edit", id, "--comment", "edited by e2e")
	if edited.ExitCode != 0 {
		t.Errorf("edit: exit %d: %s%s", edited.ExitCode, edited.Stdout, edited.Stderr)
	}
}

func TestMissingInvoiceIsANotFound(t *testing.T) {
	requireAPIAccess(t)

	res := run(t, "invoice", "view", "999999999")
	if res.ExitCode == 0 {
		t.Fatal("expected a failure for a nonexistent invoice")
	}
	// 2 is not found, 7 is a generic API error — the API is not consistent
	// about which one it returns, but it must not be a success.
	if res.ExitCode != 2 && res.ExitCode != 7 {
		t.Errorf("exit = %d, want 2 (not found) or 7 (api error)", res.ExitCode)
	}
}

// extractID digs the new record's ID out of a write response, which nests it
// under either "data" or the model name depending on the endpoint.
func extractID(t *testing.T, data json.RawMessage, model string) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		return ""
	}

	for _, path := range [][]string{
		{"data", model, "id"},
		{model, "id"},
		{"data", "id"},
	} {
		if id := dig(body, path); id != "" {
			return id
		}
	}
	return ""
}

func dig(obj map[string]any, path []string) string {
	var current any = obj
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current, ok = m[key]
		if !ok {
			return ""
		}
	}
	switch v := current.(type) {
	case string:
		return v
	case float64:
		return strings.TrimSuffix(strings.TrimRight(json.Number(jsonNumber(v)).String(), "0"), ".")
	default:
		return ""
	}
}

func jsonNumber(v float64) string {
	encoded, _ := json.Marshal(v)
	return string(encoded)
}
