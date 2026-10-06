// Package journal implements Holocron's core records — entries, projects,
// tags and report marks — and the queries over them, including full-text
// search. It is the single implementation shared by the CLI and the TUI.
package journal

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Type is an optional classification of an entry.
type Type string

// Entry types. Types are optional; an entry with no type is simply a note
// of something that happened.
const (
	TypeNone           Type = ""
	TypeWork           Type = "work"
	TypeDecision       Type = "decision"
	TypeInvestigation  Type = "investigation"
	TypeProblem        Type = "problem"
	TypeAccomplishment Type = "accomplishment"
	TypeNote           Type = "note"
	TypeFollowUp       Type = "follow-up"
)

// Types lists every entry type in display order.
var Types = []Type{TypeWork, TypeAccomplishment, TypeDecision, TypeInvestigation, TypeProblem, TypeFollowUp, TypeNote}

// TypeStrings returns Types as strings, for help text and completion.
func TypeStrings() []string {
	out := make([]string, len(Types))
	for i, t := range Types {
		out[i] = string(t)
	}
	return out
}

// Opens reports whether entries of this type stay "open" until resolved.
func (t Type) Opens() bool { return t == TypeProblem || t == TypeFollowUp }

// ParseType accepts a type name, a unique prefix ("dec", "inv"), or a common
// spelling variant ("followup").
func ParseType(s string) (Type, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "none":
		return TypeNone, nil
	case "followup", "follow_up", "todo":
		return TypeFollowUp, nil
	}
	return matchVocabulary(s, Types, "type")
}

// TypeAliases maps shorthand names to entry types: "win" for accomplishment.
// Aliases are input only: entries always store and display the real type.
// Keys are lower case.
type TypeAliases map[string]Type

// DefaultTypeAliases returns the built-in aliases.
func DefaultTypeAliases() map[string]string {
	return map[string]string{
		"win":  string(TypeAccomplishment),
		"look": string(TypeInvestigation),
	}
}

var aliasRe = regexp.MustCompile(`^\p{L}[\p{L}\p{N}_-]*$`)

// NewTypeAliases validates configured aliases. An empty target removes the
// alias, so a default can be switched off. Aliases may not be type names.
func NewTypeAliases(in map[string]string) (TypeAliases, error) {
	out := TypeAliases{}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(in)) {
		target := strings.TrimSpace(in[name])
		alias := strings.ToLower(strings.TrimSpace(name))
		if target == "" {
			continue
		}
		if !aliasRe.MatchString(alias) {
			errs = append(errs, fmt.Errorf("%w: type alias %q must start with a letter and contain only letters, digits, dashes and underscores", ErrInvalid, name))
			continue
		}
		if _, ok := leadType(alias); ok {
			errs = append(errs, fmt.Errorf("%w: type alias %q is already a type name", ErrInvalid, name))
			continue
		}
		t, err := ParseType(target)
		if err != nil {
			errs = append(errs, fmt.Errorf("type alias %q: %w", name, err))
			continue
		}
		if t == TypeNone {
			errs = append(errs, fmt.Errorf("%w: type alias %q must name a type", ErrInvalid, name))
			continue
		}
		out[alias] = t
	}
	return out, errors.Join(errs...)
}

// Parse is ParseType that also accepts an alias. An exact alias wins over a
// type prefix.
func (al TypeAliases) Parse(s string) (Type, error) {
	if t, ok := al[strings.ToLower(strings.TrimSpace(s))]; ok {
		return t, nil
	}
	return ParseType(s)
}

// Lead resolves the word before the colon in a "Decision: ..." capture
// prefix. Only whole type names, the follow-up spellings and aliases count;
// prefixes do not, so "Dec: ..." stays as text.
func (al TypeAliases) Lead(word string) (Type, bool) {
	if t, ok := al[strings.ToLower(word)]; ok {
		return t, true
	}
	return leadType(word)
}

func leadType(word string) (Type, bool) {
	w := strings.ToLower(word)
	if w == "followup" {
		return TypeFollowUp, true
	}
	for _, t := range Types {
		if string(t) == w {
			return t, true
		}
	}
	return TypeNone, false
}

// Mark is an explicit report signal: "this entry belongs in the staff
// update". Marks are application-level metadata, deliberately separate from
// free-form tags. See docs/adr/0005-report-marks.md.
type Mark string

// Report marks.
const (
	MarkStaff     Mark = "staff"
	MarkOneOnOne  Mark = "one-on-one"
	MarkQuarterly Mark = "quarterly"
	MarkImportant Mark = "important"
	MarkCrossTeam Mark = "cross-team"
)

// Marks lists every report mark.
var Marks = []Mark{MarkStaff, MarkOneOnOne, MarkQuarterly, MarkImportant, MarkCrossTeam}

// MarkStrings returns Marks as strings.
func MarkStrings() []string {
	out := make([]string, len(Marks))
	for i, m := range Marks {
		out[i] = string(m)
	}
	return out
}

// ParseMark accepts a mark name, a unique prefix, or a common variant.
func ParseMark(s string) (Mark, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "1on1", "1:1", "1-1", "oneonone", "one_on_one", "1-on-1":
		return MarkOneOnOne, nil
	case "crossteam", "cross_team", "other-teams":
		return MarkCrossTeam, nil
	case "quarter", "q":
		return MarkQuarterly, nil
	}
	return matchVocabulary(s, Marks, "mark")
}

