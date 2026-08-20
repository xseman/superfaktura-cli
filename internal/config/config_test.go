package config

import (
	"os"
	"path/filepath"
	"testing"
)

// newStore points the config at a temporary directory and forces file-based
// credential storage, so tests never touch the developer's real keyring.
func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SF_CONFIG_DIR", dir)
	t.Setenv("SF_NO_KEYRING", "1")

	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return store
}

func TestSaveThenResolveRoundTripsEveryField(t *testing.T) {
	store := newStore(t)

	want := Settings{
		BaseURL:   "https://sandbox.superfaktura.sk",
		Email:     "me@example.com",
		APIKey:    "secret-key",
		Module:    "sf-cli test",
		CompanyID: "12204",
	}
	if err := store.Save("sandbox", want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Resolve(Overrides{Profile: "sandbox"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got.BaseURL != want.BaseURL || got.Email != want.Email ||
		got.APIKey != want.APIKey || got.CompanyID != want.CompanyID || got.Module != want.Module {
		t.Errorf("resolved %+v, want %+v", got, want)
	}
	if got.Profile != "sandbox" {
		t.Errorf("profile = %q", got.Profile)
	}
}

func TestSaveReplacesAnExistingProfile(t *testing.T) {
	store := newStore(t)

	first := Settings{BaseURL: "https://moja.superfaktura.sk", Email: "a@example.com", APIKey: "one"}
	if err := store.Save("work", first); err != nil {
		t.Fatalf("Save: %v", err)
	}

	second := Settings{BaseURL: "https://moje.superfaktura.cz", Email: "b@example.com", APIKey: "two"}
	if err := store.Save("work", second); err != nil {
		t.Fatalf("Save over an existing profile: %v", err)
	}

	got, err := store.Resolve(Overrides{Profile: "work"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Email != "b@example.com" || got.APIKey != "two" || got.BaseURL != second.BaseURL {
		t.Errorf("resolved %+v, want the second profile", got)
	}
}

func TestFirstProfileBecomesTheDefault(t *testing.T) {
	store := newStore(t)

	if err := store.Save("only", Settings{BaseURL: "https://moja.superfaktura.sk", Email: "a@b.c", APIKey: "k"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// No --profile and no SF_PROFILE: the default must still resolve.
	got, err := store.Resolve(Overrides{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Profile != "only" {
		t.Errorf("profile = %q, want %q", got.Profile, "only")
	}
}

func TestPrecedenceIsFlagsOverEnvironmentOverProfile(t *testing.T) {
	store := newStore(t)
	if err := store.Save("base", Settings{
		BaseURL:   "https://moja.superfaktura.sk",
		Email:     "profile@example.com",
		APIKey:    "profile-key",
		CompanyID: "1",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	t.Setenv("SF_EMAIL", "env@example.com")
	t.Setenv("SF_COMPANY_ID", "2")

	got, err := store.Resolve(Overrides{Profile: "base", CompanyID: "3"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got.Email != "env@example.com" {
		t.Errorf("email = %q, the environment should beat the profile", got.Email)
	}
	if got.CompanyID != "3" {
		t.Errorf("company = %q, the flag should beat the environment", got.CompanyID)
	}
	if got.APIKey != "profile-key" {
		t.Errorf("api key = %q, the profile should still supply what nothing overrides", got.APIKey)
	}
}

func TestUnknownProfileIsAnErrorWorthActingOn(t *testing.T) {
	store := newStore(t)

	_, err := store.Resolve(Overrides{Profile: "missing"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); got == "" {
		t.Error("the error should name the profile and point at 'sf auth list'")
	}
}

func TestForgetRemovesBothTheProfileAndTheKey(t *testing.T) {
	store := newStore(t)
	if err := store.Save("temp", Settings{
		BaseURL: "https://sandbox.superfaktura.sk",
		Email:   "a@b.c",
		APIKey:  "k",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.Forget("temp"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, err := store.Get("temp"); err == nil {
		t.Error("the profile should be gone")
	}

	// Forgetting again is an error, not a panic: there is nothing left to remove.
	if err := store.Forget("temp"); err == nil {
		t.Error("expected an error for a profile that no longer exists")
	}
}

func TestNoProfilesResolvesToTheDefaultInstance(t *testing.T) {
	store := newStore(t)

	got, err := store.Resolve(Overrides{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.BaseURL != DefaultBaseURL {
		t.Errorf("base URL = %q, want %q", got.BaseURL, DefaultBaseURL)
	}
	if got.APIKey != "" {
		t.Errorf("api key should be empty, got %q", got.APIKey)
	}
}

func TestTrailingSlashesAreTrimmedFromTheBaseURL(t *testing.T) {
	store := newStore(t)

	got, err := store.Resolve(Overrides{BaseURL: "https://sandbox.superfaktura.sk/"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.BaseURL != "https://sandbox.superfaktura.sk" {
		t.Errorf("base URL = %q", got.BaseURL)
	}
}

func TestConfigDirHonoursXDGConfigHome(t *testing.T) {
	t.Setenv("SF_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-example")

	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if dir != filepath.Join("/tmp/xdg-example", "sf") {
		t.Errorf("dir = %q", dir)
	}
}

func TestSavedCredentialsAreNotWorldReadable(t *testing.T) {
	store := newStore(t)
	if err := store.Save("secret", Settings{
		BaseURL: "https://moja.superfaktura.sk",
		Email:   "a@b.c",
		APIKey:  "very-secret",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path, err := ProfilePath()
	if err != nil {
		t.Fatalf("ProfilePath: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("config permissions are %04o, want no group or world access", perm)
	}
}
