package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/config"
	"github.com/Unscheduled-Maintenance/Holocron/internal/export"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/report"
	"github.com/Unscheduled-Maintenance/Holocron/internal/style"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

type reportFlags struct {
	filterFlags
	date    string
	format  string
	output  string
	ids     bool
	explain bool
	max     int
	ai      bool
	yes     bool
	record  bool
}

func newReportCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "report",
		Aliases: []string{"reports", "r"},
		Short:   "Generate reports from your entries",
		Long: `Generate deterministic reports from your entries. No AI is involved unless
you pass --ai.

  day          chronological record of one day (default: today)
  week         the week's meaningful work grouped by project (default: this week)
  staff        concise team update: completed, decisions, problems, coming up
  one-on-one   discussion items, wins, open problems, recurring friction (default: 14d)
  quarter      evidence of work over a quarter, by project and theme

Every item can be traced to its source entries: --ids shows their numbers and
--explain shows why each item was included. Mark entries for a report with
holocron mark <id> staff|one-on-one|quarterly|important|cross-team.`,
		Example: `  holocron report week
  holocron report staff --range last-week
  holocron report staff --record
  holocron report staff --since last
  holocron report one-on-one --explain
  holocron report quarter --range 2026-Q3 --format markdown -o q3.md
  holocron report day --date yesterday`,
	}
	for _, k := range report.Kinds {
		cmd.AddCommand(newReportKindCmd(e, k))
	}
	return cmd
}

func newReportKindCmd(e *env, kind report.Kind) *cobra.Command {
	f := &reportFlags{}
	use := string(kind)
	var aliases []string
	switch kind {
	case report.OneOnOne:
		aliases = []string{"1on1", "oneonone"}
	case report.Day:
		aliases = []string{"daily"}
	case report.Week:
		aliases = []string{"weekly"}
	case report.Quarter:
		aliases = []string{"quarterly"}
	}
	cmd := &cobra.Command{
		Use:     use,
		Aliases: aliases,
		Short:   kind.Describe(),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReport(cmd.Context(), e, kind, f)
		},
	}
	f.register(cmd, false)
	fs := cmd.Flags()
	if kind == report.Day {
		fs.StringVar(&f.date, "date", "", "the day to report on: today, yesterday or YYYY-MM-DD")
	}
	fs.StringVarP(&f.format, "format", "f", "text", "output format: text, markdown or json")
	fs.StringVarP(&f.output, "output", "o", "", "write to a file instead of stdout")
	fs.BoolVar(&f.ids, "ids", false, "show the entry numbers behind each item")
	fs.BoolVar(&f.explain, "explain", false, "show why each item was included")
	fs.IntVar(&f.max, "max", 0, "items per section (default from config, 8)")
	fs.BoolVar(&f.ai, "ai", false, "rewrite the report with the configured AI provider (sends the selected entries to it)")
	fs.BoolVarP(&f.yes, "yes", "y", false, "with --ai and --output, write without asking for review")
	fs.BoolVar(&f.record, "record", false, "remember this report as sent, so the next one can start after it with --since last")
	_ = cmd.RegisterFlagCompletionFunc("format", fixedCompletion("text", "markdown", "json"))
	registerDynamicCompletion(cmd, e)
	return cmd
}

