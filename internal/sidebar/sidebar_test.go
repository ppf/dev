package sidebar

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

// recorder is a fake tmux that logs each command and answers from reply (nil: empty output).
func recorder(reply func(args []string) (string, error)) (*tmux.Client, *[][]string) {
	var calls [][]string
	return tmux.NewFake(func(args []string) (string, error) {
		calls = append(calls, args)
		if reply == nil {
			return "", nil
		}
		return reply(args)
	}), &calls
}

func TestModelSetSelected(t *testing.T) {
	c, calls := recorder(nil)
	if err := setSelected(c, ""); err != nil || len(*calls) != 0 {
		t.Fatalf("empty name: err %v, calls %q", err, *calls)
	}
	if err := setSelected(c, "app"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"set-option", "-gq", "@sidebar_selected_session", "app"}; !slices.Equal((*calls)[0], want) {
		t.Errorf("args = %q, want %q", (*calls)[0], want)
	}
}

func TestModelShowMessage(t *testing.T) {
	c, calls := recorder(func([]string) (string, error) { return "", os.ErrClosed })
	showMessage(c, "", "hi")
	showMessage(c, "/dev/ttys001", "hi")
	want := [][]string{{"display-message", "hi"}, {"display-message", "-c", "/dev/ttys001", "hi"}}
	if !slices.EqualFunc(*calls, want, slices.Equal) {
		t.Errorf("calls = %q, want %q", *calls, want)
	}
}

func TestModelFirstContentPane(t *testing.T) {
	c, calls := recorder(func([]string) (string, error) {
		return tmux.FormatRow("%1", "%1") + tmux.FormatRow("%2", "%1") + tmux.FormatRow("%3", "%1"), nil
	})
	pane, err := firstContentPane(c, "app")
	if err != nil || pane != "%2" {
		t.Errorf("firstContentPane = %q, %v; want %%2", pane, err)
	}
	if want := []string{"list-panes", "-t", "=app:", "-F", "#{pane_id}\x1f#{@sidebar_pane}\x1f"}; !slices.Equal((*calls)[0], want) {
		t.Errorf("args = %q, want %q", (*calls)[0], want)
	}

	c, _ = recorder(func([]string) (string, error) { return tmux.FormatRow("%1", "%1"), nil })
	if pane, err := firstContentPane(c, "app"); err != nil || pane != "" {
		t.Errorf("only the sidebar: %q, %v; want none", pane, err)
	}
}

func TestModelActiveClient(t *testing.T) {
	clients := []Client{
		{TTY: "/dev/a", Session: "app", Activity: 10},
		{TTY: "/dev/b", Session: "web", Activity: 30},
		{TTY: "/dev/c", Session: "app", Activity: 20},
		{TTY: "/dev/d", Session: "app", Activity: 20},
	}
	for _, tt := range []struct {
		session, want string
		ok            bool
	}{
		{"app", "/dev/c", true}, // most recent; a tie keeps list order
		{"web", "/dev/b", true},
		{"", "/dev/b", true},
		{"none", "", false},
	} {
		if got, ok := activeClient(clients, tt.session); got.TTY != tt.want || ok != tt.ok {
			t.Errorf("activeClient(%q) = %q, %v; want %q, %v", tt.session, got.TTY, ok, tt.want, tt.ok)
		}
	}
}

func TestModelCloseCommand(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := CloseArgs{Current: "app", Target: "app-x", MainRoot: "/r", Worktree: "/r/.claude/worktrees/x"}.command()
	if err != nil {
		t.Fatal(err)
	}
	want := tmux.ShellJoin(exe, "sidebar", "close-worktree", "--client", "", "--", "app", "app-x", "/r", "/r/.claude/worktrees/x")
	if got != want || !strings.HasPrefix(got, "'"+exe+"' ") {
		t.Errorf("command = %s, want %s", got, want)
	}
}
