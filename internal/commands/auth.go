package commands

import (
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/config"
	"github.com/xseman/superfaktura-cli/internal/output"
	"github.com/xseman/superfaktura-cli/internal/tui"
)

var (
	loginEmail   string
	loginAPIKey  string
	loginDefault bool
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage credentials and profiles",
	Long: `A profile names one SuperFaktura account: an instance (country), an email,
an API key and optionally a company. The API key is kept in the system keyring
when one is available and in a 0600 file otherwise.`,
}

var authLoginCmd = &cobra.Command{
	Use:   "login [profile]",
	Short: "Store credentials for a profile",
	Long: `Stores credentials under a profile name (default "default").

With no flags on a terminal this prompts for each value. Supply --email,
--api-key, --api-url and --company to run unattended.`,
	Args: cobra.MaximumNArgs(1),
	Example: `  sf auth login
  sf auth login sandbox --api-url https://sandbox.superfaktura.sk \
      --email me@example.com --api-key KEY --company 123`,
	RunE: runAuthLogin,
}

var authListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List configured profiles",
	Args:    cobra.NoArgs,
	RunE:    runAuthList,
}

var authSwitchCmd = &cobra.Command{
	Use:   "switch <profile>",
	Short: "Make a profile the default",
	Args:  cobra.ExactArgs(1),
	RunE:  runAuthSwitch,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout [profile]",
	Short: "Remove a profile and its stored key",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runAuthLogout,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the active account and verify it against the API",
	Args:  cobra.NoArgs,
	RunE:  runAuthStatus,
}

func init() {
	authLoginCmd.Flags().StringVar(&loginEmail, "email", "", "Account email (prompted for when omitted)")
	authLoginCmd.Flags().StringVar(&loginAPIKey, "api-key", "", "API key (prompted for when omitted)")
	authLoginCmd.Flags().BoolVar(&loginDefault, "default", false, "Also make this profile the default")

	authCmd.AddCommand(authLoginCmd, authListCmd, authSwitchCmd, authLogoutCmd, authStatusCmd)
	rootCmd.AddCommand(authCmd)
}

// loginSettings assembles the profile to store, flag over environment over
// default, for every field rather than only the two secrets.
//
// Reading SF_EMAIL and SF_APIKEY while ignoring SF_API_URL would let a
// scripted `export SF_API_URL=…sandbox…; sf auth login sandbox` save the
// production instance under a profile named "sandbox" — the failure mode being
// invoices issued for real against an account meant for testing.
func loginSettings() config.Settings {
	return config.Settings{
		BaseURL:   firstNonEmpty(flagAPIURL, os.Getenv("SF_API_URL"), config.DefaultBaseURL),
		Email:     firstNonEmpty(loginEmail, os.Getenv("SF_EMAIL")),
		APIKey:    firstNonEmpty(loginAPIKey, os.Getenv("SF_APIKEY")),
		CompanyID: firstNonEmpty(flagCompany, os.Getenv("SF_COMPANY_ID")),
		Module:    firstNonEmpty(flagModule, os.Getenv("SF_MODULE")),
	}
}

// keyField is the API key box, required only when nothing is stored yet.
func keyField(value *string, hasKey bool) huh.Field {
	field := huh.NewInput().
		Title("API key").
		EchoMode(huh.EchoModePassword).
		Value(value)
	if hasKey {
		return field.Description("Leave empty to keep the key already stored")
	}
	return field.
		Description("Found under Tools > API access").
		Validate(required("API key"))
}

func runAuthLogin(cmd *cobra.Command, args []string) error {
	name := "default"
	if len(args) == 1 {
		name = args[0]
	}

	entry := loginSettings()

	// Signing in again is usually correcting one field, not retyping four, so
	// the form opens on what is already stored. Flags and the environment still
	// win — they were given for this invocation, the profile was not.
	saved, hasKey, _ := store.Load(name)
	setIfEmpty(&entry.Email, saved.Email)
	setIfEmpty(&entry.CompanyID, saved.CompanyID)
	setIfEmpty(&entry.Module, saved.Module)
	if flagAPIURL == "" && os.Getenv("SF_API_URL") == "" && saved.BaseURL != "" {
		entry.BaseURL = saved.BaseURL
	}

	if entry.Email == "" || (entry.APIKey == "" && !hasKey) {
		if !interactive() {
			return &output.Error{
				Code:    output.CodeUsage,
				Message: "--email and --api-key are required when not running on a terminal",
				Hint:    "Find both under Tools > API access in SuperFaktura",
			}
		}
	}
	if interactive() && (entry.Email == "" || entry.APIKey == "") {
		if err := promptCredentials(&entry, hasKey); err != nil {
			return err
		}
	}

	// An empty key means "the one already stored" — the form says so, and the
	// alternative is making the user paste it again to change their company id.
	if entry.APIKey == "" && hasKey {
		entry.APIKey = saved.APIKey
	}
	if entry.APIKey == "" {
		return &output.Error{
			Code:    output.CodeUsage,
			Message: "no api key given and none stored for this profile",
			Hint:    "Find one under Tools > API access in SuperFaktura",
		}
	}

	if err := store.Save(name, entry); err != nil {
		return &output.Error{Code: output.CodeUsage, Message: err.Error()}
	}
	if loginDefault {
		if err := store.SetDefault(name); err != nil {
			return &output.Error{Code: output.CodeUsage, Message: err.Error()}
		}
	}

	if warning := store.FallbackWarning(); warning != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
	}

	return emitAction(map[string]any{
		"profile":  name,
		"base_url": entry.BaseURL,
		"email":    entry.Email,
	}, fmt.Sprintf("Saved profile %q for %s", name, entry.Email))
}

