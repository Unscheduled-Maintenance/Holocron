// Package style holds Holocron's terminal palette and the plain-text
// fallbacks used when colour is disabled.
//
// The visual identity is deliberately restrained: a cool cyan accent for
// structure, warm amber for highlights, and plenty of dim grey. Colours are
// ANSI-256 indices so they render predictably on older terminals.
package style

import (
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
)

// Palette colours.
var (
	Accent    = lipgloss.Color("44")  // archive cyan
	AccentDim = lipgloss.Color("30")  // deep teal
	Amber     = lipgloss.Color("214") // highlights and search matches
	Muted     = lipgloss.Color("245")
	Faint     = lipgloss.Color("240")
	Red       = lipgloss.Color("167")
	Green     = lipgloss.Color("108")
	Blue      = lipgloss.Color("110")
	Violet    = lipgloss.Color("140")
)

// Mark is the small geometric emblem used in headers.
const Mark = "◆"

// Styler renders text with or without terminal styling.
type Styler struct {
	enabled bool
}

// New returns a Styler; when enabled is false every method returns its input.
func New(enabled bool) Styler { return Styler{enabled: enabled} }

// Enabled reports whether styling is active.
func (s Styler) Enabled() bool { return s.enabled }

func (s Styler) render(st lipgloss.Style, text string) string {
	if !s.enabled || text == "" {
		return text
	}
	return st.Render(text)
}

// Title renders a heading.
func (s Styler) Title(t string) string {
	return s.render(lipgloss.NewStyle().Bold(true).Foreground(Accent), t)
}

// Heading renders a section heading.
func (s Styler) Heading(t string) string { return s.render(lipgloss.NewStyle().Bold(true), t) }

// Dim renders secondary text.
func (s Styler) Dim(t string) string { return s.render(lipgloss.NewStyle().Foreground(Muted), t) }

// Faint renders tertiary text such as IDs.
func (s Styler) Faint(t string) string { return s.render(lipgloss.NewStyle().Foreground(Faint), t) }

// Accent renders structural accents.
func (s Styler) Accent(t string) string { return s.render(lipgloss.NewStyle().Foreground(Accent), t) }

// Project renders a project name.
func (s Styler) Project(t string) string { return s.render(lipgloss.NewStyle().Foreground(Blue), t) }

// Tag renders a tag.
func (s Styler) Tag(t string) string { return s.render(lipgloss.NewStyle().Foreground(Violet), t) }

// Type renders an entry type label.
func (s Styler) Type(t string) string {
	return s.render(lipgloss.NewStyle().Foreground(TypeColor(t)), t)
}

// Highlight renders a search match.
func (s Styler) Highlight(t string) string {
	return s.render(lipgloss.NewStyle().Foreground(Amber).Bold(true), t)
}

// Success renders a confirmation.
func (s Styler) Success(t string) string { return s.render(lipgloss.NewStyle().Foreground(Green), t) }

// Warn renders a warning.
func (s Styler) Warn(t string) string { return s.render(lipgloss.NewStyle().Foreground(Amber), t) }

// Error renders an error label.
func (s Styler) Error(t string) string {
	return s.render(lipgloss.NewStyle().Foreground(Red).Bold(true), t)
}

// TypeColor returns the colour associated with an entry type.
func TypeColor(t string) color.Color {
	switch t {
	case "accomplishment":
		return Green
	case "decision":
		return Violet
	case "problem":
		return Red
	case "investigation":
		return Blue
	case "follow-up":
		return Amber
	default:
		return Muted
	}
}

// ColorEnabled decides whether to emit colour for a stream.
// mode is "auto", "always" or "never".
func ColorEnabled(mode string, isTTY bool) bool {
	switch strings.ToLower(mode) {
	case "always":
		return true
	case "never":
		return false
	}
	// Per no-color.org, only a non-empty NO_COLOR disables colour.
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTTY
}
