package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/tui"
)

// quotaWarnRatio is the share of the daily allowance below which the CLI says
// something unprompted. SuperFaktura enforces a hard daily cap — 1000 requests
// on a standard plan — and the only warning otherwise is the request that
// fails, so the last tenth is worth flagging while there is still room to act.
const quotaWarnRatio = 10

// installProgress attaches a spinner to the API client.
//
// It is fitted only when a person is watching: machine output must stay
// byte-for-byte predictable, and a spinner on a redirected stderr would just
// litter a log file with control characters.
func installProgress() {
	if api == nil || !human() || !isatty.IsTerminal(os.Stderr.Fd()) {
		return
	}
	api.OnRequest = func(method, path string) func() {
		return tui.Start(os.Stderr, requestLabel(method, path)).Stop
	}
}

// requestLabel describes a request in progress. The path is the only thing the
// client knows about the call, so the resource comes from its first segment.
func requestLabel(method, path string) string {
	resource := strings.ReplaceAll(strings.Trim(strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)[0], "/"), "_", " ")
	if resource == "" {
		resource = "data"
	}

	switch method {
	case "GET":
		return "Loading " + resource + "…"
	case "DELETE":
		return "Deleting…"
	default:
		return "Saving " + resource + "…"
	}
}

// reportQuota warns on stderr when the daily allowance is nearly spent.
//
// This goes to stderr unconditionally rather than only in --verbose: a script
// that is about to start failing with rate-limit errors benefits from the
// warning more than an interactive user does, and stderr keeps piped output
// clean either way.
func reportQuota(limits client.RateLimit) {
	if !limits.Seen {
		return
	}

	if limits.Message != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", limits.Message)
		return
	}

	if limits.DailyLimit > 0 && limits.DailyRemaining*quotaWarnRatio <= limits.DailyLimit {
		fmt.Fprintf(os.Stderr,
			"Warning: %d of %d daily API requests left, resets %s. See 'sf limits'.\n",
			limits.DailyRemaining, limits.DailyLimit, limits.DailyReset)
		return
	}

	if limits.MonthlyLimit > 0 && limits.MonthlyRemaining*quotaWarnRatio <= limits.MonthlyLimit {
		fmt.Fprintf(os.Stderr,
			"Warning: %d of %d monthly API requests left, resets %s. See 'sf limits'.\n",
			limits.MonthlyRemaining, limits.MonthlyLimit, limits.MonthlyReset)
	}
}
