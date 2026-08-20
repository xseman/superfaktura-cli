package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xseman/superfaktura-cli/internal/config"
	"github.com/xseman/superfaktura-cli/internal/output"
)

// doctorFixture points the package-level store at a throwaway config directory
// and clears the invocation state the sweep reads.
func doctorFixture(t *testing.T) {
	t.Helper()
	t.Setenv("SF_CONFIG_DIR", t.TempDir())
	t.Setenv("SF_NO_KEYRING", "1")
	for _, name := range []string{"SF_PROFILE", "SF_API_URL", "SF_EMAIL", "SF_APIKEY", "SF_COMPANY_ID", "SF_MODULE"} {
		t.Setenv(name, "")
	}

	previousStore, previousSettings := store, settings
	previousProfile, previousCompany, previousURL := flagProfile, flagCompany, flagAPIURL
	t.Cleanup(func() {
		store, settings = previousStore, previousSettings
		flagProfile, flagCompany, flagAPIURL = previousProfile, previousCompany, previousURL
	})
	flagProfile, flagCompany, flagAPIURL = "", "", ""
	settings = config.Settings{}

	opened, err := config.OpenStore()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	store = opened
}

func saveProfile(t *testing.T, name string, settings config.Settings) {
	t.Helper()
	if err := store.Save(name, settings); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
}

// findCheck returns a named check from a list, or fails the test.
func findCheck(t *testing.T, checks []doctorCheck, name string) doctorCheck {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in %v", name, checks)
	return doctorCheck{}
}

func findProfile(t *testing.T, result *doctorResult, name string) doctorProfile {
	t.Helper()
	for _, p := range result.Profiles {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("profile %q missing from the sweep", name)
	return doctorProfile{}
}

func sweep(t *testing.T, live bool) *doctorResult {
	t.Helper()
	result, err := doctorSweep(context.Background(), live)
	if err != nil {
		t.Fatalf("doctorSweep: %v", err)
	}
	return result
}

// TestDoctorSweepsEveryProfileNotOnlyTheActiveOne is the whole point of the
// command: `sf auth status` reports the profile this invocation resolved to,
// and the misconfiguration worth catching is in one of the others.
func TestDoctorSweepsEveryProfileNotOnlyTheActiveOne(t *testing.T) {
	doctorFixture(t)
	saveProfile(t, "work", config.Settings{
		BaseURL: config.DefaultBaseURL, Email: "a@example.com", APIKey: "K1", CompanyID: "2204",
	})
	saveProfile(t, "sandbox", config.Settings{
		BaseURL: "https://sandbox.superfaktura.sk", Email: "a@example.com", APIKey: "K2", CompanyID: "9",
	})

	result := sweep(t, false)
	if len(result.Profiles) != 2 {
		t.Fatalf("swept %d profiles, want 2", len(result.Profiles))
	}
	if findProfile(t, result, "work").CompanyID != "2204" {
		t.Error("the sweep lost the stored company id")
	}
	if !findProfile(t, result, "work").Default {
		t.Error("the first profile saved is the default and should be marked as one")
	}
}

// TestDoctorSpendsNothingUnlessAskedTo guards the constraint the whole design
// serves: a health check that quietly cost one request per profile per run
// would be spending the very allowance it exists to protect.
func TestDoctorSpendsNothingUnlessAskedTo(t *testing.T) {
	doctorFixture(t)

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-DailyLimit", "1000")
		w.Header().Set("X-RateLimit-DailyRemaining", "871")
		w.Header().Set("X-RateLimit-MonthlyLimit", "30000")
		w.Header().Set("X-RateLimit-MonthlyRemaining", "29000")
		_, _ = w.Write([]byte(`{"itemCount":0,"pageCount":1,"items":[]}`))
	}))
	defer server.Close()

	saveProfile(t, "one", config.Settings{BaseURL: server.URL, Email: "a@example.com", APIKey: "K1", CompanyID: "1"})
	saveProfile(t, "two", config.Settings{BaseURL: server.URL, Email: "b@example.com", APIKey: "K2", CompanyID: "2"})

	offline := sweep(t, false)
	if requests.Load() != 0 || offline.Requests != 0 {
		t.Fatalf("an offline sweep made %d requests and reported %d", requests.Load(), offline.Requests)
	}
	if findCheck(t, findProfile(t, offline, "one").Checks, "API").Status != doctorSkip {
		t.Error("the API check should be skipped when --live is not given")
	}

	live := sweep(t, true)
	if requests.Load() != 2 || live.Requests != 2 {
		t.Fatalf("a live sweep of two profiles made %d requests and reported %d, want 2",
			requests.Load(), live.Requests)
	}

	// The quota is what the sweep exists to put side by side, so it has to
	// survive from the response headers onto the profile.
	one := findProfile(t, live, "one")
	if one.Quota == nil || one.Quota.DailyRemaining != 871 {
		t.Errorf("quota = %+v, want 871 of 1000 remaining", one.Quota)
	}
	if findCheck(t, one.Checks, "API").Status != doctorPass {
		t.Error("credentials the server accepted should pass")
	}
}

