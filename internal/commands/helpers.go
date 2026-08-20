package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/output"
	"github.com/xseman/superfaktura-cli/internal/render"
)

// plural picks the noun form for a count. Both forms are spelled out rather
// than an "s" appended, because not every noun takes one — and "1 item(s)" in
// output a person reads looks unfinished.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// perPageCap values are the documented per_page ceilings. They differ by
// resource: invoices allow 200, expenses only 100. Sending more is a wasted
// request out of a budget that is capped per day.
const (
	defaultPerPageCap = 200
	expensePerPageCap = 100
)

// listOptions are the pagination and sorting flags shared by every list command.
type listOptions struct {
	page      int
	perPage   int
	sort      string
	direction string
	search    string
	fetchAll  bool
	filters   []string

	// cap overrides the documented per_page ceiling for resources that allow
	// fewer than the default. Zero means defaultPerPageCap.
	cap int
}

// limit returns the per_page ceiling that applies to this resource.
func (o *listOptions) limit() int {
	if o.cap > 0 {
		return o.cap
	}
	return defaultPerPageCap
}

func (o *listOptions) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.IntVar(&o.page, "page", 0, "Page number")
	f.IntVar(&o.perPage, "per-page", 0,
		fmt.Sprintf("Items per page (max %d)", o.limit()))
	f.StringVar(&o.sort, "sort", "", "Attribute to sort by")
	f.StringVar(&o.direction, "direction", "", "Sort direction (ASC or DESC)")
	f.StringVar(&o.search, "search", "", "Full-text search term")
	f.BoolVar(&o.fetchAll, "all", false, "Fetch every page")

	// The documented filter sets run to twenty-odd parameters per resource and
	// grow with the API. Promoting each to a flag would bury the useful ones,
	// so the common filters get flags and --filter passes through the rest.
	f.StringArrayVar(&o.filters, "filter", nil,
		"Additional API filter as key=value (repeatable), e.g. --filter delivery_type=courier")
}

func (o *listOptions) params() (client.Params, error) {
	if o.perPage > o.limit() {
		return nil, &output.Error{
			Code:    output.CodeUsage,
			Message: fmt.Sprintf("--per-page is capped at %d for this resource", o.limit()),
		}
	}
	if o.direction != "" && o.direction != "ASC" && o.direction != "DESC" {
		return nil, &output.Error{
			Code:    output.CodeUsage,
			Message: fmt.Sprintf("--direction must be ASC or DESC, got %q", o.direction),
		}
	}

	p := client.Params{"listinfo": "1"}
	p.SetInt("page", o.page)
	p.SetInt("per_page", o.perPage)
	p.Set("sort", o.sort)
	p.Set("direction", o.direction)
	if o.search != "" {
		p.Set("search", client.EncodeSearch(o.search))
	}

	for _, filter := range o.filters {
		key, value, found := strings.Cut(filter, "=")
		if !found || key == "" {
			return nil, &output.Error{
				Code:    output.CodeUsage,
				Message: fmt.Sprintf("--filter must be key=value, got %q", filter),
			}
		}
		p.Set(key, value)
	}
	return p, nil
}

// listResult is what an index.json endpoint returns with listinfo:1.
type listResult struct {
	Items     []map[string]any `json:"items"`
	ItemCount int              `json:"itemCount"`
	PageCount int              `json:"pageCount"`
}

// fetchList runs a list request, following pages when --all is set.
//
// Paging is driven by pageCount rather than by comparing counts, because a
// filtered list reports itemCount for the filtered set while the caller may
// have asked for an unfiltered page size.
func fetchList(ctx context.Context, path string, opts *listOptions, extra client.Params) (listResult, error) {
	base, err := opts.params()
	if err != nil {
		return listResult{}, err
	}
	for k, v := range extra {
		base[k] = v
	}

	if opts.fetchAll {
		base["per_page"] = strconv.Itoa(opts.limit())
	}

	page := opts.page
	if page < 1 {
		page = 1
	}

	var combined listResult
	for {
		base["page"] = strconv.Itoa(page)
		raw, err := api.Get(ctx, path, base)
		if err != nil {
			return listResult{}, err
		}

		result, err := decodeList(raw)
		if err != nil {
			return listResult{}, err
		}
		combined.Items = append(combined.Items, result.Items...)
		combined.ItemCount = result.ItemCount
		combined.PageCount = result.PageCount

		if !opts.fetchAll || len(result.Items) == 0 || page >= result.PageCount {
			break
		}
		page++
	}
	return combined, nil
}

