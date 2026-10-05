// Package markdown renders entry text as Markdown for the terminal using
// Glamour, Charm's standard renderer.
//
// Entries are stored exactly as typed; rendering is display-only. Two
// adjustments keep the rendering faithful to a journal:
//
//   - Raw HTML is never interpreted: "<" outside code is escaped, so text
//     such as "fixed <script> handling" or "Vec<T>" is shown, not swallowed.
//   - Single line breaks are preserved, because people press Enter in a
//     journal when they mean a new line.
package markdown

import (
	"strings"
	"sync"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type key struct {
	width int
	dark  bool
}

var (
	mu        sync.Mutex
	renderers = map[key]*glamour.TermRenderer{}
)

func renderer(width int, dark bool) (*glamour.TermRenderer, error) {
	k := key{width, dark}
	if r, ok := renderers[k]; ok {
		return r, nil
	}
	cfg := styles.LightStyleConfig
	if dark {
		cfg = styles.DarkStyleConfig
	}
	// The surrounding view supplies its own margins and spacing.
	var zero uint
	cfg.Document.Margin = &zero
	cfg.Document.BlockPrefix, cfg.Document.BlockSuffix = "", ""
	cfg.Document.Color = nil // inherit the terminal's own text colour
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(cfg),
		glamour.WithWordWrap(width),
		glamour.WithPreservedNewLines(),
	)
	if err != nil {
		return nil, err
	}
	renderers[k] = r
	return r, nil
}

// Render renders text as Markdown wrapped to width cells. dark selects the
// palette for dark or light terminal backgrounds. If rendering fails the
// text is returned word-wrapped but otherwise unchanged.
func Render(text string, width int, dark bool) string {
	if width < 10 {
		width = 10
	}
	plain := lipgloss.NewStyle().Width(width).Render(text)
	mu.Lock()
	defer mu.Unlock()
	r, err := renderer(width, dark)
	if err != nil {
		return plain
	}
	out, err := r.Render(escapeHTML(text))
	if err != nil {
		return plain
	}
	return tidy(out)
}

// tidy removes the padding Glamour adds: trailing spaces on each line and
// blank lines before and after the content.
func tidy(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(ansi.Strip(lines[start])) == "" {
		start++
	}
	for end > start && strings.TrimSpace(ansi.Strip(lines[end-1])) == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

// escapeHTML backslash-escapes "<" outside code spans and fenced code blocks
// so Markdown never treats entry text as HTML. Inside code, where escapes are
// shown literally, text is left alone.
func escapeHTML(s string) string {
	var b strings.Builder
	inFence := false
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			b.WriteString(line)
			continue
		}
		if inFence {
			b.WriteString(line)
			continue
		}
		b.WriteString(escapeLine(line))
	}
	return b.String()
}

func escapeLine(line string) string {
	var b strings.Builder
	rs := []rune(line)
	for i := 0; i < len(rs); {
		if rs[i] == '`' {
			// A code span opens with a run of n backticks and closes with
			// the next run of exactly n backticks.
			n := 0
			for i+n < len(rs) && rs[i+n] == '`' {
				n++
			}
			if end := closingRun(rs, i+n, n); end >= 0 {
				b.WriteString(string(rs[i : end+n]))
				i = end + n
				continue
			}
			b.WriteString(string(rs[i : i+n]))
			i += n
			continue
		}
		if rs[i] == '<' && (i == 0 || rs[i-1] != '\\') {
			b.WriteString(`\<`)
		} else {
			b.WriteRune(rs[i])
		}
		i++
	}
	return b.String()
}

func closingRun(rs []rune, from, n int) int {
	for j := from; j < len(rs); {
		if rs[j] != '`' {
			j++
			continue
		}
		m := 0
		for j+m < len(rs) && rs[j+m] == '`' {
			m++
		}
		if m == n {
			return j
		}
		j += m
	}
	return -1
}
