//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The user starts their own item from a shell: its live boss gets "you were asked to start" and the item reads
// starting until a --replaces; a second start is refused. The boss's parked item and a gone worker's start too, a live
// worker's and the manager's own item don't. A closed boss's item goes to the manager, refused while none runs.
func TestTodoStart(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	dev := func(env []string, argv ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(env...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return string(out), &exec.ExitError{Stderr: []byte(stderr.String())}
		}
		return string(out), nil
	}
	refused := func(env []string, want string, argv ...string) {
		t.Helper()
		if _, err := dev(env, argv...); err == nil || !strings.Contains(string(err.(*exec.ExitError).Stderr), want) {
			t.Errorf("❌ dev %s: %v, want %q", strings.Join(argv, " "), err, want)
		}
	}
	open := func(name string) (pane, log string) {
		t.Helper()
		log = filepath.Join(tmp, name+".log")
		s.T("new-session", "-d", "-s", name, "bash", script, "", log)
		s.T("set-option", "-t", "="+name+":", "@dev_herd", "1")
		pane = s.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		cmd := exec.Command(devBin, "state", "idle")
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("❌ register %s: %v %s", name, err, out)
		}
		waitFor(t, name+" stub box drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", pane), "Try") })
		return pane, log
	}
	logged := func(log, want string) {
		t.Helper()
		waitFor(t, "note "+want, func() bool {
			data, _ := os.ReadFile(log)
			return strings.Contains(string(data), want)
		})
	}
	listed := func() string {
		t.Helper()
		out, err := dev([]string{"TMUX_PANE="}, "todo", "--all", "--json")
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(out), "")
	}

	s.T("new-session", "-d", "-s", "other", "exec sleep 600")
	pane, log := open("boss-a")
	boss := []string{"TMUX_PANE=" + pane, "CLAUDECODE=1"}
	user := []string{"TMUX_PANE=", "CLAUDECODE="}
	for _, text := range []string{"Deploy the fix", "Write the notes"} {
		if _, err := dev(boss, "todo", "add", "--no-details", text); err != nil {
			t.Fatal(err)
		}
	}

	refused(boss, "refused in a Claude Code session", "todo", "start", "boss-a", "1", "--question", "Deploy the fix")
	refused(user, "--question <the item's text> is needed", "todo", "start", "boss-a", "1")
	refused(user, `is "Deploy the fix" now, not "Other"`, "todo", "start", "boss-a", "1", "--question", "Other")
	out, err := dev(user, "todo", "start", "boss-a", "1", "--question", "Deploy the fix")
	check(t, "start", out, "#1 sent to boss-a\n")
	if err != nil {
		t.Fatal(err)
	}
	logged(log, `- you were asked to start #1 "Deploy the fix": while it still reads so and is marked started on your list, start a worker on it and link it with dev todo add --replaces 1 --on <worker>`)
	if j := listed(); !strings.Contains(j, `"started":{"at":`) || !strings.Contains(j, `"status":"starting","status_on":"boss-a"`) {
		t.Errorf("❌ --all --json after start:\n%s", j)
	}
	refused(user, "item 1: already sent to boss-a 0m ago", "todo", "start", "boss-a", "1", "--question", "Deploy the fix")
	if _, err := dev(boss, "todo", "add", "--replaces", "1", "--on", "me", "--no-details"); err != nil {
		t.Fatal(err)
	}
	if j := listed(); strings.Contains(j, `"started"`) || strings.Contains(j, `"starting"`) {
		t.Errorf("❌ --replaces kept the start:\n%s", j)
	}

	// The boss's own parked item and a gone worker's start too, each note worded by whose it is; a live worker's doesn't
	worker := func(name string) {
		s.T("new-session", "-d", "-s", name, "exec sleep 600")
		s.T("set-option", "-t", "="+name+":", "@dev_started_by", s.T("display", "-t", "=boss-a:", "-p", "#{session_id}"))
	}
	worker("w-old")
	worker("w-busy")
	for _, argv := range [][]string{{"--on", "boss", "Park the idea"}, {"--on", "w-old", "Fix the login"}, {"--on", "w-busy", "Ship it"}} {
		if _, err := dev(boss, append([]string{"todo", "add"}, argv...)...); err != nil {
			t.Fatal(err)
		}
	}
	s.T("kill-session", "-t", "=w-old")
	for _, tt := range []struct{ id, text string }{{"3", "Park the idea"}, {"4", "Fix the login"}} {
		out, err := dev(user, "todo", "start", "boss-a", tt.id, "--question", tt.text)
		check(t, "start #"+tt.id, out, "#"+tt.id+" sent to boss-a\n")
		if err != nil {
			t.Fatal(err)
		}
	}
	// The stub took one paste: the boss gets these at its Stop
	reason := inboxAtStop(t, s, pane)
	for _, want := range []string{
		`- you were asked to start #3 "Park the idea": while it still reads so and is marked started on your list, it is your own parked item: do it yourself (dev todo doing 3 takes the start), or start a worker on it and link it with dev todo add --replaces 3 --on <worker>`,
		`- you were asked to start #4 "Fix the login": while it still reads so and is marked started on your list, its worker w-old is gone: start a fresh one on it and link it with dev todo add --replaces 4 --on <worker>`,
	} {
		if !strings.Contains(reason, want) {
			t.Errorf("❌ boss Stop reason =\n%s\nwant\n%s", reason, want)
		}
	}
	refused(user, "item 5 is in In progress (w-busy), not To do: only a To do card starts", "todo", "start", "boss-a", "5", "--question", "Ship it")
	// The boss's own item it is on: In progress while the boss runs, To do once it closed (below)
	if _, err := dev(boss, "todo", "add", "--on", "boss", "Tidy the docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := dev(boss, "todo", "doing", "6", "drafting"); err != nil {
		t.Fatal(err)
	}
	out, _ = dev(user, "todo", "--all", "--json")
	for id, want := range map[int]string{3: "idle boss-a progress", 4: "gone w-old progress", 5: "idle w-busy progress"} {
		if got := placedAs(t, out, "boss-a", id); got != want {
			t.Errorf("❌ #%d after start = %q, want %q (old word kept, In progress)", id, got, want)
		}
	}

	// Closed: refused with no manager, then sent to the manager, naming the boss
	s.T("kill-session", "-t", "=boss-a")
	refused(user, "boss-a is closed and no manager takes it (no session manager): dev boss a reopens it", "todo", "start", "boss-a", "2", "--question", "Write the notes")
	mpane, mlog := open("manager")
	out, err = dev(boss[1:], "todo", "start", "boss-a", "2", "--question", "Write the notes", "--from-board")
	check(t, "start a closed boss's item", out, "#2 sent to manager\n")
	if err != nil {
		t.Fatal(err)
	}
	logged(mlog, `- you were asked to start boss-a #2 "Write the notes": boss-a is closed: reopen it (dev boss a --background), its start list asks it to start #2`)
	if j := listed(); !strings.Contains(j, `"status":"starting","status_on":"manager"`) {
		t.Errorf("❌ --all --json after a closed boss's start:\n%s", j)
	}
	// The manager's own list: the user's item starts, the manager's own doesn't
	mgr := []string{"TMUX_PANE=" + mpane, "CLAUDECODE=1"}
	for _, argv := range [][]string{{"--no-details", "Read the plan"}, {"--on", "boss", "Tidy the lists"}} {
		if _, err := dev(mgr, append([]string{"todo", "add"}, argv...)...); err != nil {
			t.Fatal(err)
		}
	}
	refused(user, "item 2 on the manager's own list isn't yours: ask the manager in its session", "todo", "start", "manager", "2", "--question", "Tidy the lists")
	out, err = dev(user, "todo", "start", "manager", "1", "--question", "Read the plan")
	check(t, "start the user's item on the manager's list", out, "#1 sent to manager\n")
	if err != nil {
		t.Fatal(err)
	}
	// A closed boss's item on a running worker is To do: it goes to the manager too
	out, err = dev(user, "todo", "start", "boss-a", "5", "--question", "Ship it")
	check(t, "start a closed boss's item on a worker", out, "#5 sent to manager\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := `- you were asked to start boss-a #5 "Ship it": boss-a is closed: reopen it (dev boss a --background), its start list asks it to start #5`; !strings.Contains(inboxAtStop(t, s, mpane), want) {
		t.Errorf("❌ manager Stop reason lacks\n%s", want)
	}
	// A start ends doing: the closed boss's own item it was on sits in To do, goes to the manager, no one on it any more
	out, _ = dev(user, "todo", "--all", "--json")
	if got := placedAs(t, out, "boss-a", 6); got != "boss closed boss todo" || !strings.Contains(out, "drafting") {
		t.Errorf("❌ #6 before start = %q, want boss closed, To do, still doing:\n%s", got, out)
	}
	out, err = dev(user, "todo", "start", "boss-a", "6", "--question", "Tidy the docs")
	check(t, "start a closed boss's own item marked doing", out, "#6 sent to manager\n")
	if err != nil {
		t.Fatal(err)
	}
	out, _ = dev(user, "todo", "--all", "--json")
	if got := placedAs(t, out, "boss-a", 6); got != "boss closed boss progress" {
		t.Errorf("❌ #6 after start = %q, want boss closed, In progress", got)
	}
	if j := listed(); strings.Contains(j, `"doing"`) {
		t.Errorf("❌ start kept doing:\n%s", j)
	}
}

