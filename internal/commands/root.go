// Package commands implements the sf command tree.
package commands

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/itchyny/gojq"
	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/config"
	"github.com/xseman/superfaktura-cli/internal/output"
)

var (
	flagProfile string
	flagAPIURL  string
	flagCompany string
	flagModule  string
	flagJQ      string

	flagJSON    bool
	flagQuiet   bool
	flagIDs     bool
	flagCount   bool
	flagStyled  bool
	flagAgent   bool
	flagVerbose bool
	flagNoCache bool
	flagDryRun  bool
)

// Process-wide state assembled by PersistentPreRunE.
var (
	out      *output.Writer
	outw     io.Writer = os.Stdout
	store    *config.Store
	settings config.Settings
	api      *client.Client
	jqFilter *gojq.Code
	version  = "dev"
)

var rootCmd = &cobra.Command{
	Use:   "sf",
	Short: "Command-line interface for the SuperFaktura API",
	Long: `sf drives SuperFaktura from the terminal: invoices, clients, expenses,
stock, cash registers, tags, exports and bank accounts.

Output adapts to where it goes — a table on a terminal, a JSON envelope when
piped. Force either with --json or --styled.`,
	SilenceUsage:      true,
	SilenceErrors:     true,
	PersistentPreRunE: setup,
}

// SetVersion records the build version for `sf version` and `--version`.
func SetVersion(v string) {
	if v != "" {
		version = v
		rootCmd.Version = v
	}
}

// Root exposes the command tree for surface snapshot tests.
func Root() *cobra.Command { return rootCmd }

func init() {
	f := rootCmd.PersistentFlags()
	f.StringVarP(&flagProfile, "profile", "p", "", "Named profile to use")
	f.StringVar(&flagAPIURL, "api-url", "", "SuperFaktura base URL")
	f.StringVar(&flagCompany, "company", "", "Company ID")
	f.StringVar(&flagModule, "module", "", "Module name reported to the API")
	f.BoolVar(&flagJSON, "json", false, "Output a JSON envelope")
	f.BoolVar(&flagQuiet, "quiet", false, "Output raw JSON data without the envelope")
	f.BoolVar(&flagIDs, "ids-only", false, "Output one ID per line")
	f.BoolVar(&flagCount, "count", false, "Output the number of results")
	f.BoolVar(&flagStyled, "styled", false, "Force table output even when piped")
	f.BoolVar(&flagAgent, "agent", false, "Machine-readable output and JSON help")
	f.StringVar(&flagJQ, "jq", "", "Filter JSON output with a jq expression")
	f.BoolVarP(&flagVerbose, "verbose", "v", false, "Print rate-limit and request details to stderr")
	f.BoolVar(&flagNoCache, "no-cache", false, "Ignore cached value lists for this invocation")
	f.BoolVar(&flagDryRun, "dry-run", false,
		"Show the request a write would send, without sending it. Reads still run.")
}

func setup(cmd *cobra.Command, _ []string) error {
	format, err := resolveFormat()
	if err != nil {
		return err
	}
	out = output.New(output.Options{Format: format, Writer: outw, Verbose: flagVerbose})

	if flagJQ != "" {
		query, err := gojq.Parse(flagJQ)
		if err != nil {
			return &output.Error{Code: output.CodeUsage, Message: fmt.Sprintf("invalid --jq expression: %s", err)}
		}
		jqFilter, err = gojq.Compile(query)
		if err != nil {
			return &output.Error{Code: output.CodeUsage, Message: fmt.Sprintf("invalid --jq expression: %s", err)}
		}
	}

	store, err = config.OpenStore()
	if err != nil {
		return &output.Error{Code: output.CodeUsage, Message: err.Error()}
	}

	settings, err = store.Resolve(config.Overrides{
		Profile:   flagProfile,
		BaseURL:   flagAPIURL,
		CompanyID: flagCompany,
		Module:    flagModule,
	})
	if err != nil {
		return &output.Error{Code: output.CodeUsage, Message: err.Error()}
	}

	api = client.New(settings.BaseURL, client.Credentials{
		Email:     settings.Email,
		APIKey:    settings.APIKey,
		Module:    settings.Module,
		CompanyID: settings.CompanyID,
	})
	api.DryRun = flagDryRun
	installProgress()
	openCache()

	// Last, so a bad date is reported rather than a missing credential when
	// both are wrong: the credential is what the user has to fix first.
	return checkDateFlags(cmd)
}

// resolveFormat picks the output format. At most one format flag may be set;
// --jq and --agent imply machine output because a table cannot be filtered.
func resolveFormat() (output.Format, error) {
	set := map[string]bool{
		"--json":     flagJSON,
		"--quiet":    flagQuiet,
		"--ids-only": flagIDs,
		"--count":    flagCount,
		"--styled":   flagStyled,
	}
	var chosen []string
	for name, on := range set {
		if on {
			chosen = append(chosen, name)
		}
	}
	if len(chosen) > 1 {
		return output.FormatAuto, &output.Error{
			Code:    output.CodeUsage,
			Message: fmt.Sprintf("%s cannot be combined", strings.Join(slices.Sorted(slices.Values(chosen)), " and ")),
		}
	}

	if flagStyled && (flagJQ != "" || flagAgent) {
		return output.FormatAuto, &output.Error{
			Code:    output.CodeUsage,
			Message: "--styled cannot be combined with --jq or --agent",
		}
	}

	switch {
	case flagQuiet:
		return output.FormatQuiet, nil
	case flagIDs:
		return output.FormatIDs, nil
	case flagCount:
		return output.FormatCount, nil
	case flagStyled:
		return output.FormatStyled, nil
	case flagJSON, flagAgent, flagJQ != "":
		return output.FormatJSON, nil
	default:
		return output.FormatAuto, nil
	}
}

