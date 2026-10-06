# Architecture decision records

Short records of decisions with long-term consequences, in the Nygard format
(context, decision, consequences).

| # | Decision | Status |
|---|---|---|
| [0001](0001-sqlite-driver.md) | Pure-Go SQLite (modernc.org/sqlite) with FTS5 | Accepted |
| [0002](0002-identifiers.md) | Integer entry numbers plus ULIDs | Accepted |
| [0003](0003-schema-migrations.md) | Embedded, ordered, transactional SQL migrations | Accepted |
| [0004](0004-cli-tui-boundary.md) | One application layer shared by CLI and TUI | Accepted |
| [0005](0005-report-marks.md) | Report marks are separate from tags | Accepted |
| [0006](0006-ai-provider-boundary.md) | AI behind a provider interface, fed only selected entries | Accepted |
| [0007](0007-multi-device-sync.md) | Multi-device sync through an encrypted folder | Proposed |
