package commands

import (
	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/render"
)

var tagCmd = &cobra.Command{
	Use:   "tag",
	Short: "Manage tags",
	Long:  "Tags can be attached to invoices, expenses and clients.",
}

func init() {
	rootCmd.AddCommand(tagCmd)

	var name string
	add := &cobra.Command{
		Use:     "add <name>",
		Aliases: []string{"create"},
		Short:   "Add a tag",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Tags are the exception to the model-nesting rule: the payload is
			// flat. Wrapping it in a "Tag" key earns "Chýbajúce údaje".
			raw, err := api.Post(ctx(cmd), "/tags/add",
				map[string]any{"name": args[0]})
			if err != nil {
				return err
			}
			return emitWrite(raw, "Tag", "Tag "+args[0]+" added")
		},
	}

	edit := &cobra.Command{
		Use:   "edit <id>",
		Short: "Rename a tag",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("tag", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Post(ctx(cmd), "/tags/edit/"+id,
				map[string]any{"name": name})
			if err != nil {
				return err
			}
			return emitWrite(raw, "Tag", "Tag "+id+" renamed")
		},
	}
	edit.Flags().StringVar(&name, "name", "", "New tag name (required)")
	_ = edit.MarkFlagRequired("name")

	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List tags",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := cachedGet(cmd, "/tags/index.json", nil, valueListTTL)
			if err != nil {
				return err
			}
			// This endpoint answers with an id-to-name map rather than a list.
			result, err := decodeKeyValueList(raw, "Tag", "name")
			if err != nil {
				return err
			}
			return emitList(result, []render.Column{
				{Header: "ID", Path: "Tag.id"},
				{Header: "Name", Path: "Tag.name"},
			})
		},
	}

	remove := &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "Delete a tag",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("tag", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/tags/delete/"+id, nil)
			if err != nil {
				return err
			}
			return emitWrite(raw, "Tag", "Tag "+id+" deleted")
		},
	}

	tagCmd.AddCommand(add, edit, list, remove)
}
