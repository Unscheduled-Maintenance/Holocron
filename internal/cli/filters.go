package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

// filterFlags are the selection flags shared by list, search, export and reports.
type filterFlags struct {
	rangeExpr string
	since     string
	from      string
	to        string
	projects  []string
	tags      []string
	types     []string
	marks     []string
	open      bool
}

func (f *filterFlags) register(cmd *cobra.Command, withEntryFilters bool) {
	fs := cmd.Flags()
	fs.StringVarP(&f.rangeExpr, "range", "r", "", "date range: today, yesterday, 7d, this-week, last-month, this-quarter, YYYY-MM-DD, all …")
	fs.StringVar(&f.since, "since", "", "only entries since this range starts, e.g. 30d or last-week (open-ended)")
	fs.StringVar(&f.from, "from", "", "first day to include, inclusive (YYYY-MM-DD or a range expression)")
	fs.StringVar(&f.to, "to", "", "last day to include, inclusive (YYYY-MM-DD or a range expression)")
	fs.StringSliceVarP(&f.projects, "project", "p", nil, "only these projects (name or alias; repeatable; - for entries without a project)")
	_ = cmd.RegisterFlagCompletionFunc("range", fixedCompletion(timerange.Expressions...))
	_ = cmd.RegisterFlagCompletionFunc("since", fixedCompletion("7d", "14d", "30d", "90d", "this-week", "last-week", "this-month", "this-quarter"))
	if !withEntryFilters {
		return
	}
	fs.StringSliceVarP(&f.tags, "tag", "t", nil, "only entries with all of these tags (repeatable)")
	fs.StringSliceVar(&f.types, "type", nil, "only these entry types (repeatable): "+strings.Join(journal.TypeStrings(), ", "))
	fs.StringSliceVar(&f.marks, "mark", nil, "only entries with any of these report marks (repeatable)")
	fs.BoolVar(&f.open, "open", false, "only unresolved problems and follow-ups")
	_ = cmd.RegisterFlagCompletionFunc("type", fixedCompletion(journal.TypeStrings()...))
	_ = cmd.RegisterFlagCompletionFunc("mark", fixedCompletion(journal.MarkStrings()...))
}

// completion helpers that read the archive need the env.
func registerDynamicCompletion(cmd *cobra.Command, e *env) {
	_ = cmd.RegisterFlagCompletionFunc("project", projectCompletion(e))
	if cmd.Flags().Lookup("tag") != nil {
		_ = cmd.RegisterFlagCompletionFunc("tag", tagCompletion(e))
	}
}

func projectCompletion(e *env) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		a, err := e.open(cmd.Context())
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ps, err := a.Store.ListProjects(cmd.Context(), false)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var out []string
		for _, p := range ps {
			out = append(out, p.Name)
			out = append(out, p.Aliases...)
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

func tagCompletion(e *env) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		a, err := e.open(cmd.Context())
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		tags, err := a.Store.ListTags(cmd.Context())
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		out := make([]string, len(tags))
		for i, t := range tags {
			out[i] = t.Name
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// resolveRange combines the date flags with a default.
func (f *filterFlags) resolveRange(a *app.App, def timerange.Range) (timerange.Range, error) {
	r, err := a.Clock().Bounds(f.rangeExpr, f.since, f.from, f.to, def)
	if err != nil {
		return r, usageError{err}
	}
	return r, nil
}

// query builds a journal query from the flags.
func (f *filterFlags) query(a *app.App, def timerange.Range) (journal.Query, error) {
	r, err := f.resolveRange(a, def)
	if err != nil {
		return journal.Query{}, err
	}
	q := journal.Query{Range: r, Projects: f.projects, Tags: f.tags, OpenOnly: f.open}
	for _, t := range splitValues(f.types) {
		typ, err := a.ParseType(t)
		if err != nil {
			return q, err
		}
		if typ != journal.TypeNone {
			q.Types = append(q.Types, typ)
		}
	}
	marks, err := journal.ParseMarks(f.marks)
	if err != nil {
		return q, err
	}
	q.Marks = marks
	return q, nil
}

// describe summarises active filters for headings and export descriptions.
func (f *filterFlags) describe(q journal.Query) string {
	var parts []string
	if !q.Range.IsZero() {
		parts = append(parts, q.Range.Label)
	}
	if len(q.Projects) > 0 {
		parts = append(parts, "project "+strings.Join(q.Projects, ", "))
	}
	if len(q.Tags) > 0 {
		parts = append(parts, "tagged #"+strings.Join(q.Tags, " #"))
	}
	if len(q.Types) > 0 {
		ts := make([]string, len(q.Types))
		for i, t := range q.Types {
			ts[i] = string(t)
		}
		parts = append(parts, "type "+strings.Join(ts, ", "))
	}
	if len(q.Marks) > 0 {
		ms := make([]string, len(q.Marks))
		for i, m := range q.Marks {
			ms[i] = string(m)
		}
		parts = append(parts, "marked "+strings.Join(ms, ", "))
	}
	if q.OpenOnly {
		parts = append(parts, "open only")
	}
	if q.Text != "" {
		parts = append(parts, "matching "+q.Text)
	}
	if len(parts) == 0 {
		return "all entries"
	}
	return strings.Join(parts, "; ")
}

func splitValues(in []string) []string {
	var out []string
	for _, s := range in {
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
