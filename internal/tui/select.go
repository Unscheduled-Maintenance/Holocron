package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// SelectItem is one row in a checklist.
type SelectItem struct {
	Label    string
	Detail   string
	Selected bool
}

// selectModel is a full-screen checklist, used to choose commits to import.
type selectModel struct {
	title         string
	items         []SelectItem
	cursor        int
	offset        int
	width, height int
	done          bool
	cancelled     bool
}

func (s selectModel) Init() tea.Cmd { return nil }

func (s selectModel) count() int {
	n := 0
	for _, it := range s.items {
		if it.Selected {
			n++
		}
	}
	return n
}

func (s selectModel) visibleRows() int { return max(1, s.height-5) }

func (s selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			s.cancelled = true
			return s, tea.Quit
		case "enter":
			s.done = true
			return s, tea.Quit
		case "up", "k":
			if s.cursor > 0 {
				s.cursor--
			}
		case "down", "j":
			if s.cursor < len(s.items)-1 {
				s.cursor++
			}
		case "pgup":
			s.cursor = max(0, s.cursor-s.visibleRows())
		case "pgdown":
			s.cursor = min(len(s.items)-1, s.cursor+s.visibleRows())
		case "home", "g":
			s.cursor = 0
		case "end", "G":
			s.cursor = len(s.items) - 1
		case "space", "x":
			s.items[s.cursor].Selected = !s.items[s.cursor].Selected
		case "a":
			all := s.count() < len(s.items)
			for i := range s.items {
				s.items[i].Selected = all
			}
		}
	}
	h := s.visibleRows()
	if s.cursor < s.offset {
		s.offset = s.cursor
	}
	if s.cursor >= s.offset+h {
		s.offset = s.cursor - h + 1
	}
	return s, nil
}

func (s selectModel) View() tea.View {
	if s.done || s.cancelled {
		return tea.NewView("")
	}
	w := max(20, s.width)
	var b strings.Builder
	b.WriteString(sMark.Render("◆") + " " + sTitle.Render(truncate(s.title, w-4)) + "\n")
	b.WriteString(sDim.Render(fmt.Sprintf("  %d of %d selected", s.count(), len(s.items))) + "\n\n")
	end := min(len(s.items), s.offset+s.visibleRows())
	for i := s.offset; i < end; i++ {
		it := s.items[i]
		marker := "  "
		if i == s.cursor {
			marker = sCursor.Render("▌ ")
		}
		box := "[ ] "
		if it.Selected {
			box = sSuccess.Render("[x] ")
		}
		label := it.Label
		if i == s.cursor {
			label = sSelected.Render(label)
		}
		line := marker + box + label
		if it.Detail != "" {
			line += "  " + sFaint.Render(it.Detail)
		}
		b.WriteString(truncateANSI(line, w) + "\n")
	}
	b.WriteString("\n" + truncateANSI(strings.Join([]string{hint("space", "toggle"), hint("a", "all/none"), hint("enter", "import selected"), hint("esc", "cancel")}, "  "), w))
	v := tea.NewView(lipgloss.NewStyle().MaxWidth(w).Render(b.String()))
	v.AltScreen = true
	return v
}

// Select shows a checklist and returns the indexes chosen. ok is false if
// the person cancelled.
func Select(ctx context.Context, title string, items []SelectItem) (chosen []int, ok bool, err error) {
	m := selectModel{title: title, items: items, width: 80, height: 24}
	var final tea.Model
	err = withUnambiguousKeys(func() (err error) {
		final, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
		return err
	})
	if err != nil {
		return nil, false, err
	}
	sm := final.(selectModel)
	if sm.cancelled {
		return nil, false, nil
	}
	for i, it := range sm.items {
		if it.Selected {
			chosen = append(chosen, i)
		}
	}
	return chosen, true, nil
}
