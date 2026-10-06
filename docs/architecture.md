# Architecture

Holocron is a single Go binary with three layers:

```text
presentation        internal/cli (Cobra)          internal/tui (Bubble Tea)
                          │                              │
                          └──────────────┬───────────────┘
                                         ▼
application         internal/app   — composition root and shared workflows
                    (capture, edit documents, backup/restore, doctor, export assembly)
                                         │
domain              internal/journal   entries, projects, tags, marks, search
                    internal/report    deterministic report builders + renderers
                    internal/export    Markdown / JSON archive writers
                    internal/timerange human date expressions
                    internal/ai        optional report rewriting (provider interface)
                    internal/gitimport reads commits via the git executable
                                         │
storage             internal/database  SQLite open, pragmas, migrations, backup
                                         │
                                  modernc.org/sqlite (pure Go, FTS5)
```

Supporting packages: `internal/markdown` (display-only Markdown rendering of
entry text with Glamour; HTML is escaped, line breaks preserved),
`internal/config` (file locations and TOML settings),
`internal/editor` (resolving and launching `$VISUAL`/`$EDITOR`),
`internal/style` (palette and colour on/off handling).

## Boundaries

**Presentation never implements behaviour.** Cobra commands parse flags,
call `app`/`journal`/`report`/`export`, and render results. Bubble Tea models
do the same through `tea.Cmd` functions. Neither contains SQL, capture
parsing, report selection or export formatting. Anything the TUI can do has a
CLI equivalent built on the same call (see ADR 0004).

**`internal/app` is the composition root.** `app.Open` loads configuration,
opens and migrates the database, and constructs the `journal.Store` with the
right clock and timezone. Workflows that combine several domain operations
live here: `Capture` (shorthand parsing, project creation, type inference,
`--at` parsing), the editable entry document used by `holocron edit` and the
TUI's external editor, `Backup`/`Restore`, `Doctor`, and export assembly.

**`internal/journal` owns the data model and its SQL.** There is no separate
repository layer: the store is the one concrete implementation and is tested
directly against temporary SQLite files. Every multi-statement write runs in
a single transaction (`database.DB.Tx`), including the full-text index update
for the affected entries, so the index can never drift from the data after a
failure.

**`internal/report` is pure selection plus rendering.** A builder queries the
store once for the range (and once more for open items when needed), then
selects, ranks and groups in memory. Each `report.Item` carries the IDs of
its source entries and human-readable reasons. Renderers (`Text`, `Markdown`,
`JSON`) never query anything.

**`internal/ai` sits behind an interface.** `ai.Provider` has one method,
`Generate`. The report command builds the deterministic report, passes
`ai.BuildRequest(report)` — containing only the entries that report selected —
to the provider, and appends `ai.Provenance` to the result. Nothing else in
Holocron imports a vendor SDK (ADR 0006).

## Data flow examples

**Capture** — `holocron add "Fixed S3 policy +aws #security"`

1. `cli.runAdd` collects text and flags into `app.CaptureInput`.
2. `app.Capture` runs `journal.ParseShorthand`, merges flags, parses `--at`
   with `timerange.Clock.ParseMoment`, and notes whether the project is new.
3. `journal.Store.AddEntry` opens a transaction: resolve or create the
   project, insert the entry (UTC timestamp plus captured UTC offset), upsert
   tags, attach marks, rebuild the entry's FTS row, commit.
4. The CLI prints a one-line confirmation, or JSON with `--json`.

**Search** — `holocron search "bucket +aws" --since 30d`

1. `journal.SplitSearch` moves `+aws` into the project filter.
2. `timerange.Clock.Bounds` turns `--since 30d` into a half-open UTC range.
3. `journal.BuildMatch` turns the words into a safe FTS5 expression
   (quoted terms, prefix matching, relaxed suffixes, `-` exclusions, `OR`).
4. One SQL query joins `entries_fts`, applies range/project/tag/type/mark
   filters, returns a highlighted snippet, and loads tags and marks with
   correlated subqueries.

