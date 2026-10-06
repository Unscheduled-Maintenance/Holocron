# 2. Integer entry numbers plus ULIDs

Date: 2026-10-05

## Status

Accepted. Amended by [0007](0007-multi-device-sync.md): with sync enabled, new
entries are numbered per device (`#12a`); existing numbers keep their meaning.

## Context

People refer to entries constantly (`holocron show 42`, `holocron mark 42
staff`) and write references into tickets and notes. UUIDs are unpleasant to
type; short hash prefixes become ambiguous as an archive grows. A reference
must never silently start meaning a different record: not after deletion,
not after restoring a backup, not years later.

## Decision

Every entry has two identifiers:

- `id`: an `INTEGER PRIMARY KEY AUTOINCREMENT`, shown as `#42`. SQLite's
  AUTOINCREMENT never reissues a number, even after the highest row is
  deleted.
- `uid`: a ULID (time-ordered, 128 bits, 26 characters), used in exports,
  provenance and any future merging of archives.

Commands accept `42`, `#42` or a full ULID. Prefix matching is deliberately
not supported.

`holocron restore` reads the live archive's `sqlite_sequence` before
replacing it and raises the restored archive's sequence to at least that
value, so numbers issued after the backup was taken are never reissued.
Undoing a delete in the TUI re-creates the entry with its original `id` and
`uid`: the same record, not a reuse.

## Consequences

- References are short and stable within an archive.
- Numbers are per archive. Two archives can both contain a `#42`; the ULID
  disambiguates, and exports carry both.
- Deleting entries leaves gaps in the numbering, which is intentional.