func runReport(ctx context.Context, e *env, kind report.Kind, f *reportFlags) error {
	a, err := e.open(ctx)
	if err != nil {
		return err
	}
	if f.date != "" {
		if f.rangeExpr != "" {
			return usagef("use either --date or --range")
		}
		f.rangeExpr = f.date
	}
	def, note := a.DefaultReportRange(kind)
	var rng timerange.Range
	if strings.EqualFold(strings.TrimSpace(f.since), "last") {
		if rng, err = sinceLastReport(ctx, a, kind, f); err != nil {
			return err
		}
		note = ""
	} else {
		if rng, err = f.resolveRange(a, def); err != nil {
			return err
		}
		if f.rangeExpr != "" || f.since != "" || f.from != "" || f.to != "" {
			note = ""
		}
	}
	format := strings.ToLower(f.format)
	switch format {
	case "text", "markdown", "md", "json":
	default:
		return usagef("--format must be text, markdown or json")
	}
	rep, err := a.ReportBuilder().Build(ctx, kind, report.Options{Range: rng, Projects: f.projects, MaxItems: f.max, Note: note})
	if err != nil {
		return err
	}
	if f.ai {
		if sent, err := runAIReport(ctx, e, a, rep, f); err != nil || !sent {
			return err
		}
		return recordReport(ctx, e, a, kind, rng, f)
	}

	toFile := f.output != ""
	ro := report.RenderOptions{ShowIDs: f.ids, Explain: f.explain, Loc: a.Loc, TimeLayout: a.TimeLayout(),
		Styler: style.New(!toFile && style.ColorEnabled(e.g.color, e.io.OutTTY))}
	if !toFile {
		ro.Width = e.io.Width
	}
	var buf bytes.Buffer
	switch format {
	case "text":
		err = report.Text(&buf, rep, ro)
	case "markdown", "md":
		err = report.Markdown(&buf, rep, ro)
	case "json":
		err = report.JSON(&buf, rep)
	}
	if err != nil {
		return err
	}
	if err := writeOutput(e, f.output, buf.Bytes(), "report"); err != nil {
		return err
	}
	return recordReport(ctx, e, a, kind, rng, f)
}

// writeOutput writes to a file (refusing to clobber silently) or stdout.
func writeOutput(e *env, path string, data []byte, what string) error {
	if path == "" || path == "-" {
		_, err := e.io.Out.Write(data)
		return err
	}
	path = expandPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", what, err)
	}
	e.note("Wrote %s to %s", what, path)
	return nil
}

func expandPath(p string) string {
	p = config.ExpandHome(p)
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

type exportFlags struct {
	listFlags
	format string
	output string
}

func newExportCmd(e *env) *cobra.Command {
	f := &exportFlags{}
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export entries as Markdown or JSON",
		Long: `Export the archive, or a filtered part of it, as Markdown (readable without
Holocron) or JSON (documented, versioned structure; see docs/export-format.md).
Entries are written oldest first.`,
		Example: `  holocron export --format markdown -o holocron.md
  holocron export --format json -o holocron.json
  holocron export --since 30d
  holocron export --project aws --format markdown`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := e.open(ctx)
			if err != nil {
				return err
			}
			q, err := f.query(a, emptyRange())
			if err != nil {
				return err
			}
			arc, err := a.ExportArchive(ctx, q, f.describe(q))
			if err != nil {
				return err
			}
			var buf bytes.Buffer
			switch strings.ToLower(f.format) {
			case "markdown", "md":
				err = export.Markdown(&buf, arc, export.Options{Loc: a.Loc, TimeLayout: a.TimeLayout()})
			case "json":
				err = export.JSON(&buf, arc)
			default:
				return usagef("--format must be markdown or json")
			}
			if err != nil {
				return err
			}
			if err := writeOutput(e, f.output, buf.Bytes(), fmt.Sprintf("%d entries", len(arc.Entries))); err != nil {
				return err
			}
			return nil
		},
	}
	f.register(cmd, true)
	cmd.Flags().StringVarP(&f.format, "format", "f", "markdown", "markdown or json")
	cmd.Flags().StringVarP(&f.output, "output", "o", "", "write to a file instead of stdout")
	_ = cmd.RegisterFlagCompletionFunc("format", fixedCompletion("markdown", "json"))
	registerDynamicCompletion(cmd, e)
	return cmd
}

