-- Holocron schema version 5: a follow-up can be planned for a day
-- ("holocron plan ..."), as a local calendar date (YYYY-MM-DD). The day's
-- plan is the follow-ups planned for it. See docs/adr/0009-daily-plan.md.

ALTER TABLE entries ADD COLUMN planned_for TEXT;
CREATE INDEX entries_planned_for ON entries (planned_for) WHERE planned_for IS NOT NULL;