// human reports whether this invocation should print a table rather than JSON.
func human() bool {
	return out != nil && out.EffectiveFormat() == output.FormatStyled
}

// emit writes one result. Human output is delegated to render, which knows the
// shape of the resource; the envelope carries everything else.
//
// --jq is the exception, and deliberately: it filters the *data*, not the
// envelope, so a script writes `.[0].Invoice.id` rather than `.data[0]…`. That
// means envelope fields — summary, meta, next — are not visible to a filter.
// A caller who wants them asks for --json.
//
// This comment used to claim that "exit codes, breadcrumbs and --jq behave
// identically everywhere". There were no breadcrumbs, the human branch never
// looked at its options, and --jq has never seen the envelope at all.
func emit(data any, table func(io.Writer), opts ...output.ResponseOption) error {
	if jqFilter != nil {
		return emitJQ(data)
	}
	if human() && table != nil {
		table(outw)
		// The options are not only the envelope's. A human who is told what
		// happened and nothing else has to work out what the record is called
		// and which command touches it next — and the command that just ran
		// knows both. This branch used to return here, which is how the comment
		// above came to promise something the human path never did.
		printNextSteps(outw, opts...)
		return nil
	}
	return out.OK(data, opts...)
}

// printNextSteps renders the follow-on commands an option set carries.
func printNextSteps(w io.Writer, opts ...output.ResponseOption) {
	var resp output.Response
	for _, opt := range opts {
		opt(&resp)
	}
	if len(resp.Next) == 0 {
		return
	}

	width := 0
	for _, step := range resp.Next {
		width = max(width, len(step.Cmd))
	}
	_, _ = fmt.Fprintln(w, "\nNext:")
	for _, step := range resp.Next {
		_, _ = fmt.Fprintf(w, "  %-*s  %s\n", width, step.Cmd, step.Does)
	}
}

func emitJQ(data any) error {
	// Round-trip through JSON so gojq sees plain maps and slices rather than
	// Go structs, which it cannot traverse.
	encoded, err := json.Marshal(output.NormalizeData(data))
	if err != nil {
		return &output.Error{Code: output.CodeAPI, Message: err.Error()}
	}
	var input any
	if err := json.Unmarshal(encoded, &input); err != nil {
		return &output.Error{Code: output.CodeAPI, Message: err.Error()}
	}

	iter := jqFilter.Run(input)
	enc := json.NewEncoder(outw)
	enc.SetIndent("", "  ")
	for {
		value, ok := iter.Next()
		if !ok {
			return nil
		}
		if jqErr, isErr := value.(error); isErr {
			return &output.Error{Code: output.CodeUsage, Message: fmt.Sprintf("jq filter error: %s", jqErr)}
		}
		if err := enc.Encode(value); err != nil {
			return err
		}
	}
}

// ctx returns the command's context, which carries interrupt cancellation.
func ctx(cmd *cobra.Command) context.Context {
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
}

// reportPlan prints what --dry-run held back, and exits successfully: nothing
// went wrong, and a caller checking the exit code should not be told otherwise.
func reportPlan(planned *client.Planned) {
	_ = emit(planned, func(w io.Writer) {
		fmt.Fprintf(w, "%s %s\n", planned.Method, planned.Path)
		if planned.ContentType != "" {
			fmt.Fprintf(w, "Content-Type: %s\n", planned.ContentType)
		}
		if planned.Body != nil {
			encoded, err := json.MarshalIndent(planned.Body, "", "  ")
			if err == nil {
				fmt.Fprintf(w, "\n%s\n", encoded)
			}
		}
		fmt.Fprintln(w, "\nNot sent (--dry-run).")
	}, output.WithSummary("Not sent (--dry-run)"))
}

// reported wraps an error whose detail the command has already printed. It
// carries the exit code without producing a second envelope on stdout — one
// invocation must emit exactly one document.
type reported struct{ err error }

func (r reported) Error() string { return r.err.Error() }
func (r reported) Unwrap() error { return r.err }

// Execute runs the command tree and translates errors into exit codes.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		var already reported
		if stderrors.As(err, &already) {
			os.Exit(output.AsError(already.err).ExitCode())
		}

		// A withheld write is not a failure. It travels as an error only so it
		// cannot be mistaken for a result on the way up.
		var planned *client.Planned
		if stderrors.As(err, &planned) {
			reportPlan(planned)
			return
		}

		// Anything Cobra raises itself — unknown command, unknown flag, wrong
		// argument count — is the caller's mistake, not the API's.
		var e *output.Error
		if !stderrors.As(err, &e) {
			e = &output.Error{Code: output.CodeUsage, Message: err.Error()}
		}
		if out == nil {
			out = output.New(output.Options{Format: output.FormatAuto, Writer: outw})
		}
		if human() {
			fmt.Fprintf(os.Stderr, "Error: %s\n", e.Message)
			if e.Hint != "" {
				fmt.Fprintf(os.Stderr, "Hint:  %s\n", e.Hint)
			}
		} else {
			_ = out.Err(e)
		}
		os.Exit(e.ExitCode())
	}

	if api == nil {
		return
	}
	limits := api.Limits()
	if flagVerbose && limits.Seen {
		fmt.Fprintf(os.Stderr, "rate limit: %d/%d today, %d/%d this month\n",
			limits.DailyRemaining, limits.DailyLimit,
			limits.MonthlyRemaining, limits.MonthlyLimit)
		return
	}
	reportQuota(limits)
}
