# Development report

Holocron was built from an empty repository in one session. This report
describes what exists, how it was checked, and what remains.

## Implemented

Everything below works end to end and is covered by automated tests unless
noted.

**Capture**
- `holocron add` with arguments, stdin, or `--editor`; `--project`, `--type`
  (unique prefixes), `--tag`, `--mark`, `--at` (clock times, `yesterday
  16:00`, dates, `-2h`), `--raw`, `--json`, `-q`.
- `+project` / `#tag` shorthand with predictable rules; `Decision:`-style type
  inference; unknown projects created on first use (configurable).
- `holocron now`: a focused one-line prompt (also used by `add` with no text
  at a terminal), or `--editor` for `$VISUAL`/`$EDITOR`.

**Browse, search, manage**
- `today`, `yesterday`, `week`, `list`, `show`, `search` with shared filters:
  `--range`, `--since`, inclusive `--from/--to`, project (incl. aliases and
  "no project"), tags (AND), types, marks, `--open`, `--limit`, `--reverse`,
  `--json`.
- SQLite FTS5 search over text, project names/aliases and tags: prefix
  matching, light suffix relaxation, phrases, exclusions, `OR`,
  `+project`/`#tag` filters, highlighted snippets, `--sort relevance`.
- `edit` (editor document or flags), `delete` (confirmation / `--yes`),
  `mark`/`unmark`, `resolve`/`--reopen`.
- Projects: add, list, show, edit (rename, aliases, description, repository
  paths, URLs), archive/unarchive, delete (entries kept). Tags: list, rename/merge.

**Reports** (deterministic; text, Markdown and JSON)
- `day`, `week`, `staff`, `one-on-one`, `quarter`, each with a sensible
  default range and the shared range/project flags.
- Report marks (`staff`, `one-on-one`, `quarterly`, `important`,
  `cross-team`) stored separately from tags.
- Provenance: every item carries source entry IDs and reasons (`--ids`,
  `--explain`, always present in JSON). Section caps state omissions; imported
  commits collapse into one summarised item; each entry appears once.

**Data ownership**
- Markdown export (readable without Holocron) and versioned JSON export
  (`holocron.export/v1`, documented in `docs/export-format.md`).
- `backup` via `VACUUM INTO` with integrity verification; `restore` with
  validation, confirmation/`--force`, automatic pre-restore backup, migration
  of older backups, and preservation of issued entry numbers.
- Automatic pre-migration backups.

**Interactive archive (TUI)** â Bubble Tea v2, Lip Gloss v2, Bubbles v2
- Recent activity on launch; wide layout with a detail pane at 110+ columns;
  usable down to 30Ã8 with a clear message below that.
- Add (quick and full form), edit (form or external editor), delete with
  confirmation and undo, marks checklist, resolve toggle, search as you type,
  project/tag/type pickers with type-to-filter, open-only toggle, date-range
  picker and previous/next period, reports (cycle kinds, change period, toggle
  IDs and reasons, save Markdown), help overlay, reload.
- Consistent Escape semantics ("back one level", never quits).

**P1/P2 features**
- `holocron import git`: commits by the configured author(s) from one or more
  repositories or all project repository paths, interactive checklist or
  `--yes`/`--dry-run`/`--json`, provenance (hash, commit URL for
  GitHub/GitLab/Bitbucket, import time), duplicate-proof.
- `holocron doctor` (config, integrity, schema, FTS5, index consistency,
  backups, editor, git, AI) and `--rebuild-index`.
- Markdown rendering of entry text (Glamour) in the TUI detail view and in
  `holocron show` at a terminal; stored text is never changed, raw HTML is
  never interpreted, and piped output stays exactly as stored.
- Shell completion (Bash, Zsh, Fish, PowerShell) including dynamic project,
  tag and entry-ID completion.
- `config path|show|edit`.
- Optional `report â¦ --ai` through a provider interface with one Anthropic
  provider (official Go SDK, `claude-opus-5-5` by default), consent prompt,
  only-selected-entries prompt, citation checking and a Sources appendix,
  fallback to the deterministic report on failure.
- CI workflow (format, tidy, vet, golangci-lint, tests on three OSes,
  six-target build) and GoReleaser release workflow.

## Architecture

```text
cmd/holocron            entry point (signal-aware context, exit codes)
internal/cli            Cobra commands: parse, call, render
internal/tui            Bubble Tea model, capture prompt, import checklist
internal/app            composition root and shared workflows
internal/journal        entries, projects, tags, marks, search, imports
internal/report         report builders and text/Markdown/JSON renderers
internal/export         archive export (Markdown, JSON)
internal/timerange      date expressions and moments
internal/database       SQLite open/pragmas, migrations, backup, read-only open
internal/config         locations and TOML configuration
internal/editor         editor resolution and invocation
internal/gitimport      git log reading and commit URLs
internal/ai             provider interface, Anthropic provider, provenance
internal/style          palette and colour handling
```

