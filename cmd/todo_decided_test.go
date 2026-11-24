package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/todo"
)

// --recommended is the manager settling a marked ask: refused without --via manager, with an answer, without
// --question, for the manager's own list, and outside the manager's coordinator pane, before any list is read.
func TestTodoAnswerRecommendedRefused(t *testing.T) {
	todoEnv(t)
	t.Setenv("TMUX_PANE", "")
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"boss-dev", "1", "Merge", "--recommended", "--question", "Q?"}, "needs --via manager"},
		{[]string{"boss-dev", "1", "--recommended", "--question", "Q?", "--via", "manager"}, "needs the recommended option you checked"},
		{[]string{"boss-dev", "1", "Merge", "--recommended", "--via", "manager"}, "needs --question"},
		{[]string{"boss-dev", "1", "1", "--recommended", "--question", "Q?", "--via", "manager"}, "by its label, not its number"},
		{[]string{"manager", "1", "Merge", "--recommended", "--question", "Q?", "--via", "manager"}, "never the manager's own"},
		{[]string{"boss-dev", "1", "Merge", "--recommended", "--question", "Q?", "--via", "manager"}, "only in the manager's coordinator pane"},
		{[]string{"boss-dev", "1"}, "no answer: give an option"},
	} {
		t.Setenv("CLAUDECODE", "1")
		args := append([]string{"todo", "answer"}, tt.args...)
		if _, _, err := execute(t, "", args...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("dev %s: %v, want %q", strings.Join(args, " "), err, tt.want)
		}
	}
}

// ask --obvious and --decided: only the list's own boss marks, with a why and exactly one recommended option;
// --decided puts the ask on the list ticked, decided by the boss.
func TestTodoAskObvious(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDECODE", "1")
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"--obvious", "flake", "--option", "A", "--option", "B"}, "it has 0"},
		{[]string{"--obvious", "flake", "--option", "A (Recommended)", "--option", "B (Recommended)"}, "it has 2"},
		{[]string{"--obvious", " ", "--option", "A (Recommended)"}, "--obvious needs why the ask is obvious"},
		{[]string{"--obvious", "-", "--option", "A (Recommended)"}, "only with --replaces"},
		{[]string{"--decided", "", "--option", "A (Recommended)"}, "--decided needs why the decision is easy"},
		{[]string{"--obvious", "flake", "--decided", "flake", "--option", "A (Recommended)"}, "give one"},
		{[]string{"--decided", "flake", "--option", "A (Recommended)", "--for", "1"}, "no --replaces or --for"},
		{[]string{"--obvious", "flake", "--option", "A (Recommended)", "--boss", "boss-x"}, "leave out --boss"},
	} {
		args := append([]string{"todo", "ask", "Re-run CI?"}, tt.args...)
		if _, _, err := execute(t, "", args...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("dev %s: %v, want %q", strings.Join(args, " "), err, tt.want)
		}
	}
	if got := run(t, "", "todo", "ask", "Re-run CI?", "--option", "Re-run (Recommended)", "--option", "Look first", "--obvious", "known flake", "--details", "x"); got != "asked 1 · to do 1\n" {
		t.Errorf("ask --obvious = %q", got)
	}
	run(t, "", "todo", "ask", "Bump the dep?", "--option", "Bump (Recommended)", "--option", "Hold", "--details", "x")
	if got := run(t, "", "todo", "ask", "--replaces", "2", "--obvious", "patch release"); got != "replaced 2 · to do 2\n" {
		t.Errorf("ask --replaces --obvious = %q", got)
	}
	if got := run(t, "", "todo"); !strings.Contains(got, "\tobvious: patch release") {
		t.Errorf("dev todo = %q, want the mark on an existing ask", got)
	}
	run(t, "", "todo", "done", "2")
	if got := run(t, "", "todo"); !strings.Contains(got, "\tobvious: known flake") {
		t.Errorf("dev todo = %q, want the mark", got)
	}
	if got := run(t, "", "todo", "ask", "Parser first?", "--option", "Parser first (Recommended)", "--option", "Export first", "--decided", "the export needs it"); got != "decided 3 · to do 1\n" {
		t.Errorf("ask --decided = %q", got)
	}
	if got := run(t, "", "todo"); !strings.Contains(got, "3\t☑\tParser first?\tdecided for you: boss, Parser first over Export first at ") {
		t.Errorf("dev todo = %q, want the decided line", got)
	}
	if got := run(t, "", "todo", "--json"); !strings.Contains(got, `"obvious": "known flake"`) || !strings.Contains(got, `"by": "boss"`) {
		t.Errorf("dev todo --json = %s", got)
	}
}

// The decided line names who, the pick over the others by their labels, and when.
func TestDecidedLine(t *testing.T) {
	at := time.Date(2026, 11, 24, 14, 32, 0, 0, time.Local)
	d := todo.Decided{By: todo.DecidedManager, Pick: "Re-run | known flake (Recommended)", Over: []string{"Look first | reads the log", "Skip"}, At: at.Unix()}
	if got, want := decidedLine(d), "decided for you: manager, Re-run over Look first · Skip at 14:32"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}
