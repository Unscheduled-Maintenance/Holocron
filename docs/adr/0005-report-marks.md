# 5. Report marks are separate from tags

Date: 2026-10-05

## Status

Accepted

## Context

Deterministic reports need a way to say "this belongs in the staff update" or
"raise this in my one-on-one" without making capture cumbersome. Re-using
tags (`#staff`) was considered. It needs no new concept, but it gives
ordinary words hidden behaviour, collides with existing tagging habits, and
makes tag renames or merges silently change report contents.

## Decision

Introduce *report marks*: a small fixed vocabulary stored in
`entry_marks(entry_id, mark, created_at)`.

| Mark | Effect |
|---|---|
| `staff` | included in the staff update, ranked first |
| `one-on-one` | listed under "To discuss" |
| `quarterly` | featured in the quarterly report |
| `important` | ranked above unmarked items everywhere |
| `cross-team` | own section in the staff update |

Marks are set with `--mark` at capture, `holocron mark`/`unmark`, the TUI's
`m` checklist, or the `marks:` field of the editor document. Tags never affect
report selection.

Resolution of problems and follow-ups is a separate `resolved_at` timestamp
rather than a mark, because it is a change of state, not an audience.

## Consequences

- Report behaviour is explicit and documented; `#staff` is just a tag.
- Capture stays fast: marks are optional and usually added later.
- Adding a mark is a code change (validation plus report logic), not a
  schema migration.
- Report items record "marked staff" (and similar) as a reason, so
  provenance explains the selection.
