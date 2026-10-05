package journal

import (
	"fmt"
	"regexp"
	"strings"
)

// Capture is the result of parsing quick-capture shorthand.
type Capture struct {
	Body    string
	Project string
	Tags    []string
	Type    Type
}

var (
	wordRe     = regexp.MustCompile(`\S+`)
	sigilRe    = regexp.MustCompile(`^([+#])(\p{L}[\p{L}\p{N}_./:-]*?)([.,;:!?)]*)$`)
	typeLeadRe = regexp.MustCompile(`(?i)^(decision|problem|investigation|follow-?up|accomplishment|note|work)\s*:\s*\S`)
)

// ParseShorthand extracts +project and #tag tokens from quick-capture text.
//
// Rules, chosen to be predictable:
//
//   - A token is a whitespace-separated word starting with + or # followed by
//     a letter: +aws, #security. "#123", "C#" and "+1" are ordinary text.
//   - Tokens at the start or end of the text are removed from the body.
//   - Tokens in the middle of a sentence keep their word in the body without
//     the sigil: "Patched #security hole" stores "Patched security hole".
//   - At most one +project may be given.
//   - Text beginning with a type name and a colon ("Decision: ...") gets
//     that type unless one is set explicitly. The body is left unchanged.
//
// Shells treat an unquoted # as a comment, so #tags must be inside quotes.
func ParseShorthand(text string) (Capture, error) {
	var c Capture
	locs := wordRe.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return c, nil
	}
	type tok struct {
		start, end int
		sigil      byte
		name, tail string
	}
	toks := make([]tok, len(locs))
	for i, l := range locs {
		toks[i] = tok{start: l[0], end: l[1]}
		if m := sigilRe.FindStringSubmatch(text[l[0]:l[1]]); m != nil {
			toks[i].sigil = m[1][0]
			toks[i].name = m[2]
			toks[i].tail = m[3]
		}
	}
	first, last := 0, len(toks)-1
	for first <= last && toks[first].sigil != 0 {
		first++
	}
	for last >= first && toks[last].sigil != 0 {
		last--
	}

	for _, t := range toks {
		switch t.sigil {
		case '+':
			if c.Project != "" && !strings.EqualFold(c.Project, t.name) {
				return c, fmt.Errorf("%w: more than one +project in %q (use one, or --project)", ErrInvalid, text)
			}
			c.Project = t.name
		case '#':
			c.Tags = append(c.Tags, t.name)
		}
	}

	if first <= last {
		var b strings.Builder
		pos := toks[first].start
		for i := first; i <= last; i++ {
			t := toks[i]
			b.WriteString(text[pos:t.start])
			if t.sigil != 0 {
				b.WriteString(t.name + t.tail)
			} else {
				b.WriteString(text[t.start:t.end])
			}
			pos = t.end
		}
		c.Body = strings.TrimSpace(b.String())
	}
	if m := typeLeadRe.FindStringSubmatch(c.Body); m != nil {
		if t, err := ParseType(m[1]); err == nil {
			c.Type = t
		}
	}
	return c, nil
}

// SplitSearch separates +project and #tag filters from search text, so the
// same shorthand works when searching as when capturing:
// "cloudtrail +aws #security" searches for "cloudtrail" in project aws
// with tag security.
func SplitSearch(q string) (text string, projects, tags []string) {
	var words []string
	for _, w := range strings.Fields(q) {
		if m := sigilRe.FindStringSubmatch(w); m != nil {
			if m[1] == "+" {
				projects = append(projects, m[2])
			} else {
				tags = append(tags, m[2])
			}
			continue
		}
		words = append(words, w)
	}
	return strings.Join(words, " "), projects, tags
}
