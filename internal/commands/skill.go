package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/output"
	"github.com/xseman/superfaktura-cli/skills"
)

func init() {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Print or install the agent skill for this CLI",
		Long: `The skill tells a coding agent how to drive sf: the output contract, the
exit codes, and the parts of the API that bite.

It is embedded in the binary, so this works from a release tarball without
cloning the repository.`,
	}
	rootCmd.AddCommand(cmd)

	show := &cobra.Command{
		Use:   "show",
		Short: "Print the skill to stdout",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, err := io.WriteString(outw, skills.SuperFaktura)
			return err
		},
	}

	var dir string
	install := &cobra.Command{
		Use:   "install",
		Short: "Write the skill into an agent's skills directory",
		Args:  cobra.NoArgs,
		Example: "  sf skill install\n" +
			"  sf skill install --dir .claude/skills",
		RunE: func(*cobra.Command, []string) error {
			target := dir
			if target == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return &output.Error{Code: output.CodeUsage, Message: err.Error()}
				}
				target = filepath.Join(home, ".claude", "skills")
			}

			path := filepath.Join(target, skills.Name, "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				return &output.Error{Code: output.CodeUsage, Message: err.Error()}
			}
			if err := os.WriteFile(path, []byte(skills.SuperFaktura), 0o644); err != nil { //nolint:gosec // G306: agents read this, it is not a secret
				return &output.Error{Code: output.CodeUsage, Message: err.Error()}
			}

			return emitAction(map[string]any{"path": path},
				fmt.Sprintf("Installed the skill at %s", path))
		},
	}
	install.Flags().StringVar(&dir, "dir", "", "Skills directory (default ~/.claude/skills)")

	cmd.AddCommand(show, install)
}