// ParseMarks parses a list, also splitting comma-separated values.
func ParseMarks(in []string) ([]Mark, error) {
	var out []Mark
	for _, raw := range splitList(in) {
		m, err := ParseMark(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return uniqueSorted(out), nil
}

func matchVocabulary[T ~string](s string, vocab []T, what string) (T, error) {
	var zero T
	var matches []T
	for _, v := range vocab {
		if string(v) == s {
			return v, nil
		}
		if s != "" && strings.HasPrefix(string(v), s) {
			matches = append(matches, v)
		}
	}
	names := make([]string, len(vocab))
	for i, v := range vocab {
		names[i] = string(v)
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return zero, fmt.Errorf("%w: unknown %s %q (choose from %s)", ErrInvalid, what, s, strings.Join(names, ", "))
	default:
		return zero, fmt.Errorf("%w: %s %q is ambiguous (choose from %s)", ErrInvalid, what, s, strings.Join(names, ", "))
	}
}

// Source records where an imported entry came from.
type Source struct {
	Type       string    `json:"type"`
	ID         string    `json:"id"`
	URL        string    `json:"url,omitempty"`
	ImportedAt time.Time `json:"imported_at"`
}

// Entry is one journal record.
type Entry struct {
	ID         int64
	UID        string
	OccurredAt time.Time // the moment the work happened (UTC instant)
	UTCOffset  int       // seconds east of UTC where the entry was recorded
	Body       string
	Type       Type
	ProjectID  int64
	Project    string
	Tags       []string
	Marks      []Mark
	ResolvedAt *time.Time
	// ResolvedBy is the entry that resolved this one, or 0.
	ResolvedBy int64
	// Resolves lists the entries this one resolved, in ID order.
	Resolves  []int64
	CreatedAt time.Time
	UpdatedAt time.Time
	Source    *Source

	// Snippet is set by text searches: a fragment of the body with matches
	// wrapped in HighlightStart/HighlightEnd.
	Snippet string
}

// Markers wrapping search matches in Entry.Snippet.
const (
	HighlightStart = "\x01"
	HighlightEnd   = "\x02"
)

// Ref returns the short, human-friendly reference for an entry: "#42".
func (e Entry) Ref() string { return fmt.Sprintf("#%d", e.ID) }

// Title returns the first line of the body.
func (e Entry) Title() string {
	first, _, _ := strings.Cut(strings.TrimSpace(e.Body), "\n")
	return strings.TrimSpace(first)
}

// HasMark reports whether the entry carries m.
func (e Entry) HasMark(m Mark) bool {
	for _, x := range e.Marks {
		if x == m {
			return true
		}
	}
	return false
}

// HasTag reports whether the entry carries tag t (case-insensitive).
func (e Entry) HasTag(t string) bool {
	for _, x := range e.Tags {
		if strings.EqualFold(x, t) {
			return true
		}
	}
	return false
}

// IsOpen reports whether the entry is an unresolved problem or follow-up.
func (e Entry) IsOpen() bool { return e.Type.Opens() && e.ResolvedAt == nil }

// RecordedOffset returns the fixed zone the entry was recorded in.
func (e Entry) RecordedOffset() *time.Location {
	return time.FixedZone("", e.UTCOffset)
}

// Errors returned by the journal. Callers match them with errors.Is.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input")
	ErrConflict = errors.New("conflict")
)

// NormalizeTag lower-cases a tag and validates its characters. Tags may
// contain letters, digits, '-', '_', '.', '/' and ':'.
func NormalizeTag(s string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.TrimPrefix(t, "#")
	if t == "" {
		return "", fmt.Errorf("%w: empty tag", ErrInvalid)
	}
	if len(t) > 64 {
		return "", fmt.Errorf("%w: tag %q is longer than 64 characters", ErrInvalid, s)
	}
	for _, r := range t {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_./:", r) {
			continue
		}
		return "", fmt.Errorf("%w: tag %q may only contain letters, digits, dashes, underscores, dots, slashes and colons", ErrInvalid, s)
	}
	return t, nil
}

// NormalizeTags normalises, de-duplicates and sorts tags. Comma-separated
// values are split.
func NormalizeTags(in []string) ([]string, error) {
	var out []string
	for _, raw := range splitList(in) {
		t, err := NormalizeTag(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return uniqueSorted(out), nil
}

func splitList(in []string) []string {
	var out []string
	for _, s := range in {
		for _, part := range strings.Split(s, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func uniqueSorted[T ~string](in []T) []T {
	seen := map[T]bool{}
	out := make([]T, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// NormalizeProjectName trims a project name and validates it.
func NormalizeProjectName(s string) (string, error) {
	n := strings.Join(strings.Fields(s), " ")
	n = strings.TrimPrefix(n, "+")
	if n == "" {
		return "", fmt.Errorf("%w: empty project name", ErrInvalid)
	}
	if len(n) > 100 {
		return "", fmt.Errorf("%w: project name is longer than 100 characters", ErrInvalid)
	}
	if strings.ContainsAny(n, ",\n\t") {
		return "", fmt.Errorf("%w: project name %q may not contain commas or line breaks", ErrInvalid, s)
	}
	return n, nil
}

// NormalizeBody trims surrounding whitespace and normalises line endings.
func NormalizeBody(s string) (string, error) {
	b := strings.ReplaceAll(s, "\r\n", "\n")
	b = strings.TrimSpace(b)
	if b == "" {
		return "", fmt.Errorf("%w: entry text is empty", ErrInvalid)
	}
	return b, nil
}
