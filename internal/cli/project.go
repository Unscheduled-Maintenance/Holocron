package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

func newProjectCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "project",
		Aliases: []string{"projects", "p"},
		Short:   "Manage projects",
		Long: `Projects are optional context for entries. They are created automatically
the first time you use one (+aws or --project aws). Aliases make capture
faster: a project "Chocolatey Infrastructure" with aliases "infra" and "ops"
can be referred to by any of the three names. Repository paths connect a
project to Git repositories for holocron import git.`,
	}
	cmd.AddCommand(newProjectAddCmd(e), newProjectListCmd(e), newProjectShowCmd(e), newProjectEditCmd(e),
		newProjectArchiveCmd(e, true), newProjectArchiveCmd(e, false), newProjectDeleteCmd(e))
	return cmd
}

func projectArgCompletion(e *env) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	pc := projectCompletion(e)
	return func(cmd *cobra.Command, args []string, s string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return pc(cmd, args, s)
	}
}

func newProjectAddCmd(e *env) *cobra.Command {
	var n journal.NewProject
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create a project",
		Example: `  holocron project add aws
  holocron project add "Chocolatey Infrastructure" --alias infra --alias ops --path ~/src/infra`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.open(cmd.Context())
			if err != nil {
				return err
			}
			n.Name = args[0]
			for i, p := range n.Paths {
				n.Paths[i] = expandPath(p)
			}
			p, err := a.Store.CreateProject(cmd.Context(), n)
			if err != nil {
				return err
			}
			fmt.Fprintf(e.io.Out, "%s project %s\n", e.out().Success("Created"), e.out().Project(p.Name))
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&n.Aliases, "alias", nil, "alternative name (repeatable)")
	cmd.Flags().StringVar(&n.Description, "description", "", "short description")
	cmd.Flags().StringSliceVar(&n.Paths, "path", nil, "local repository path (repeatable)")
	cmd.Flags().StringSliceVar(&n.URLs, "url", nil, "related URL (repeatable)")
	return cmd
}

func newProjectListCmd(e *env) *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List projects, most recently active first",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.open(cmd.Context())
			if err != nil {
				return err
			}
			ps, err := a.Store.ListProjects(cmd.Context(), all)
			if err != nil {
				return err
			}
			if asJSON {
				out := make([]map[string]any, len(ps))
				for i, p := range ps {
					out[i] = projectJSON(p)
				}
				return writeJSON(e.io.Out, out)
			}
			if len(ps) == 0 {
				e.hint("No projects yet. They are created when first used: holocron add \"...\" --project NAME")
				return nil
			}
			st := e.out()
			width := 0
			for _, p := range ps {
				width = max(width, len([]rune(p.Name)))
			}
			for _, p := range ps {
				last := "no entries"
				if p.LastEntry != nil {
					last = "last " + relativeDay(a, *p.LastEntry)
				}
				line := fmt.Sprintf("%s  %s", st.Project(padRight(p.Name, width)), st.Dim(fmt.Sprintf("%5d %-7s  %s", p.EntryCount, plural(p.EntryCount, "entry", "entries"), last)))
				if len(p.Aliases) > 0 {
					line += "  " + st.Faint("aka "+strings.Join(p.Aliases, ", "))
				}
				if p.Archived() {
					line += "  " + st.Warn("archived")
				}
				fmt.Fprintln(e.io.Out, line)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "include archived projects")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func projectJSON(p journal.Project) map[string]any {
	m := map[string]any{
		"uid": p.UID, "name": p.Name, "description": p.Description,
		"aliases": nonNilStrings(p.Aliases), "repository_paths": nonNilStrings(p.Paths), "urls": nonNilStrings(p.URLs),
		"archived": p.Archived(), "entry_count": p.EntryCount,
		"created_at": p.CreatedAt.UTC().Format(time.RFC3339),
	}
	if p.LastEntry != nil {
		m["last_entry_at"] = p.LastEntry.UTC().Format(time.RFC3339)
	}
	return m
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func newProjectShowCmd(e *env) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:               "show <name>",
		Short:             "Show a project and its recent entries",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: projectArgCompletion(e),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := e.open(ctx)
			if err != nil {
				return err
			}
			p, err := a.Store.Project(ctx, args[0])
			if err != nil {
				return err
			}
			count, _ := a.Store.Count(ctx, journal.Query{Projects: []string{p.Name}})
			p.EntryCount = count
			if asJSON {
				return writeJSON(e.io.Out, projectJSON(p))
			}
			st := e.out()
			fmt.Fprintf(e.io.Out, "%s %s\n", st.Accent("◆"), st.Title(p.Name))
			row := func(label, v string) {
				if v != "" {
					fmt.Fprintf(e.io.Out, "  %s %s\n", st.Dim(padRight(label, 12)), v)
				}
			}
			row("Description", p.Description)
			row("Aliases", strings.Join(p.Aliases, ", "))
			row("Repositories", strings.Join(p.Paths, "\n"+strings.Repeat(" ", 15)))
			row("URLs", strings.Join(p.URLs, "\n"+strings.Repeat(" ", 15)))
			row("Entries", fmt.Sprint(count))
			if p.Archived() {
				row("Archived", p.ArchivedAt.In(a.Loc).Format("2006-01-02"))
			}
			recent, err := a.Store.Find(ctx, journal.Query{Projects: []string{p.Name}, Limit: 10})
			if err != nil {
				return err
			}
			if len(recent) > 0 {
				fmt.Fprintln(e.io.Out, "\n"+st.Heading("RECENT"))
				renderList(e.io.Out, a, st, recent, listOptions{width: e.io.Width})
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

func newProjectEditCmd(e *env) *cobra.Command {
	var (
		name, desc                     string
		addAlias, rmAlias              []string
		addPath, rmPath, addURL, rmURL []string
	)
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Rename a project or change its aliases, description, paths and URLs",
		Example: `  holocron project edit aws --name "Amazon Web Services" --alias aws
  holocron project edit infra --path ~/src/infrastructure --remove-alias ops`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: projectArgCompletion(e),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.open(cmd.Context())
			if err != nil {
				return err
			}
			var p journal.ProjectPatch
			if cmd.Flags().Changed("name") {
				p.Name = &name
			}
			if cmd.Flags().Changed("description") {
				p.Description = &desc
			}
			p.AddAliases, p.RemoveAliases = addAlias, rmAlias
			for _, v := range addPath {
				p.AddPaths = append(p.AddPaths, expandPath(v))
			}
			for _, v := range rmPath {
				p.RemovePaths = append(p.RemovePaths, expandPath(v))
			}
			p.AddURLs, p.RemoveURLs = addURL, rmURL
			if p.Name == nil && p.Description == nil && len(addAlias)+len(rmAlias)+len(addPath)+len(rmPath)+len(addURL)+len(rmURL) == 0 {
				return usagef("nothing to change: use --name, --description, --alias, --remove-alias, --path, --remove-path, --url or --remove-url")
			}
			updated, err := a.Store.UpdateProject(cmd.Context(), args[0], p)
			if err != nil {
				return err
			}
			fmt.Fprintf(e.io.Out, "%s project %s\n", e.out().Success("Updated"), e.out().Project(updated.Name))
			return nil
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&name, "name", "", "rename the project")
	fs.StringVar(&desc, "description", "", "set the description")
	fs.StringSliceVar(&addAlias, "alias", nil, "add an alias")
	fs.StringSliceVar(&rmAlias, "remove-alias", nil, "remove an alias")
	fs.StringSliceVar(&addPath, "path", nil, "add a repository path")
	fs.StringSliceVar(&rmPath, "remove-path", nil, "remove a repository path")
	fs.StringSliceVar(&addURL, "url", nil, "add a URL")
	fs.StringSliceVar(&rmURL, "remove-url", nil, "remove a URL")
	return cmd
}

func newProjectArchiveCmd(e *env, archive bool) *cobra.Command {
	use, short, verb := "archive <name>", "Hide a finished project from lists (entries are kept)", "Archived"
	if !archive {
		use, short, verb = "unarchive <name>", "Restore an archived project", "Unarchived"
	}
	return &cobra.Command{
		Use:               use,
		Short:             short,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: projectArgCompletion(e),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.open(cmd.Context())
			if err != nil {
				return err
			}
			p, err := a.Store.UpdateProject(cmd.Context(), args[0], journal.ProjectPatch{Archived: &archive})
			if err != nil {
				return err
			}
			fmt.Fprintf(e.io.Out, "%s project %s\n", e.out().Success(verb), e.out().Project(p.Name))
			return nil
		},
	}
}

