# 3. Embedded, ordered, transactional SQL migrations

Date: 2026-10-05

## Status

Accepted

## Context

The archive must remain readable for years while the schema evolves. Ad hoc
"create if missing" logic at startup produces databases whose shape depends
on their history, and a half-applied change can lose data.

## Decision

- Schema changes are SQL files `internal/database/migrations/NNNN_name.sql`
  embedded in the binary. Versions must be contiguous from 1; tests enforce
  this.
- `schema_migrations` records applied versions; `PRAGMA user_version` mirrors
  the current version for external tools.
- Each migration and its bookkeeping run in one transaction, so a failure
  leaves the previous version intact.
- Before migrating an archive that already has data, Holocron writes a
  verified backup (`holocron-pre-migration-v<N>-<time>.db`) and refuses to
  proceed if that fails.
- An archive with a newer schema than the binary understands is refused,
  never downgraded.
- An existing SQLite file without `schema_migrations` is refused rather than
  modified, so pointing `--db` at the wrong file cannot damage it.
- Migrations run when a command opens the archive. Diagnostics open it
  without migrating.

Vocabularies (entry types, report marks) are validated in Go rather than by
CHECK constraints, because SQLite cannot alter a CHECK constraint without
rebuilding the table.

## Consequences

- Upgrades are automatic and recoverable.
- Every schema change needs a new migration file and a test of the upgrade
  path; a released migration is never edited.
- Restoring an old backup works: it is migrated after the restore.