// TestDoctorReportsARejectedKeyPerProfile: a live sweep must attribute the
// failure to the profile that owns it, not to the run as a whole.
func TestDoctorReportsARejectedKeyPerProfile(t *testing.T) {
	doctorFixture(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("Authorization"), "apikey=BAD") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":1,"message":"Invalid API key"}`))
			return
		}
		_, _ = w.Write([]byte(`{"itemCount":0,"pageCount":1,"items":[]}`))
	}))
	defer server.Close()

	saveProfile(t, "good", config.Settings{BaseURL: server.URL, Email: "a@example.com", APIKey: "OK", CompanyID: "1"})
	saveProfile(t, "bad", config.Settings{BaseURL: server.URL, Email: "b@example.com", APIKey: "BAD", CompanyID: "2"})

	result := sweep(t, true)
	if got := findProfile(t, result, "good").Status; got != doctorPass && got != doctorWarn {
		t.Errorf("the accepted profile is %q", got)
	}
	if findProfile(t, result, "bad").Status != doctorFail {
		t.Error("the rejected profile should fail")
	}
	if got := doctorExitCode(result); got != output.CodeAuth {
		t.Errorf("exit code = %q, want %q — a rejected key is an auth failure", got, output.CodeAuth)
	}
}

func TestDoctorFailsWithNoProfilesAtAll(t *testing.T) {
	doctorFixture(t)

	result := sweep(t, false)
	if len(result.Profiles) != 0 {
		t.Fatalf("swept %d profiles from an empty store", len(result.Profiles))
	}
	if findCheck(t, result.Checks, "Profiles").Status != doctorFail {
		t.Error("an empty store is a failure: nothing the CLI does can work")
	}
	if got := doctorExitCode(result); got != output.CodeAuth {
		t.Errorf("exit code = %q, want %q", got, output.CodeAuth)
	}
}

// TestDoctorChecksTheEnvironmentWhenNoProfileIsSaved covers the CI shape:
// credentials arrive in the environment and there is no profile behind them.
func TestDoctorChecksTheEnvironmentWhenNoProfileIsSaved(t *testing.T) {
	doctorFixture(t)
	t.Setenv("SF_EMAIL", "ci@example.com")
	t.Setenv("SF_APIKEY", "KEY")
	t.Setenv("SF_COMPANY_ID", "2204")
	settings = config.Settings{Email: "ci@example.com", APIKey: "KEY", CompanyID: "2204"}

	result := sweep(t, false)
	if len(result.Profiles) != 1 {
		t.Fatalf("swept %d targets, want the environment as one", len(result.Profiles))
	}
	env := findProfile(t, result, "(environment)")
	if env.CompanyID != "2204" || env.KeySource != "environment" {
		t.Errorf("environment target = %+v", env)
	}
	if findCheck(t, result.Checks, "Environment").Status != doctorWarn {
		t.Error("SF_COMPANY_ID overrides every profile's quota accounting and should be flagged")
	}
}

func TestDoctorFailsAProfileWithNoCredentials(t *testing.T) {
	doctorFixture(t)
	saveProfile(t, "ok", config.Settings{
		BaseURL: config.DefaultBaseURL, Email: "a@example.com", APIKey: "K1", CompanyID: "1",
	})

	// A profile whose secret never made it to the store — a hand-edited config,
	// or a key saved into a keyring this machine no longer has.
	writeRawProfile(t, "stale", `{"base_url":"https://moja.superfaktura.sk"}`)

	result := sweep(t, false)
	stale := findProfile(t, result, "stale")
	if stale.Status != doctorFail {
		t.Fatalf("a profile with neither email nor key is %q", stale.Status)
	}
	if findCheck(t, stale.Checks, "Email").Status != doctorFail {
		t.Error("a missing email should fail")
	}
	if findCheck(t, stale.Checks, "API key").Status != doctorFail {
		t.Error("a missing key should fail")
	}
	if got := doctorExitCode(result); got != output.CodeAuth {
		t.Errorf("exit code = %q, want %q", got, output.CodeAuth)
	}
	// The healthy profile is still reported: one bad profile must not hide the
	// rest of the sweep.
	if findProfile(t, result, "ok").Status == doctorFail {
		t.Error("the healthy profile was dragged down with the broken one")
	}
}

