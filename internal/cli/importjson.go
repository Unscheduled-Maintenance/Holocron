package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
)

func newImportJSONCmd(e *env) *cobra.Command {
	var dryRun, asJSON bool
	cmd := &cobra.Command{
		Use:   "json <file>",
		Short: "Merge a JSON export into this archive",
		Long: `Merge a file made by "holocron export --format json" into this archive, for
example to bring entries from another computer or restore part of an archive.

Entries and projects are matched on their UIDs, so importing the same file
twice changes nothing. Where an entry or project exists in both, each field
keeps whichever version changed most recently. Entries deleted here after
the export was made stay deleted. Projects with the same name are merged.
An imported entry whose number is already used here by a different entry
gets the next free number, and the change is listed.

A verified backup of the archive is taken before anything changes. Use
--dry-run to see what would happen first. Use "-" to read from stdin.`,
		Example: "  holocron import json laptop.json --dry-run\n  holocron import json laptop.json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in := e.io.In
			if args[0] != "-" {
				f, err := os.Open(expandPath(args[0]))
				if err != nil {
					return err
				}
				defer f.Close()
				in = f
			}
			a, err := e.open(ctx)
			if err != nil {
				return err
			}
			res, err := a.ImportJSON(ctx, in, dryRun)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(e.io.Out, importJSONSummary(res, dryRun))
			}
			printImportSummary(e, res, dryRun)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "show what would change without changing anything")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

func printImportSummary(e *env, res app.ImportResult, dryRun bool) {
	st := e.out()
	w := e.io.Out
	if !res.Changed() {
		fmt.Fprintln(w, "Nothing to import: the archive already has everything in this file.")
		if res.EntriesSkipped > 0 {
			fmt.Fprintf(w, "Skipped %s deleted here or already imported from the same source.\n", count(res.EntriesSkipped, "entry", "entries"))
		}
		return
	}
	verb := map[bool]string{true: "Would add", false: "Added"}[dryRun]
	var parts []string
	if res.EntriesAdded > 0 {
		parts = append(parts, count(res.EntriesAdded, "entry", "entries"))
	}
	if res.ProjectsAdded > 0 {
		parts = append(parts, count(res.ProjectsAdded, "project", "projects"))
	}
	if res.Reports > 0 {
		parts = append(parts, count(res.Reports, "recorded report", "recorded reports"))
	}
	if len(parts) > 0 {
		fmt.Fprintf(w, "%s %s.\n", st.Success(verb), strings.Join(parts, " and "))
	}
	if n := res.EntriesUpdated + res.ProjectsUpdated; n > 0 {
		fmt.Fprintf(w, "%s %s with newer changes from the file.\n", map[bool]string{true: "Would update", false: "Updated"}[dryRun],
			count(n, "record", "records"))
	}
	if res.ProjectsMerged > 0 {
		fmt.Fprintf(w, "%s %s with the same name as one here.\n", map[bool]string{true: "Would merge", false: "Merged"}[dryRun],
			count(res.ProjectsMerged, "project", "projects"))
	}
	if res.EntriesUnchanged > 0 {
		fmt.Fprintf(w, "%s already up to date.\n", count(res.EntriesUnchanged, "entry", "entries"))
	}
	if res.EntriesSkipped > 0 {
		fmt.Fprintf(w, "Skipped %s deleted here or already imported from the same source.\n", count(res.EntriesSkipped, "entry", "entries"))
	}
	if len(res.Renumbered) > 0 {
		fmt.Fprintf(w, "%s because their numbers are taken here:\n", map[bool]string{true: "Would renumber", false: "Renumbered"}[dryRun])
		for _, r := range res.Renumbered {
			fmt.Fprintf(w, "  %s → %s\n", r.From, r.To)
		}
	}
	for _, warning := range res.Warnings {
		e.note("%s %s", e.errStyle().Warn("warning:"), warning)
	}
	if res.Backup != "" {
		e.note("%s", e.errStyle().Dim("Backup of the archive before importing: "+res.Backup))
	}
	if dryRun {
		e.note("Dry run: nothing was changed.")
	}
}

func importJSONSummary(res app.ImportResult, dryRun bool) map[string]any {
	renumbered := []map[string]string{}
	for _, r := range res.Renumbered {
		renumbered = append(renumbered, map[string]string{"uid": r.UID, "from": r.From, "to": r.To})
	}
	warnings := res.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return map[string]any{
		"dry_run": dryRun,
		"entries": map[string]int{"added": res.EntriesAdded, "updated": res.EntriesUpdated,
			"unchanged": res.EntriesUnchanged, "skipped": res.EntriesSkipped, "deleted": res.EntriesDeleted},
		"projects": map[string]int{"added": res.ProjectsAdded, "updated": res.ProjectsUpdated,
			"merged": res.ProjectsMerged, "deleted": res.ProjectsDeleted},
		"recorded_reports": res.Reports,
		"renumbered":       renumbered,
		"warnings":         warnings,
		"backup":           res.Backup,
	}
}

// count formats "1 entry" or "3 entries".
func count(n int, one, many string) string { return fmt.Sprintf("%d %s", n, plural(n, one, many)) }