// decodeList tolerates the two list shapes the API uses: an object carrying
// "items" (listinfo:1) and a bare array (endpoints that ignore listinfo).
func decodeList(raw json.RawMessage) (listResult, error) {
	var wrapped listResult
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Items != nil {
		return wrapped, nil
	}

	var bare []map[string]any
	if err := json.Unmarshal(raw, &bare); err == nil {
		return listResult{Items: bare, ItemCount: len(bare), PageCount: 1}, nil
	}

	// A keyed object, e.g. {"1": {...}, "2": {...}} as some value lists return.
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keyed); err == nil {
		items := make([]map[string]any, 0, len(keyed))
		for _, key := range slices.Sorted(maps.Keys(keyed)) {
			var item map[string]any
			if json.Unmarshal(keyed[key], &item) == nil {
				if _, taken := item["id"]; !taken {
					item["id"] = key
				}
				items = append(items, item)
			}
		}
		return listResult{Items: items, ItemCount: len(items), PageCount: 1}, nil
	}

	return listResult{}, &output.Error{
		Code:    output.CodeAPI,
		Message: "unrecognized list response from the API",
	}
}

// decodeListUnder pulls a nested collection out of a response, for the
// endpoints that wrap their list in a named key rather than in "items" —
// /bank_accounts/index returns {"BankAccounts":[...],"error":0}.
func decodeListUnder(raw json.RawMessage, key string) (listResult, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return listResult{}, &output.Error{
			Code:    output.CodeAPI,
			Message: fmt.Sprintf("unexpected response from the API: %s", err),
		}
	}
	nested, ok := envelope[key]
	if !ok {
		return listResult{}, nil
	}
	return decodeList(nested)
}

// decodeKeyValueList turns an identifier-to-label map into rows.
// /tags/index.json answers {"1":"abc"}, and an empty [] when there are none.
func decodeKeyValueList(raw json.RawMessage, model, field string) (listResult, error) { //nolint:unparam // the generic shape is the point; only tags call it today
	var pairs map[string]string
	if err := json.Unmarshal(raw, &pairs); err != nil {
		// An empty result is an array, not an object.
		return listResult{}, nil
	}

	items := make([]map[string]any, 0, len(pairs))
	for _, key := range slices.Sorted(maps.Keys(pairs)) {
		items = append(items, map[string]any{
			model: map[string]any{"id": key, field: pairs[key]},
		})
	}
	return listResult{Items: items, ItemCount: len(items), PageCount: 1}, nil
}

// decodeObject decodes a single-record response.
func decodeObject(raw json.RawMessage) (map[string]any, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, &output.Error{
			Code:    output.CodeAPI,
			Message: fmt.Sprintf("unexpected response from the API: %s", err),
		}
	}
	return obj, nil
}

// emitList prints a list as a table for humans and as an envelope otherwise,
// carrying the server's total so `--json` consumers can page correctly.
func emitList(result listResult, cols []render.Column, next ...output.Step) error {
	if idsRequested() && len(cols) > 0 {
		return writeIDs(result.Items, cols[0].Path)
	}
	opts := []output.ResponseOption{
		output.WithMeta("item_count", result.ItemCount),
		output.WithMeta("page_count", result.PageCount),
	}
	// A list carries suggestions too. `item list` exists to feed `item delete`,
	// and a list that names neither the command nor the flag it fills leaves the
	// reader to work that out from an id column alone.
	if len(next) > 0 && len(result.Items) > 0 {
		opts = append(opts, output.WithNext(next...))
	}
	return emit(result.Items,
		func(w io.Writer) { render.Table(w, cols, result.Items) },
		opts...,
	)
}

// emitObject prints one record.
func emitObject(obj map[string]any, fields []render.Field) error {
	idPath := ""
	if len(fields) > 0 {
		idPath = fields[0].Path
	}
	return emitDetail(obj, idPath, render.Pairs(nil, fields))
}

// emitDetail renders a record however the resource says. Most want the default
// two columns; a document composes its own, because it has line items and
// payments that a bag of labeled values cannot show.
func emitDetail(obj map[string]any, idPath string, show render.Renderer) error {
	if idsRequested() && idPath != "" {
		return writeIDs([]map[string]any{obj}, idPath)
	}
	return emit(obj, func(w io.Writer) {
		for _, line := range show(obj, 0, nil) {
			_, _ = io.WriteString(w, line+"\n")
		}
	})
}

// idsRequested reports whether the caller asked for bare identifiers.
func idsRequested() bool {
	return out != nil && out.EffectiveFormat() == output.FormatIDs
}

// writeIDs prints one identifier per line.
//
// The shared output writer looks for a top-level "id", which a SuperFaktura
// record never has: the identifier lives under the model name. The first
// column or field of every resource is that identifier, so its path is what
// locates it.
func writeIDs(rows []map[string]any, path string) error {
	for _, row := range rows {
		id := render.Text(render.Get(row, path))
		if id == "" {
			continue
		}
		if _, err := fmt.Fprintln(outw, id); err != nil {
			return err
		}
	}
	return nil
}

