package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// An agent driving this CLI needs the command surface as data, not as help
// text it has to parse. `sf commands --json` returns the whole tree and
// `sf --agent <cmd> --help` returns one command, both in the same shape.

type commandInfo struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Usage       string        `json:"usage,omitempty"`
	Aliases     []string      `json:"aliases,omitempty"`
	Flags       []flagInfo    `json:"flags,omitempty"`
	Subcommands []commandInfo `json:"subcommands,omitempty"`
}

type flagInfo struct {
	Name        string `json:"name"`
	Shorthand   string `json:"shorthand,omitempty"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description"`
}

func init() {
	var filter string

	cmd := &cobra.Command{
		Use:     "commands [filter]",
		Short:   "List every command as a catalog",
		Long:    "Lists every command. Use --json for a structured catalog an agent can consume.",
		Args:    cobra.MaximumNArgs(1),
		Example: "  sf commands\n  sf commands invoice\n  sf commands --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				filter = args[0]
			}
			catalog := walkCommands(rootCmd, "sf", filter)
			return emit(catalog, func(w io.Writer) { renderCatalog(w, catalog) })
		},
	}
	rootCmd.AddCommand(cmd)

	// Cobra's help is text; under --agent it becomes the same JSON as the
	// catalog so one parser handles both.
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if !flagAgent {
			cmd.Root().SetHelpFunc(nil)
			_ = cmd.Help()
			return
		}
		info := describe(cmd, cmd.CommandPath())
		info.Subcommands = walkCommands(cmd, cmd.CommandPath(), "")
		encoded, _ := json.MarshalIndent(info, "", "  ")
		fmt.Fprintln(outw, string(encoded))
	})
}

func walkCommands(parent *cobra.Command, prefix, filter string) []commandInfo {
	var result []commandInfo
	for _, sub := range parent.Commands() {
		if sub.Hidden || sub.Name() == "help" {
			continue
		}
		path := prefix + " " + sub.Name()
		children := walkCommands(sub, path, filter)

		// A parent whose children match stays in the tree even when its own
		// name and description do not.
		if !matchesFilter(sub, path, filter) && len(children) == 0 {
			continue
		}

		info := describe(sub, path)
		info.Subcommands = children
		result = append(result, info)
	}
	slices.SortFunc(result, func(a, b commandInfo) int { return strings.Compare(a.Name, b.Name) })
	return result
}

func describe(cmd *cobra.Command, path string) commandInfo {
	info := commandInfo{
		Name:        path,
		Description: cmd.Short,
		Aliases:     cmd.Aliases,
		Flags:       collectFlags(cmd),
	}
	if cmd.Runnable() {
		info.Usage = cmd.UseLine()
	}
	return info
}

func collectFlags(cmd *cobra.Command) []flagInfo {
	var flags []flagInfo
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		flags = append(flags, flagInfo{
			Name:        "--" + f.Name,
			Shorthand:   f.Shorthand,
			Type:        f.Value.Type(),
			Default:     f.DefValue,
			Description: f.Usage,
		})
	})
	slices.SortFunc(flags, func(a, b flagInfo) int { return strings.Compare(a.Name, b.Name) })
	return flags
}

func matchesFilter(cmd *cobra.Command, path, filter string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return true
	}
	for _, field := range []string{cmd.Name(), path, cmd.Short, cmd.Long} {
		if strings.Contains(strings.ToLower(field), filter) {
			return true
		}
	}
	return false
}

func renderCatalog(w io.Writer, catalog []commandInfo) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	var walk func(entries []commandInfo, depth int)
	walk = func(entries []commandInfo, depth int) {
		for _, entry := range entries {
			fmt.Fprintf(tw, "%s%s\t%s\n", strings.Repeat("  ", depth), entry.Name, entry.Description)
			walk(entry.Subcommands, depth+1)
		}
	}
	walk(catalog, 0)
	_ = tw.Flush()
}