// setIfEmpty fills a field that nothing more specific has claimed.
func setIfEmpty(target *string, value string) {
	if *target == "" {
		*target = value
	}
}

// promptCredentials collects the missing values with a form. Only the API key
// is masked; the rest is worth seeing while typing.
//
// hasKey relaxes the key from required to optional: there is already one
// stored, so an empty box means keep it. Its value is deliberately not put in
// the field — nothing is gained by loading a secret into a widget when leaving
// the box alone already means "unchanged".
func promptCredentials(entry *config.Settings, hasKey bool) error {
	options := make([]huh.Option[string], 0, len(config.Instances))
	for _, instance := range config.Instances {
		options = append(options, huh.NewOption(fmt.Sprintf("%s (%s)", instance.Label, instance.URL), instance.URL))
	}

	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Instance").
			Options(options...).
			Value(&entry.BaseURL),
		huh.NewInput().
			Title("Email").
			Description("The account email shown under Tools > API access").
			Value(&entry.Email).
			Validate(required("email")),
		keyField(&entry.APIKey, hasKey),
		huh.NewInput().
			Title("Company ID").
			Description("Optional — leave empty to use the account's default company").
			Value(&entry.CompanyID),
	))

	form = form.WithTheme(tui.FormTheme())
	if err := form.Run(); err != nil {
		return &output.Error{Code: output.CodeUsage, Message: err.Error()}
	}
	return nil
}

func required(field string) func(string) error {
	return func(value string) error {
		if value == "" {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
}

func runAuthList(*cobra.Command, []string) error {
	profiles, defaultName, err := store.List()
	if err != nil {
		return &output.Error{Code: output.CodeUsage, Message: err.Error()}
	}

	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	slices.Sort(names)

	rows := make([]map[string]any, 0, len(names))
	for _, name := range names {
		rows = append(rows, map[string]any{
			"name":     name,
			"base_url": profiles[name].BaseURL,
			"default":  name == defaultName,
		})
	}

	return emit(rows, func(w io.Writer) {
		if len(rows) == 0 {
			fmt.Fprintln(w, "No profiles. Run 'sf auth login' to add one.")
			return
		}
		for _, row := range rows {
			marker := " "
			if row["default"].(bool) {
				marker = "*"
			}
			fmt.Fprintf(w, "%s %-16s %s\n", marker, row["name"], row["base_url"])
		}
	})
}

func runAuthSwitch(_ *cobra.Command, args []string) error {
	if err := store.SetDefault(args[0]); err != nil {
		return &output.Error{
			Code:    output.CodeNotFound,
			Message: err.Error(),
			Hint:    "See 'sf auth list'",
		}
	}
	return emitAction(map[string]any{"profile": args[0]},
		fmt.Sprintf("Default profile is now %q", args[0]))
}

func runAuthLogout(_ *cobra.Command, args []string) error {
	name := firstNonEmpty(argAt(args, 0), flagProfile, settings.Profile)
	if name == "" {
		return &output.Error{
			Code:    output.CodeUsage,
			Message: "no profile to remove",
			Hint:    "Pass a profile name, e.g. 'sf auth logout default'",
		}
	}
	if err := store.Forget(name); err != nil {
		return &output.Error{Code: output.CodeNotFound, Message: err.Error()}
	}
	return emitAction(map[string]any{"profile": name},
		fmt.Sprintf("Removed profile %q", name))
}

func runAuthStatus(cmd *cobra.Command, _ []string) error {
	status := map[string]any{
		"profile":    settings.Profile,
		"base_url":   settings.BaseURL,
		"email":      settings.Email,
		"company_id": settings.CompanyID,
		"module":     firstNonEmpty(settings.Module, client.DefaultModule),
		"keyring":    store.UsingKeyring(),
	}

	// One cheap authenticated call is the only honest way to report status:
	// stored credentials say nothing about whether the API still accepts them.
	_, err := api.Get(ctx(cmd), "/clients/index.json", client.Params{"per_page": "1"})
	status["authenticated"] = err == nil
	if err != nil {
		status["error"] = output.AsError(err).Message
	}

	if err := emit(status, func(w io.Writer) {
		fmt.Fprintf(w, "Profile:   %s\n", orDash(settings.Profile))
		fmt.Fprintf(w, "Instance:  %s\n", settings.BaseURL)
		fmt.Fprintf(w, "Email:     %s\n", orDash(settings.Email))
		fmt.Fprintf(w, "Company:   %s\n", orDash(settings.CompanyID))
		fmt.Fprintf(w, "Keyring:   %v\n", store.UsingKeyring())
		if status["authenticated"].(bool) {
			fmt.Fprintln(w, "API:       ok")
		} else {
			fmt.Fprintf(w, "API:       %s\n", status["error"])
		}
	}); err != nil {
		return err
	}

	// A failing check is a failing command, so `sf auth status || sf auth login`
	// works. The detail is already in the envelope above, so the exit code
	// travels on its own rather than printing a second one.
	if err != nil {
		return reported{err}
	}
	return nil
}

func interactive() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}

func argAt(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func orDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