// inboxAtStop is the inbox notes boss pane gets at its Stop: the block's reason.
func inboxAtStop(t *testing.T, s *server, pane string) string {
	t.Helper()
	stop := exec.Command(devBin, "state", "idle")
	stop.Env = s.scriptEnv("TMUX_PANE=" + pane)
	stop.Stdin = strings.NewReader(`{"hook_event_name":"Stop","stop_hook_active":false}`)
	out, err := stop.Output()
	var block struct{ Reason string }
	if err != nil || json.Unmarshal(out, &block) != nil {
		t.Fatalf("❌ Stop: %v %s", err, out)
	}
	return block.Reason
}

// placedAs is item id of boss's list in dev todo --all --json output out, as "<status> <status_on> <column>".
func placedAs(t *testing.T, out, boss string, id int) string {
	t.Helper()
	var lists []struct {
		Boss  string
		Items []struct {
			ID       int
			Status   string
			StatusOn string `json:"status_on"`
			Column   string
		}
	}
	if err := json.Unmarshal([]byte(out), &lists); err != nil {
		t.Fatalf("❌ --all --json: %v\n%s", err, out)
	}
	for _, l := range lists {
		for _, it := range l.Items {
			if l.Boss == boss && it.ID == id {
				return fmt.Sprintf("%s %s %s", it.Status, it.StatusOn, it.Column)
			}
		}
	}
	return "none"
}