// writeRawProfile appends a profile to config.json as written, so tests can
// produce states Save would never create.
func writeRawProfile(t *testing.T, name, body string) {
	t.Helper()
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var file struct {
		Profiles map[string]json.RawMessage `json:"profiles"`
		Default  string                     `json:"default_profile"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if file.Profiles == nil {
		file.Profiles = map[string]json.RawMessage{}
	}
	file.Profiles[name] = json.RawMessage(body)
	encoded, err := json.Marshal(file)
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	if err := os.WriteFile(store.Path(), encoded, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestDoctorWarnsAboutAProfileWithNoCompany: the request still succeeds, which
// is exactly why this is worth saying — it lands on the account's default
// company and spends that company's day.
func TestDoctorWarnsAboutAProfileWithNoCompany(t *testing.T) {
	doctorFixture(t)
	saveProfile(t, "nocompany", config.Settings{
		BaseURL: config.DefaultBaseURL, Email: "a@example.com", APIKey: "K1",
	})

	result := sweep(t, false)
	company := findCheck(t, findProfile(t, result, "nocompany").Checks, "Company")
	if company.Status != doctorWarn {
		t.Errorf("a missing company is %q, want a warning", company.Status)
	}
	if result.Failed != 0 {
		t.Errorf("a missing company should not fail the run: %+v", result)
	}
}

func TestDoctorFailsACompanyThatIsNotAnIdentifier(t *testing.T) {
	doctorFixture(t)
	saveProfile(t, "typo", config.Settings{
		BaseURL: config.DefaultBaseURL, Email: "a@example.com", APIKey: "K1", CompanyID: "acme s.r.o.",
	})

	result := sweep(t, false)
	if findCheck(t, findProfile(t, result, "typo").Checks, "Company").Status != doctorFail {
		t.Error("company_id is numeric; anything else never reaches the right account")
	}
	if got := doctorExitCode(result); got != output.CodeUsage {
		t.Errorf("exit code = %q, want %q", got, output.CodeUsage)
	}
}

// TestDoctorWarnsWhenTwoProfilesShareOneCompany: the limit is counted per
// company, so these two look independent and are not.
func TestDoctorWarnsWhenTwoProfilesShareOneCompany(t *testing.T) {
	doctorFixture(t)
	saveProfile(t, "billing", config.Settings{
		BaseURL: config.DefaultBaseURL, Email: "a@example.com", APIKey: "K1", CompanyID: "2204",
	})
	saveProfile(t, "reports", config.Settings{
		BaseURL: config.DefaultBaseURL, Email: "b@example.com", APIKey: "K2", CompanyID: "2204",
	})

	shared := findCheck(t, sweep(t, false).Checks, "Quota sharing")
	if shared.Status != doctorWarn {
		t.Fatalf("Quota sharing = %q, want a warning", shared.Status)
	}
	if !strings.Contains(shared.Message, "2204") ||
		!strings.Contains(shared.Message, "billing") || !strings.Contains(shared.Message, "reports") {
		t.Errorf("the warning does not name both profiles and the company: %q", shared.Message)
	}
}

// TestDoctorEmitsOneDocumentAndStillExitsNonZero mirrors `sf auth status`:
// the report is printed, and the failure travels as the exit code alone.
func TestDoctorEmitsOneDocumentAndStillExitsNonZero(t *testing.T) {
	doctorFixture(t)

	var buf bytes.Buffer
	previousOut, previousWriter := out, outw
	t.Cleanup(func() { out, outw = previousOut, previousWriter })
	outw = &buf
	out = output.New(output.Options{Format: output.FormatJSON, Writer: &buf})

	result := sweep(t, false) // an empty store: one failing check
	err := reportDoctor(result)

	var already reported
	if !errors.As(err, &already) {
		t.Fatalf("reportDoctor returned %T (%v), want a reported failure", err, err)
	}
	if code := output.AsError(already.err).ExitCode(); code != output.ExitAuth {
		t.Errorf("exit code = %d, want %d", code, output.ExitAuth)
	}

	var envelope map[string]any
	decoder := json.NewDecoder(&buf)
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope["ok"] != true {
		t.Errorf("envelope = %v", envelope)
	}
	if decoder.More() {
		t.Error("a second document followed the report; one invocation emits exactly one")
	}
}
