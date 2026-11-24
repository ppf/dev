//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The popup's columns hold the cards dev todo --all --json gives them, by id and not only by count, on stub bosses with
// every kind of item (an ask, the user's own, the boss's parked one, workers working, blocked and gone, a person's, a
// ticked one, a closed boss's), and its top line reads the JSON's: what needs the user, and what is theirs.
func TestTodoBoardColumns(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	agentBin := sleeper(t, "fakeclaude")
	register := func(name string, extra ...string) string {
		t.Helper()
		s.T(append([]string{"new-session", "-d", "-s", name, "-x", "200", "-y", "90"}, append(extra, "bash", script, "", filepath.Join(tmp, name+".log"))...)...)
		s.T("set-option", "-t", "="+name+":", "@dev_herd", "1")
		pane := s.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		reg := exec.Command(devBin, "state", "idle")
		reg.Env = s.scriptEnv("TMUX_PANE=" + pane)
		reg.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
		if out, err := reg.CombinedOutput(); err != nil {
			t.Fatalf("❌ register %s: %v %s", name, err, out)
		}
		return pane
	}
	dev := func(env []string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(env...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("❌ dev %v: %v", argv, err)
		}
		return string(out)
	}
	paneA, paneB := register("boss-a"), register("boss-b")
	bossID := s.T("display", "-t", "=boss-a:", "-p", "#{session_id}")
	worker := func(name, state string, deciding bool) {
		s.T("new-session", "-d", "-s", name, agentBin)
		s.T("set-option", "-t", "="+name+":", "@dev_started_by", bossID)
		pane := s.T("display", "-t", "="+name+":", "-p", "#{pane_id}")
		waitFor(t, name+" runs its agent", func() bool {
			return s.T("display", "-t", pane, "-p", "#{pane_current_command}") == "fakeclaude"
		})
		s.T("set-option", "-p", "-t", pane, "@agent_cmd", "fakeclaude")
		s.T("set-option", "-p", "-t", pane, "@agent_state", state)
		if deciding {
			s.T("set-option", "-p", "-t", pane, "@agent_waiting_for", "permission_prompt")
		}
	}
	worker("w-busy", "working", false)
	worker("w-ask", "waiting", true)
	worker("w-old", "idle", false)
	a := []string{"TMUX_PANE=" + paneA, "CLAUDECODE=1"}
	dev(a, "todo", "ask", "Merge #148?", "--option", "Merge | ships today (Recommended)", "--option", "Wait")
	dev(a, "todo", "add", "--no-details", "Restart the server")
	dev(a, "todo", "add", "--no-details", "Shipped already")
	dev(a, "todo", "add", "Parked idea", "--on", "boss")
	dev(a, "todo", "add", "Review of the coupon change", "--on", "Ola")
	dev(a, "todo", "add", "Works", "--on", "w-busy")
	dev(a, "todo", "add", "Asks", "--on", "w-ask")
	dev(a, "todo", "add", "Gone", "--on", "w-old")
	s.T("kill-session", "-t", "=w-old")
	user := []string{"TMUX_PANE=", "CLAUDECODE="}
	dev(user, "todo", "done", "--boss", "boss-a", "--text", "Shipped already", "3")
	b := []string{"TMUX_PANE=" + paneB, "CLAUDECODE=1"}
	dev(b, "todo", "add", "--no-details", "Left on a closed boss")
	dev(b, "todo", "add", "Closed boss, a person's", "--on", "Ola")
	s.T("kill-session", "-t", "=boss-b")

	var lists []struct {
		Boss  string `json:"boss"`
		Items []struct {
			ID       int    `json:"id"`
			Done     bool   `json:"done"`
			Quiet    bool   `json:"quiet"`
			Column   string `json:"column"`
			NeedsYou bool   `json:"needs_you"`
			Assignee struct {
				Kind string `json:"kind"`
			} `json:"assignee"`
		} `json:"items"`
		Finished []struct {
			ID int `json:"id"`
		} `json:"finished"`
	}
	if err := json.Unmarshal([]byte(dev(user, "todo", "--all", "--json")), &lists); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{}
	var need, mine int
	for _, l := range lists {
		key := func(id int) string { return strings.TrimPrefix(l.Boss, "boss-") + "#" + strconv.Itoa(id) }
		for _, it := range l.Items {
			col := it.Column
			if it.Done {
				col = "done"
			}
			want[col] = append(want[col], key(it.ID))
			if !it.Done && !it.Quiet {
				if it.NeedsYou {
					need++
				}
				if it.Assignee.Kind == "you" {
					mine++
				}
			}
		}
		for _, f := range l.Finished {
			want["done"] = append(want["done"], key(f.ID))
		}
	}
	for col, ids := range want {
		slices.Sort(ids)
		want[col] = ids
	}
	if len(want["todo"]) < 4 || len(want["progress"]) == 0 || len(want["waiting"]) == 0 || len(want["done"]) == 0 || need == 0 {
		t.Fatalf("❌ the fixture misses a column: %v need %d", want, need)
	}

	s.T("new-window", "-t", "=boss-a:", "-e", "DEV_TODO_BOARD_IN_POPUP=1", "-e", "CLAUDECODE=", devBin, "todo", "board", "--client", "none")
	view := s.T("display", "-t", "=boss-a:2", "-p", "#{pane_id}")
	capture := func() string { return s.T("capture-pane", "-p", "-t", view) }
	waitFor(t, "the board drawn", func() bool { return strings.Contains(capture(), "Done") && strings.Contains(capture(), "need you") })
	screen := capture()
	t.Logf("popup:\n%s", screen)

	got := popupColumns(screen)
	for _, col := range popupHeads {
		slices.Sort(got[col])
		if !slices.Equal(got[col], want[col]) {
			t.Errorf("❌ %s: popup %v, --all --json %v\n%s", col, got[col], want[col], screen)
		}
	}
	if line := fmt.Sprintf("%d need you · %d mine", need, mine); !strings.Contains(screen, line) {
		t.Errorf("❌ top line lacks %q:\n%s", line, screen)
	}

	// At 100 columns three show, h/l bringing in the rest
	s.T("resize-window", "-t", "=boss-a:2", "-x", "100", "-y", "70")
	waitFor(t, "the board at 100 columns", func() bool { return strings.Contains(capture(), "columns 1–3 of 5") })
	t.Logf("popup at 100 columns:\n%s", capture())
	s.T("send-keys", "-t", view, "l", "l", "l", "l")
	waitFor(t, "the last columns", func() bool { return strings.Contains(capture(), "columns 3–5 of 5") })
	t.Logf("popup at 100 columns, scrolled right:\n%s", capture())
}

