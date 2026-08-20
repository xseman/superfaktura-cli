package commands

import (
	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"github.com/xseman/superfaktura-cli/internal/client"
	"github.com/xseman/superfaktura-cli/internal/output"
	"github.com/xseman/superfaktura-cli/internal/render"
	"github.com/xseman/superfaktura-cli/internal/tui"
)

var clientColumns = []render.Column{
	{Header: "ID", Path: "Client.id"},
	{Header: "Name", Path: "Client.name", Format: render.Truncate(32)},
	{Header: "IČO", Path: "Client.ico"},
	{Header: "IČ DPH", Path: "Client.ic_dph"},
	{Header: "City", Path: "Client.city", Format: render.Truncate(18)},
	{Header: "Email", Path: "Client.email", Format: render.Truncate(28)},
}

var clientFields = []render.Field{
	{Label: "ID", Path: "Client.id"},
	{Label: "Name", Path: "Client.name"},
	{Label: "IČO", Path: "Client.ico"},
	{Label: "DIČ", Path: "Client.dic"},
	{Label: "IČ DPH", Path: "Client.ic_dph"},
	{Label: "Address", Path: "Client.address"},
	{Label: "City", Path: "Client.city"},
	{Label: "ZIP", Path: "Client.zip"},
	{Label: "Country", Path: "Client.country"},
	{Label: "Email", Path: "Client.email"},
	{Label: "Phone", Path: "Client.phone"},
	{Label: "IBAN", Path: "Client.iban"},
	{Label: "Due days", Path: "Client.due_date"},
	{Label: "Comment", Path: "Client.comment"},
}

// clientWriteFlags are the fields shared by client create and edit.
type clientWriteFlags struct {
	name, ico, dic, icDPH  string
	address, city, zip     string
	email, phone, comment  string
	country, iban, dueDate string
	tags                   []string
}

func (c *clientWriteFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&c.name, "name", "", "Client name")
	f.StringVar(&c.ico, "ico", "", "IČO (company registration number)")
	f.StringVar(&c.dic, "dic", "", "DIČ (tax ID)")
	f.StringVar(&c.icDPH, "ic-dph", "", "IČ DPH (VAT ID)")
	f.StringVar(&c.address, "address", "", "Street address")
	f.StringVar(&c.city, "city", "", "City")
	f.StringVar(&c.zip, "zip", "", "Postal code")
	f.StringVar(&c.country, "country-id", "", "Country ID (see 'sf values countries')")
	f.StringVar(&c.email, "email", "", "Contact email")
	f.StringVar(&c.phone, "phone", "", "Phone number")
	f.StringVar(&c.iban, "iban", "", "IBAN")
	f.StringVar(&c.dueDate, "due-days", "", "Default payment term in days")
	f.StringVar(&c.comment, "comment", "", "Comment")
	tagFlag(cmd, &c.tags)
}

func (c *clientWriteFlags) apply(cmd *cobra.Command, doc map[string]any) error {
	put(doc, "Client", "name", c.name)
	put(doc, "Client", "ico", c.ico)
	put(doc, "Client", "dic", c.dic)
	put(doc, "Client", "ic_dph", c.icDPH)
	put(doc, "Client", "address", c.address)
	put(doc, "Client", "city", c.city)
	put(doc, "Client", "zip", c.zip)
	put(doc, "Client", "country_id", c.country)
	put(doc, "Client", "email", c.email)
	put(doc, "Client", "phone", c.phone)
	put(doc, "Client", "iban", c.iban)
	put(doc, "Client", "due_date", c.dueDate)
	put(doc, "Client", "comment", c.comment)

	tagIDs, err := resolveTags(cmd, c.tags)
	if err != nil {
		return err
	}
	putTags(doc, tagIDs)
	return nil
}

var clientCmd = &cobra.Command{
	Use:   "client",
	Short: "Manage clients",
}

func init() {
	rootCmd.AddCommand(clientCmd)
	clientCmd.AddCommand(
		clientListCmd(),
		clientViewCmd(),
		clientCreateCmd(),
		clientEditCmd(),
		clientDeleteCmd(),
		contactCmd(),
	)
}

func clientListCmd() *cobra.Command {
	opts := &listOptions{}
	var ico, tag string

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List clients",
		Args:    cobra.NoArgs,
		Example: "  sf client list --search 'Acme'\n  sf client list --ico 46655034 --json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			extra := client.Params{}
			extra.Set("ico", ico)
			extra.Set("tag", tag)

			result, err := fetchList(ctx(cmd), "/clients/index.json", opts, extra)
			if err != nil {
				return err
			}
			return emitList(result, clientColumns)
		},
	}

	opts.bind(cmd)
	cmd.Flags().StringVar(&ico, "ico", "", "Filter by IČO")
	cmd.Flags().StringVar(&tag, "tag", "", "Filter by tag ID")
	return cmd
}

func clientViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "view <id>",
		Aliases: []string{"show"},
		Short:   "Show one client",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("client", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/clients/view/"+id, nil)
			if err != nil {
				return err
			}
			obj, err := decodeObject(raw)
			if err != nil {
				return err
			}
			return emitObject(obj, clientFields)
		},
	}
}

