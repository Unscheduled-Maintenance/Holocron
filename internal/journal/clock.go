package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Unscheduled-Maintenance/Holocron/internal/database"
)

// HLC is a hybrid logical clock reading: wall-clock milliseconds, a counter
// for changes within the same millisecond (or while the wall clock lags a
// clock already seen), and the device that made the change. Merging keeps,
// for each field, the change with the latest clock. See
// docs/adr/0007-multi-device-sync.md.
type HLC struct {
	Wall   int64
	Count  int
	Device string
}

// String encodes the clock; the encoding round-trips through ParseHLC.
func (h HLC) String() string { return fmt.Sprintf("%013d.%06d.%s", h.Wall, h.Count, h.Device) }

// IsZero reports whether the clock is unset.
func (h HLC) IsZero() bool { return h.Wall == 0 && h.Count == 0 && h.Device == "" }

// After reports whether h is a later change than o. Ties on time go to the
// lower device UID, so every device picks the same winner.
func (h HLC) After(o HLC) bool {
	if h.Wall != o.Wall {
		return h.Wall > o.Wall
	}
	if h.Count != o.Count {
		return h.Count > o.Count
	}
	switch {
	case h.Device == o.Device, h.Device == "":
		return false
	case o.Device == "":
		return true // a known device beats a clock inferred from a timestamp
	}
	return h.Device < o.Device
}

// ParseHLC decodes a clock written by HLC.String.
func ParseHLC(s string) (HLC, error) {
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return HLC{}, fmt.Errorf("%w: malformed clock %q", ErrInvalid, s)
	}
	wall, err1 := strconv.ParseInt(parts[0], 10, 64)
	count, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return HLC{}, fmt.Errorf("%w: malformed clock %q", ErrInvalid, s)
	}
	return HLC{Wall: wall, Count: count, Device: parts[2]}, nil
}

// ClockFromTime is the clock assumed for a change known only by its time,
// such as a row written before clocks existed or an entry in an export.
func ClockFromTime(t time.Time) HLC { return HLC{Wall: t.UnixMilli()} }

// Clocks maps field names to the clock of their last change.
type Clocks map[string]HLC

// Fields that merge independently.
const (
	FieldBody     = "body"
	FieldTime     = "time"
	FieldType     = "type"
	FieldProject  = "project"
	FieldTags     = "tags"
	FieldMarks    = "marks"
	FieldResolved = "resolved"

	FieldName        = "name"
	FieldDescription = "description"
	FieldArchived    = "archived"
	FieldAliases     = "aliases"
	FieldURLs        = "urls"
)

// EntryFields and ProjectFields list the fields with clocks.
var (
	EntryFields   = []string{FieldBody, FieldTime, FieldType, FieldProject, FieldTags, FieldMarks, FieldResolved}
	ProjectFields = []string{FieldName, FieldDescription, FieldArchived, FieldAliases, FieldURLs}
)

// Get returns the clock for field, or fallback when the field has none
// (rows written before clocks existed).
func (c Clocks) Get(field string, fallback HLC) HLC {
	if h, ok := c[field]; ok {
		return h
	}
	return fallback
}

// Latest returns the latest clock in c.
func (c Clocks) Latest() HLC {
	var max HLC
	for _, h := range c {
		if h.After(max) {
			max = h
		}
	}
	return max
}

func decodeClocks(s string) Clocks {
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return Clocks{}
	}
	out := make(Clocks, len(m))
	for k, v := range m {
		if h, err := ParseHLC(v); err == nil {
			out[k] = h
		}
	}
	return out
}

// Device is a computer taking part in sync. Before sync is enabled the only
// device is this one, with no label.
type Device struct {
	ID      int64
	UID     string
	Label   string
	Name    string
	IsSelf  bool
	NextNum int64
}

// SelfDevice returns this computer's device, creating it on first use.
func (s *Store) SelfDevice(ctx context.Context) (Device, error) {
	var d Device
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		d, err = s.self(ctx, tx)
		return err
	})
	return d, err
}

func (s *Store) self(ctx context.Context, q queryer) (Device, error) {
	d, err := deviceWhere(ctx, q, `is_self = 1`)
	if errors.Is(err, ErrNotFound) {
		_, err = q.ExecContext(ctx, `INSERT INTO devices (uid, is_self, created_at) VALUES (?, 1, ?)`,
			NewUID(s.now()), formatTime(s.now()))
		if err != nil {
			return Device{}, database.Describe(fmt.Errorf("registering this device: %w", err))
		}
		return deviceWhere(ctx, q, `is_self = 1`)
	}
	return d, err
}

