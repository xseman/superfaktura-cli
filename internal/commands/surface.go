package commands

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The surface snapshot is a flat, sorted description of the command tree:
// every command, subcommand, flag and positional argument, one per line. It
// exists so that renaming a flag or dropping a subcommand shows up as a diff
// in review rather than as a broken script in somebody's cron.
//
// Lines are deliberately plain text. Anything richer would need parsing to
// compare, and a garbled diff defeats the point.

// argPattern matches the bracketed tokens in a cobra Use string:
// <required>, [optional], <name>... and [name]...
var argPattern = regexp.MustCompile(`([<\[])([^>\]]+)([>\]])(\.\.\.)?`)

// Snapshot renders the command tree as sorted lines.
func Snapshot(root *cobra.Command) []string {
	var entries []string
	walkSurface(root, root.Name(), &entries)
	slices.Sort(entries)
	return entries
}

// SnapshotString renders the tree as a single newline-joined document.
func SnapshotString(root *cobra.Command) string {
	return strings.Join(Snapshot(root), "\n")
}

func walkSurface(cmd *cobra.Command, path string, entries *[]string) {
	if cmd.Hidden {
		return
	}
	*entries = append(*entries, "CMD "+path)

	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		*entries = append(*entries, fmt.Sprintf("FLAG %s --%s type=%s", path, f.Name, f.Value.Type()))
	})

	// Positional arguments are described only by the Use string, which is what
	// a caller reads, so that is what is recorded.
	if cmd.Runnable() {
		for i, arg := range argPattern.FindAllString(cmd.Use, -1) {
			*entries = append(*entries, fmt.Sprintf("ARG %s %02d %s", path, i, arg))
		}
	}

	for _, sub := range cmd.Commands() {
		if sub.Name() == "help" {
			continue
		}
		*entries = append(*entries, fmt.Sprintf("SUB %s %s", path, sub.Name()))
		walkSurface(sub, path+" "+sub.Name(), entries)

		// Aliases are part of the surface too: somebody's script may use one.
		for _, alias := range sub.Aliases {
			*entries = append(*entries, fmt.Sprintf("ALIAS %s %s -> %s", path, alias, sub.Name()))
		}
	}
}