**Report** — `holocron report staff`

1. `app.DefaultReportRange` picks the configured range.
2. `report.Builder.Build` loads entries in range plus open problems and
   follow-ups from the lookback window, selects them into sections
   (each entry appears at most once), ranks by marks then type, caps sections
   and records omissions.
3. The chosen renderer writes text, Markdown or JSON.

## Persistence

- SQLite in WAL mode with `foreign_keys=ON`, `synchronous=FULL`,
  `busy_timeout=5000` and immediate write transactions (rationale in
  `internal/database/database.go`).
- Timestamps are fixed-width UTC strings (`2006-01-02T15:04:05.000Z`) so
  lexical order is chronological and range queries use the
  `entries(occurred_at)` index. Each entry also stores `utc_offset`.
- Schema changes are ordered SQL files in `internal/database/migrations`,
  embedded in the binary and applied in a transaction each (ADR 0003).
- Full-text search uses an FTS5 table keyed by entry ID with columns for body,
  project (name plus aliases) and tags, maintained by the application in the
  same transaction as each write. Project renames, alias changes and tag
  renames re-index affected entries. `holocron doctor` verifies the index;
  `--rebuild-index` regenerates it.

### Schema (version 3)

| Table | Purpose |
|---|---|
| `entries` | `id` (AUTOINCREMENT, the `#42` number), `uid` (ULID), `occurred_at`, `utc_offset`, `body`, `type`, `project_id` → projects (SET NULL), `resolved_at`, `resolved_by` → the entry that resolved it (SET NULL; version 2), `created_at`, `updated_at`, provenance (`source_type`, `source_id`, `source_url`, `imported_at`; unique on type+id) |
| `projects` | `id`, `uid`, `name` (unique, case-insensitive), `description`, `archived_at`, timestamps |
| `project_aliases` | `alias` (unique, case-insensitive) → project |
| `project_links` | repository `path`s and `url`s per project |
| `tags`, `entry_tags` | reusable tags; unused tags are pruned |
| `entry_marks` | report marks per entry (ADR 0005) |
| `report_log` | reports recorded as sent (`--record`): `kind`, `range_start`, `range_end`, `recorded_at`; read by `--since last` (version 3) |
| `entries_fts` | FTS5 index (`unicode61`, diacritics removed) |
| `schema_migrations` | applied migrations; mirrored in `PRAGMA user_version` |

Vocabularies (entry types, marks) are validated in Go rather than with CHECK
constraints, so adding a type later does not require rebuilding a table.

## Identifiers

People type small integers (`#42`); exports and provenance use 26-character
ULIDs. AUTOINCREMENT guarantees numbers are never reused within an archive,
and `restore` carries the highest issued numbers forward so a restored
backup cannot re-issue them either (ADR 0002).

## The TUI

`internal/tui.Model` is one Bubble Tea model with a *screen* (list, entry
detail, report) and at most one *layer* on top (an input, picker, confirmation,
form or help). Escape always pops the layer, then the screen, then clears
filters on the list — it never quits. All database work happens in `tea.Cmd`
functions returning messages; list loads carry a sequence number so stale
results are discarded. The view is rendered to exactly the terminal size
(`fitBlock`), with progressive simplification as width and height shrink.

The model is tested by feeding it key messages and window sizes directly
(`tui_test.go`), and the full program loop is exercised with scripted raw
terminal input (`program_test.go`).

`tui.Capture` (the `holocron now` prompt) and `tui.Select` (the Git import
checklist) are small standalone programs.

## Imports

`internal/gitimport` shells out to `git` (`git log --branches --no-merges`
with a machine-readable format), filters by author email, and maps remotes to
commit URLs. The CLI resolves projects from registered repository paths and
calls `journal.Store.ImportEntries`, which skips any `(source_type,
source_id)` already present inside the same transaction; a unique index
backs this up.

A future importer needs only to produce `journal.NewEntry` values with a
`Source`; deduplication, provenance display, export and reporting already
work for any source type. No generic importer framework exists until a second
source justifies one.
