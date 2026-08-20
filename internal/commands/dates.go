package commands

import (
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/xseman/superfaktura-cli/internal/output"
)

// Date fields are not validated against YYYY-MM-DD, because the server accepts
// far more than that and rejecting it would be a regression.
//
// SuperFaktura is PHP and puts every date through strtotime, so these all work
// and were verified against a live account:
//
//	2026-08-15    15.08.2026    31.12.2026    tomorrow    +14 days    next friday
//
// For an invoicing tool "+14 days" is the common case, so it stays. What is
// checked here is the one shape that can be read wrongly *without an error*:
// strtotime takes slashes in American month/day order, so a European typing
// 08/09/2026 for the 8th of September silently books the 9th of August.
//
// Everything else either works or fails loudly. An expression strtotime cannot
// parse becomes the epoch and trips a later check, which reports "due date is
// before the issue date" — misleading, but not silent, and not something this
// package can predict without reimplementing strtotime.
//
// See API-DISCREPANCIES.md §B6.

// isoDate matches the shape the documentation asks for. Matching the shape is
// not the same as being a real day, which is the second thing checked.
var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// dateHint is the same advice everywhere it is refused.
const dateHint = "Use 2026-08-15, 15.08.2026, or a relative date like '+14 days'"

// checkDate reports why a date input cannot be trusted, or nil.
func checkDate(value string) *output.Error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	if strings.Contains(value, "/") {
		return &output.Error{
			Code: output.CodeUsage,
			Message: "slashes are read in american month/day order, so " +
				value + " would not be the date you mean",
			Hint: dateHint,
		}
	}

	if isoDate.MatchString(value) {
		if _, err := time.Parse("2006-01-02", value); err != nil {
			return &output.Error{
				Code:    output.CodeUsage,
				Message: value + " is not a real date",
				Hint:    dateHint,
			}
		}
	}
	return nil
}

// isDateFlag reports whether a flag takes a date.
//
// The usage string is the signal because it is written by whoever decided the
// flag is a date, so the two cannot drift apart — and it correctly passes over
// `--date` on a list command, which takes a time-filter constant rather than a
// date.
func isDateFlag(f *pflag.Flag) bool {
	return strings.Contains(f.Usage, "YYYY-MM-DD")
}

// checkDateFlags validates every date flag the invocation actually set.
//
// Hooked once at the root rather than per command, so a new date flag is
// covered the day it is added. The browser's forms do not pass through here —
// they call RunE directly — and validate in the form instead, which is better
// anyway: the complaint lands under the field being typed into.
func checkDateFlags(cmd *cobra.Command) error {
	var failure error
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if failure != nil || !f.Changed || !isDateFlag(f) {
			return
		}
		if err := checkDate(f.Value.String()); err != nil {
			err.Message = "--" + f.Name + ": " + err.Message
			failure = err
		}
	})
	return failure
}
