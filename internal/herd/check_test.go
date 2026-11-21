package herd

import (
	"errors"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/tmux"
)

func TestCheck(t *testing.T) {
	for _, tt := range []struct {
		name, mark, want string
		exists           bool
	}{
		{"missing", "", "", false},
		{"not a boss", "", "isn't a boss session", true},
	} {
		c := tmux.NewFake(func(args []string) (string, error) {
			switch args[0] {
			case "has-session":
				if !tt.exists {
					return "", &tmux.Error{Args: args, Stderr: "can't find session", Err: errors.New("exit status 1")}
				}
				return "", nil
			case "show-option":
				return tt.mark, nil
			}
			return "", nil
		})
		err := Check(c, "boss-app", agent.Start{})
		if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.want)
		}
	}
}
