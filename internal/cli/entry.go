package cli

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/editor"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

func newEditCmd(e *env) *cobra.Command {
	var (
		text, project, typ, at string
		addTags, removeTags    []string
		clearProject           bool
		asJSON                 bool
	)
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Change an entry (in your editor, or with flags)",
		Long: `Change an entry.

With no flags, the entry opens in $VISUAL/$EDITOR as a short document: a few
fields (time, project, type, tags, marks) above a --- line, and the text
below it. Save and close to apply; leave it unchanged to cancel.

With flags, the change is applied directly, which suits scripts.`,
		Example: `  holocron edit 42
  holocron edit 42 --type decision --tag architecture
  holocron edit 42 --text "Enabled IAM Access Analyzer in every region" --at "09:10"
  holocron edit 42 --untag draft --no-project`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: entryCompletion(e),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := e.open(ctx)
			if err != nil {
				return err
			}
			en, err := a.Store.Get(ctx, args[0])
			if err != nil {
				return err
			}
			fs := cmd.Flags()
			flagged := fs.Changed("text") || fs.Changed("project") || fs.Changed("type") || fs.Changed("at") ||
				fs.Changed("tag") || fs.Changed("untag") || fs.Changed("no-project")
			var p journal.Patch
			if flagged {
				if fs.Changed("text") {
					p.Body = &text
				}
				if clearProject && fs.Changed("project") {
					return usagef("use either --project or --no-project")
				}
				if fs.Changed("project") {
					p.Project, p.CreateProject = &project, a.Config.CreateProjectsEnabled()
				}
				if clearProject {
					empty := ""
					p.Project = &empty
				}
				if fs.Changed("type") {
					t, err := a.ParseType(typ)
					if err != nil {
						return err
					}
					p.Type = &t
				}
				if fs.Changed("at") {
					t, err := a.Clock().ParseMoment(at)
					if err != nil {
						return usageError{err}
					}
					p.OccurredAt = &t
				}
				p.AddTags, p.RemoveTags = addTags, removeTags
			} else {
				if !e.io.InTTY {
					return usagef("no changes given: pass flags such as --text or --tag, or run interactively to use your editor")
				}
				argv, _, err := editor.Resolve(a.Config.Editor)
				if err != nil {
					return err
				}
				edited, err := editor.Edit(ctx, argv, a.DocFor(&en).Format(fmt.Sprintf("Holocron entry %s (uid %s)", en.Ref(), en.UID)))
				if err != nil {
					return err
				}
				doc, err := app.ParseEntryDoc(edited)
				if err != nil {
					return fmt.Errorf("%w; the entry was not changed", err)
				}
				p, err = a.PatchFromDoc(en, doc)
				if errors.Is(err, app.ErrNoChanges) {
					e.note("No changes; %s left as it was.", en.Ref())
					return nil
				}
				if err != nil {
					return fmt.Errorf("%w; the entry was not changed", err)
				}
			}
			updated, err := a.Store.Update(ctx, en.ID, p)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(e.io.Out, entriesJSON([]journal.Entry{updated})[0])
			}
			st := e.out()
			fmt.Fprintf(e.io.Out, "%s %s\n", st.Success("Updated"), summaryLine(a, st, updated))
			return nil
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&text, "text", "", "replace the entry text")
	fs.StringVarP(&project, "project", "p", "", "set the project (name or alias)")
	fs.BoolVar(&clearProject, "no-project", false, "remove the project")
	fs.StringVar(&typ, "type", "", "set the type (use \"none\" to clear)")
	fs.StringVar(&at, "at", "", "change when it happened")
	fs.StringSliceVarP(&addTags, "tag", "t", nil, "add tags")
	fs.StringSliceVar(&removeTags, "untag", nil, "remove tags")
	fs.BoolVar(&asJSON, "json", false, "print the updated entry as JSON")
	_ = cmd.RegisterFlagCompletionFunc("type", fixedCompletion(append(journal.TypeStrings(), "none")...))
	registerDynamicCompletion(cmd, e)
	return cmd
}

