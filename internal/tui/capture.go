package tui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Unscheduled-Maintenance/Holocron/internal/style"
)

// captureModel is the focused one-line prompt behind `holocron now`. It
// runs inline (no alternate screen) so the terminal history keeps a record.
type captureModel struct {
	input     textinput.Model
	context   string
	done      bool
	cancelled bool
	width     int
}

func (c captureModel) Init() tea.Cmd { return c.input.Focus() }

func (c captureModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.width = msg.Width
		c.input.SetWidth(max(10, msg.Width-4))
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			c.cancelled = true
			return c, tea.Quit
		case "enter":
			if strings.TrimSpace(c.input.Value()) == "" {
				c.cancelled = true
			}
			c.done = true
			return c, tea.Quit
		}
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)
	return c, cmd
}

func (c captureModel) View() tea.View {
	if c.done || c.cancelled {
		return tea.NewView("")
	}
	head := lipgloss.NewStyle().Foreground(style.Accent).Render(style.Mark) + " " +
		lipgloss.NewStyle().Foreground(style.Muted).Render("Holocron · new entry")
	if c.context != "" {
		head += lipgloss.NewStyle().Foreground(style.Faint).Render("  " + c.context)
	}
	help := lipgloss.NewStyle().Foreground(style.Faint).Render("  enter save · esc cancel · +project #tag shorthand")
	return tea.NewView(head + "\n" + c.input.View() + "\n" + help + "\n")
}

// Capture shows the focused capture prompt and returns the text typed.
// ok is false when the person cancelled or entered nothing. contextLine
// describes defaults already chosen with flags (e.g. "project aws").
func Capture(ctx context.Context, contextLine string) (text string, ok bool, err error) {
	in := newInput("› ", "What did you just do?")
	in.SetWidth(76)
	m := captureModel{input: in, context: contextLine}
	var final tea.Model
	err = withUnambiguousKeys(func() (err error) {
		final, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
		return err
	})
	if err != nil {
		return "", false, err
	}
	cm := final.(captureModel)
	if cm.cancelled {
		return "", false, nil
	}
	return strings.TrimSpace(cm.input.Value()), true, nil
}
