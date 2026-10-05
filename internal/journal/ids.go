package journal

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Every record has two identifiers:
//
//   - a small integer ID ("#42") for people to type. SQLite AUTOINCREMENT
//     never reuses a number, even after deletion, so a short ID can never
//     silently start referring to a different record.
//   - a ULID (26 characters, time-ordered, globally unique) for exports,
//     provenance and any future merging of archives.
//
// See docs/adr/0002-identifiers.md.

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewUID returns a new ULID string.
func NewUID(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	if _, err := rand.Read(b[6:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	// 128 bits -> 26 base32 characters (the first carries only 3 bits).
	var out [26]byte
	var acc uint64
	var bits uint
	idx := 25
	for i := 15; i >= 0; i-- {
		acc |= uint64(b[i]) << bits
		bits += 8
		for bits >= 5 && idx >= 0 {
			out[idx] = crockford[acc&31]
			acc >>= 5
			bits -= 5
			idx--
		}
	}
	if idx >= 0 {
		out[idx] = crockford[acc&31]
	}
	return string(out[:])
}

// IsUID reports whether s looks like a ULID.
func IsUID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, r := range strings.ToUpper(s) {
		if !strings.ContainsRune(crockford, r) {
			return false
		}
	}
	return true
}

// ParseRef interprets an entry reference typed by a person: "42", "#42" or
// a full ULID. It returns either a numeric ID or a UID.
func ParseRef(ref string) (id int64, uid string, err error) {
	r := strings.TrimSpace(ref)
	r = strings.TrimPrefix(r, "#")
	if r == "" {
		return 0, "", fmt.Errorf("%w: empty entry reference", ErrInvalid)
	}
	if n, perr := strconv.ParseInt(r, 10, 64); perr == nil {
		if n <= 0 {
			return 0, "", fmt.Errorf("%w: entry IDs are positive numbers", ErrInvalid)
		}
		return n, "", nil
	}
	if IsUID(r) {
		return 0, strings.ToUpper(r), nil
	}
	return 0, "", fmt.Errorf("%w: %q is not an entry ID (use the number shown as #42, or a full UID)", ErrInvalid, ref)
}

// Timestamps are stored as fixed-width UTC strings so that lexical order is
// chronological order and range queries can use indexes.
const dbTimeLayout = "2006-01-02T15:04:05.000Z"

func formatTime(t time.Time) string { return t.UTC().Format(dbTimeLayout) }

func parseTime(s string) (time.Time, error) {
	if t, ok := parseStoredTime(s); ok {
		return t, nil
	}
	t, err := time.Parse(dbTimeLayout, s)
	if err != nil {
		// Be lenient with hand-edited or imported rows.
		t, err = time.Parse(time.RFC3339Nano, s)
	}
	return t.UTC(), err
}

// parseStoredTime decodes the exact layout Holocron writes,
// "2006-01-02T15:04:05.000Z", without the general-purpose parser. Listing
// thousands of entries parses three timestamps per row, and time.Parse was a
// measurable share of that work. Anything else falls back to time.Parse.
func parseStoredTime(s string) (time.Time, bool) {
	if len(s) != 24 || s[4] != '-' || s[7] != '-' || s[10] != 'T' || s[13] != ':' ||
		s[16] != ':' || s[19] != '.' || s[23] != 'Z' {
		return time.Time{}, false
	}
	num := func(from, to int) (int, bool) {
		n := 0
		for i := from; i < to; i++ {
			c := s[i]
			if c < '0' || c > '9' {
				return 0, false
			}
			n = n*10 + int(c-'0')
		}
		return n, true
	}
	year, ok1 := num(0, 4)
	month, ok2 := num(5, 7)
	day, ok3 := num(8, 10)
	hour, ok4 := num(11, 13)
	minute, ok5 := num(14, 16)
	sec, ok6 := num(17, 19)
	milli, ok7 := num(20, 23)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || !ok7 ||
		month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 || sec > 59 {
		return time.Time{}, false
	}
	t := time.Date(year, time.Month(month), day, hour, minute, sec, milli*int(time.Millisecond), time.UTC)
	if t.Day() != day { // e.g. 31 February normalised into March: let time.Parse reject it
		return time.Time{}, false
	}
	return t, true
}
