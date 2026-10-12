# 9. The daily plan is follow-ups with a planned day

Date: 2026-10-12

## Status

Accepted. Tracked in #36.

## Context

A daily standup ends with "what I plan to do tomorrow". The next morning,
that list is the baseline for the day. It should stay in view until it is
done, and a new list is written at the end of each day.

Holocron has follow-ups, which stay open until resolved, but no idea of a
day. A plan also has rules about time that follow-ups don't:

- "Tomorrow" on a Friday means Monday.
- After a sick day, the plan written before it is still the plan.
- Planned leave means planning for a day further ahead.

Plans must sync between computers (ADR 0007) and survive export and import.
Sync already runs between computers on different Holocron versions.

## Options considered

1. **A new `plan` entry type, dated by when it was written.** Needs no
   migration, because `type` has no CHECK constraint. But the day a plan is
   for would be guessed from when it was written, which is wrong for a plan
   written the same morning. An unfinished plan item would have to change
   type to become an ordinary follow-up, and older clients reject edits to
   an unknown type.
2. **A job that turns yesterday's plan into follow-ups.** Two computers
   could both run the job and conflict, and the job needs a scheduler that
   Holocron doesn't have.
3. **A follow-up with a planned day** (chosen). Add `planned_for`, a local
   calendar date, and work out which plan is current when it is read.

## Decision

- `entries.planned_for` (schema version 5) holds a `YYYY-MM-DD` local date.
  Only follow-ups can be planned. An entry whose type changes away from
  follow-up keeps the value but is no longer on any plan.
- **The current plan is the latest planned day not after today. The next
  plan is the earliest planned day after today.** A plan stays current until
  a newer one starts, so a day off needs no special handling. Nothing is
  rewritten when the day changes.
- An open follow-up planned for a day before the current plan is no longer
  on a plan. It is still an open follow-up, so it stays in `--open`, "Still
  open" and "Coming up".
- Carrying an item over means changing its `planned_for`. It keeps its
  number and history. Carrying is a choice for each item and is off by
  default, so each day starts with a fresh list.
- `work_days` in the configuration (default Monday to Friday) defines "the
  next working day". That is the default target for `holocron plan` and the
  TUI's `P`. `--for` takes `today`, a weekday or a date, which covers
  planned leave. There is no leave or holiday calendar.
- The TUI shows the current plan in a panel under the header, with a count
  for the next plan. `tab` focuses the panel so items can be resolved there.

### Sync and import

`planned_for` merges as its own field, with a `plan` clock, under the same
last-writer-wins rule as every other field. It differs in one way: **a
record with no `plan` clock says nothing about the plan.**

- Holocron versions from before plans publish records with neither the
  field nor the clock. Without this rule, the usual fallback (treating a
  missing clock as the record's `updated_at`) would let an edit to an
  entry's text, made on an older computer, take that entry off its plan.
- In the same way, a JSON export from before plans has no `planned_for` key.
  On import it gets no `plan` clock. A `"planned_for": null` in a newer
  export is an explicit "not planned" and does carry a clock.
- An older computer drops the `plan` clock when it merges a record, because
  it doesn't know the field. That is harmless: it can't publish a plan
  anyway, and the computers that know about plans keep their own clock.

The sync format stays `holocron.sync/v1` and the export format stays
`holocron.export/v1`: both only gain an optional field.

## Consequences

- One migration and one more merged field, but no new concept in the merge
  engine beyond "a missing clock is not a change".
- Reports barely change: plan items are follow-ups and already appear
  where open follow-ups do. An open one's reasons (`--explain`) say which plan
  it came from; `holocron list` shows the same as "planned <day>".
- An archive at schema version 5 can't be opened by an older Holocron (ADR
  0003), but it still syncs with one.
- Planned leave is handled by planning ahead with `--for`. If people need
  more than that, a leave calendar could be added later without changing
  how the current plan is chosen.
