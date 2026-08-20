package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/cache"
	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/config"
	"github.com/xseman/superfaktura-cli/internal/output"
)

// Caching is deliberately narrow. Only data that a person edits rarely goes in
// — countries, tags, bank accounts, the company roster — plus the per-invoice
// PDF token, which never changes at all. Documents are never cached: a list
// served from disk could show a paid invoice as unpaid, and in accounting that
// is a worse failure than spending one of the day's 1000 requests.

const (
	// valueListTTL covers lists a user edits by hand, if at all.
	valueListTTL = 24 * time.Hour

	// tokenTTL covers the PDF token, which is fixed for the life of the
	// invoice. Long, but not unbounded, so a stale entry cannot outlive a
	// deleted invoice indefinitely.
	tokenTTL = 30 * 24 * time.Hour
)

var responses *cache.Store

// openCache prepares the response cache for the resolved account.
func openCache() {
	dir, err := config.CacheDir()
	if err != nil {
		// Without a cache directory the CLI simply makes the request.
		return
	}
	// The scope keys entries to one account, so two profiles on one machine
	// never see each other's tags or bank accounts.
	responses = cache.Open(dir, settings.BaseURL+"\x00"+settings.Email+"\x00"+settings.CompanyID)
	responses.Disabled = flagNoCache
}

// cachedGet is Get for a response worth reusing. A miss, an expired entry or a
// disabled cache all fall through to the API.
func cachedGet(cmd *cobra.Command, path string, params client.Params, ttl time.Duration) (json.RawMessage, error) { //nolint:unparam // params mirrors Get; every response cached today takes none
	key := path + params.CacheKey()
	if body, ok := responses.Get(key, ttl); ok {
		return body, nil
	}

	body, err := api.Get(ctx(cmd), path, params)
	if err != nil {
		return nil, err
	}
	responses.Put(key, body)
	return body, nil
}

func init() {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect and clear cached value lists",
		Long: `Value lists — countries, tags, bank accounts, companies, expense
categories, sequences and logos — are cached for a day, and the per-invoice PDF
token for a month. Nothing else is: an invoice or expense served from cache
could be wrong in a way that matters.

Entries are keyed per account, so profiles never see each other's data. Pass
--no-cache to any command to bypass it for one invocation.`,
	}
	rootCmd.AddCommand(cmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Delete every cached entry",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			removed, err := responses.Clear()
			if err != nil {
				return &output.Error{Code: output.CodeAPI, Message: err.Error()}
			}
			return emitAction(map[string]any{"removed": removed},
				fmt.Sprintf("Removed %d cached entries", removed))
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the cache directory",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, err := config.CacheDir()
			if err != nil {
				return &output.Error{Code: output.CodeUsage, Message: err.Error()}
			}
			return emit(map[string]any{"path": dir},
				func(w io.Writer) { fmt.Fprintln(w, dir) })
		},
	})
}
