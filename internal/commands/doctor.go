package commands

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/config"
	"github.com/xseman/superfaktura-cli/internal/output"
	"github.com/xseman/superfaktura-cli/internal/render"
)

// `sf doctor` inspects every saved profile at once, which is the part
// `sf auth status` cannot do: status reports the one account this invocation
// resolved to, and the mistake worth catching is in the profile you are not
// looking at.
//
// The quota is counted per company_id, not per API key (API-DISCREPANCIES B2).
// So a company id that is wrong, missing, or shared between two profiles does
// not fail — it quietly spends an allowance somebody else is relying on. Seeing
// every profile's instance, company and remaining quota on adjacent lines is
// the only way that shows up before the day it runs out.
//
// **Nothing here talks to the API unless --live is given.** A sweep that spent
// one request per profile on every run would be committing the exact fault it
// exists to find: charging a 1000-a-day allowance for a health check, and — for
// a profile whose company id is wrong — charging it to a company that never
// asked. Everything that can be decided from the config alone is, and --live
// says in its own help what it costs.

// Check statuses.
const (
	doctorPass = "pass"
	doctorWarn = "warn"
	doctorFail = "fail"
	doctorSkip = "skip"
)

// doctorEnvTarget names the target a sweep falls back to when the environment
// carries a whole account and no profile is saved behind it, which is how a CI
// job is configured. doctorKeyInEnv is the matching key location, alongside
// config.KeyInKeyring and config.KeyInFile.
const (
	doctorEnvTarget = "(environment)"
	doctorKeyInEnv  = "environment"
)

// doctorMarks are the leading glyphs of the human report, deliberately narrow
// so the check names stay aligned.
var doctorMarks = map[string]string{
	doctorPass: "✓",
	doctorWarn: "!",
	doctorFail: "✗",
	doctorSkip: "○",
}

// doctorCheck is one diagnosis.
type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`

	// code classifies a failure for the exit status. It stays out of the
	// envelope: one invocation reports one code, chosen from the failures.
	code string
}

func checkPass(name, message string) doctorCheck {
	return doctorCheck{Name: name, Status: doctorPass, Message: message}
}

func checkWarn(name, message, hint string) doctorCheck {
	return doctorCheck{Name: name, Status: doctorWarn, Message: message, Hint: hint}
}

func checkFail(name, code, message, hint string) doctorCheck {
	return doctorCheck{Name: name, Status: doctorFail, Message: message, Hint: hint, code: code}
}

func checkSkip(name, message string) doctorCheck {
	return doctorCheck{Name: name, Status: doctorSkip, Message: message}
}

// doctorProfile is one profile's effective configuration and what it produced.
// The values are the resolved ones — environment and flags layered over the
// stored profile, exactly what `sf --profile <name> …` would send.
type doctorProfile struct {
	Name      string            `json:"profile"`
	Default   bool              `json:"default"`
	BaseURL   string            `json:"base_url,omitempty"`
	Email     string            `json:"email,omitempty"`
	CompanyID string            `json:"company_id,omitempty"`
	Module    string            `json:"module,omitempty"`
	KeySource string            `json:"key_source"`
	Quota     *client.RateLimit `json:"quota,omitempty"`
	Checks    []doctorCheck     `json:"checks"`
	Status    string            `json:"status"`
}

// doctorResult is the whole sweep.
type doctorResult struct {
	Live     bool            `json:"live"`
	Requests int             `json:"requests_spent"`
	Checks   []doctorCheck   `json:"checks"`
	Profiles []doctorProfile `json:"profiles"`
	Passed   int             `json:"passed"`
	Warned   int             `json:"warned"`
	Failed   int             `json:"failed"`
	Skipped  int             `json:"skipped"`
}

func init() { rootCmd.AddCommand(doctorCmd()) }

func doctorCmd() *cobra.Command {
	var live bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check every saved profile for problems",
		Long: `Sweeps every saved profile and reports its effective instance, company,
credential storage and — with --live — whether the API still accepts it.

