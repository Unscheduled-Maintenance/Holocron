// Package app wires configuration, storage and services together. It is the
// single application layer used by both the CLI and the TUI: presentation
// code calls these methods and never reimplements capture, search,
// reporting, export or persistence.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/config"
	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
	"github.com/Unscheduled-Maintenance/Holocron/internal/devsync"
	"github.com/Unscheduled-Maintenance/Holocron/internal/editor"
	"github.com/Unscheduled-Maintenance/Holocron/internal/export"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/report"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

// Options control how the application starts.
type Options struct {
	ConfigPath string
	DBPath     string
	// UTC renders times in UTC instead of the local timezone.
	UTC bool
	// Now overrides the clock (tests).
	Now func() time.Time
	// NoSync skips the automatic sync when the archive opens (used by the
	// sync commands themselves).
	NoSync bool
	// Keychain overrides the OS keychain for sync keys (tests).
	Keychain devsync.Keychain
}

// App is an open Holocron archive plus its configuration.
type App struct {
	Config config.Config
	Paths  config.Paths
	DB     *database.DB
	Store  *journal.Store
	Loc    *time.Location
	// Sync keeps the archive in step with other computers, when set up.
	Sync *devsync.Syncer

	now       func() time.Time
	weekStart time.Weekday
	types     journal.TypeAliases

	syncMu     sync.Mutex
	syncPaused bool
	syncNotes  []string
}

// LoadConfig reads configuration without opening the database.
func LoadConfig(opts Options) (config.Config, config.Paths, error) {
	return config.Load(opts.ConfigPath, opts.DBPath)
}

// Open loads configuration and opens (creating and migrating if needed) the
// archive.
func Open(ctx context.Context, opts Options) (*App, error) {
	cfg, paths, err := LoadConfig(opts)
	if err != nil {
		return nil, err
	}
	db, err := database.Open(ctx, paths.Database, database.Options{BackupDir: paths.BackupDir})
	if err != nil {
		return nil, err
	}
	a := newApp(cfg, paths, db, opts)
	if !opts.NoSync && cfg.SyncAutoEnabled() {
		a.pull(ctx)
	}
	return a, nil
}