func newProjectDeleteCmd(e *env) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:               "delete <name>",
		Short:             "Delete a project; its entries are kept without a project",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: projectArgCompletion(e),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := e.open(ctx)
			if err != nil {
				return err
			}
			p, err := a.Store.Project(ctx, args[0])
			if err != nil {
				return err
			}
			if !yes {
				if !e.io.InTTY {
					return usagef("refusing to delete without confirmation; pass --yes")
				}
				n, _ := a.Store.Count(ctx, journal.Query{Projects: []string{p.Name}})
				if !confirm(e, fmt.Sprintf("Delete project %q? Its %d entries are kept but will have no project.", p.Name, n)) {
					e.note("Nothing deleted.")
					return nil
				}
			}
			n, err := a.Store.DeleteProject(ctx, p.Name)
			if err != nil {
				return err
			}
			fmt.Fprintf(e.io.Out, "%s project %s (%d entries now have no project)\n", e.out().Warn("Deleted"), p.Name, n)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking")
	return cmd
}

func newTagCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "tag",
		Aliases: []string{"tags"},
		Short:   "List and rename tags",
	}
	var asJSON bool
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List tags with how often they are used",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.open(cmd.Context())
			if err != nil {
				return err
			}
			tags, err := a.Store.ListTags(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				out := make([]map[string]any, len(tags))
				for i, t := range tags {
					out[i] = map[string]any{"name": t.Name, "count": t.Count}
				}
				return writeJSON(e.io.Out, out)
			}
			if len(tags) == 0 {
				e.hint("No tags yet. Add them with --tag or #tag in quoted text.")
				return nil
			}
			st := e.out()
			width := 0
			for _, t := range tags {
				width = max(width, len([]rune(t.Name))+1)
			}
			for _, t := range tags {
				fmt.Fprintf(e.io.Out, "%s  %s\n", st.Tag(padRight("#"+t.Name, width)), st.Dim(fmt.Sprintf("%5d", t.Count)))
			}
			return nil
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	rename := &cobra.Command{
		Use:     "rename <old> <new>",
		Short:   "Rename a tag everywhere (merging if the new tag exists)",
		Example: "  holocron tag rename sec security",
		Args:    cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, s string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return tagCompletion(e)(cmd, args, s)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.open(cmd.Context())
			if err != nil {
				return err
			}
			n, err := a.Store.RenameTag(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Fprintf(e.io.Out, "%s #%s to #%s on %d %s\n", e.out().Success("Renamed"),
				strings.TrimPrefix(args[0], "#"), strings.TrimPrefix(args[1], "#"), n, plural(n, "entry", "entries"))
			return nil
		},
	}
	cmd.AddCommand(list, rename)
	return cmd
}
