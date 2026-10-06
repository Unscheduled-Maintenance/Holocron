-- Holocron schema version 2: an entry can record which later entry resolved
-- it ("holocron add ... --resolves 42"). Deleting the resolving entry keeps
-- the resolution but drops the link.

ALTER TABLE entries ADD COLUMN resolved_by INTEGER REFERENCES entries (id) ON DELETE SET NULL;
CREATE INDEX entries_resolved_by ON entries (resolved_by) WHERE resolved_by IS NOT NULL;
