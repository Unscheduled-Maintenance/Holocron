-- Holocron schema version 1.
--
-- Timestamps are TEXT in UTC using the fixed-width layout
-- 'YYYY-MM-DDTHH:MM:SS.sssZ' so that lexical order equals time order.
-- Vocabularies (entry types, report marks) are validated by the application
-- rather than CHECK constraints so they can grow without table rebuilds.

CREATE TABLE projects (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    uid         TEXT    NOT NULL UNIQUE,
    name        TEXT    NOT NULL UNIQUE COLLATE NOCASE CHECK (length(trim(name)) > 0),
    description TEXT    NOT NULL DEFAULT '',
    archived_at TEXT,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);

CREATE TABLE project_aliases (
    alias      TEXT    PRIMARY KEY COLLATE NOCASE,
    project_id INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE
);
CREATE INDEX project_aliases_project ON project_aliases (project_id);

-- Repository paths and URLs associated with a project.
CREATE TABLE project_links (
    project_id INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    kind       TEXT    NOT NULL CHECK (kind IN ('path', 'url')),
    value      TEXT    NOT NULL,
    PRIMARY KEY (project_id, kind, value)
) WITHOUT ROWID;

-- AUTOINCREMENT guarantees an entry number is never reused after deletion,
-- so a short ID such as 42 can never silently start meaning another record.
CREATE TABLE entries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    uid         TEXT    NOT NULL UNIQUE,
    occurred_at TEXT    NOT NULL,
    -- Seconds east of UTC where the entry was recorded; preserved so the
    -- original local wall-clock time can always be reconstructed.
    utc_offset  INTEGER NOT NULL DEFAULT 0,
    body        TEXT    NOT NULL CHECK (length(trim(body)) > 0),
    type        TEXT,
    project_id  INTEGER REFERENCES projects (id) ON DELETE SET NULL,
    resolved_at TEXT,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL,
    -- Provenance for imported records.
    source_type TEXT,
    source_id   TEXT,
    source_url  TEXT,
    imported_at TEXT,
    CHECK ((source_type IS NULL) = (source_id IS NULL))
);
CREATE INDEX entries_occurred ON entries (occurred_at);
CREATE INDEX entries_project ON entries (project_id, occurred_at);
CREATE INDEX entries_type ON entries (type, occurred_at);
CREATE UNIQUE INDEX entries_source ON entries (source_type, source_id) WHERE source_type IS NOT NULL;

CREATE TABLE tags (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT    NOT NULL UNIQUE COLLATE NOCASE
);

CREATE TABLE entry_tags (
    entry_id INTEGER NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    tag_id   INTEGER NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
    PRIMARY KEY (entry_id, tag_id)
) WITHOUT ROWID;
CREATE INDEX entry_tags_tag ON entry_tags (tag_id, entry_id);

-- Report marks: explicit, application-level signals such as "staff" that
-- say an entry belongs in a particular report. Deliberately separate from
-- free-form tags.
CREATE TABLE entry_marks (
    entry_id   INTEGER NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    mark       TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    PRIMARY KEY (entry_id, mark)
) WITHOUT ROWID;
CREATE INDEX entry_marks_mark ON entry_marks (mark, entry_id);

-- Full-text index. rowid = entries.id. Maintained by the application inside
-- the same transaction as every write that changes body, project or tags.
CREATE VIRTUAL TABLE entries_fts USING fts5 (
    body,
    project,
    tags,
    tokenize = 'unicode61 remove_diacritics 2'
);