func newApp(cfg config.Config, paths config.Paths, db *database.DB, opts Options) *App {
	loc := time.Local
	if opts.UTC {
		loc = time.UTC
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	ws, _ := timerange.ParseWeekday(cfg.WeekStart)
	// Validated with the rest of the configuration in config.Load.
	types, _ := journal.NewTypeAliases(cfg.TypeAliases)
	a := &App{Config: cfg, Paths: paths, DB: db, Loc: loc, now: now, weekStart: ws, types: types}
	a.Store = journal.NewStore(db, journal.WithClock(now), journal.WithLocation(loc))
	keychain := opts.Keychain
	if keychain == nil {
		keychain = devsync.SystemKeychain{}
	}
	a.Sync = &devsync.Syncer{Store: a.Store, Keychain: keychain, KeyCommand: editor.SplitCommand(cfg.Sync.KeyCommand), Now: now}
	return a
}

// Close closes the archive.
func (a *App) Close() error {
	if a == nil || a.DB == nil {
		return nil
	}
	return a.DB.Close()
}

// Now returns the current time in the display timezone.
func (a *App) Now() time.Time { return a.now().In(a.Loc) }

// Clock returns a date-range clock for the current moment.
func (a *App) Clock() timerange.Clock { return timerange.NewClock(a.now(), a.Loc, a.weekStart) }

// ParseType parses an entry type, accepting the configured type aliases.
func (a *App) ParseType(s string) (journal.Type, error) { return a.types.Parse(s) }

// TimeLayout returns the configured time-of-day layout.
func (a *App) TimeLayout() string {
	if a.Config.Use12HourClock() {
		return "3:04pm"
	}
	return "15:04"
}

// CaptureInput is everything a person can say when adding an entry.
type CaptureInput struct {
	Text    string
	Project string
	Type    string
	Tags    []string
	Marks   []string
	At      string
	// Raw disables +project/#tag shorthand for this entry.
	Raw bool
	// Resolves names open problems or follow-ups ("42", "#42" or a UID) that
	// this entry resolves.
	Resolves []string
}

// CaptureResult reports side effects worth telling the person about.
type CaptureResult struct {
	Entry          journal.Entry
	CreatedProject string
	// Resolved holds the entries the new entry resolved, after resolving.
	Resolved []journal.Entry
}

// Capture parses quick-capture input and saves a new entry.
func (a *App) Capture(ctx context.Context, in CaptureInput) (CaptureResult, error) {
	text := in.Text
	var sh journal.Capture
	if a.Config.ShorthandEnabled() && !in.Raw {
		var err error
		sh, err = journal.ParseShorthand(text, a.types)
		if err != nil {
			return CaptureResult{}, err
		}
		text = sh.Body
	}
	if strings.TrimSpace(text) == "" {
		return CaptureResult{}, fmt.Errorf("%w: entry text is empty", journal.ErrInvalid)
	}
	project := strings.TrimSpace(in.Project)
	if sh.Project != "" {
		if project != "" && !strings.EqualFold(project, sh.Project) {
			return CaptureResult{}, fmt.Errorf("%w: --project %q conflicts with +%s in the text", journal.ErrInvalid, project, sh.Project)
		}
		project = sh.Project
	}
	typ := sh.Type
	if in.Type != "" {
		t, err := a.ParseType(in.Type)
		if err != nil {
			return CaptureResult{}, err
		}
		if sh.TypePrefix != "" && t != sh.Type {
			// An explicit, different --type wins, so the leading "Note:" was
			// not shorthand after all: keep the text exactly as typed.
			text = sh.TypePrefix + text
		}
		typ = t
	}
	marks, err := journal.ParseMarks(in.Marks)
	if err != nil {
		return CaptureResult{}, err
	}
	at := time.Time{}
	if in.At != "" {
		at, err = a.Clock().ParseMoment(in.At)
		if err != nil {
			return CaptureResult{}, fmt.Errorf("%w: %w", journal.ErrInvalid, err)
		}
	}
	var res CaptureResult
	var resolves []int64
	for _, ref := range splitRefs(in.Resolves) {
		target, err := a.Store.Get(ctx, ref)
		if err != nil {
			return CaptureResult{}, err
		}
		if slices.Contains(resolves, target.ID) {
			continue
		}
		if target.ResolvedAt != nil {
			return CaptureResult{}, fmt.Errorf("%w: %s is already resolved (reopen it with `holocron resolve %d --reopen`)", journal.ErrConflict, target.Ref(), target.ID)
		}
		resolves = append(resolves, target.ID)
		res.Resolved = append(res.Resolved, target)
	}
	if project != "" {
		if _, err := a.Store.Project(ctx, project); errors.Is(err, journal.ErrNotFound) {
			if !a.Config.CreateProjectsEnabled() {
				return CaptureResult{}, fmt.Errorf("%w (create it with `holocron project add`, or enable capture.create_projects)", err)
			}
			res.CreatedProject = project
		} else if err != nil {
			return CaptureResult{}, err
		}
	}
	e, err := a.Store.AddEntry(ctx, journal.NewEntry{
		Body: text, OccurredAt: at, Type: typ, Project: project, CreateProject: true,
		Tags: append(append([]string{}, in.Tags...), sh.Tags...), Marks: marks,
		Resolves: resolves,
	})
	if err != nil {
		return CaptureResult{}, err
	}
	if res.CreatedProject != "" {
		res.CreatedProject = e.Project
	}
	for i, r := range res.Resolved {
		if res.Resolved[i], err = a.Store.GetByID(ctx, r.ID); err != nil {
			return CaptureResult{}, err
		}
	}
	res.Entry = e
	return res, nil
}

// ReportBuilder returns a configured report builder.
func (a *App) ReportBuilder() report.Builder {
	return report.Builder{
		Store:          a.Store,
		Clock:          a.Clock(),
		MaxItems:       a.Config.Reports.MaxItems,
		OpenLookback:   a.Config.Reports.OpenLookback,
		StaffEarlyDays: a.Config.Reports.StaffEarlyDays,
	}
}

// DefaultReportRange returns the configured default range for a report, and a
// note for the report when the choice needs explaining.
func (a *App) DefaultReportRange(k report.Kind) (timerange.Range, string) {
	return a.ReportBuilder().DefaultRange(k, a.Config.Reports.StaffRange, a.Config.Reports.OneOnOneRange)
}

// ExportArchive collects entries (and the projects they reference) for export.
func (a *App) ExportArchive(ctx context.Context, q journal.Query, description string) (export.Archive, error) {
	q.Order = journal.OldestFirst
	q.Limit = 0
	entries, err := a.Store.Find(ctx, q)
	if err != nil {
		return export.Archive{}, err
	}
	all, err := a.Store.ListProjects(ctx, true)
	if err != nil {
		return export.Archive{}, err
	}
	used := map[int64]bool{}
	for _, e := range entries {
		used[e.ProjectID] = true
	}
	unfiltered := len(q.Projects) == 0 && q.Text == "" && q.Range.IsZero() && len(q.Tags) == 0 && len(q.Types) == 0 && len(q.Marks) == 0
	var projects []journal.Project
	for _, p := range all {
		if unfiltered || used[p.ID] {
			projects = append(projects, p)
		}
	}
	return export.Archive{ExportedAt: a.now(), Description: description, Projects: projects, Entries: entries}, nil
}

// splitRefs splits repeated and comma-separated entry references.
func splitRefs(in []string) []string {
	var out []string
	for _, s := range in {
		for _, part := range strings.Split(s, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// ImportResult reports what a JSON import did (or, for a dry run, would do).
type ImportResult struct {
	journal.ApplyResult
	// Backup is the archive backup taken before importing; empty for a dry
	// run or when nothing would change.
	Backup string
}

// ImportJSON merges a JSON export into the archive: entries and projects are
// matched on their UIDs, the most recent change to each field wins, and
// importing the same file again changes nothing. A verified backup is taken
// first, unless this is a dry run or the import changes nothing.
func (a *App) ImportJSON(ctx context.Context, r io.Reader, dryRun bool) (ImportResult, error) {
	recs, err := export.ReadJSON(r)
	if err != nil {
		return ImportResult{}, err
	}
	preview, err := a.Store.Apply(ctx, recs, journal.ApplyOptions{DryRun: true})
	if err != nil || dryRun || !preview.Changed() {
		return ImportResult{ApplyResult: preview}, err
	}
	var res ImportResult
	if res.Backup, _, err = a.Backup(ctx, ""); err != nil {
		return ImportResult{}, fmt.Errorf("refusing to import without a backup of the archive: %w", err)
	}
	res.ApplyResult, err = a.Store.Apply(ctx, recs, journal.ApplyOptions{})
	return res, err
}
<<<<<<< HEAD

// pull merges other computers' changes when sync is set up. Problems never
// stop the command: they are kept for SyncNotes, and a sync that cannot run
// at all is paused for the rest of the process.
func (a *App) pull(ctx context.Context) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	res, err := a.Sync.Pull(ctx)
	if errors.Is(err, devsync.ErrNotConfigured) {
		a.syncPaused = true
		return
	}
	if err != nil {
		a.syncPaused = true
		a.note("sync is paused: %v", err)
		return
	}
	for _, w := range append(res.Warnings, res.Applied.Warnings...) {
		a.note("sync: %s", w)
	}
}

// SyncPush publishes this computer's changes when sync is set up. Like
// pull, problems become SyncNotes.
func (a *App) SyncPush(ctx context.Context) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	if a.syncPaused || !a.Config.SyncAutoEnabled() {
		return
	}
	if _, err := a.Sync.Push(ctx); err != nil && !errors.Is(err, devsync.ErrNotConfigured) {
		a.syncPaused = true
		a.note("sync could not publish this computer's changes (they are kept and will be published later): %v", err)
	}
}

// SyncNotes returns sync problems met so far, each once.
func (a *App) SyncNotes() []string {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	return slices.Clone(a.syncNotes)
}

func (a *App) note(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !slices.Contains(a.syncNotes, msg) {
		a.syncNotes = append(a.syncNotes, msg)
	}
}
=======
>>>>>>> origin/main
