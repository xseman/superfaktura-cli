//go:build e2e

// Package e2e drives the built binary against a real SuperFaktura account.
//
// These tests are opt-in twice over: they need the e2e build tag and they need
// credentials. Put them in .env.test at the repository root (it is gitignored):
//
//	SF_TEST_BASE_URL=https://sandbox.superfaktura.sk
//	SF_TEST_EMAIL=you@example.com
//	SF_TEST_APIKEY=...
//	SF_TEST_COMPANY_ID=...
//
// Run against a sandbox. The write tests create real records.
package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type credentials struct {
	BaseURL   string
	Email     string
	APIKey    string
	CompanyID string
}

var (
	loadOnce sync.Once
	creds    credentials
	repoRoot string
)

// load reads .env.test, letting real environment variables win so CI can
// supply credentials without a file.
func load(t *testing.T) credentials {
	t.Helper()
	loadOnce.Do(func() {
		repoRoot = findRepoRoot()
		values := readEnvFile(filepath.Join(repoRoot, ".env.test"))
		get := func(key string) string {
			if v := os.Getenv(key); v != "" {
				return v
			}
			return values[key]
		}
		creds = credentials{
			BaseURL:   get("SF_TEST_BASE_URL"),
			Email:     get("SF_TEST_EMAIL"),
			APIKey:    get("SF_TEST_APIKEY"),
			CompanyID: get("SF_TEST_COMPANY_ID"),
		}
	})

	if creds.Email == "" || creds.APIKey == "" || creds.BaseURL == "" {
		t.Skip("no credentials: set SF_TEST_BASE_URL, SF_TEST_EMAIL and SF_TEST_APIKEY, or fill in .env.test")
	}
	return creds
}

func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

func readEnvFile(path string) map[string]string {
	values := map[string]string{}
	file, err := os.Open(path) //nolint:gosec // G304: a fixed, gitignored test file
	if err != nil {
		return values
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return values
}

// result is the outcome of one CLI invocation.
type result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// envelope is the JSON shape every machine-readable command emits.
type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error"`
	Code  string          `json:"code"`
	Meta  map[string]any  `json:"meta"`
}

// run invokes the built binary with credentials supplied through the
// environment, so the developer's own profiles are never touched.
func run(t *testing.T, args ...string) result {
	t.Helper()
	c := load(t)

	binary := filepath.Join(repoRoot, "bin", "sf")
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("%s is missing — run 'make build' first", binary)
	}

	cmd := exec.Command(binary, append([]string{"--json"}, args...)...) //nolint:gosec // G204: arguments come from the test itself
	cmd.Env = append(os.Environ(),
		"SF_API_URL="+c.BaseURL,
		"SF_EMAIL="+c.Email,
		"SF_APIKEY="+c.APIKey,
		"SF_COMPANY_ID="+c.CompanyID,
		"SF_CONFIG_DIR="+t.TempDir(),
		"SF_NO_KEYRING=1",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	if err != nil {
		if !asExitError(err, &exitErr) {
			t.Fatalf("running %v: %v", args, err)
		}
		res.ExitCode = exitErr.ExitCode()
	}
	return res
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

// decode parses the envelope, failing the test when the output is not JSON.
func (r result) decode(t *testing.T) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal([]byte(r.Stdout), &env); err != nil {
		t.Fatalf("output is not a JSON envelope (exit %d):\nstdout: %s\nstderr: %s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	return env
}

// requireAPIAccess skips the rest of a test when the account cannot reach the
// API at all — a blocked sandbox should read as "not run", not as a failure of
// the code under test.
func requireAPIAccess(t *testing.T) {
	t.Helper()
	res := run(t, "auth", "status")
	env := res.decode(t)

	var status struct {
		Authenticated bool   `json:"authenticated"`
		Error         string `json:"error"`
	}
	if len(env.Data) > 0 {
		_ = json.Unmarshal(env.Data, &status)
	}
	if !status.Authenticated {
		t.Skipf("the account cannot reach the API: %s", status.Error)
	}
}
