// Package config resolves which SuperFaktura account a command talks to.
//
// A profile bundles everything the API needs to identify a caller: the country
// instance (base URL), the user (email), the company within that user's
// account (company_id), and the integration name (module). The API key is the
// only secret, so it alone lives in the credential store; the rest is plain
// config.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultBaseURL is the Slovak production instance.
const DefaultBaseURL = "https://moja.superfaktura.sk"

// Instances are the known SuperFaktura deployments, offered during setup.
var Instances = []struct {
	Label string
	URL   string
}{
	{"Slovensko", "https://moja.superfaktura.sk"},
	{"Česko", "https://moje.superfaktura.cz"},
	{"Rakúsko", "https://meine.superfaktura.at"},
	{"Sandbox SK", "https://sandbox.superfaktura.sk"},
	{"Sandbox CZ", "https://sandbox.superfaktura.cz"},
}

// Settings is the fully resolved account context for one invocation.
type Settings struct {
	Profile   string `json:"profile,omitempty"`
	BaseURL   string `json:"base_url"`
	Email     string `json:"email,omitempty"`
	APIKey    string `json:"-"`
	Module    string `json:"module,omitempty"`
	CompanyID string `json:"company_id,omitempty"`
}

// Overrides are the values a command line supplied explicitly. They win over
// both the environment and the stored profile.
type Overrides struct {
	Profile   string
	BaseURL   string
	Email     string
	APIKey    string
	CompanyID string
	Module    string
}

// Dir returns the configuration directory, honoring SF_CONFIG_DIR (used by
// tests) and then XDG_CONFIG_HOME.
func Dir() (string, error) {
	if dir := os.Getenv("SF_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "sf"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "sf"), nil
}

// ProfilePath returns the profile store location.
func ProfilePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// CacheDir returns where cached value lists are kept. Cached data is derived,
// not authored, so it belongs under XDG_STATE_HOME rather than beside config.
func CacheDir() (string, error) {
	if dir := os.Getenv("SF_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "cache"), nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "sf", "cache"), nil
}

// Store holds the named profiles and their API keys.
type Store struct {
	path string
	// forceFile skips the system keyring, for SF_NO_KEYRING and for tests that
	// must not touch the developer's real keychain.
	forceFile bool
	usedFile  bool
}

// OpenStore prepares the profile store under the configuration directory.
func OpenStore() (*Store, error) {
	path, err := ProfilePath()
	if err != nil {
		return nil, err
	}
	return &Store{path: path, forceFile: os.Getenv("SF_NO_KEYRING") != ""}, nil
}

// Save writes a profile and stores its API key, replacing any profile of the
// same name.
func (s *Store) Save(name string, settings Settings) error {
	if err := ValidateName(name); err != nil {
		return err
	}

	file, err := s.load()
	if err != nil {
		return err
	}
	file.Profiles[name] = &Profile{
		Name:      name,
		BaseURL:   settings.BaseURL,
		Email:     settings.Email,
		Module:    settings.Module,
		CompanyID: settings.CompanyID,
	}
	// The first profile becomes the default; otherwise adding one would leave
	// the CLI with credentials it never uses.
	if file.Default == "" {
		file.Default = name
	}
	if err := s.save(file); err != nil {
		return err
	}
	return s.saveSecret(name, settings.APIKey)
}

// Forget removes a profile and its stored key. A missing key is not an error:
// the point of the call is that the credential is gone afterwards.
func (s *Store) Forget(name string) error {
	file, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := file.Profiles[name]; !ok {
		return fmt.Errorf("profile %q not found", name)
	}

	delete(file.Profiles, name)
	if file.Default == name {
		file.Default = ""
	}
	s.deleteSecret(name)
	return s.save(file)
}

// Resolve produces the settings for this invocation, applying overrides over
// the environment over the selected profile over the defaults.
func (s *Store) Resolve(o Overrides) (Settings, error) {
	settings := Settings{BaseURL: DefaultBaseURL, Module: ""}

	name := firstNonEmpty(o.Profile, os.Getenv("SF_PROFILE"))
	if name == "" {
		file, err := s.load()
		if err != nil {
			return settings, err
		}
		name = file.Default
	}

	if name != "" {
		p, err := s.Get(name)
		if err != nil {
			return settings, fmt.Errorf("%w (see 'sf auth list')", err)
		}
		settings.Profile = name
		settings.BaseURL = p.BaseURL
		settings.Email = p.Email
		settings.Module = p.Module
		settings.CompanyID = p.CompanyID

		if key, ok := s.loadSecret(name); ok {
			settings.APIKey = key
		}
	}

	applyEnv(&settings)
	applyOverrides(&settings, o)
	settings.BaseURL = strings.TrimRight(settings.BaseURL, "/")
	return settings, nil
}

func applyEnv(s *Settings) {
	setIf(&s.BaseURL, os.Getenv("SF_API_URL"))
	setIf(&s.Email, os.Getenv("SF_EMAIL"))
	setIf(&s.APIKey, os.Getenv("SF_APIKEY"))
	setIf(&s.CompanyID, os.Getenv("SF_COMPANY_ID"))
	setIf(&s.Module, os.Getenv("SF_MODULE"))
}

func applyOverrides(s *Settings, o Overrides) {
	setIf(&s.BaseURL, o.BaseURL)
	setIf(&s.Email, o.Email)
	setIf(&s.APIKey, o.APIKey)
	setIf(&s.CompanyID, o.CompanyID)
	setIf(&s.Module, o.Module)
}

func setIf(target *string, value string) {
	if value != "" {
		*target = value
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