Presentation (CLI, TUI) never contains SQL or business rules; both call the
same `app`/`journal`/`report`/`export` functions. Details and data flows are
in `docs/architecture.md`.

## Important decisions

Recorded as ADRs in `docs/adr/`:

1. **Pure-Go SQLite (`modernc.org/sqlite`)** â single static binary with no
   CGO; FTS5, WAL, `VACUUM INTO` verified before adoption.
2. **Integer IDs plus ULIDs** â `#42` for people (AUTOINCREMENT, never
   reused, preserved across restore), ULIDs for exports and provenance.
3. **Embedded, transactional migrations** with safety backups, refusal of
   newer or foreign databases.
4. **One application layer for CLI and TUI.**
5. **Report marks separate from tags**, so tags never change report contents.
6. **AI behind a provider interface**, fed only the deterministic report's
   selected entries, with explicit consent and citation checking.

Other choices made along the way:
- Bubble Tea **v2** (current release line) rather than v1.
- The FTS index is maintained by application code in the same transaction as
  each write (not triggers), because it includes project aliases and tags
  from other tables.
- Search uses `unicode61` without Porter stemming plus a small query-side
  suffix relaxation: Porter stemming breaks prefix search (`analy*` does not
  match the stem `analyz`), which matters more for type-as-you-search.
- `synchronous=FULL`: journal writes are tiny, durability matters more.
- Tags are lower-cased; project names keep their case and match
  case-insensitively.
- Deletion is permanent (with confirmation, TUI undo and backups) rather than
  a soft-delete flag threaded through every query.
- TOML for configuration; unknown keys are errors so typos are not silently
  ignored.
- The "openai-compatible" AI provider initially sketched was dropped to keep
  the AI scope to one well-tested provider.

## Data model

Schema version 1 (`internal/database/migrations/0001_initial.sql`):

- `entries`: integer `id` (AUTOINCREMENT), `uid` (ULID), `occurred_at` (UTC,
  fixed-width text), `utc_offset` (captured offset), `body`, optional `type`,
  optional `project_id` (SET NULL on project delete), `resolved_at`,
  `created_at`, `updated_at`, and provenance (`source_type`, `source_id`,
  `source_url`, `imported_at`, unique per source).
- `projects` (case-insensitive unique name, description, archived time),
  `project_aliases`, `project_links` (repository paths and URLs).
- `tags` + `entry_tags` (unused tags pruned), `entry_marks`.
- `entries_fts` (FTS5: body, project name + aliases, tags).
- `schema_migrations`, mirrored by `PRAGMA user_version`.

Indexes cover time ranges, project + time, type + time, tag lookups, marks
and source deduplication. Migrations are ordered SQL files applied one per
transaction, with a verified backup before upgrading an archive that has data.

## Validation performed

All on Windows 11 (amd64) with Go 1.27.1.

- `go test ./...` (90 test functions across 14 packages, plus benchmarks), `go vet ./...`,
  `gofmt -l .`, `go mod tidy -diff`, `golangci-lint run` (v2.14, 0 issues).
- Cross-compilation for linux/darwin/windows Ã amd64/arm64 with
  `CGO_ENABLED=0`, and a full `goreleaser release --snapshot --skip=publish`
  producing all six archives and checksums; version injection checked with
  `holocron version`.
- Fresh archive in a temporary data directory: `doctor` before creation,
  creation and migration on first use, then realistic entries across four
  weeks and three projects (aliases, back-dated `--at`, stdin, shorthand,
  marks, follow-ups, problems).
- `today`, `week`, `search analyzer`, `search --project aws --since 30d`,
  `search --tag security --from 2026-09-01 --to 2026-09-30`, `search
  investigating` (suffix relaxation), `list --open`.
- Reports: staff (this week and last week, `--ids`), one-on-one
  (`--explain`), quarter (`2026-Q3`), week (last week).
- `edit` with flags, `resolve`, `mark`, `delete --yes`, search after delete.
- `export` Markdown and JSON to files; `backup`; add after backup; `restore
  --force`; confirmed the next entry got a new number (`#14`, not the `#13`
  issued before the restore); `doctor` afterwards (index in sync).
- Every command example in the README run against a fresh archive with a
  check of exit codes (all succeeded).
- `import git --since 7d --dry-run` on this repository's own history.
- TUI: launched the real binary in a terminal and read the rendered screen
  (list, wide layout with detail pane, footer hints). Interaction is covered
  by model tests (navigation, Escape semantics, add/edit/delete/undo, forms,
  pickers, ranges, marks, resolve, reports and saving, help, Ctrl+C) and a
  test that runs the real Bubble Tea program with scripted raw keystrokes.
  Narrow and short terminals are covered by a test that renders every screen
  and dialog at sizes from 160Ã50 down to 10Ã3 and checks nothing exceeds the
  terminal.

