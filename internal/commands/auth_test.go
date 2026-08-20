package commands

import (
	"testing"

	"github.com/xseman/superfaktura-cli/internal/config"
)

// resetLoginFlags clears the package-level flag state these tests write to.
func resetLoginFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		flagAPIURL, flagCompany, flagModule = "", "", ""
		loginEmail, loginAPIKey = "", ""
	})
	flagAPIURL, flagCompany, flagModule = "", "", ""
	loginEmail, loginAPIKey = "", ""
}

func TestLoginReadsEveryFieldFromTheEnvironment(t *testing.T) {
	// Regression: SF_EMAIL and SF_APIKEY were honored while SF_API_URL was
	// not, so `export SF_API_URL=<sandbox>; sf auth login sandbox` stored the
	// production instance under a profile the user believed was a sandbox.
	resetLoginFlags(t)
	t.Setenv("SF_API_URL", "https://sandbox.superfaktura.sk")
	t.Setenv("SF_EMAIL", "me@example.com")
	t.Setenv("SF_APIKEY", "KEY")
	t.Setenv("SF_COMPANY_ID", "424")
	t.Setenv("SF_MODULE", "custom 1.0")

	got := loginSettings()
	want := config.Settings{
		BaseURL:   "https://sandbox.superfaktura.sk",
		Email:     "me@example.com",
		APIKey:    "KEY",
		CompanyID: "424",
		Module:    "custom 1.0",
	}
	if got != want {
		t.Errorf("settings\n got %+v\nwant %+v", got, want)
	}
}

func TestLoginFlagsBeatTheEnvironment(t *testing.T) {
	resetLoginFlags(t)
	t.Setenv("SF_API_URL", "https://moje.superfaktura.cz")
	t.Setenv("SF_EMAIL", "env@example.com")
	t.Setenv("SF_COMPANY_ID", "111")

	flagAPIURL = "https://sandbox.superfaktura.sk"
	loginEmail = "flag@example.com"
	flagCompany = "222"

	got := loginSettings()
	if got.BaseURL != "https://sandbox.superfaktura.sk" {
		t.Errorf("base URL = %q", got.BaseURL)
	}
	if got.Email != "flag@example.com" {
		t.Errorf("email = %q", got.Email)
	}
	if got.CompanyID != "222" {
		t.Errorf("company = %q", got.CompanyID)
	}
}

func TestLoginFallsBackToTheProductionInstance(t *testing.T) {
	resetLoginFlags(t)
	t.Setenv("SF_API_URL", "")

	if got := loginSettings().BaseURL; got != config.DefaultBaseURL {
		t.Errorf("base URL = %q, want %q", got, config.DefaultBaseURL)
	}
}

func TestSigningInAgainKeepsWhatWasNotGiven(t *testing.T) {
	// Correcting one field should not mean retyping four — and above all not
	// pasting the API key again to change a company id.
	dir := t.TempDir()
	t.Setenv("SF_CONFIG_DIR", dir)
	t.Setenv("SF_NO_KEYRING", "1")

	s, err := config.OpenStore()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	first := config.Settings{
		BaseURL: "https://sandbox.superfaktura.sk", Email: "a@b.c",
		APIKey: "SECRET123", CompanyID: "2204",
	}
	if err := s.Save("work", first); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, hasKey, err := s.Load("work")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !hasKey {
		t.Error("no key reported for a profile that has one")
	}
	if got.Email != "a@b.c" || got.CompanyID != "2204" || got.APIKey != "SECRET123" {
		t.Errorf("loaded %+v", got)
	}
	if got.BaseURL != first.BaseURL {
		t.Errorf("base url = %q", got.BaseURL)
	}

	// A name that was never saved reports no key rather than failing the
	// caller, which is what lets a first login still demand one.
	if _, hasKey, err := s.Load("nonesuch"); err == nil || hasKey {
		t.Errorf("an unknown profile reported hasKey=%v err=%v", hasKey, err)
	}
}
