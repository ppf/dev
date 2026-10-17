package sidebar

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

// ensTmux is a fake tmux answering each command, keyed by its space-joined args, from out; commands
// in fail return an error. It returns the client and the joined commands it received.
func ensTmux(out map[string]string, fail ...string) (*tmux.Client, func() []string) {
	c, calls := recorder(func(args []string) (string, error) {
		key := strings.Join(args, " ")
		if slices.Contains(fail, key) {
			return "", errors.New("tmux failed")
		}
		return out[key], nil
	})
	return c, func() []string {
		var log []string
		for _, args := range *calls {
			log = append(log, strings.Join(args, " "))
		}
		return log
	}
}

const (
	ensHidden = "show-option -gqv @sidebar_hidden"
	ensPanes  = "list-panes -a -F #{pane_id}"
)

func ensStored(session string) string {
	return "show-option -qv -t =" + session + ": @sidebar_pane"
}

func ensInfo(pane string) string {
	return "display-message -t " + pane + " -p #{pane_start_command}\x1f#{window_id}\x1f#{window_zoomed_flag}\x1f"
}

func ensHerd(session string) string {
	return "show-option -qv -t =" + session + ": @dev_herd"
}

func ensContent(session string) string {
	return "list-panes -t =" + session + ": -F #{pane_id}\x1f#{@sidebar_pane}\x1f"
}

func ensSplit(t *testing.T, anchor string) string {
	t.Helper()
	cmd, err := PaneCommand()
	if err != nil {
		t.Fatal(err)
	}
	return "split-window -t " + anchor + " -hb -l 40 -PF #{pane_id} " + cmd
}

