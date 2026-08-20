package commands

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/output"
	"github.com/xseman/superfaktura-cli/internal/render"
)

// The API deals in identifiers; people and agents deal in names. Without a
// translation step, "invoice Acme for 500" means listing clients, parsing the
// result and picking one — and picking wrong when several match. These
// resolvers do that once, and fail loudly with the candidates rather than
// guessing.

// resolveClient turns a --client value into an identifier. A number passes
// straight through; anything else is looked up by name, at the cost of one
// request.
func resolveClient(cmd *cobra.Command, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if _, err := strconv.Atoi(value); err == nil {
		return value, nil
	}

	params := client.Params{
		"listinfo": "1",
		"per_page": "50",
		"search":   client.EncodeSearch(value),
	}
	raw, err := api.Get(ctx(cmd), "/clients/index.json", params)
	if err != nil {
		return "", err
	}
	result, err := decodeList(raw)
	if err != nil {
		return "", err
	}

	type candidate struct{ id, name string }
	var all []candidate
	for _, item := range result.Items {
		id := render.Text(render.Get(item, "Client.id"))
		name := render.Text(render.Get(item, "Client.name"))
		if id != "" {
			all = append(all, candidate{id, name})
		}
	}

	switch len(all) {
	case 0:
		return "", &output.Error{
			Code:    output.CodeNotFound,
			Message: fmt.Sprintf("no client matches %q", value),
			Hint:    "Search with 'sf client list --search', or pass a numeric ID",
		}
	case 1:
		return all[0].id, nil
	}

	// Several matched. An exact name is a deliberate choice, not a coincidence,
	// so prefer it over a substring hit — otherwise "Acme s.r.o." could never
	// be selected while "Acme s.r.o. 2" exists.
	var exact []candidate
	for _, c := range all {
		if strings.EqualFold(strings.TrimSpace(c.name), strings.TrimSpace(value)) {
			exact = append(exact, c)
		}
	}
	if len(exact) == 1 {
		return exact[0].id, nil
	}

	matches := make([]string, 0, len(all))
	for _, c := range all {
		matches = append(matches, fmt.Sprintf("%s (%s)", c.name, c.id))
	}
	slices.Sort(matches)
	return "", &output.Error{
		Code:    output.CodeAmbiguous,
		Message: fmt.Sprintf("%d clients match %q", len(all), value),
		Hint:    "Pass a numeric ID, or the exact name",
		Matches: matches,
	}
}

// resolveTags turns --tag values into the identifiers the API requires. Names
// come from the tag list, which is cached, so this is usually free.
//
// The distinction matters: the API silently ignores tag *names*, so sending
// them looks like success and saves nothing.
func resolveTags(cmd *cobra.Command, values []string) ([]int, error) {
	if len(values) == 0 {
		return nil, nil
	}

	var byName map[string][]string
	ids := make([]int, 0, len(values))

	for _, value := range values {
		if id, err := strconv.Atoi(value); err == nil {
			ids = append(ids, id)
			continue
		}

		if byName == nil {
			var err error
			if byName, err = tagsByName(cmd); err != nil {
				return nil, err
			}
		}

		found := byName[strings.ToLower(strings.TrimSpace(value))]
		switch len(found) {
		case 0:
			return nil, &output.Error{
				Code:    output.CodeNotFound,
				Message: fmt.Sprintf("no tag named %q", value),
				Hint:    "See 'sf tag list', or create it with 'sf tag add'",
			}
		case 1:
			id, err := strconv.Atoi(found[0])
			if err != nil {
				return nil, &output.Error{
					Code:    output.CodeAPI,
					Message: fmt.Sprintf("tag %q has a non-numeric id %q", value, found[0]),
				}
			}
			ids = append(ids, id)
		default:
			return nil, &output.Error{
				Code:    output.CodeAmbiguous,
				Message: fmt.Sprintf("%d tags are named %q", len(found), value),
				Hint:    "Pass the numeric tag ID",
				Matches: found,
			}
		}
	}
	return ids, nil
}

// tagsByName indexes the account's tags, lowercased.
func tagsByName(cmd *cobra.Command) (map[string][]string, error) {
	raw, err := cachedGet(cmd, "/tags/index.json", nil, valueListTTL)
	if err != nil {
		return nil, err
	}
	result, err := decodeKeyValueList(raw, "Tag", "name")
	if err != nil {
		return nil, err
	}

	index := map[string][]string{}
	for _, item := range result.Items {
		name := strings.ToLower(strings.TrimSpace(render.Text(render.Get(item, "Tag.name"))))
		id := render.Text(render.Get(item, "Tag.id"))
		if name != "" && id != "" {
			index[name] = append(index[name], id)
		}
	}
	return index, nil
}

// putTags attaches tags to a write payload.
//
// The shape is the API's, not a typo: a "Tag" key whose value is an object
// with its own "Tag" array of identifiers.
func putTags(doc map[string]any, ids []int) {
	if len(ids) == 0 {
		return
	}
	doc["Tag"] = map[string]any{"Tag": ids}
}

// tagFlag binds --tag to a command.
func tagFlag(cmd *cobra.Command, target *[]string) {
	cmd.Flags().StringArrayVar(target, "tag", nil,
		"Tag to attach, by name or ID (repeatable). Names are resolved for you.")
}