func newBackupCmd(e *env) *cobra.Command {
	var output string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Write a consistent, verified copy of the archive",
		Long: `Write a consistent copy of the archive using SQLite's VACUUM INTO, which is
safe while Holocron is running elsewhere. The copy is integrity-checked
before the command succeeds. By default backups go to the backups folder in
the data directory (see holocron config path).`,
		Example: "  holocron backup\n  holocron backup --output ~/backups/holocron.db\n  holocron backup -o /mnt/usb/",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.open(cmd.Context())
			if err != nil {
				return err
			}
			path, info, err := a.Backup(cmd.Context(), output)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(e.io.Out, map[string]any{"path": path, "entries": info.Entries, "projects": info.Projects, "schema_version": info.SchemaVersion, "bytes": info.Size})
			}
			st := e.out()
			fmt.Fprintf(e.io.Out, "%s %s\n", st.Success("Backed up"), path)
			fmt.Fprintf(e.io.Out, "  %s\n", st.Dim(fmt.Sprintf("%d entries, %d projects, %s, integrity check passed", info.Entries, info.Projects, humanBytes(info.Size))))
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "destination file or directory")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func newRestoreCmd(e *env) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "restore <backup-file>",
		Short: "Replace the archive with a backup (the current archive is backed up first)",
		Long: `Replace the live archive with a backup file.

The backup is checked first (it must be an intact Holocron archive). The
current archive is then copied to the backups folder as
holocron-pre-restore-<time>.db, so a restore can itself be undone. Older
backups are migrated to the current schema after restoring.

Close any other running Holocron (such as the TUI) before restoring. You are
asked to confirm; without a terminal, --force is required.`,
		Example: "  holocron restore ~/backups/holocron-20261005-090000.db",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			src := expandPath(args[0])
			info, err := app.InspectArchive(ctx, src)
			if err != nil {
				return fmt.Errorf("cannot restore %s: %w", src, err)
			}
			_, paths, err := app.LoadConfig(e.appOptions())
			if err != nil {
				return err
			}
			st := e.errStyle()
			span := ""
			if info.First != "" {
				span = fmt.Sprintf(", %s to %s", app.LocalDate(info.First, time.Local), app.LocalDate(info.Last, time.Local))
			}
			fmt.Fprintf(e.io.Err, "Backup:   %s (%d entries%s, schema v%d)\n", src, info.Entries, span, info.SchemaVersion)
			fmt.Fprintf(e.io.Err, "Replaces: %s\n", paths.Database)
			if !force {
				if !e.io.InTTY {
					return usagef("refusing to replace the archive without confirmation; pass --force")
				}
				if !confirm(e, st.Warn("Replace the current archive with this backup?")) {
					e.note("Nothing restored.")
					return nil
				}
			}
			e.close() // the archive must not be open while it is replaced
			res, err := app.Restore(ctx, e.appOptions(), src)
			if err != nil {
				return err
			}
			out := e.out()
			fmt.Fprintf(e.io.Out, "%s %d entries from %s\n", out.Success("Restored"), res.Restored.Entries, src)
			if res.SafetyBackup != "" {
				fmt.Fprintf(e.io.Out, "  %s\n", out.Dim("Previous archive saved as "+res.SafetyBackup))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "restore without asking")
	return cmd
}

func emptyRange() timerange.Range { return timerange.All() }

// sinceLastReport is the range for --since last: from where the last
// recorded report of this kind ended until now, or until --to.
func sinceLastReport(ctx context.Context, a *app.App, kind report.Kind, f *reportFlags) (timerange.Range, error) {
	if f.rangeExpr != "" || f.from != "" {
		return timerange.Range{}, usagef("--since last cannot be combined with --range or --from")
	}
	last, err := a.Store.LastReport(ctx, string(kind))
	if errors.Is(err, journal.ErrNotFound) {
		return timerange.Range{}, usagef("no %s report has been recorded yet; record one with `holocron report %s --record`", kind, kind)
	}
	if err != nil {
		return timerange.Range{}, err
	}
	start := last.Until()
	rng := timerange.Range{Start: start}
	if f.to != "" {
		if rng, err = a.Clock().Bounds("", "", "", f.to, rng); err != nil {
			return timerange.Range{}, usageError{err}
		}
		rng.Start = start
		if !rng.Start.Before(rng.End) {
			return timerange.Range{}, usagef("--to is before the last recorded %s report", kind)
		}
	}
	rng.Label = "since the last " + string(kind) + " report (" + start.In(a.Loc).Format("Mon 2 Jan "+a.TimeLayout()) + ")"
	return rng, nil
}

// recordReport remembers the report as sent when --record was given.
func recordReport(ctx context.Context, e *env, a *app.App, kind report.Kind, rng timerange.Range, f *reportFlags) error {
	if !f.record {
		return nil
	}
	rec, err := a.Store.RecordReport(ctx, string(kind), rng)
	if err != nil {
		return err
	}
	e.note("Recorded this %s report; `holocron report %s --since last` will start from %s.",
		kind, kind, rec.Until().In(a.Loc).Format("Mon 2 Jan "+a.TimeLayout()))
	return nil
}