// Devices lists every known device, this one first.
func (s *Store) Devices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, uid, coalesce(label, ''), name, is_self, next_num FROM devices ORDER BY is_self DESC, label, id`)
	if err != nil {
		return nil, database.Describe(err)
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.UID, &d.Label, &d.Name, &d.IsSelf, &d.NextNum); err != nil {
			return nil, database.Describe(err)
		}
		out = append(out, d)
	}
	return out, database.Describe(rows.Err())
}

func deviceWhere(ctx context.Context, q queryer, where string, args ...any) (Device, error) {
	var d Device
	err := q.QueryRowContext(ctx, `SELECT id, uid, coalesce(label, ''), name, is_self, next_num FROM devices WHERE `+where, args...).
		Scan(&d.ID, &d.UID, &d.Label, &d.Name, &d.IsSelf, &d.NextNum)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, fmt.Errorf("%w: no such device", ErrNotFound)
	}
	return d, database.Describe(err)
}

// tick returns a new clock reading for a change made on this device inside
// tx. It never goes backwards, even if the wall clock does, and is always
// later than any clock this archive has seen.
func (s *Store) tick(ctx context.Context, tx queryer) (HLC, error) {
	self, err := s.self(ctx, tx)
	if err != nil {
		return HLC{}, err
	}
	last, err := lastClock(ctx, tx)
	if err != nil {
		return HLC{}, err
	}
	h := HLC{Wall: s.now().UnixMilli(), Device: self.UID}
	if h.Wall <= last.Wall {
		h.Wall, h.Count = last.Wall, last.Count+1
	}
	return h, saveClock(ctx, tx, h)
}

func lastClock(ctx context.Context, q queryer) (HLC, error) {
	v, err := meta(ctx, q, "clock")
	if err != nil || v == "" {
		return HLC{}, err
	}
	return ParseHLC(v)
}

func saveClock(ctx context.Context, q queryer, h HLC) error {
	return setMeta(ctx, q, "clock", HLC{Wall: h.Wall, Count: h.Count}.String())
}

func meta(ctx context.Context, q queryer, key string) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM sync_meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, database.Describe(err)
}

func setMeta(ctx context.Context, q queryer, key, value string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO sync_meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return database.Describe(err)
}

// touchEntry records that fields of an entry changed at h and marks the
// entry as changed here.
func touchEntry(ctx context.Context, tx queryer, id int64, h HLC, fields ...string) error {
	return touch(ctx, tx, "entries", id, h, fields)
}

func touchProject(ctx context.Context, tx queryer, id int64, h HLC, fields ...string) error {
	return touch(ctx, tx, "projects", id, h, fields)
}

func touch(ctx context.Context, tx queryer, table string, id int64, h HLC, fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	expr := "clocks"
	args := make([]any, 0, 2*len(fields)+1)
	for _, f := range fields {
		expr = "json_set(" + expr + ", ?, ?)"
		args = append(args, "$."+f, h.String())
	}
	args = append(args, id)
	_, err := tx.ExecContext(ctx, `UPDATE `+table+` SET clocks = `+expr+`, dirty = 1 WHERE id = ?`, args...)
	return database.Describe(err)
}

// nextNumber issues the number for a new entry made here: this device's
// next labelled number once sync has given it a label, otherwise the next
// plain number. Numbers are never reissued.
func (s *Store) nextNumber(ctx context.Context, tx queryer) (num int64, device sql.NullInt64, err error) {
	self, err := s.self(ctx, tx)
	if err != nil {
		return 0, device, err
	}
	if self.Label != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE devices SET next_num = next_num + 1 WHERE id = ?`, self.ID); err != nil {
			return 0, device, database.Describe(err)
		}
		return self.NextNum, sql.NullInt64{Int64: self.ID, Valid: true}, nil
	}
	v, err := meta(ctx, tx, "plain_next")
	if err != nil {
		return 0, device, err
	}
	num, _ = strconv.ParseInt(v, 10, 64)
	if num < 1 {
		num = 1
	}
	return num, device, setMeta(ctx, tx, "plain_next", strconv.FormatInt(num+1, 10))
}

// bury records that the record with uid was deleted at clock.
func bury(ctx context.Context, tx queryer, kind, uid string, clock HLC) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tombstones (uid, kind, clock, dirty) VALUES (?, ?, ?, 1)
		ON CONFLICT (uid) DO UPDATE SET clock = excluded.clock, dirty = 1`, uid, kind, clock.String())
	return database.Describe(err)
}

var labelRe = regexp.MustCompile(`^[a-z]+$`)

// SetSelfLabel gives this device its sync label. From then on its new
// entries are numbered #1a, #2a, ... continuing after the highest plain
// number, so the two kinds of number never overlap. A label cannot be
// changed once set.
func (s *Store) SetSelfLabel(ctx context.Context, label string) (Device, error) {
	if !labelRe.MatchString(label) {
		return Device{}, fmt.Errorf("%w: device label %q must be lower-case letters", ErrInvalid, label)
	}
	var d Device
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		self, err := s.self(ctx, tx)
		if err != nil {
			return err
		}
		if self.Label == label {
			d = self
			return nil
		}
		if self.Label != "" {
			return fmt.Errorf("%w: this device is already labelled %q", ErrConflict, self.Label)
		}
		if other, err := deviceWhere(ctx, tx, `label = ?`, label); err == nil && other.ID != self.ID {
			return fmt.Errorf("%w: label %q belongs to another device", ErrConflict, label)
		}
		v, err := meta(ctx, tx, "plain_next")
		if err != nil {
			return err
		}
		plainNext, _ := strconv.ParseInt(v, 10, 64)
		if _, err := tx.ExecContext(ctx, `UPDATE devices SET label = ?, next_num = max(next_num, ?) WHERE id = ?`,
			label, plainNext, self.ID); err != nil {
			return database.Describe(err)
		}
		d, err = s.self(ctx, tx)
		return err
	})
	return d, err
}

// EntryClocks returns when each field of an entry last changed.
func (s *Store) EntryClocks(ctx context.Context, id int64) (Clocks, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT clocks FROM entries WHERE id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: no entry #%d", ErrNotFound, id)
	}
	if err != nil {
		return nil, database.Describe(err)
	}
	return decodeClocks(raw), nil
}