'sf auth status' reports the profile this invocation resolved to. This reports
all of them, because the rate limit is counted per company_id rather than per
API key: a company id that is wrong, missing or shared between two profiles
never fails, it spends another company's daily allowance.

Nothing is sent to the API unless --live is given, so the check itself cannot
be what exhausts the quota.`,
		Args: cobra.NoArgs,
		Example: `  sf doctor
  sf doctor --live
  sf doctor --profile sandbox --live --verbose`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := doctorSweep(ctx(cmd), live)
			if err != nil {
				return err
			}
			return reportDoctor(result)
		},
	}

	cmd.Flags().BoolVar(&live, "live", false,
		"Verify each profile against the API. Costs one request per profile, "+
			"charged to that profile's company.")
	return cmd
}

// reportDoctor emits the one document this invocation produces, then carries
// the exit code separately — the same shape `sf auth status` uses, so
// `sf doctor || sf auth login` works without a second envelope on stdout.
func reportDoctor(result *doctorResult) error {
	summary := doctorSummary(result)
	if err := emit(result, func(w io.Writer) { renderDoctor(w, result) },
		output.WithSummary(summary)); err != nil {
		return err
	}
	if result.Failed == 0 {
		return nil
	}
	return reported{&output.Error{Code: doctorExitCode(result), Message: summary}}
}

// doctorSweep runs every check. It reaches the network only when live is set,
// and then exactly once per profile that has credentials to try.
func doctorSweep(c context.Context, live bool) (*doctorResult, error) {
	// Profiles is never nil: a consumer iterating the envelope should find an
	// empty list when there is nothing to check, not a null.
	result := &doctorResult{Live: live, Profiles: []doctorProfile{}}

	all, err := store.Names()
	if err != nil {
		return nil, &output.Error{
			Code:    output.CodeUsage,
			Message: err.Error(),
			Hint:    "The profile store is unreadable; see " + store.Path(),
		}
	}

	// --profile and SF_PROFILE narrow the sweep to one account. An unknown name
	// never reaches here: resolving it failed before the command ran. The store
	// check still describes everything that is saved, not the narrowed view.
	names := all
	narrowed := false
	if only := firstNonEmpty(flagProfile, os.Getenv("SF_PROFILE")); only != "" && slices.Contains(all, only) {
		names, narrowed = []string{only}, len(all) > 1
	}

	result.Checks = append(result.Checks, checkStore(all), checkEnvironment())

	_, defaultName, _ := store.List()
	if len(names) == 0 {
		result.Checks = append(result.Checks, checkNoProfiles())
		// Credentials in the environment are a complete account with no profile
		// behind it, which is how a CI job is configured. Sweep that instead of
		// reporting nothing.
		if settings.Email != "" || settings.APIKey != "" {
			result.Profiles = append(result.Profiles, doctorTarget(c, doctorEnvTarget, false, live, result))
		}
	} else {
		result.Checks = append(result.Checks, checkDefaultProfile(names, defaultName))
		for _, name := range names {
			result.Profiles = append(result.Profiles, doctorTarget(c, name, name == defaultName, live, result))
		}
		// Two profiles sharing a company is a fact about the pair, so a sweep
		// narrowed to one of them cannot see it and says so rather than
		// reporting an all-clear it did not establish.
		if narrowed {
			result.Checks = append(result.Checks,
				checkSkip("Quota sharing", "not checked (this sweep covers one profile)"))
		} else {
			result.Checks = append(result.Checks, checkSharedCompanies(result.Profiles))
		}
	}

	tally(result)
	return result, nil
}

// doctorTarget resolves one profile the way an invocation naming it would, then
// checks what came out.
func doctorTarget(c context.Context, name string, isDefault, live bool, result *doctorResult) doctorProfile {
	overrides := config.Overrides{
		BaseURL:   flagAPIURL,
		CompanyID: flagCompany,
		Module:    flagModule,
	}
	if name != doctorEnvTarget {
		overrides.Profile = name
	}

	eff, err := store.Resolve(overrides)
	if err != nil {
		return doctorProfile{
			Name:   name,
			Status: doctorFail,
			Checks: []doctorCheck{
				checkFail("Profile", output.CodeUsage, err.Error(), "See 'sf auth list'"),
			},
		}
	}

	p := doctorProfile{
		Name:      name,
		Default:   isDefault,
		BaseURL:   eff.BaseURL,
		Email:     eff.Email,
		CompanyID: eff.CompanyID,
		Module:    firstNonEmpty(eff.Module, client.DefaultModule),
		KeySource: keySourceFor(name, eff),
	}

	p.Checks = append(p.Checks,
		checkInstance(eff.BaseURL),
		checkEmail(eff.Email),
		checkKey(name, p.KeySource),
		checkCompany(eff.CompanyID),
	)

	p.Checks = append(p.Checks, checkLive(c, eff, live, &p, result)...)
	p.Status = worst(p.Checks)
	return p
}

// keySourceFor says where the key a request would actually carry comes from.
// SF_APIKEY is checked first because it overrides a stored one: reporting the
// keyring while the environment is what gets sent would point a debugging
// session at the wrong secret.
func keySourceFor(name string, eff config.Settings) string {
	if os.Getenv("SF_APIKEY") != "" && eff.APIKey != "" {
		return doctorKeyInEnv
	}
	if name != doctorEnvTarget {
		if source := store.KeySource(name); source != config.KeyMissing {
			return source
		}
	}
	return config.KeyMissing
}

func checkStore(names []string) doctorCheck {
	path := store.Path()
	if len(names) == 0 {
		return checkPass("Store", path)
	}

	// The key file is plaintext by design when there is no keyring, so it has
	// to stay unreadable to everyone else; the profile file carries an email
	// and a company id, which are not secrets but are nobody's business.
	var loose []string
	for _, file := range []string{path, store.SecretsPath()} {
		if info, err := os.Stat(file); err == nil && info.Mode().Perm()&0o077 != 0 {
			loose = append(loose, fmt.Sprintf("%s is %04o", file, info.Mode().Perm()))
		}
	}
	if len(loose) > 0 {
		return checkWarn("Store", strings.Join(loose, ", "), "Run: chmod 600 on the files above")
	}

	message := fmt.Sprintf("%s (%d %s)", path, len(names), plural(len(names), "profile", "profiles"))
	// Naming the fallback file once here saves repeating the path under every
	// profile whose key is in it.
	if _, err := os.Stat(store.SecretsPath()); err == nil {
		message += ", keys in " + store.SecretsPath()
	}
	return checkPass("Store", message)
}

// checkEnvironment reports the variables that override every profile at once.
// SF_COMPANY_ID is the dangerous one: it redirects the quota of every profile
// in the sweep, so a stale export in a shell is invisible until a day's
// allowance has gone somewhere unexpected.
func checkEnvironment() doctorCheck {
	var set []string
	for _, name := range []string{"SF_PROFILE", "SF_API_URL", "SF_EMAIL", "SF_APIKEY", "SF_COMPANY_ID", "SF_MODULE"} {
		if value := os.Getenv(name); value != "" {
			if name == "SF_APIKEY" {
				value = "set"
			}
			set = append(set, name+"="+value)
		}
	}
	if len(set) == 0 {
		return checkPass("Environment", "nothing overriding the profiles")
	}
	joined := strings.Join(set, ", ")
	if os.Getenv("SF_COMPANY_ID") != "" {
		return checkWarn("Environment", joined,
			"SF_COMPANY_ID applies to every profile — the requests below are charged to that company")
	}
	return checkWarn("Environment", joined, "These override the stored profiles")
}

func checkNoProfiles() doctorCheck {
	if os.Getenv("SF_EMAIL") != "" && os.Getenv("SF_APIKEY") != "" {
		return checkWarn("Profiles", "none saved, using the environment",
			"Fine for CI. For a workstation run: sf auth login")
	}
	return checkFail("Profiles", output.CodeAuth, "no profiles configured",
		"Run: sf auth login")
}

// checkDefaultProfile catches the state `sf auth logout` leaves behind: profiles
// exist, but none is default, so every command runs with no credentials at all
// unless it names one.
func checkDefaultProfile(names []string, defaultName string) doctorCheck {
	if defaultName != "" {
		return checkPass("Default profile", defaultName)
	}
	if os.Getenv("SF_PROFILE") != "" {
		return checkWarn("Default profile", "none set, SF_PROFILE="+os.Getenv("SF_PROFILE")+" selects one",
			"Run: sf auth switch "+names[0])
	}
	if os.Getenv("SF_EMAIL") != "" && os.Getenv("SF_APIKEY") != "" {
		return checkWarn("Default profile", "none set, the environment supplies the credentials",
			"Run: sf auth switch "+names[0])
	}
	return checkFail("Default profile", output.CodeAuth,
		"none set — commands run without credentials unless they pass --profile",
		"Run: sf auth switch "+names[0])
}

// checkSharedCompanies reports profiles pointing at one company on one
// instance. They look independent and are not: they draw down a single
// 1000-request day between them.
func checkSharedCompanies(profiles []doctorProfile) doctorCheck {
	shared := map[string][]string{}
	for _, p := range profiles {
		if p.CompanyID == "" {
			continue
		}
		key := p.BaseURL + "#" + p.CompanyID
		shared[key] = append(shared[key], p.Name)
	}

	var collisions []string
	for _, key := range slices.Sorted(maps.Keys(shared)) {
		if len(shared[key]) > 1 {
			_, company, _ := strings.Cut(key, "#")
			collisions = append(collisions,
				fmt.Sprintf("%s share company %s", strings.Join(shared[key], " and "), company))
		}
	}
	if len(collisions) == 0 {
		return checkPass("Quota sharing", "every profile has its own company")
	}
	return checkWarn("Quota sharing", strings.Join(collisions, "; "),
		"The daily limit is counted per company, so these profiles spend one allowance between them")
}

func checkInstance(baseURL string) doctorCheck {
	if baseURL == "" {
		return checkFail("Instance", output.CodeUsage, "no base URL",
			"Run: sf auth login, or pass --api-url")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.Scheme == "" {
		return checkFail("Instance", output.CodeUsage, baseURL+" is not a URL",
			"Expected something like https://moja.superfaktura.sk")
	}
	if parsed.Scheme != "https" {
		return checkWarn("Instance", baseURL, "An API key travels in a header over this connection; use https")
	}
	if !knownInstance(baseURL) {
		return checkWarn("Instance", baseURL, "Not one of the published SuperFaktura instances — check for a typo")
	}
	return checkPass("Instance", baseURL)
}

func knownInstance(baseURL string) bool {
	for _, instance := range config.Instances {
		if strings.EqualFold(strings.TrimRight(instance.URL, "/"), strings.TrimRight(baseURL, "/")) {
			return true
		}
	}
	return false
}

func checkEmail(email string) doctorCheck {
	if email == "" {
		return checkFail("Email", output.CodeAuth, "not set",
			"Run: sf auth login, or set SF_EMAIL")
	}
	if !strings.Contains(email, "@") {
		return checkWarn("Email", email, "This does not look like the account email")
	}
	return checkPass("Email", email)
}

func checkKey(name, source string) doctorCheck {
	switch source {
	case config.KeyInKeyring:
		return checkPass("API key", "system keyring")
	case doctorKeyInEnv:
		return checkPass("API key", "SF_APIKEY")
	case config.KeyInFile:
		if os.Getenv("SF_NO_KEYRING") != "" {
			return checkWarn("API key", "plaintext file",
				"SF_NO_KEYRING is set, so the keyring is being skipped on purpose")
		}
		return checkWarn("API key", "plaintext file",
			"The system keyring was unavailable when this key was saved; re-run 'sf auth login "+name+"' where one is")
	default:
		return checkFail("API key", output.CodeAuth, "no key stored",
			"Run: sf auth login "+name)
	}
}

// checkCompany is the reason this command exists. The header carries
// company_id, the daily allowance is counted against it, and a value that is
// wrong is not rejected — it is billed to whoever owns it.
func checkCompany(companyID string) doctorCheck {
	if companyID == "" {
		return checkWarn("Company", "not set",
			"Requests fall on the account's default company, and spend that company's quota. "+
				"Pick one from 'sf company list' and store it with 'sf auth login --company <id>'")
	}
	if _, err := strconv.Atoi(companyID); err != nil {
		return checkFail("Company", output.CodeUsage, companyID+" is not a company id",
			"company_id is numeric — see 'sf company list'")
	}
	return checkPass("Company", companyID)
}

// checkLive spends the one request, and only when asked. The endpoint is the
// same cheapest read `sf auth status` and `sf limits` use: a single client row,
// whose response headers carry the quota this company has left.
func checkLive(c context.Context, eff config.Settings, live bool, p *doctorProfile, result *doctorResult) []doctorCheck {
	if !live {
		return []doctorCheck{
			checkSkip("API", "not checked (--live sends one request per profile)"),
		}
	}
	if eff.Email == "" || eff.APIKey == "" {
		return []doctorCheck{checkSkip("API", "not checked (no credentials to try)")}
	}

	probe := client.New(eff.BaseURL, client.Credentials{
		Email:     eff.Email,
		APIKey:    eff.APIKey,
		Module:    eff.Module,
		CompanyID: eff.CompanyID,
	})
	if api != nil {
		probe.OnRequest = api.OnRequest
	}

	_, err := probe.Get(c, "/clients/index.json", client.Params{"per_page": "1"})
	result.Requests++

	limits := probe.Limits()
	if limits.Seen {
		p.Quota = &limits
	}

	checks := []doctorCheck{}
	if err != nil {
		e := output.AsError(err)
		checks = append(checks, checkFail("API", e.Code, e.Message, e.Hint))
	} else {
		checks = append(checks, checkPass("API", "credentials accepted"))
	}
	return append(checks, checkQuota(limits))
}

func checkQuota(limits client.RateLimit) doctorCheck {
	if !limits.Seen {
		return checkSkip("Quota", "the API reported no rate-limit headers")
	}
	message := fmt.Sprintf("%d of %d daily, %d of %d monthly",
		limits.DailyRemaining, limits.DailyLimit,
		limits.MonthlyRemaining, limits.MonthlyLimit)

	if limits.Message != "" {
		return checkWarn("Quota", limits.Message, message)
	}
	// The same tenth-left threshold the CLI warns at after any other command.
	if limits.DailyLimit > 0 && limits.DailyRemaining*quotaWarnRatio <= limits.DailyLimit {
		return checkWarn("Quota", message, "Daily quota resets "+limits.DailyReset)
	}
	return checkPass("Quota", message)
}

// worst reduces a profile's checks to its overall state.
func worst(checks []doctorCheck) string {
	status := doctorPass
	for _, c := range checks {
		switch c.Status {
		case doctorFail:
			return doctorFail
		case doctorWarn:
			status = doctorWarn
		}
	}
	return status
}

func tally(result *doctorResult) {
	result.Passed, result.Warned, result.Failed, result.Skipped = 0, 0, 0, 0
	count := func(checks []doctorCheck) {
		for _, c := range checks {
			switch c.Status {
			case doctorPass:
				result.Passed++
			case doctorWarn:
				result.Warned++
			case doctorFail:
				result.Failed++
			case doctorSkip:
				result.Skipped++
			}
		}
	}
	count(result.Checks)
	for _, p := range result.Profiles {
		count(p.Checks)
	}
}

// doctorExitCode picks the code the process exits with. The first failure in
// report order wins, so a missing credential is reported as auth_required
// rather than being flattened into a generic failure.
func doctorExitCode(result *doctorResult) string {
	for _, c := range result.Checks {
		if c.Status == doctorFail {
			return firstNonEmpty(c.code, output.CodeAPI)
		}
	}
	for _, p := range result.Profiles {
		for _, c := range p.Checks {
			if c.Status == doctorFail {
				return firstNonEmpty(c.code, output.CodeAPI)
			}
		}
	}
	return output.CodeAPI
}

func doctorSummary(result *doctorResult) string {
	parts := []string{fmt.Sprintf("%d passed", result.Passed)}
	if result.Warned > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", result.Warned, plural(result.Warned, "warning", "warnings")))
	}
	if result.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", result.Failed))
	}
	if result.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", result.Skipped))
	}
	return fmt.Sprintf("%d %s checked · %s · %d %s spent",
		len(result.Profiles), plural(len(result.Profiles), "profile", "profiles"),
		strings.Join(parts, ", "),
		result.Requests, plural(result.Requests, "request", "requests"))
}

// doctorColumns put the three values the quota depends on next to each other:
// which instance, which company, and what is left of that company's day.
var doctorColumns = []render.Column{
	{Header: "Profile", Path: "profile"},
	{Header: "Instance", Path: "instance"},
	{Header: "Company", Path: "company"},
	{Header: "Key", Path: "key"},
	{Header: "Quota", Path: "quota"},
	{Header: "Status", Path: "status"},
}

func renderDoctor(w io.Writer, result *doctorResult) {
	for _, c := range result.Checks {
		writeCheck(w, "", c)
	}

	if len(result.Profiles) > 0 {
		fmt.Fprintln(w)
		render.Table(w, doctorColumns, doctorRows(result.Profiles))

		for _, p := range result.Profiles {
			shown := doctorDetail(p)
			if len(shown) == 0 {
				continue
			}
			fmt.Fprintf(w, "\n%s\n", p.Name)
			for _, c := range shown {
				writeCheck(w, "  ", c)
			}
		}
	}

	fmt.Fprintf(w, "\n%s\n", doctorSummary(result))
	if !result.Live {
		fmt.Fprintln(w, "Nothing was sent to the API. Run 'sf doctor --live' to verify the\n"+
			"credentials and read each company's remaining quota — one request per profile.")
	}
}

// doctorDetail is what gets spelled out below the table: anything that is not
// a pass, and with --verbose the passes too, since "why does this one work"
// is a real question when a neighboring profile does not.
func doctorDetail(p doctorProfile) []doctorCheck {
	var shown []doctorCheck
	for _, c := range p.Checks {
		if flagVerbose || c.Status != doctorPass {
			shown = append(shown, c)
		}
	}
	return shown
}

func writeCheck(w io.Writer, indent string, c doctorCheck) {
	fmt.Fprintf(w, "%s%s %-16s %s\n", indent, doctorMarks[c.Status], c.Name, c.Message)
	if c.Hint != "" && c.Status != doctorPass {
		fmt.Fprintf(w, "%s    %s\n", indent, c.Hint)
	}
}

func doctorRows(profiles []doctorProfile) []map[string]any {
	rows := make([]map[string]any, 0, len(profiles))
	for _, p := range profiles {
		name := p.Name
		if p.Default {
			name += " *"
		}
		rows = append(rows, map[string]any{
			"profile":  name,
			"instance": instanceHost(p.BaseURL),
			"company":  orDash(p.CompanyID),
			"key":      doctorKeyLabel(p.KeySource),
			"quota":    doctorQuotaLabel(p.Quota),
			"status":   p.Status,
		})
	}
	return rows
}

func instanceHost(baseURL string) string {
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return orDash(baseURL)
}

func doctorKeyLabel(source string) string {
	switch source {
	case config.KeyInKeyring:
		return "keyring"
	case config.KeyInFile:
		return "file"
	case doctorKeyInEnv:
		return "env"
	default:
		return "missing"
	}
}

func doctorQuotaLabel(limits *client.RateLimit) string {
	if limits == nil || !limits.Seen {
		return "—"
	}
	return fmt.Sprintf("%d/%d", limits.DailyRemaining, limits.DailyLimit)
}