// confirm asks a yes/no question on the terminal. It returns false when
// input is not interactive.
func confirm(e *env, question string) bool {
	if !e.io.InTTY {
		return false
	}
	fmt.Fprintf(e.io.Err, "%s [y/N] ", question)
	line, _ := bufio.NewReader(e.io.In).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func newDeleteCmd(e *env) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <id>...",
		Aliases: []string{"rm"},
		Short:   "Permanently delete entries",
		Long: `Permanently delete entries. You are asked to confirm unless --yes is given;
without a terminal, --yes is required. Deleted entry numbers are never
reused. Take a backup first if in doubt (holocron backup).`,
		Example:           "  holocron delete 42\n  holocron delete 42 43 --yes",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: entryCompletion(e),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := e.open(ctx)
			if err != nil {
				return err
			}
			var list []journal.Entry
			for _, ref := range args {
				en, err := a.Store.Get(ctx, ref)
				if err != nil {
					return err
				}
				list = append(list, en)
			}
			st := e.errStyle()
			if !yes {
				if !e.io.InTTY {
					return usagef("refusing to delete without confirmation; pass --yes to delete non-interactively")
				}
				for _, en := range list {
					fmt.Fprintln(e.io.Err, "  "+summaryLine(a, st, en))
				}
				noun := "this entry"
				if len(list) > 1 {
					noun = fmt.Sprintf("these %d entries", len(list))
				}
				if !confirm(e, "Permanently delete "+noun+"?") {
					e.note("Nothing deleted.")
					return nil
				}
			}
			for _, en := range list {
				if err := a.Store.Delete(ctx, en.ID); err != nil {
					return err
				}
				fmt.Fprintf(e.io.Out, "%s %s %s\n", e.out().Warn("Deleted"), en.Ref(), truncate(en.Title(), 60))
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking")
	return cmd
}

func newMarkCmd(e *env, remove bool) *cobra.Command {
	use, short, verb := "mark <id> <mark>...", "Mark an entry for a report audience", "Marked"
	long := `Attach report marks to an entry. Marks are explicit signals that an entry
belongs in a report, separate from tags:

  staff        include in the staff update
  one-on-one   raise in the next one-on-one
  quarterly    feature in the quarterly report
  important    rank above everything else
  cross-team   relevant to other teams (own section in the staff update)`
	example := "  holocron mark 42 staff\n  holocron mark 42 one-on-one important"
	if remove {
		use, short, verb = "unmark <id> <mark>...", "Remove report marks from an entry", "Unmarked"
		long = "Remove report marks from an entry. Use \"all\" to remove every mark."
		example = "  holocron unmark 42 staff\n  holocron unmark 42 all"
	}
	var asJSON bool
	cmd := &cobra.Command{
		Use:     use,
		Short:   short,
		Long:    long,
		Example: example,
		Args:    cobra.MinimumNArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, s string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return entryCompletion(e)(cmd, args, s)
			}
			return append(journal.MarkStrings(), "all"), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := e.open(ctx)
			if err != nil {
				return err
			}
			en, err := a.Store.Get(ctx, args[0])
			if err != nil {
				return err
			}
			var p journal.Patch
			if remove && len(args) == 2 && strings.EqualFold(args[1], "all") {
				p.Marks = &[]journal.Mark{}
			} else {
				marks, err := journal.ParseMarks(args[1:])
				if err != nil {
					return err
				}
				if remove {
					p.RemoveMarks = marks
				} else {
					p.AddMarks = marks
				}
			}
			updated, err := a.Store.Update(ctx, en.ID, p)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(e.io.Out, entriesJSON([]journal.Entry{updated})[0])
			}
			st := e.out()
			fmt.Fprintf(e.io.Out, "%s %s\n", st.Success(verb), summaryLine(a, st, updated))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the updated entry as JSON")
	return cmd
}

func newResolveCmd(e *env) *cobra.Command {
	var reopen bool
	cmd := &cobra.Command{
		Use:   "resolve <id>...",
		Short: "Mark problems or follow-ups as resolved (or --reopen them)",
		Long: `Problems and follow-ups stay open until resolved. Open items appear in the
staff and one-on-one reports and with --open; resolving them takes them off
those lists without deleting anything.

To record what resolved an item, capture it as a new entry instead:
  holocron add "Engineering approved the ARM capacity" --resolves 42
which resolves #42 and links the two.`,
		Example:           "  holocron resolve 42\n  holocron resolve 42 --reopen\n  holocron list --open",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: entryCompletion(e),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := e.open(ctx)
			if err != nil {
				return err
			}
			st := e.out()
			want := !reopen
			for _, ref := range args {
				en, err := a.Store.Get(ctx, ref)
				if err != nil {
					return err
				}
				if !en.Type.Opens() {
					e.note("Note: %s is a %s, not a problem or follow-up; recording it as resolved anyway.", en.Ref(), typeOrNone(en.Type))
				}
				updated, err := a.Store.Update(ctx, en.ID, journal.Patch{Resolved: &want})
				if err != nil {
					return err
				}
				verb := "Resolved"
				if reopen {
					verb = "Reopened"
				}
				fmt.Fprintf(e.io.Out, "%s %s\n", st.Success(verb), summaryLine(a, st, updated))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&reopen, "reopen", false, "reopen instead of resolving")
	return cmd
}

func typeOrNone(t journal.Type) string {
	if t == "" {
		return "untyped entry"
	}
	return string(t)
}
