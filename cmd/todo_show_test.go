package cmd

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/itemview"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

// dev todo show's popup runs a left press on an option as its key; a release or a right press does nothing.
func TestShowClick(t *testing.T) {
	var answered []string
	acts := itemview.Actions{Answer: func(_ string, _ int, _, a string) (string, error) {
		answered = append(answered, a)
		return "answered", nil
	}}
	it := itemview.Item{Boss: "boss-dev", Shown: todo.Shown{ID: 4, Text: "Merge?", Status: sidebar.StatusDecide, Options: []string{"Merge", "Wait"}}}
	var s tea.Model = showModel{m: itemview.New(it, acts, false)}
	s, _ = s.Update(tea.WindowSizeMsg{Width: itemview.Width, Height: 30})
	y := -1
	for i, l := range strings.Split(ansi.Strip(s.View()), "\n") {
		if strings.Contains(l, "2    Wait") {
			y = i
		}
	}
	if y < 0 {
		t.Fatalf("❌ no Wait line:\n%s", ansi.Strip(s.View()))
	}
	for _, ms := range []tea.MouseMsg{{Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease}, {Y: y, Button: tea.MouseButtonRight, Action: tea.MouseActionPress}} {
		if _, cmd := s.Update(ms); cmd != nil {
			t.Errorf("❌ %v acted", ms)
		}
	}
	s, cmd := s.Update(tea.MouseMsg{Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if cmd == nil {
		t.Fatal("❌ left press on Wait ran nothing")
	}
	s, cmd = s.Update(cmd())
	if len(answered) != 1 || answered[0] != "Wait" || !s.(showModel).m.Done || cmd == nil {
		t.Errorf("❌ answered %v, done %v", answered, s.(showModel).m.Done)
	}
}