var popupHeads = []string{"todo", "progress", "waiting", "review", "done"}

// popupColumns are the popup's cards by column, "project#id" each, read off its screen: each cell of a row, its first
// line "[! ]project #id".
func popupColumns(screen string) map[string][]string {
	card := regexp.MustCompile(`^(?:! )?(\S+) #(\d+)(?: |$)`)
	got := map[string][]string{}
	for _, line := range strings.Split(screen, "\n") {
		line = strings.TrimSuffix(strings.TrimPrefix(line, "│ "), " │")
		cells := strings.Split(line, " │ ")
		if len(cells) != len(popupHeads) {
			continue
		}
		for i, c := range cells {
			if m := card.FindStringSubmatch(strings.TrimSpace(c)); m != nil {
				got[popupHeads[i]] = append(got[popupHeads[i]], m[1]+"#"+m[2])
			}
		}
	}
	for _, ids := range got {
		slices.Sort(ids)
	}
	return got
}

// A move made through dev serve (what a web drop does: doing, start, done) shows in the open popup within its refresh,
// moved to the column the page puts it in.
func TestTodoBoardFollowsServe(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	s.T("new-session", "-d", "-s", "boss-a", "-x", "200", "-y", "60", "bash", script, "", filepath.Join(tmp, "boss-a.log"))
	s.T("set-option", "-t", "=boss-a:", "@dev_herd", "1")
	pane := s.T("display", "-t", "=boss-a:", "-p", "#{pane_id}")
	state := func(status, event string) {
		t.Helper()
		cmd := exec.Command(devBin, "state", status)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"` + event + `"}`)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("❌ %s: %v %s", event, err, out)
		}
	}
	state("idle", "SessionStart")
	state("working", "UserPromptSubmit")
	boss := []string{"TMUX_PANE=" + pane, "CLAUDECODE=1"}
	for _, text := range []string{"Do it", "Start it", "Tick it"} {
		cmd := exec.Command(devBin, "todo", "add", "--no-details", text)
		cmd.Env = s.scriptEnv(boss...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("❌ add %q: %v %s", text, err, out)
		}
	}
	url := startServe(t, s, "TMUX_PANE=", "CLAUDECODE=")
	post := func(path, body string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, url+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Dev-Page", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if data, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusOK {
			t.Fatalf("❌ POST %s %s: %d %s", path, body, resp.StatusCode, data)
		}
	}
	s.T("new-window", "-t", "=boss-a:", "-e", "DEV_TODO_BOARD_IN_POPUP=1", "-e", "CLAUDECODE=", devBin, "todo", "board", "--client", "none")
	view := s.T("display", "-t", "=boss-a:2", "-p", "#{pane_id}")
	capture := func() string { return s.T("capture-pane", "-p", "-t", view) }
	waitFor(t, "the board drawn", func() bool { return len(popupColumns(capture())["todo"]) == 3 })

	for i, c := range []struct{ what, path, body, column, id, mark string }{
		{"doing", "v1/doing", `{"boss":"boss-a","id":1,"text":"Do it","seen":{"doing":null,"started":null},"doing":"on it"}`, "progress", "a#1", "▸ on it"},
		{"start", "v1/start", `{"boss":"boss-a","id":2,"text":"Start it"}`, "progress", "a#2", "starting · boss-a"},
		{"done", "v1/done", `{"boss":"boss-a","id":3,"text":"Tick it"}`, "done", "a#3", ""},
	} {
		// Each at another point of the popup's refresh
		time.Sleep(time.Duration(1700*i) * time.Millisecond)
		post(c.path, c.body)
		began := time.Now()
		for !slices.Contains(popupColumns(capture())[c.column], c.id) || !strings.Contains(capture(), c.mark) {
			if time.Since(began) > 5*time.Second {
				t.Fatalf("❌ %s: %s not in %s after 5 s:\n%s", c.what, c.id, c.column, capture())
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("%s showed after %v", c.what, time.Since(began).Round(100*time.Millisecond))
	}
}

// r on the user's Done card in the popup's item view reopens it: back in To do with its number within 5 s, as dev todo
// --all --json has it, not among the finished items any more.
func TestTodoBoardReopen(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	s.T("new-session", "-d", "-s", "boss-a", "-x", "200", "-y", "60", "bash", script, "", filepath.Join(tmp, "boss-a.log"))
	s.T("set-option", "-t", "=boss-a:", "@dev_herd", "1")
	pane := s.T("display", "-t", "=boss-a:", "-p", "#{pane_id}")
	dev := func(env []string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("❌ dev %v: %v %s", argv, err, out)
		}
		return string(out)
	}
	reg := exec.Command(devBin, "state", "idle")
	reg.Env = s.scriptEnv("TMUX_PANE=" + pane)
	reg.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
	if out, err := reg.CombinedOutput(); err != nil {
		t.Fatalf("❌ register: %v %s", err, out)
	}
	boss := []string{"TMUX_PANE=" + pane, "CLAUDECODE=1"}
	user := []string{"TMUX_PANE=", "CLAUDECODE="}
	dev(boss, "todo", "add", "--no-details", "Ship it", "--link", "https://example.com/pull/7")
	dev(boss, "todo", "add", "--no-details", "Other")
	dev(user, "todo", "done", "--boss", "boss-a", "--text", "Ship it", "1")
	// Past the 5 s a ticked item stays, a write moves it to the finished items
	time.Sleep(5200 * time.Millisecond)
	dev(boss, "todo", "add", "--no-details", "Later")
	type item struct {
		ID     int    `json:"id"`
		Text   string `json:"text"`
		Link   string `json:"link"`
		Done   bool   `json:"done"`
		Column string `json:"column"`
	}
	read := func() (items, finished []item) {
		t.Helper()
		var lists []struct {
			Boss     string `json:"boss"`
			Items    []item `json:"items"`
			Finished []item `json:"finished"`
		}
		if err := json.Unmarshal([]byte(dev(user, "todo", "--all", "--json")), &lists); err != nil {
			t.Fatal(err)
		}
		for _, l := range lists {
			if l.Boss == "boss-a" {
				return l.Items, l.Finished
			}
		}
		t.Fatalf("❌ no boss-a list")
		return nil, nil
	}
	if items, finished := read(); len(finished) != 1 || finished[0].ID != 1 || slices.ContainsFunc(items, func(it item) bool { return it.ID == 1 }) {
		t.Fatalf("❌ before: items %+v finished %+v: want #1 finished only", items, finished)
	}

	s.T("new-window", "-t", "=boss-a:", "-e", "DEV_TODO_BOARD_IN_POPUP=1", "-e", "CLAUDECODE=", devBin, "todo", "board", "--client", "none")
	view := s.T("display", "-t", "=boss-a:2", "-p", "#{pane_id}")
	capture := func() string { return s.T("capture-pane", "-p", "-t", view) }
	waitFor(t, "the board drawn", func() bool { return slices.Contains(popupColumns(capture())["done"], "a#1") })
	s.T("send-keys", "-t", view, "l", "l", "l", "l", "Space")
	waitFor(t, "the Done card's item view", func() bool { return strings.Contains(capture(), "r  Reopen") })
	t.Logf("item view:\n%s", capture())
	s.T("send-keys", "-t", view, "r")
	began := time.Now()
	for !slices.Contains(popupColumns(capture())["todo"], "a#1") {
		if time.Since(began) > 5*time.Second {
			t.Fatalf("❌ a#1 not back in To do after 5 s:\n%s", capture())
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("reopened after %v:\n%s", time.Since(began).Round(100*time.Millisecond), capture())
	items, finished := read()
	i := slices.IndexFunc(items, func(it item) bool { return it.ID == 1 })
	if i < 0 || items[i].Done || items[i].Column != "todo" || items[i].Text != "Ship it" || items[i].Link != "https://example.com/pull/7" || len(finished) != 0 {
		t.Errorf("❌ after: items %+v finished %+v: want #1 open in To do with its text and link, none finished", items, finished)
	}
}

// i on the user's own To do card in the popup's item view asks what they do now: ⏎ moves it to In progress with that
// doing line, on the card, in its item view and in dev todo --all --json, its boss told; i again stops it, back to To
// do; esc in the field leaves it as it was.
func TestTodoBoardDoing(t *testing.T) {
	tmp := t.TempDir()
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	log := filepath.Join(tmp, "boss-a.log")
	s.T("new-session", "-d", "-s", "boss-a", "-x", "200", "-y", "60", "bash", script, "", log)
	s.T("set-option", "-t", "=boss-a:", "@dev_herd", "1")
	pane := s.T("display", "-t", "=boss-a:", "-p", "#{pane_id}")
	reg := exec.Command(devBin, "state", "idle")
	reg.Env = s.scriptEnv("TMUX_PANE=" + pane)
	reg.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
	if out, err := reg.CombinedOutput(); err != nil {
		t.Fatalf("❌ register: %v %s", err, out)
	}
	waitFor(t, "the stub boss drawn", func() bool { return strings.Contains(s.T("capture-pane", "-p", "-t", pane), "Try") })
	cmd := exec.Command(devBin, "todo", "add", "--no-details", "Send the cert")
	cmd.Env = s.scriptEnv("TMUX_PANE="+pane, "CLAUDECODE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("❌ add: %v %s", err, out)
	}
	type item struct {
		ID     int    `json:"id"`
		Column string `json:"column"`
		Doing  *struct {
			Text string `json:"text"`
		} `json:"doing"`
	}
	first := func() item {
		t.Helper()
		cmd := exec.Command(devBin, "todo", "--all", "--json")
		cmd.Env = s.scriptEnv("TMUX_PANE=", "CLAUDECODE=")
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		var lists []struct {
			Items []item `json:"items"`
		}
		if err := json.Unmarshal(out, &lists); err != nil || len(lists) != 1 || len(lists[0].Items) != 1 {
			t.Fatalf("❌ --all --json: %v %s", err, out)
		}
		return lists[0].Items[0]
	}

	s.T("new-window", "-t", "=boss-a:", "-e", "DEV_TODO_BOARD_IN_POPUP=1", "-e", "CLAUDECODE=", devBin, "todo", "board", "--client", "none")
	view := s.T("display", "-t", "=boss-a:2", "-p", "#{pane_id}")
	capture := func() string { return s.T("capture-pane", "-p", "-t", view) }
	waitFor(t, "the board drawn", func() bool { return slices.Contains(popupColumns(capture())["todo"], "a#1") })
	s.T("send-keys", "-t", view, "Space")
	waitFor(t, "the item view", func() bool { return strings.Contains(capture(), "I'm on it") })

	// esc leaves the field, and the item, as they were
	s.T("send-keys", "-t", view, "i")
	s.T("send-keys", "-t", view, "-l", "not this")
	waitFor(t, "the field typed in", func() bool {
		return strings.Contains(capture(), "not this") && strings.Contains(capture(), "⏎ on it · esc back")
	})
	s.T("send-keys", "-t", view, "Escape")
	waitFor(t, "the field left, the item view still open", func() bool {
		return !strings.Contains(capture(), "not this") && strings.Contains(capture(), "i  I'm on it")
	})
	if it := first(); it.Column != "todo" || it.Doing != nil {
		t.Fatalf("❌ after esc: %+v, want To do, no doing line", it)
	}

	s.T("send-keys", "-t", view, "i")
	s.T("send-keys", "-t", view, "-l", "mailing ops")
	waitFor(t, "the field typed in", func() bool { return strings.Contains(capture(), "mailing ops") })
	t.Logf("item view, the field typed in:\n%s", capture())
	s.T("send-keys", "-t", view, "Enter")
	began := time.Now()
	for !slices.Contains(popupColumns(capture())["progress"], "a#1") || !strings.Contains(capture(), "mailing ops") {
		if time.Since(began) > 5*time.Second {
			t.Fatalf("❌ a#1 not In progress with its doing line after 5 s:\n%s", capture())
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("In progress after %v:\n%s", time.Since(began).Round(100*time.Millisecond), capture())
	if it := first(); it.Column != "progress" || it.Doing == nil || it.Doing.Text != "mailing ops" {
		t.Errorf("❌ --all --json after i: %+v %+v, want In progress doing mailing ops", it, it.Doing)
	}
	waitFor(t, "the boss told", func() bool {
		data, _ := os.ReadFile(log)
		return strings.Contains(string(data), `the user is on #1 "Send the cert": mailing ops`)
	})

	// Its item view shows the doing line and Stop; i stops it
	s.T("send-keys", "-t", view, "Space") // the selection followed the card
	waitFor(t, "the In progress item view", func() bool {
		return strings.Contains(capture(), "doing   mailing ops") && strings.Contains(capture(), "i  Stop")
	})
	t.Logf("item view, doing:\n%s", capture())
	s.T("send-keys", "-t", view, "i")
	waitFor(t, "back in To do", func() bool { return slices.Contains(popupColumns(capture())["todo"], "a#1") })
	if it := first(); it.Column != "todo" || it.Doing != nil {
		t.Errorf("❌ --all --json after i again: %+v, want To do, no doing line", it)
	}
}