func clientCreateCmd() *cobra.Command {
	var data string
	flags := &clientWriteFlags{}

	cmd := &cobra.Command{
		Use:     "create",
		Aliases: []string{"add"},
		Short:   "Create a client",
		Args:    cobra.NoArgs,
		Example: "  sf client create --name 'Acme s.r.o.' --ico 46655034\n  sf client create --data @client.json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := readPayload(data)
			if err != nil {
				return err
			}
			if err := flags.apply(cmd, doc); err != nil {
				return err
			}

			if len(doc) == 0 {
				if !interactive() {
					return &output.Error{
						Code:    output.CodeUsage,
						Message: "nothing to create",
						Hint:    "Pass --name, or --data",
					}
				}
				if err := promptClient(flags); err != nil {
					return err
				}
				if err := flags.apply(cmd, doc); err != nil {
					return err
				}
			}

			if err := requirePayload(doc, "Pass --name, or --data"); err != nil {
				return err
			}
			raw, err := api.PostJSON(ctx(cmd), "/clients/create", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "Client", "Client created",
				output.Step{Cmd: "sf invoice create --client %s --item 'ITEM:QTY:PRICE:VAT'", Does: "bill them"},
				output.Step{Cmd: "sf client view %s", Does: "see the record"})
		},
	}

	dataFlag(cmd, &data)
	flags.bind(cmd)
	return cmd
}

func promptClient(flags *clientWriteFlags) error {
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Name").Value(&flags.name).Validate(required("name")),
		huh.NewInput().Title("IČO").Description("Optional").Value(&flags.ico),
		huh.NewInput().Title("IČ DPH").Description("Optional VAT ID").Value(&flags.icDPH),
		huh.NewInput().Title("Email").Description("Optional").Value(&flags.email),
		huh.NewInput().Title("Address").Description("Optional").Value(&flags.address),
		huh.NewInput().Title("City").Description("Optional").Value(&flags.city),
	))
	form = form.WithTheme(tui.FormTheme())
	if err := form.Run(); err != nil {
		return &output.Error{Code: output.CodeUsage, Message: err.Error()}
	}
	return nil
}

func clientEditCmd() *cobra.Command {
	var data string
	flags := &clientWriteFlags{}

	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit a client",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("client", args[0])
			if err != nil {
				return err
			}
			doc, err := readPayload(data)
			if err != nil {
				return err
			}
			if err := flags.apply(cmd, doc); err != nil {
				return err
			}
			put(doc, "Client", "id", id)

			raw, err := api.PostJSON(ctx(cmd), "/clients/edit/"+id, doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "Client", "Client "+id+" updated")
		},
	}

	dataFlag(cmd, &data)
	flags.bind(cmd)
	return cmd
}

func clientDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "Delete a client",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("client", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/clients/delete/"+id, nil)
			if err != nil {
				return err
			}
			return emitWrite(raw, "Client", "Client "+id+" deleted")
		},
	}
}

// contactCmd manages the contact people attached to a client.
func contactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "contact",
		Short: "Manage contact people for a client",
	}

	list := &cobra.Command{
		Use:     "list <client-id>",
		Aliases: []string{"ls"},
		Short:   "List a client's contact people",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("client", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/contact_people/getContactPeople/"+id, nil)
			if err != nil {
				return err
			}
			result, err := decodeList(raw)
			if err != nil {
				return err
			}
			return emitList(result, []render.Column{
				{Header: "ID", Path: "ContactPerson.id"},
				{Header: "Name", Path: "ContactPerson.name"},
				{Header: "Email", Path: "ContactPerson.email"},
				{Header: "Phone", Path: "ContactPerson.phone"},
			})
		},
	}

	var data, name, email, phone string
	add := &cobra.Command{
		Use:   "add <client-id>",
		Short: "Add a contact person to a client",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("client", args[0])
			if err != nil {
				return err
			}
			doc, err := readPayload(data)
			if err != nil {
				return err
			}
			put(doc, "ContactPerson", "client_id", id)
			put(doc, "ContactPerson", "name", name)
			put(doc, "ContactPerson", "email", email)
			put(doc, "ContactPerson", "phone", phone)

			raw, err := api.Post(ctx(cmd), "/contact_people/add/api:1", doc)
			if err != nil {
				return err
			}
			return emitWrite(raw, "ContactPerson", "Contact person added")
		},
	}
	dataFlag(add, &data)
	add.Flags().StringVar(&name, "name", "", "Contact name")
	add.Flags().StringVar(&email, "email", "", "Contact email")
	add.Flags().StringVar(&phone, "phone", "", "Contact phone")

	remove := &cobra.Command{
		Use:     "delete <contact-id>",
		Aliases: []string{"rm"},
		Short:   "Delete a contact person",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireID("contact person", args[0])
			if err != nil {
				return err
			}
			raw, err := api.Get(ctx(cmd), "/contact_people/delete/"+id, nil)
			if err != nil {
				return err
			}
			return emitWrite(raw, "ContactPerson", "Contact person "+id+" deleted")
		},
	}

	cmd.AddCommand(list, add, remove)
	return cmd
}
