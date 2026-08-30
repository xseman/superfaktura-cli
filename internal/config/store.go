package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"

	"github.com/zalando/go-keyring"
)

// keyringService namespaces this CLI's entries in the system keyring.
const keyringService = "superfaktura-cli"

// Profile is one named account: an instance and the details that identify a
// caller to it. The API key is not here — it is the only secret, so it lives
// in the keyring, or in a 0600 file when there is no keyring.
type Profile struct {
	Name      string `json:"-"`
	BaseURL   string `json:"base_url"`
	Email     string `json:"email,omitempty"`
	Module    string `json:"module,omitempty"`
	CompanyID string `json:"company_id,omitempty"`
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// ValidateName rejects names that would be awkward on a command line or in a
// filename.
func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid profile name %q: use letters, digits, - and _, starting with a letter or digit", name)
	}
	return nil
}

// profiles is the on-disk shape of config.json.
type profiles struct {
	Profiles map[string]*Profile `json:"profiles,omitempty"`
	Default  string              `json:"default_profile,omitempty"`
}

func (s *Store) load() (*profiles, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &profiles{Profiles: map[string]*Profile{}}, nil
		}
		return nil, err
	}

	var file profiles
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("malformed config at %s: %w", s.path, err)
	}
	if file.Profiles == nil {
		file.Profiles = map[string]*Profile{}
	}
	for name, p := range file.Profiles {
		p.Name = name
	}
	return &file, nil
}

// save writes config.json atomically, so an interrupted write cannot leave a
// half-parsed file where the profiles used to be.
func (s *Store) save(file *profiles) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.path, data)
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()

	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}

	if err := os.Rename(name, path); err != nil {
		// Windows refuses to rename onto an existing file.
		if runtime.GOOS == "windows" {
			_ = os.Remove(path)
			if err := os.Rename(name, path); err == nil {
				return nil
			}
		}
		cleanup()
		return err
	}
	return nil
}

// List returns every profile and the name of the default one.
func (s *Store) List() (map[string]*Profile, string, error) {
	file, err := s.load()
	if err != nil {
		return nil, "", err
	}
	return file.Profiles, file.Default, nil
}

// Names returns the profile names in a stable order.
func (s *Store) Names() ([]string, error) {
	file, err := s.load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(file.Profiles))
	for name := range file.Profiles {
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

// Get returns one profile.
func (s *Store) Get(name string) (*Profile, error) {
	file, err := s.load()
	if err != nil {
		return nil, err
	}
	p, ok := file.Profiles[name]
	if !ok {
		return nil, fmt.Errorf("profile %q not found", name)
	}
	return p, nil
}

// SetDefault marks which profile applies when none is named.
func (s *Store) SetDefault(name string) error {
	file, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := file.Profiles[name]; !ok {
		return fmt.Errorf("profile %q not found", name)
	}
	file.Default = name
	return s.save(file)
}

// secretKey namespaces one profile's API key.
func secretKey(profile string) string { return "profile:" + profile }

// saveSecret stores an API key, preferring the system keyring.
//
// The fallback is a plaintext file, so it is reported rather than silent: a
// user who thinks their key is in the keychain should find out that it is not.
func (s *Store) saveSecret(profile, key string) error {
	if !s.forceFile {
		if err := keyring.Set(keyringService, secretKey(profile), key); err == nil {
			return nil
		}
	}

	secrets, err := s.loadSecrets()
	if err != nil {
		return err
	}
	secrets[secretKey(profile)] = key

	data, err := json.MarshalIndent(secrets, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.secretsPath()), 0o700); err != nil {
		return err
	}
	s.usedFile = true
	return writeAtomic(s.secretsPath(), data)
}

// Load returns a stored profile as settings, and whether an API key is stored
// alongside it.
//
// Resolve answers "what should this invocation use", layering flags and the
// environment over a profile. This answers the narrower question `auth login`
// has to ask: what is already saved under this name, so the form can open on
// it instead of empty.
func (s *Store) Load(name string) (Settings, bool, error) {
	p, err := s.Get(name)
	if err != nil {
		return Settings{}, false, err
	}
	key, hasKey := s.loadSecret(name)
	return Settings{
		Profile:   name,
		BaseURL:   p.BaseURL,
		Email:     p.Email,
		Module:    p.Module,
		CompanyID: p.CompanyID,
		APIKey:    key,
	}, hasKey, nil
}

func (s *Store) loadSecret(profile string) (string, bool) {
	if !s.forceFile {
		if value, err := keyring.Get(keyringService, secretKey(profile)); err == nil {
			return value, true
		}
	}
	secrets, err := s.loadSecrets()
	if err != nil {
		return "", false
	}
	value, ok := secrets[secretKey(profile)]
	return value, ok
}

func (s *Store) deleteSecret(profile string) {
	if !s.forceFile {
		_ = keyring.Delete(keyringService, secretKey(profile))
	}
	secrets, err := s.loadSecrets()
	if err != nil {
		return
	}
	if _, ok := secrets[secretKey(profile)]; !ok {
		return
	}
	delete(secrets, secretKey(profile))
	if data, err := json.MarshalIndent(secrets, "", "  "); err == nil {
		_ = writeAtomic(s.secretsPath(), data)
	}
}

// Key locations, as reported by KeySource.
const (
	KeyInKeyring = "keyring"
	KeyInFile    = "file"
	KeyMissing   = "none"
)

// KeySource reports where a profile's API key actually is.
//
// UsingKeyring answers this for the process as a whole and only after a write;
// a health check has to ask per profile and without saving anything, because
// "the keyring works here" and "this profile's key is in it" are different
// facts — a profile saved on a machine with no keyring leaves its key in
// plaintext for good.
func (s *Store) KeySource(profile string) string {
	if !s.forceFile {
		if _, err := keyring.Get(keyringService, secretKey(profile)); err == nil {
			return KeyInKeyring
		}
	}
	secrets, err := s.loadSecrets()
	if err != nil {
		return KeyMissing
	}
	if _, ok := secrets[secretKey(profile)]; ok {
		return KeyInFile
	}
	return KeyMissing
}

// Path is the profile file this store reads and writes.
func (s *Store) Path() string { return s.path }

// SecretsPath is where API keys land when the system keyring is unavailable.
func (s *Store) SecretsPath() string { return s.secretsPath() }

func (s *Store) secretsPath() string {
	return filepath.Join(filepath.Dir(s.path), "credentials", "credentials.json")
}

func (s *Store) loadSecrets() (map[string]string, error) {
	data, err := os.ReadFile(s.secretsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	secrets := map[string]string{}
	if err := json.Unmarshal(data, &secrets); err != nil {
		// A corrupt secret file should not make the CLI unusable; the user can
		// log in again.
		return map[string]string{}, nil
	}
	return secrets, nil
}

// UsingKeyring reports whether secrets reached the system keyring.
func (s *Store) UsingKeyring() bool { return !s.forceFile && !s.usedFile }

// FallbackWarning describes plaintext storage, or is empty when the keyring
// was used.
func (s *Store) FallbackWarning() string {
	if s.UsingKeyring() {
		return ""
	}
	return "system keyring unavailable, the API key is stored in plaintext at " + s.secretsPath()
}
