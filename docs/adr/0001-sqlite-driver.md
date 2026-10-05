# 1. Pure-Go SQLite with FTS5

Date: 2026-10-05

## Status

Accepted

## Context

Holocron must ship as a single binary for Windows, macOS and Linux on amd64
and arm64, built without a C toolchain, and must support full-text search.
The candidates were `mattn/go-sqlite3` (CGO), `modernc.org/sqlite` (SQLite
transpiled to Go) and `ncruces/go-sqlite3` (SQLite compiled to WebAssembly
and run by wazero).

## Decision

Use `modernc.org/sqlite` (SQLite 3.53 at the time of writing) through
`database/sql`.

Verified before adopting: FTS5 is compiled in (`ENABLE_FTS5`), including
`bm25`, `highlight` and `snippet`; WAL mode, foreign keys, `VACUUM INTO`,
`PRAGMA quick_check`, read-only URI opens and multi-statement `Exec` all
work; and the binary cross-compiles to all six targets with `CGO_ENABLED=0`.
`holocron doctor` re-checks FTS5 at runtime.

## Consequences

- `go build` is the whole build; CI cross-compiles every target from Linux.
- Binaries are larger than with the C build (the whole binary was about
  27 MB before Markdown rendering was added) and SQLite is somewhat slower.
  For a personal journal neither matters.
- SQLite upgrades arrive when modernc regenerates its sources, typically
  some weeks after upstream releases.
- Switching drivers later touches only `internal/database` (driver name and
  connection pragmas) and error classification in `database.describe`.
