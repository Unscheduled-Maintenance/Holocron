package tui

import (
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"

	"github.com/Unscheduled-Maintenance/Holocron/internal/style"
)

// styles used across the TUI. Important state is never conveyed by colour
// alone: the selected row has a ▌ marker, focus is shown with a label, and
// open items say "open".
var (
	sTitle     = lipgloss.NewStyle().Foreground(style.Accent).Bold(true)
	sMark      = lipgloss.NewStyle().Foreground(style.Accent)
	sDim       = lipgloss.NewStyle().Foreground(style.Muted)
	sFaint     = lipgloss.NewStyle().Foreground(style.Faint)
	sRule      = lipgloss.NewStyle().Foreground(style.AccentDim)
	sProject   = lipgloss.NewStyle().Foreground(style.Blue)
	sTag       = lipgloss.NewStyle().Foreground(style.Violet)
	sWarn      = lipgloss.NewStyle().Foreground(style.Amber)
	sError     = lipgloss.NewStyle().Foreground(style.Red).Bold(true)
	sSuccess   = lipgloss.NewStyle().Foreground(style.Green)
	sHighlight = lipgloss.NewStyle().Foreground(style.Amber).Bold(true)
	sHeading   = lipgloss.NewStyle().Bold(true)
	sDay       = lipgloss.NewStyle().Foreground(style.Accent).Bold(true)
	sSelected  = lipgloss.NewStyle().Bold(true)
	sCursor    = lipgloss.NewStyle().Foreground(style.Accent).Bold(true)
	sKey       = lipgloss.NewStyle().Foreground(style.Accent)
	sChip      = lipgloss.NewStyle().Foreground(style.Amber)
	sBox       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(style.AccentDim).Padding(0, 1)
	sFocusBox  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(style.Accent).Padding(0, 1)
)

func typeStyle(t string) lipgloss.Style { return lipgloss.NewStyle().Foreground(style.TypeColor(t)) }

func newInput(prompt, placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.Placeholder = placeholder
	ti.CharLimit = 4000
	st := textinput.DefaultDarkStyles()
	st.Cursor.Blink = false
	st.Focused.Prompt = lipgloss.NewStyle().Foreground(style.Accent).Bold(true)
	st.Blurred.Prompt = lipgloss.NewStyle().Foreground(style.Muted)
	st.Blurred.Text = lipgloss.NewStyle().Foreground(style.Muted)
	ti.SetStyles(st)
	return ti
}

func newTextArea() textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = "│ "
	ta.CharLimit = 20000
	ta.Placeholder = "What happened?"
	st := textarea.DefaultDarkStyles()
	st.Cursor.Blink = false
	st.Focused.CursorLine = lipgloss.NewStyle()
	st.Focused.Prompt = lipgloss.NewStyle().Foreground(style.Accent)
	st.Blurred.Prompt = lipgloss.NewStyle().Foreground(style.Faint)
	ta.SetStyles(st)
	return ta
}
