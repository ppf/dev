package control

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

func TestAgyReady(t *testing.T) {
	const dir = "/src/app/.claude/worktrees/fix"
	rule := strings.Repeat("─", 40)
	idle := rule + "\n>\n" + rule + "\n? for shortcuts\n"
	trust := func(asked, cursor string) string {
		return "Accessing workspace:\n\n" + asked + "\n\nDo you trust the contents of this project?\n\n" + cursor + "\n  No, exit\n"
	}
	tests := []struct {
		name     string
		trust    bool
		unhooked bool
		screens  []string // capture-pane's replies in turn, the last one repeated
		enters   int
		left     string // the start of AgyReady's note; "" when it recorded the pane idle
	}{
		{"idle at once", false, false, []string{idle}, 0, ""},
		{"without its hooks", false, true, []string{idle}, 0, "agy runs no dev state --agy hook"},
		{"its trust dialog confirmed", true, false, []string{trust(dir, "> Yes, I trust this folder"), trust(dir, "> Yes, I trust this folder"), idle}, 1, ""},
		{"a folder this start didn't make", false, false, []string{trust(dir, "> Yes, I trust this folder")}, 0,
			"agy asks whether to trust " + dir + ", a folder this start didn't make: left for the user; once it's answered, run this start again"},
		{"another folder", true, false, []string{trust("/src/app", "> Yes, I trust this folder")}, 0,
			"agy asks whether to trust /src/app, not its pane's " + dir + ", after 5s: left for the user"},
		{"its folder once redrawn", true, false, []string{trust("/src/app/.clau", "> Yes, I trust this folder"), trust(dir, "> Yes, I trust this folder"), idle}, 1, ""},
		{"cursor on No", true, false, []string{trust(dir, "  Yes, I trust this folder")}, 0,
			"agy asks whether to trust " + dir + ", its cursor not on Yes, after 5s: left for the user"},
		{"a login screen", true, false, []string{"Sign in to continue\n"}, 0, "agy shows no input box after 5s: left as it is; once it's answered"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			captures, enters, recorded := 0, 0, false
			c := tmux.NewFake(func(args []string) (string, error) {
				switch args[0] {
				case "display-message":
					return dir + "\n", nil
				case "capture-pane":
					s := tt.screens[min(captures, len(tt.screens)-1)]
					captures++
					return s, nil
				case "send-keys":
					enters++
				case "set-option":
					recorded = recorded || slices.Contains(args, "@agent_state")
				}
				return "", nil
			})
			left, err := AgyReady(c, fakeClock(), "%1", tt.trust, !tt.unhooked, 5*time.Second)
			if err != nil || !strings.HasPrefix(left, tt.left) || (tt.left == "") != (left == "") {
				t.Errorf("AgyReady = %q, %v; want it to start %q", left, err, tt.left)
			}
			if enters != tt.enters || recorded != (tt.left == "") {
				t.Errorf("Enter pressed %d times, state recorded %v; want %d, %v", enters, recorded, tt.enters, tt.left == "")
			}
		})
	}
}