// emitAction reports a write that returns no interesting body.
func emitAction(data any, message string) error {
	return emit(data, func(w io.Writer) { fmt.Fprintln(w, message) }, output.WithSummary(message))
}

// writeBinary sends a downloaded document to a file or to stdout.
func writeBinary(destination string, body []byte) (string, error) {
	if destination == "-" {
		_, err := os.Stdout.Write(body)
		return "", err
	}
	if err := os.WriteFile(destination, body, 0o600); err != nil {
		return "", &output.Error{Code: output.CodeAPI, Message: err.Error()}
	}
	return destination, nil
}

// parseNumber converts a user-supplied amount. Values are emitted as numbers
// rather than strings because the API rejects quoted amounts on some endpoints.
//
// "NaN", "Inf" and "infinity" are numbers to strconv and to nobody else:
// ParseFloat takes them without an error, and they then travel as far as
// json.Marshal, which refuses the whole payload with "unsupported value: NaN".
// The amount the user typed is never named in that message. Refusing here
// keeps the complaint next to the flag it belongs to, for every caller.
func parseNumber(value string) (float64, error) {
	number, err := strconv.ParseFloat(strings.TrimSpace(strings.Replace(value, ",", ".", 1)), 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, fmt.Errorf("%q is not a finite number", value)
	}
	return number, nil
}

// usageErrorf builds a usage error, which exits with code 1.
func usageErrorf(format string, args ...any) error {
	return &output.Error{Code: output.CodeUsage, Message: fmt.Sprintf(format, args...)}
}

// requireID validates a positional numeric identifier.
func requireID(kind, value string) (string, error) {
	if _, err := strconv.Atoi(value); err != nil {
		return "", &output.Error{
			Code:    output.CodeUsage,
			Message: fmt.Sprintf("%s ID must be a number, got %q", kind, value),
		}
	}
	return value, nil
}

// emitWrite reports the result of a write: the API's own response body for
// machine consumers, a one-line confirmation plus the new ID for humans.
//
// The model names what was created. It has to be given rather than guessed: a
// create response carries several models — an invoice comes back with its
// Client attached — and picking one by any general rule reports the wrong
// identifier. An empty model means the response has no identifier worth
// showing.
func emitWrite(raw json.RawMessage, model, message string, next ...output.Step) error {
	obj, err := decodeObject(raw)
	if err != nil {
		// A write that succeeded but answered with something unexpected is
		// still a success; the confirmation is what the caller asked for.
		return emitAction(nil, message)
	}

	opts := []output.ResponseOption{output.WithSummary(message)}
	// The suggestions are written with a %s where the new record's id goes,
	// because the command cannot know it until the server answers.
	//
	// Anything the user still has to supply is spelled in CAPITALS rather than
	// <brackets>: a bracket is a shell redirect, so the line does not survive a
	// paste — and a plausible-looking placeholder is worse, because a step that
	// runs verbatim quietly creates a real invoice for a real client out of
	// filler, on an allowance of a thousand requests a day.
	if id := newRecordID(obj, model); id != "" && len(next) > 0 {
		filled := make([]output.Step, 0, len(next))
		for _, step := range next {
			filled = append(filled, output.Step{
				Cmd:  fmt.Sprintf(step.Cmd, id),
				Does: step.Does,
			})
		}
		opts = append(opts, output.WithNext(filled...))
	}

	return emit(obj, func(w io.Writer) {
		fmt.Fprintln(w, message)
		if id := newRecordID(obj, model); id != "" {
			fmt.Fprintf(w, "ID: %s\n", id)
		}
	}, opts...)
}

// newRecordID finds the identifier of the record just created.
//
// The named model is authoritative. Nothing is inferred when it is empty,
// because the alternative — taking the first model that happens to carry an id
// — reports an invoice's client instead of the invoice.
func newRecordID(obj map[string]any, model string) string {
	if model == "" {
		return ""
	}

	// Some endpoints answer with a top-level identifier named after the model
	// rather than a nested record: /tags/add returns {"error":0,"tag_id":"1"}.
	if id := render.Text(obj[strings.ToLower(model)+"_id"]); id != "" {
		return id
	}

	// The rest nest it under data.<Model>.id.
	data, ok := obj["data"].(map[string]any)
	if !ok {
		return ""
	}
	if section, ok := data[model].(map[string]any); ok {
		if id := render.Text(section["id"]); id != "" {
			return id
		}
	}
	return render.Text(data["id"])
}
