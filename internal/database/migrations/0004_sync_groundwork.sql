-- Holocron schema version 4: groundwork for multi-device sync
-- (docs/adr/0007-multi-device-sync.md). Nothing is synced yet.

-- This computer and, once sync is used, the others. A device gets a label
-- ("a", "b", ...) when sync is enabled; entries it numbers after that are
-- shown as #12a. next_num is that device's counter.
CREATE TABLE devices (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    uid        TEXT    NOT NULL UNIQUE,
    label      TEXT    UNIQUE,
    name       TEXT    NOT NULL DEFAULT '',
    is_self    INTEGER NOT NULL DEFAULT 0,
    next_num   INTEGER NOT NULL DEFAULT 1,
    created_at TEXT    NOT NULL
);
CREATE UNIQUE INDEX devices_self ON devices (is_self) WHERE is_self = 1;

-- The number people see is now separate from the internal row ID. Entries
-- without a device keep plain numbers (#42), which never change meaning.
ALTER TABLE entries ADD COLUMN num INTEGER;
ALTER TABLE entries ADD COLUMN num_device INTEGER REFERENCES devices (id);
UPDATE entries SET num = id;
CREATE UNIQUE INDEX entries_number ON entries (coalesce(num_device, 0), num);

-- Per-field change clocks (hybrid logical clocks, as JSON) for merging, and
-- a flag for rows changed here since they were last published.
ALTER TABLE entries ADD COLUMN clocks TEXT NOT NULL DEFAULT '{}';
ALTER TABLE entries ADD COLUMN dirty INTEGER NOT NULL DEFAULT 1;
CREATE INDEX entries_dirty ON entries (dirty) WHERE dirty = 1;
ALTER TABLE projects ADD COLUMN clocks TEXT NOT NULL DEFAULT '{}';
ALTER TABLE projects ADD COLUMN dirty INTEGER NOT NULL DEFAULT 1;
CREATE INDEX projects_dirty ON projects (dirty) WHERE dirty = 1;

-- Deleted entries and projects, so a delete reaches other devices and an
-- old copy cannot bring the record back.
CREATE TABLE tombstones (
    uid   TEXT    PRIMARY KEY,
    kind  TEXT    NOT NULL CHECK (kind IN ('entry', 'project')),
    clock TEXT    NOT NULL,
    dirty INTEGER NOT NULL DEFAULT 1
);

-- A project merged into another keeps its old UID here, so references to
-- it still resolve.
CREATE TABLE project_redirects (
    uid        TEXT    PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE
);

ALTER TABLE report_log ADD COLUMN uid TEXT;
UPDATE report_log SET uid = upper(hex(randomblob(16)));
CREATE UNIQUE INDEX report_log_uid ON report_log (uid);
ALTER TABLE report_log ADD COLUMN dirty INTEGER NOT NULL DEFAULT 1;

-- Small key/value state: the plain-number counter and the last clock.
CREATE TABLE sync_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT INTO sync_meta (key, value)
SELECT 'plain_next', max(coalesce((SELECT max(id) FROM entries), 0),
                         coalesce((SELECT seq FROM sqlite_sequence WHERE name = 'entries'), 0)) + 1;
