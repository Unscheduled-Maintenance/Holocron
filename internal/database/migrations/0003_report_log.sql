-- Holocron schema version 3: reports recorded as sent ("holocron report staff
-- --record"), so the next report can start where the last one ended
-- ("--since last"). range_start and range_end are NULL when unbounded.

CREATE TABLE report_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT    NOT NULL,
    range_start TEXT,
    range_end   TEXT,
    recorded_at TEXT    NOT NULL
);
CREATE INDEX report_log_kind ON report_log (kind, recorded_at);