Problems found during validation and fixed: a panic in the TUI from an
uninitialised text area on first resize; the initial TUI load being
discarded (sequence counter mutated on a value receiver); restored archives
able to re-issue entry numbers; footer hints cut mid-word; duplicate open
items in week and day reports; an empty "Project" row in the detail pane;
UTC dates in summaries; an unhelpful error when `HOLOCRON_CONFIG` points at a
missing file. After the first review: entry text shown twice in the
detail view, and an empty `NO_COLOR` wrongly disabling colour (only a
non-empty value should, per no-color.org). A later performance pass measured real
key latency through a Windows pseudo-console and found Esc took 55–80 ms
against 10–25 ms for other keys (Bubble Tea's 50 ms escape-sequence wait);
Holocron now enables win32-input mode inside Windows Terminal, bringing Esc
to 15–25 ms.

## Performance

A dedicated pass measured Holocron against a seeded archive of 15,000
entries (about five years of heavy use, 9.5 MB), with Go benchmarks
(`go test -run '^$' -bench . ./internal/journal ./internal/tui`), CPU
profiles, and real keystrokes sent to the built binary through a Windows
pseudo-console. Changes were kept only where a side-by-side measurement
showed a gain; one experiment (deferring search snippets) measured slower
and was reverted.

| Operation (15,000 entries) | Before | After |
|---|---|---|
| List tags (tag picker, `tag list`) | 59 ms | 2.1 ms |
| List projects | 3.9 ms | 0.9 ms |
| Load 2,000 entries | 14.9 ms | 7.6 ms |
| TUI "all" range initial load | 9.2 ms | 3.3 ms |
| TUI search keystroke | 15.7 ms | 9.1 ms |
| Search page (50 results) | 4.3 ms | 1.7 ms |
| Rare tag filter, 50 results | 14.9 ms | 0.2 ms |
| TUI cursor move with a full list | 1.9 ms | 1.3 ms |
| Process start (`holocron version`) | ~88 ms | ~68 ms |
| Real console: tag picker opening | 100 ms | 18 ms |

What changed:

- Tags and marks for a page of entries are loaded with set queries (a
  primary-key range scan when IDs are dense) instead of two sorted
  correlated subqueries per row.
- Stored timestamps are parsed by a fast path for Holocron's own layout.
- Tag and mark filters use `IN (subquery)`, bounding the cost by how often
  the tag is used rather than by archive size. Common tags with a small
  limit are slightly slower (1.3 → 3.7 ms), accepted for the 75× better
  worst case.
- `ListTags` no longer joins entries for an unused "last used" value;
  `ListProjects` no longer issues three queries per project.
- SQLite's page cache is 32 MB (from 2 MB), enough for a decade of entries.
- The TUI loads 500 entries at a time and fetches more as the cursor nears
  the end; End/`G` loads the rest. List rows are built once per load.
- `go-runewidth` (v0.0.30) and Chroma (v2.27.0) were upgraded: the former
  no longer builds its full Unicode tables at start-up (package init went
  from ~40 ms to ~20 ms).

## Test results

Final run (`go test -count=1 ./...`): all 14 packages with tests pass;
`cmd/holocron` and `internal/style` have no tests. golangci-lint: 0 issues.

## Known limitations

- **Linux and macOS were not run locally.** Builds for them were verified by
  cross-compilation and the snapshot release; the CI workflow runs the tests
  on those platforms but has not yet run because the repository has no
  remote.
- **The live AI provider was not called.** The Anthropic provider compiles
  against the official SDK and the whole `--ai` flow is tested with a fake
  provider; no request was made to the real API.
- **External editor flows were not driven interactively.** The editor
  document format, parsing and patching are tested, and the editor command
  construction is tested, but `holocron edit 42`, `now --editor` and the
  TUI's `E` were not exercised with a real editor in this session.
- Markdown rendering (Glamour, with its syntax-highlighting dependency)
  roughly doubled the binary, to about 55 MB unstripped.
- The archive is not encrypted at rest (documented; rely on disk encryption).
- `restore` cannot detect another running Holocron on macOS/Linux (on
  Windows the file lock makes it fail safely); the docs say to close other
  sessions first.
- Search relaxes common English suffixes but is not a full stemmer, and has
  no fuzzy matching for typos.
- There is no JSON *import*, so the JSON export is not yet a round-trip
  format for moving between machines (copying a backup file works).
- The module path assumes `github.com/Unscheduled-Maintenance/Holocron`; adjust `go.mod`,
  `.goreleaser.yaml` and README install lines if the repository lives
  elsewhere. No licence file has been chosen.

## Next five improvements

1. **Run CI on a real remote and fix whatever Linux/macOS turn up**, then
   tag a first release and dogfood the released binary for a week.
2. **`holocron import json`** to restore or merge a JSON export (matching on
   ULIDs), making the export a true round-trip format.
3. **Staff report timing:** default to the previous week when run early in
   the week, and let reports start from "since the last staff report".
4. **TUI polish from daily use:** show tags in list rows when there is room,
   scrollable help in short terminals, mouse-wheel scrolling, and remembering
   the last range and filters between sessions.
5. **Entry links for follow-ups:** let a later entry resolve a follow-up
   (`holocron add ... --resolves 42`) so reports can show what happened to
   each open item.
