package tmux

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

type call struct{ args, env []string }

// fake records invocations and answers with fixed output, so no tmux process ever starts.
func fake(stdout, stderr string, err error) (runner, *[]call) {
	var calls []call
	return func(args, env []string) ([]byte, []byte, error) {
		calls = append(calls, call{args, env})
		return []byte(stdout), []byte(stderr), err
	}, &calls
}

func TestDefaultServerRefusedUnderTest(t *testing.T) {
	c := New()
	run, calls := fake("", "", nil)
	c.run = run
	if _, err := c.Run("list-sessions"); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("err = %v, want ErrNoSocket", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("tmux was invoked: %v", *calls)
	}
}

func TestIsolatedSocketAndConfigOverride(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1/default,1,0")
	t.Setenv("TMUX_PANE", "%1")
	c := NewIsolated("sock", "/tmp/test.conf", "XDG_STATE_HOME=/tmp/state")
	run, calls := fake("out\n", "", nil)
	c.run = run

	out, err := c.Run("display", "-p", "x")
	if err != nil || out != "out\n" {
		t.Fatalf("Run = %q, %v", out, err)
	}
	got := (*calls)[0]
	if want := []string{"-L", "sock", "-f", "/tmp/test.conf", "display", "-p", "x"}; !slices.Equal(got.args, want) {
		t.Errorf("args = %q, want %q", got.args, want)
	}
	for _, kv := range got.env {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			t.Errorf("child env leaks %s", kv)
		}
	}
	if !slices.Contains(got.env, "XDG_STATE_HOME=/tmp/state") {
		t.Errorf("child env missing extra entry: %q", got.env)
	}
}

func TestErrorWrapping(t *testing.T) {
	exitErr := &exec.ExitError{}
	c := NewIsolated("sock", "")
	c.run, _ = fake("", "can't find session: =nope:\n", exitErr)

	_, err := c.Run("has-session", "-t", SessionTarget("nope"))
	var tmuxErr *Error
	if !errors.As(err, &tmuxErr) {
		t.Fatalf("err = %T %v, want *Error", err, err)
	}
	if !errors.Is(err, exitErr) {
		t.Errorf("err does not unwrap to the exec error")
	}
	if tmuxErr.Stderr != "can't find session: =nope:" {
		t.Errorf("Stderr = %q", tmuxErr.Stderr)
	}
	if want := "tmux -L sock has-session -t =nope:: "; !strings.HasPrefix(err.Error(), want) ||
		!strings.HasSuffix(err.Error(), ": can't find session: =nope:") {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestSessionTarget(t *testing.T) {
	if got := SessionTarget("dev-a"); got != "=dev-a:" {
		t.Errorf("SessionTarget = %q", got)
	}
}

type pane struct {
	ID      string `tmux:"pane_id"`
	Title   string `tmux:"pane_title"`
	Width   int    `tmux:"pane_width"`
	Active  bool   `tmux:"pane_active"`
	derived string
}

func TestQueryBuildsFormatAndParses(t *testing.T) {
	c := NewIsolated("sock", "")
	run, calls := fake("%1\x1fclaude\x1f80\x1f1\x1f\n%2\x1f\x1f40\x1f0\x1f\n", "", nil)
	c.run = run

	panes, err := Query[pane](c, "list-panes", "-a")
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"-L", "sock", "list-panes", "-a", "-F", "#{pane_id}\x1f#{pane_title}\x1f#{pane_width}\x1f#{pane_active}\x1f"}
	if !slices.Equal((*calls)[0].args, wantArgs) {
		t.Errorf("args = %q, want %q", (*calls)[0].args, wantArgs)
	}
	want := []pane{{ID: "%1", Title: "claude", Width: 80, Active: true}, {ID: "%2", Width: 40}}
	if !slices.Equal(panes, want) {
		t.Errorf("panes = %+v, want %+v", panes, want)
	}
}

func TestParse(t *testing.T) {
	type row struct {
		A string `tmux:"a"`
		B string `tmux:"b"`
		C string `tmux:"c"`
	}
	tests := []struct {
		name, out string
		want      []row
		wantErr   bool
	}{
		{name: "empty output", out: "", want: nil},
		{name: "empty middle field", out: "x\x1f\x1fz\x1f\n", want: []row{{A: "x", C: "z"}}},
		{name: "all fields empty", out: "\x1f\x1f\x1f\n", want: []row{{}}},
		{name: "spaces kept", out: " x \x1fy\x1f z \x1f\n", want: []row{{A: " x ", B: "y", C: " z "}}},
		{name: "two rows", out: "1\x1f2\x1f3\x1f\n4\x1f5\x1f6\x1f\n", want: []row{{"1", "2", "3"}, {"4", "5", "6"}}},
		{name: "newline in value", out: "a\nb\x1fx\n\x1fz\n\x1f\n", want: []row{{A: "a\nb", B: "x\n", C: "z\n"}}},
		{name: "too few fields", out: "x\x1fy\x1f\n", wantErr: true},
		{name: "too many fields", out: "x\x1fy\x1fz\x1fw\x1f\n", wantErr: true},
		{name: "Sep in value", out: "x\x1fy\x1fz\x1fw\x1f\n", wantErr: true},
		{name: "unterminated", out: "x\x1fy\x1fz\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse[row](tt.out)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseSingleEmptyField(t *testing.T) {
	type row struct {
		Project string `tmux:"@project"`
	}
	got, err := Parse[row]("\x1f\n")
	if err != nil || len(got) != 1 || got[0] != (row{}) {
		t.Errorf("Parse = %+v, %v; want one empty row", got, err)
	}
}

func TestParseBadInt(t *testing.T) {
	type row struct {
		N int `tmux:"pane_width"`
	}
	if _, err := Parse[row]("wide\x1f\n"); err == nil || !strings.Contains(err.Error(), "pane_width") {
		t.Errorf("err = %v, want pane_width parse error", err)
	}
}

func TestNewFake(t *testing.T) {
	var got []string
	c := NewFake(func(args []string) (string, error) {
		got = args
		return FormatRow("%1", "claude") + FormatRow("%2", ""), nil
	})
	panes, err := Query[struct {
		ID    string `tmux:"pane_id"`
		Title string `tmux:"pane_title"`
	}](c, "list-panes", "-a")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"list-panes", "-a", "-F", "#{pane_id}\x1f#{pane_title}\x1f"}; !slices.Equal(got, want) {
		t.Errorf("args = %q, want %q (no -L/-f)", got, want)
	}
	if len(panes) != 2 || panes[0].Title != "claude" || panes[1].ID != "%2" {
		t.Errorf("panes = %+v", panes)
	}

	exitErr := &exec.ExitError{}
	c = NewFake(func([]string) (string, error) { return "", exitErr })
	if _, err := c.Run("has-session"); !errors.Is(err, exitErr) {
		t.Errorf("err = %v, want the fake's error wrapped", err)
	}
	c = NewFake(func([]string) (string, error) { return "", &Error{Stderr: "can't find pane: %9", Err: exitErr} })
	_, err = c.Run("kill-pane", "-t", "%9")
	if e, ok := errors.AsType[*Error](err); !ok || e.Stderr != "can't find pane: %9" || !errors.Is(err, exitErr) ||
		!slices.Equal(e.Args, []string{"kill-pane", "-t", "%9"}) {
		t.Errorf("err = %#v, want the fake's stderr and exit error under the real argv", err)
	}
	if err := c.Interactive("attach-session"); !errors.Is(err, ErrNoSocket) {
		t.Errorf("Interactive err = %v, want ErrNoSocket", err)
	}
}