func ensCheck(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("calls:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestEnsurePaneCommand(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := PaneCommand()
	if want := "exec " + tmux.ShellJoin(exe, "sidebar"); err != nil || got != want {
		t.Errorf("PaneCommand = %q, %v; want %q", got, err, want)
	}
}

func TestEnsureResolveOrigin(t *testing.T) {
	c, log := ensTmux(map[string]string{"display-message -p #{session_name}\x1f#{pane_id}\x1f": tmux.FormatRow("app", "%4")})
	if got, err := ResolveOrigin(c, ""); err != nil || got != (Origin{"app", "%4"}) {
		t.Errorf("ResolveOrigin(\"\") = %+v, %v", got, err)
	}
	ensCheck(t, log(), []string{"display-message -p #{session_name}\x1f#{pane_id}\x1f"})

	clients := "list-clients -F #{client_tty}\x1f#{client_session}\x1f#{client_activity}\x1f"
	c, log = ensTmux(map[string]string{
		clients:                                  tmux.FormatRow("/dev/a", "app", "5") + tmux.FormatRow("/dev/b", "web", "9"),
		"display-message -t =app: -p #{pane_id}": "%2\n",
	})
	if got, err := ResolveOrigin(c, "/dev/a"); err != nil || got != (Origin{"app", "%2"}) {
		t.Errorf("ResolveOrigin(/dev/a) = %+v, %v", got, err)
	}
	ensCheck(t, log(), []string{clients, "display-message -t =app: -p #{pane_id}"})

	if _, err := ResolveOrigin(c, "/dev/gone"); err == nil {
		t.Error("unknown client: want error")
	}
}

func TestEnsure(t *testing.T) {
	fromOther := Origin{"web", "%7"}
	bashStart := "exec /Users/x/.tmux/sessions/sidebar.sh"
	tests := []struct {
		name    string
		target  string
		from    Origin
		out     map[string]string
		fail    []string
		want    func(t *testing.T) []string
		wantErr bool
	}{
		{
			name: "no target", target: "", from: fromOther, wantErr: true,
			want: func(*testing.T) []string { return nil },
		},
		{
			name: "hidden is a no-op", target: "app", from: fromOther,
			out:  map[string]string{ensHidden: "1\n"},
			want: func(*testing.T) []string { return []string{ensHidden} },
		},
		{
			name: "stale stored pane cleared, created, focus back to another session", target: "app", from: fromOther,
			out: map[string]string{
				ensStored("app"):  "%9\n",
				ensPanes:          "%1\n%2\n%7\n",
				ensContent("app"): tmux.FormatRow("%1", "") + tmux.FormatRow("%2", ""),
				ensSplit(t, "%1"): "%5\n",
			},
			want: func(t *testing.T) []string {
				return []string{ensHidden, ensStored("app"), ensPanes, "set-option -t =app: -u @sidebar_pane",
					ensHerd("app"), ensContent("app"), ensSplit(t, "%1"), "set-option -t =app: @sidebar_pane %5",
					"display-message -t %7 -p ", "select-pane -t %7"}
			},
		},
		{
			name: "created from the same session focuses it", target: "app", from: Origin{"app", "%1"},
			out: map[string]string{
				ensContent("app"): tmux.FormatRow("%1", ""),
				ensSplit(t, "%1"): "%5\n",
			},
			want: func(t *testing.T) []string {
				return []string{ensHidden, ensStored("app"), ensHerd("app"), ensContent("app"), ensSplit(t, "%1"),
					"set-option -t =app: @sidebar_pane %5", "select-pane -t %5", "set-option -gq @sidebar_selected_session app"}
			},
		},
		{
			name: "origin pane gone keeps focus", target: "app", from: fromOther,
			out: map[string]string{
				ensContent("app"): tmux.FormatRow("%1", ""),
				ensSplit(t, "%1"): "%5\n",
			},
			fail: []string{"display-message -t %7 -p "},
			want: func(t *testing.T) []string {
				return []string{ensHidden, ensStored("app"), ensHerd("app"), ensContent("app"), ensSplit(t, "%1"),
					"set-option -t =app: @sidebar_pane %5", "display-message -t %7 -p "}
			},
		},
		{
			name: "no origin pane", target: "app", from: Origin{},
			out: map[string]string{
				ensContent("app"): tmux.FormatRow("%1", ""),
				ensSplit(t, "%1"): "%5\n",
			},
			want: func(t *testing.T) []string {
				return []string{ensHidden, ensStored("app"), ensHerd("app"), ensContent("app"), ensSplit(t, "%1"),
					"set-option -t =app: @sidebar_pane %5"}
			},
		},
		{
			name: "no content pane", target: "app", from: fromOther, wantErr: true,
			out: map[string]string{ensStored("app"): "%3\n", ensPanes: "%3\n", ensInfo("%3"): tmux.FormatRow(bashStart, "@1", "0")},
			want: func(*testing.T) []string {
				return []string{ensHidden, ensStored("app"), ensPanes, ensInfo("%3"), "kill-pane -t %3",
					"set-option -t =app: -u @sidebar_pane", ensHerd("app"), ensContent("app")}
			},
		},
		{
			name: "bash sidebar replaced, keeping the zoom", target: "app", from: fromOther,
			out: map[string]string{
				ensStored("app"):  "%3\n",
				ensPanes:          "%1\n%3\n",
				ensInfo("%3"):     tmux.FormatRow(bashStart, "@1", "1"),
				ensContent("app"): tmux.FormatRow("%1", ""),
				ensSplit(t, "%1"): "%5\n",
			},
			want: func(t *testing.T) []string {
				return []string{ensHidden, ensStored("app"), ensPanes, ensInfo("%3"), "kill-pane -t %3",
					"resize-pane -Z -t @1", "set-option -t =app: -u @sidebar_pane", ensHerd("app"), ensContent("app"), ensSplit(t, "%1"),
					"set-option -t =app: @sidebar_pane %5", "display-message -t %7 -p ", "select-pane -t %7"}
			},
		},
		{
			name: "from the sidebar itself hands focus back", target: "app", from: Origin{"app", "%3"},
			out: map[string]string{
				ensStored("app"):  "%3\n",
				ensPanes:          "%1\n%3\n",
				ensInfo("%3"):     tmux.FormatRow("exec dev sidebar", "@1", "0"),
				ensContent("app"): tmux.FormatRow("%3", "%3") + tmux.FormatRow("%1", "%3"),
			},
			want: func(*testing.T) []string {
				return []string{ensHidden, ensStored("app"), ensPanes, ensInfo("%3"), ensContent("app"),
					"select-pane -t %1", "switch-client -T root"}
			},
		},
		{
			name: "existing sidebar focused and selected", target: "app", from: Origin{"app", "%1"},
			out: map[string]string{
				ensStored("app"): "%3\n",
				ensPanes:         "%1\n%3\n",
				ensInfo("%3"):    tmux.FormatRow("exec dev sidebar", "@1", "0"),
			},
			want: func(*testing.T) []string {
				return []string{ensHidden, ensStored("app"), ensPanes, ensInfo("%3"),
					"select-pane -t %3", "set-option -gq @sidebar_selected_session app"}
			},
		},
		{
			name: "zoomed sidebar not focused", target: "app", from: fromOther,
			out: map[string]string{
				ensStored("app"): "%3\n",
				ensPanes:         "%1\n%3\n",
				ensInfo("%3"):    tmux.FormatRow("exec dev sidebar", "@1", "1"),
			},
			want: func(*testing.T) []string {
				return []string{ensHidden, ensStored("app"), ensPanes, ensInfo("%3")}
			},
		},
		{
			name: "failing split is returned", target: "app", from: fromOther, wantErr: true,
			out:  map[string]string{ensContent("app"): tmux.FormatRow("%1", "")},
			fail: []string{ensSplit(t, "%1")},
			want: func(t *testing.T) []string {
				return []string{ensHidden, ensStored("app"), ensHerd("app"), ensContent("app"), ensSplit(t, "%1")}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, log := ensTmux(tt.out, tt.fail...)
			err := Ensure(c, tt.target, tt.from)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
			ensCheck(t, log(), tt.want(t))
		})
	}
}

func TestToggleHide(t *testing.T) {
	sessions := "list-sessions -F #{session_name}\x1f#{@sidebar_pane}\x1f"
	c, log := ensTmux(map[string]string{
		ensStored("app"): "%3\n",
		ensPanes:         "%1\n%3\n%8\n",
		sessions:         tmux.FormatRow("app", "%3") + tmux.FormatRow("plain", "") + tmux.FormatRow("web", "%8") + tmux.FormatRow("old", "%9"),
		ensInfo("%3"):    tmux.FormatRow("exec dev sidebar", "@1", "0"),
		ensInfo("%8"):    tmux.FormatRow("exec dev sidebar", "@4", "1"),
	}, ensInfo("%9"), "kill-pane -t %9", "switch-client -T root")
	if err := Toggle(c, Origin{"app", "%1"}); err != nil {
		t.Fatal(err)
	}
	ensCheck(t, log(), []string{ensStored("app"), ensPanes, sessions,
		ensInfo("%3"), "kill-pane -t %3", "set-option -t =app: -u @sidebar_pane",
		ensInfo("%8"), "kill-pane -t %8", "resize-pane -Z -t @4", "set-option -t =web: -u @sidebar_pane",
		ensInfo("%9"), "kill-pane -t %9", "set-option -t =old: -u @sidebar_pane",
		"set-option -g @sidebar_hidden 1", "switch-client -T root"})
}

func TestToggleShow(t *testing.T) {
	c, log := ensTmux(map[string]string{
		ensStored("app"):  "%3\n", // stale: hidden sidebars were killed
		ensPanes:          "%1\n",
		ensContent("app"): tmux.FormatRow("%1", ""),
		ensSplit(t, "%1"): "%5\n",
	}, "set-option -gu @sidebar_hidden")
	if err := Toggle(c, Origin{"app", "%1"}); err != nil {
		t.Fatal(err)
	}
	ensCheck(t, log(), []string{ensStored("app"), ensPanes, "set-option -t =app: -u @sidebar_pane",
		"set-option -gu @sidebar_hidden", ensHerd("app"), ensContent("app"), ensSplit(t, "%1"),
		"set-option -t =app: @sidebar_pane %5", "select-pane -t %5", "set-option -gq @sidebar_selected_session app"})

	c, log = ensTmux(nil)
	if err := Toggle(c, Origin{}); err == nil || len(log()) != 0 {
		t.Errorf("no session: err %v, calls %q", err, log())
	}
}

func TestEnsureSwitchTo(t *testing.T) {
	// Ensure failing (no sidebar possible) never blocks the switch
	c, log := ensTmux(nil, ensHidden, "switch-client -c /dev/a -T root")
	if err := switchTo(c, "/dev/a", "web", Origin{"app", "%1"}); err != nil {
		t.Fatal(err)
	}
	ensCheck(t, log(), []string{ensHidden, "switch-client -c /dev/a -t =web:", "switch-client -c /dev/a -T root"})

	c, log = ensTmux(map[string]string{ensHidden: "1\n"}, "switch-client -t =web:")
	if err := switchTo(c, "", "web", Origin{"app", "%1"}); err == nil {
		t.Error("failing switch-client: want error")
	}
	ensCheck(t, log(), []string{ensHidden, "switch-client -t =web:"})
}

// A herd session's previews pane stands in for its sidebar: none is created.
func TestEnsureHerd(t *testing.T) {
	c, calls := ensTmux(map[string]string{ensHerd("herd"): "1\n"})
	if err := Ensure(c, "herd", Origin{}); err != nil {
		t.Fatal(err)
	}
	ensCheck(t, calls(), []string{ensHidden, ensStored("herd"), ensHerd("herd")})
}
