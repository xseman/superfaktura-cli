package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/spf13/cobra"
)

// SURFACE.txt is a golden snapshot of the command tree: every command,
// subcommand, flag and positional argument. It is not documentation — it is a
// tripwire. Renaming a flag or dropping a subcommand breaks somebody's script,
// so that change has to be deliberate and visible in a diff.
//
// Regenerate with: make surface-snapshot
const surfaceFile = "SURFACE.txt"

func surfacePath(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	return filepath.Join(root, surfaceFile)
}

func TestSurfaceSnapshot(t *testing.T) {
	path := surfacePath(t)
	current := SnapshotString(rootCmd)

	if os.Getenv("UPDATE_SURFACE") != "" {
		if err := os.WriteFile(path, []byte(current+"\n"), 0o644); err != nil {
			t.Fatalf("write snapshot: %v", err)
		}
		t.Logf("wrote %s", path)
		return
	}

	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\nRegenerate it with: make surface-snapshot", surfaceFile, err)
	}

	// Compare the lines verbatim. Parsing them back into surface.Entry and
	// re-rendering would garble the report — the point of this gate is that a
	// reviewer can read exactly what changed.
	added, removed := diffLines(lines(string(stored)), lines(current))
	if len(added) == 0 && len(removed) == 0 {
		return
	}

	var report strings.Builder
	report.WriteString("the CLI surface changed\n")
	for _, line := range removed {
		report.WriteString("  - " + line + "\n")
	}
	for _, line := range added {
		report.WriteString("  + " + line + "\n")
	}
	report.WriteString("\nIf this is intended, run 'make surface-snapshot' and commit SURFACE.txt.")
	if len(removed) > 0 {
		report.WriteString("\nRemovals break existing scripts — say so in the release notes.")
	}
	t.Fatal(report.String())
}

func lines(text string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func diffLines(before, after []string) (added, removed []string) {
	old := make(map[string]bool, len(before))
	for _, line := range before {
		old[line] = true
	}
	current := make(map[string]bool, len(after))
	for _, line := range after {
		current[line] = true
		if !old[line] {
			added = append(added, line)
		}
	}
	for _, line := range before {
		if !current[line] {
			removed = append(removed, line)
		}
	}
	return added, removed
}

// TestEveryCommandDeclaresItsArgumentCount guards against a class of bug the
// surface snapshot cannot see: a runnable command with no Args validator
// silently accepts any number of positional arguments and ignores the extras.
func TestEveryCommandDeclaresItsArgumentCount(t *testing.T) {
	var missing []string
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Runnable() && cmd.Args == nil && !cmd.Hidden {
			missing = append(missing, cmd.CommandPath())
		}
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)

	// Cobra's generated commands are outside our control.
	filtered := missing[:0]
	for _, path := range missing {
		if strings.Contains(path, "completion") || strings.Contains(path, "help") {
			continue
		}
		filtered = append(filtered, path)
	}

	if len(filtered) > 0 {
		t.Errorf("commands without an Args validator: %s", strings.Join(filtered, ", "))
	}
}

func TestCreateAndEditOfferTheSameFields(t *testing.T) {
	// They drifted apart once already: create could set a client and a type
	// that edit could never correct, and edit an issue date that create could
	// not — so a back-dated invoice had to be made today and then edited.
	// Sharing one flag set is what stops it happening again; this catches
	// anyone binding a flag to only one of the pair.
	for _, pair := range []struct {
		create, edit []string
		// creationOnly are the flags that mean nothing after the fact.
		creationOnly []string
	}{
		{
			create:       []string{"invoice", "create"},
			edit:         []string{"invoice", "edit"},
			creationOnly: []string{"checksum", "item", "new-client"},
		},
		{
			create: []string{"expense", "add"},
			edit:   []string{"expense", "edit"},
			// Same reason as the invoice pair: /expenses/edit appends line
			// items, so the flag belongs to creation alone. This list was empty
			// once, and the test passed vacuously while `expense edit --item`
			// quietly doubled an expense's contents.
			creationOnly: []string{"item"},
		},
	} {
		create := localFlagNames(t, pair.create...)
		edit := localFlagNames(t, pair.edit...)

		for name := range edit {
			if !create[name] {
				t.Errorf("%v can set --%s and %v cannot",
					pair.edit, name, pair.create)
			}
		}
		allowed := map[string]bool{}
		for _, name := range pair.creationOnly {
			allowed[name] = true
		}
		for name := range create {
			if !edit[name] && !allowed[name] {
				t.Errorf("%v can set --%s and %v cannot; if that is deliberate, "+
					"name it in creationOnly", pair.create, name, pair.edit)
			}
		}
	}
}

func localFlagNames(t *testing.T, path ...string) map[string]bool {
	t.Helper()
	cmd := findCommand(path...)
	if cmd == nil {
		t.Fatalf("no such command: sf %s", strings.Join(path, " "))
	}
	names := map[string]bool{}
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if f.Name != "help" {
			names[f.Name] = true
		}
	})
	return names
}

func TestAddingItemsIsNotAnEditFlag(t *testing.T) {
	// /invoices/edit appends line items rather than replacing them — measured,
	// API-DISCREPANCIES §B7. An --item on edit would read as "these are the
	// items" and would duplicate them, so the append carries its own name.
	for _, doc := range []struct{ resource, create string }{
		{"invoice", "create"},
		{"expense", "add"},
	} {
		if localFlagNames(t, doc.resource, "edit")["item"] {
			t.Errorf("--item is on %s edit; it would double the record's contents", doc.resource)
		}
		if !localFlagNames(t, doc.resource, doc.create)["item"] {
			t.Errorf("%s %s cannot take line items", doc.resource, doc.create)
		}
		for _, verb := range []string{"list", "add", "delete"} {
			if findCommand(doc.resource, "item", verb) == nil {
				t.Errorf("sf %s item %s does not exist", doc.resource, verb)
			}
		}
	}
}
