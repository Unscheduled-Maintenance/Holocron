package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

type listFlags struct {
	filterFlags
	limit   int
	json    bool
	reverse bool
	sort    string
}

func (f *listFlags) registerOutput(cmd *cobra.Command, defLimit int) {
	cmd.Flags().IntVarP(&f.limit, "limit", "n", defLimit, "maximum entries to show (0 for no limit)")
	cmd.Flags().BoolVar(&f.json, "json", false, "print entries as JSON")
	cmd.Flags().BoolVar(&f.reverse, "reverse", false, "reverse the order")
}

// newDayCmd builds today, yesterday and week: chronological views of a
// fixed range that still accept the other filters.
func newDayCmd(e *env, name, short, expr string) *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{
		Use:   name,
		Short: short,
		Long: short + `, oldest first. Accepts the same filters as list.
Use --range to look at a different period with the same layout.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.open(cmd.Context())
			if err != nil {
				return err
			}
			def, _ := a.Clock().Parse(expr)
			return runList(cmd.Context(), e, f, "", def, journal.OldestFirst)
		},
	}
	f.register(cmd, true)
	f.registerOutput(cmd, 0)
	registerDynamicCompletion(cmd, e)
	return cmd
}

func newListCmd(e *env) *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List entries, newest first",
		Long: `List entries, newest first, filtered by date range, project, tag, type or mark.

Date ranges: --range takes a whole period (today, yesterday, 7d, 30d,
this-week, last-week, this-month, last-month, this-quarter, last-quarter,
this-year, YYYY-MM-DD, YYYY-MM, YYYY-Qn, all). --since starts an open-ended
range. --from and --to are inclusive calendar days.`,
		Example: `  holocron list --since 7d
  holocron list --project aws --type decision
  holocron list --tag security --from 2026-09-01 --to 2026-09-30
  holocron list --open
  holocron list --json --limit 0 > entries.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd.Context(), e, f, "", timerange.All(), journal.NewestFirst)
		},
	}
	f.register(cmd, true)
	f.registerOutput(cmd, 50)
	registerDynamicCompletion(cmd, e)
	return cmd
}

func newSearchCmd(e *env) *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{
		Use:     "search [words...]",
		Aliases: []string{"find", "s"},
		Short:   "Full-text search across the archive",
		Long: `Search entry text, projects and tags, combined with any filters.

  word          matches words beginning with "word"; common endings are
                relaxed, so "investigating" also finds "investigated"
  "two words"   matches the exact phrase (quote it for your shell too)
  -word         excludes entries containing word
  a OR b        matches either
  +project      filters by project, #tag filters by tag (inside quotes)

Results are newest first; --sort relevance ranks by match quality.
With no words, search simply applies the filters.`,
		Example: `  holocron search cloudtrail
  holocron search "access analyzer" --since 30d
  holocron search --project aws --type decision
  holocron search 'exporter restart -cloudflare'
  holocron search "bucket +aws #security"
  holocron search --tag security --from 2026-09-01 --to 2026-09-30`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd.Context(), e, f, strings.Join(args, " "), timerange.All(), journal.NewestFirst)
		},
	}
	f.register(cmd, true)
	f.registerOutput(cmd, 50)
	cmd.Flags().StringVar(&f.sort, "sort", "newest", "result order: newest, oldest or relevance")
	_ = cmd.RegisterFlagCompletionFunc("sort", fixedCompletion("newest", "oldest", "relevance"))
	registerDynamicCompletion(cmd, e)
	return cmd
}

func runList(ctx context.Context, e *env, f *listFlags, text string, def timerange.Range, order journal.Order) error {
	a, err := e.open(ctx)
	if err != nil {
		return err
	}
	q, err := f.query(a, def)
	if err != nil {
		return err
	}
	if text != "" {
		t, projects, tags := journal.SplitSearch(text)
		q.Text = t
		q.Projects = append(q.Projects, projects...)
		q.Tags = append(q.Tags, tags...)
	}
	switch strings.ToLower(f.sort) {
	case "", "newest":
	case "oldest":
		order = journal.OldestFirst
	case "relevance", "rank":
		order = journal.Relevance
	default:
		return usagef("--sort must be newest, oldest or relevance")
	}
	if f.reverse {
		switch order {
		case journal.OldestFirst:
			order = journal.NewestFirst
		default:
			order = journal.OldestFirst
		}
	}
	q.Order = order
	if f.limit < 0 {
		return usagef("--limit must not be negative")
	}
	q.Limit = f.limit
	entries, err := a.Store.Find(ctx, q)
	if err != nil {
		return err
	}
	if f.json {
		return writeJSON(e.io.Out, entriesJSON(entries))
	}
	if len(entries) == 0 {
		e.hint("No entries for %s.", f.describe(q))
		return nil
	}
	renderList(e.io.Out, a, e.out(), entries, listOptions{width: e.io.Width, snippets: q.Text != ""})
	if f.limit > 0 && len(entries) == f.limit {
		if total, err := a.Store.Count(ctx, q); err == nil && total > len(entries) {
			e.hint("\nShowing %d of %d entries; use --limit 0 to see all.", len(entries), total)
		}
	}
	return nil
}

func newShowCmd(e *env) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:               "show <id>...",
		Short:             "Show entries in full",
		Example:           "  holocron show 42\n  holocron show 42 43 --json",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: entryCompletion(e),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.open(cmd.Context())
			if err != nil {
				return err
			}
			var list []journal.Entry
			for _, ref := range args {
				en, err := a.Store.Get(cmd.Context(), ref)
				if err != nil {
					return err
				}
				list = append(list, en)
			}
			if asJSON {
				if len(list) == 1 {
					return writeJSON(e.io.Out, entriesJSON(list)[0])
				}
				return writeJSON(e.io.Out, entriesJSON(list))
			}
			for i, en := range list {
				if i > 0 {
					fmt.Fprintln(e.io.Out)
				}
				renderDetail(e.io.Out, a, e.out(), en, e.markdownRenderer())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

// entryCompletion offers recent entry IDs with their titles.
func entryCompletion(e *env) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		a, err := e.open(cmd.Context())
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		es, err := a.Store.Find(cmd.Context(), journal.Query{Limit: 30})
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		out := make([]string, len(es))
		for i, en := range es {
			out[i] = fmt.Sprintf("%d\t%s", en.ID, truncate(en.Title(), 50))
		}
		return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
	}
}
