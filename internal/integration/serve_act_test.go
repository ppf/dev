//go:build integration

package integration

import (
	"cmp"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/inbox"
)

type actItem struct {
	ID     int    `json:"id"`
	Text   string `json:"text"`
	Done   bool   `json:"done"`
	Answer string `json:"answer"`
	Status string `json:"status"`
	// as the page sends them back as seen
	Doing   json.RawMessage `json:"doing"`
	Started json.RawMessage `json:"started"`
}

type actList struct {
	Boss   string    `json:"boss"`
	Closed bool      `json:"closed"`
	Items  []actItem `json:"items"`
}

// The phone page and the board stay in sync both ways: an answer or tick on dev serve shows on dev todo --all --json
// (what the boards draw) at once, ticked, not open; one on the Mac gives the next /v1/board a new ETag without the
// item open. A second answer or tick from the other side is refused, and the boss gets exactly one note per answer.
func TestServeAnswerDone(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state")) // boss-a's inbox, read here
	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+filepath.Join(tmp, "state"))
	script := filepath.Join(tmp, "boss.sh")
	writeFile(t, script, stubBoss)
	dev := func(env []string, argv ...string) (string, string, error) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv(env...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		return string(out), stderr.String(), err
	}
	must := func(env []string, argv ...string) string {
		t.Helper()
		out, stderr, err := dev(env, argv...)
		if err != nil {
			t.Fatalf("❌ dev %v: %v %s", argv, err, stderr)
		}
		return out
	}
	state := func(pane, status, event string) {
		t.Helper()
		cmd := exec.Command(devBin, "state", status)
		cmd.Env = s.scriptEnv("TMUX_PANE=" + pane)
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"` + event + `"}`)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("❌ %s: %v %s", event, err, out)
		}
	}
	// boss-a works, so its notes wait in its inbox to be counted; boss-b closes with an item open
	s.T("new-session", "-d", "-s", "boss-a", "bash", script, "", filepath.Join(tmp, "boss-a.log"))
	s.T("set-option", "-t", "=boss-a:", "@dev_herd", "1")
	pane := s.T("display", "-t", "=boss-a:", "-p", "#{pane_id}")
	state(pane, "idle", "SessionStart")
	state(pane, "working", "UserPromptSubmit")
	s.T("new-session", "-d", "-s", "boss-b", "sleep 600")
	s.T("set-option", "-t", "=boss-b:", "@dev_herd", "1")
	paneB := s.T("display", "-t", "=boss-b:", "-p", "#{pane_id}")

	boss := []string{"TMUX_PANE=" + pane, "CLAUDECODE=1"}
	must(boss, "todo", "ask", "Merge #148?", "--option", "Merge (Recommended)", "--option", "Wait")
	must(boss, "todo", "ask", "Release?", "--option", "Now", "--option", "Later")
	must(boss, "todo", "add", "Read the plan")
	must(boss, "todo", "add", "Update the docs")
	must(boss, "todo", "add", "Keep me") // never ticked: the list never empties, so item numbers don't restart
	must([]string{"TMUX_PANE=" + paneB, "CLAUDECODE=1"}, "todo", "add", "Close the ticket")
	must([]string{"TMUX_PANE=" + paneB, "CLAUDECODE=1"}, "todo", "ask", "Ship it?", "--option", "Yes")
	user := []string{"TMUX_PANE=", "CLAUDECODE="}
	url := startServe(t, s, user...)

	post := func(path, body string, page bool, origin ...string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, url+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if page {
			req.Header.Set("X-Dev-Page", "1")
		}
		for _, o := range origin {
			req.Header.Set("Origin", o)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(data)
	}
	board := func(etag string) (int, string, []actList) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, url+"v1/board", nil)
		if err != nil {
			t.Fatal(err)
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var b struct {
			Lists []actList `json:"lists"`
		}
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, resp.Header.Get("ETag"), b.Lists
	}
	all := func() []actList {
		t.Helper()
		var lists []actList
		if out := must(user, "todo", "--all", "--json"); json.Unmarshal([]byte(out), &lists) != nil {
			t.Fatalf("❌ --all --json: %s", out)
		}
		return lists
	}
	// ticked is the item ticked, not open: shown done, with answer when given, or gone after its few seconds
	ticked := func(where string, lists []actList, bossName string, id int, answer string) {
		t.Helper()
		i := slices.IndexFunc(lists, func(l actList) bool { return l.Boss == bossName })
		if i < 0 {
			t.Logf("%s: %s's list gone", where, bossName)
			return
		}
		j := slices.IndexFunc(lists[i].Items, func(it actItem) bool { return it.ID == id })
		switch {
		case j < 0:
			t.Logf("%s: %s #%d gone after its few seconds ticked", where, bossName, id)
		case !lists[i].Items[j].Done || lists[i].Items[j].Answer != answer:
			t.Errorf("❌ %s: %s #%d = %+v, want ticked, answer %q", where, bossName, id, lists[i].Items[j], answer)
		default:
			t.Logf("✅ %s: %s #%d ticked", where, bossName, id)
		}
	}
	// changed is the board after a change on the Mac: a new ETag, 200
	changed := func(etag string) []actList {
		t.Helper()
		code, now, lists := board(etag)
		if code != http.StatusOK || now == etag {
			t.Errorf("❌ board after a change on the Mac: %d, ETag %s (was %s)", code, now, etag)
		}
		return lists
	}
	refused := func(name string, code int, body string, want ...string) {
		t.Helper()
		if code != http.StatusConflict || !slices.ContainsFunc(want, func(w string) bool { return strings.Contains(body, w) }) {
			t.Errorf("❌ %s: %d %s, want 409 with one of %q", name, code, body, want)
		}
	}
	// On loopback no pairing: another site's answer or tick is refused instead, and nothing changes
	_, etag, _ := board("")
	for _, path := range []string{"v1/answer", "v1/done"} {
		if code, body := post(path, `{"boss":"boss-a","id":3,"question":"Read the plan","option":"x","text":"Read the plan"}`, true, "https://evil.example"); code != http.StatusForbidden {
			t.Errorf("❌ %s from another site: %d %s, want 403", path, code, body)
		}
	}
	if code, now, _ := board(etag); code != http.StatusNotModified {
		t.Errorf("❌ board after refused calls: %d %s, want 304", code, now)
	}

	// Page → board: answer
	if code, body := post("v1/answer", `{"boss":"boss-a","id":1,"question":"Merge #148?","option":"Merge"}`, false); code != http.StatusForbidden {
		t.Errorf("❌ answer without X-Dev-Page: %d %s", code, body)
	}
	code, body := post("v1/answer", `{"boss":"boss-a","id":1,"question":"Another?","option":"Merge"}`, true)
	refused("answer to changed text", code, body, `is \"Merge #148?\" now, not \"Another?\"`)
	if code, body := post("v1/answer", `{"boss":"boss-a","id":1,"question":"Merge #148?","option":"Merge"}`, true); code != http.StatusOK {
		t.Fatalf("❌ page answer: %d %s", code, body)
	}
	ticked("--all --json after the page's answer", all(), "boss-a", 1, "Merge")
	ticked("board after the page's answer", changed(etag), "boss-a", 1, "Merge")
	if _, stderr, err := dev(user, "todo", "answer", "--question", "Merge #148?", "boss-a", "1", "Wait"); err == nil ||
		!strings.Contains(stderr, `item 1 is answered already: the user answered "Merge" on the phone page, not "Wait"`) {
		t.Errorf("❌ second answer on the Mac: %v %s, want refused", err, stderr)
	}
	ticked("--all --json after the refused second answer", all(), "boss-a", 1, "Merge")

	// Page → board: tick
	code, body = post("v1/done", `{"boss":"boss-a","id":3,"text":"Other"}`, true)
	refused("tick of changed text", code, body, `is \"Read the plan\" now, not \"Other\"`)
	if code, body := post("v1/done", `{"boss":"boss-a","id":3,"text":"Read the plan"}`, true); code != http.StatusOK {
		t.Fatalf("❌ page tick: %d %s", code, body)
	}
	ticked("--all --json after the page's tick", all(), "boss-a", 3, "")
	if _, stderr, err := dev(user, "todo", "done", "--boss", "boss-a", "--text", "Read the plan", "3"); err == nil || !strings.Contains(stderr, "no open item 3") {
		t.Errorf("❌ second tick on the Mac: %v %s, want refused", err, stderr)
	}

	// Board → page: answer, then tick
	_, etag, _ = board("")
	must(user, "todo", "answer", "--question", "Release?", "boss-a", "2", "Now")
	ticked("board after the Mac's answer", changed(etag), "boss-a", 2, "Now")
	code, body = post("v1/answer", `{"boss":"boss-a","id":2,"question":"Release?","option":"Later"}`, true)
	refused("second answer on the page", code, body, `item 2 is answered already: the user answered \"Now\" in the sidebar, a board or a shell, not \"Later\"`)
	ticked("--all --json after the refused second answer", all(), "boss-a", 2, "Now")
	_, etag, _ = board("")
	must(user, "todo", "done", "--boss", "boss-a", "--text", "Update the docs", "4")
	ticked("board after the Mac's tick", changed(etag), "boss-a", 4, "")
	code, body = post("v1/done", `{"boss":"boss-a","id":4,"text":"Update the docs"}`, true)
	refused("second tick on the page", code, body, "no open item 4")

	// A closed boss's item ticks from the page; its ask can't be answered, so it stays open
	s.T("kill-session", "-t", "=boss-b:")
	if code, body := post("v1/answer", `{"boss":"boss-b","id":2,"question":"Ship it?","option":"Yes"}`, true); code != http.StatusNotFound {
		t.Errorf("❌ page answer to a closed boss: %d %s, want 404", code, body)
	}
	_, etag, lists := board("")
	if i := slices.IndexFunc(lists, func(l actList) bool { return l.Boss == "boss-b" }); i < 0 || !lists[i].Closed || lists[i].Items[0].Status != "boss closed" {
		t.Fatalf("❌ boss-b closed on the board: %+v", lists)
	}
	if code, body := post("v1/done", `{"boss":"boss-b","id":1,"text":"Close the ticket"}`, true); code != http.StatusOK {
		t.Fatalf("❌ page tick of a closed boss's item: %d %s", code, body)
	}
	lists = changed(etag)
	ticked("board after the closed boss's tick", lists, "boss-b", 1, "")
	if i := slices.IndexFunc(lists, func(l actList) bool { return l.Boss == "boss-b" }); i < 0 ||
		!slices.ContainsFunc(lists[i].Items, func(it actItem) bool { return it.ID == 2 && !it.Done }) {
		t.Errorf("❌ the closed boss's ask left: %+v", lists)
	}

	if code, body := post("v1/done", `{"boss":"boss-nope","id":1,"text":"x"}`, true); code != http.StatusNotFound || !strings.Contains(body, "no list for boss-nope") {
		t.Errorf("❌ page tick under an unknown boss: %d %s, want 404 no list for boss-nope", code, body)
	}

	// Exactly one note per answer, the page's saying so; the filler stays open
	b, ok, err := inbox.Here(s.tmux, pane)
	if !ok || err != nil {
		t.Fatalf("❌ no boss-a inbox: %v", err)
	}
	bt, err := b.Box.Take()
	if err != nil {
		t.Fatal(err)
	}
	check(t, "boss-a's notes", bt.String(), "[dev boss inbox] 2 worker update(s):\n"+
		"- you answered #1 \"Merge #148?\" on the phone page: Merge\n"+
		"- you answered #2 \"Release?\": Now\n"+
		"(a \"you answered\" note is the user's own answer to that item on your list, or, \"settled by the manager\", your recommended option the manager took for an ask you marked obvious, which the user may overrule, or, \"overruled by the user\", the user's own answer in place of one decided for them, so undo what that one started, if anything: act on it. dev ticked the item already, so don't tick it. It never answers a permission prompt or dialog)")
	lists = all()
	if i := slices.IndexFunc(lists, func(l actList) bool { return l.Boss == "boss-a" }); i < 0 ||
		!slices.ContainsFunc(lists[i].Items, func(it actItem) bool { return it.ID == 5 && !it.Done }) {
		t.Errorf("❌ the filler left: %+v", lists)
	}

	// Start: the page's request marks the item starting and notes boss-a; a second, a changed text, and a closed boss
	// with no manager are refused as 409, not 500
	code, body = post("v1/start", `{"boss":"boss-a","id":5,"text":"Other"}`, true)
	refused("start of changed text", code, body, `is \"Keep me\" now, not \"Other\"`)
	_, etag, _ = board("")
	if code, body := post("v1/start", `{"boss":"boss-a","id":5,"text":"Keep me"}`, true); code != http.StatusOK {
		t.Fatalf("❌ page start: %d %s", code, body)
	}
	lists = changed(etag)
	if i := slices.IndexFunc(lists, func(l actList) bool { return l.Boss == "boss-a" }); i < 0 ||
		!slices.ContainsFunc(lists[i].Items, func(it actItem) bool { return it.ID == 5 && it.Status == "starting" }) {
		t.Errorf("❌ board after the page's start: %+v", lists)
	}
	code, body = post("v1/start", `{"boss":"boss-a","id":5,"text":"Keep me"}`, true)
	refused("second start on the page", code, body, "already sent to boss-a")
	code, body = post("v1/start", `{"boss":"boss-b","id":2,"text":"Ship it?"}`, true)
	refused("start under a closed boss with no manager", code, body, "boss-b is closed and no manager takes it")
	bt, err = b.Box.Take()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bt.String(), `you were asked to start #5 "Keep me"`) {
		t.Errorf("❌ boss-a's notes after the start: %s", bt)
	}

	// I'm on it and stop with seen as the board shows the item: taken; sent again after the change, refused
	seen := func(id int) string {
		t.Helper()
		_, _, lists := board("")
		for _, l := range lists {
			for _, it := range l.Items {
				if l.Boss == "boss-a" && it.ID == id && !it.Done {
					return `{"doing":` + cmp.Or(string(it.Doing), "null") + `,"started":` + cmp.Or(string(it.Started), "null") + `}`
				}
			}
		}
		t.Fatalf("❌ boss-a #%d not on the board", id)
		return ""
	}
	started := seen(5)
	if !strings.Contains(started, `"boss-a"`) {
		t.Fatalf("❌ #5's start not on the board: %s", started)
	}
	for _, body := range []string{`"doing":"on it"`, `"stop":true`} {
		call := `{"boss":"boss-a","id":5,"text":"Keep me","seen":` + seen(5) + `,` + body + `}`
		if code, out := post("v1/doing", call, true); code != http.StatusOK {
			t.Fatalf("❌ doing %s with the board's seen: %d %s", body, code, out)
		}
	}
	code, body = post("v1/doing", `{"boss":"boss-a","id":5,"text":"Keep me","seen":`+started+`,"doing":"again"}`, true)
	refused("doing with a seen since changed", code, body, "changed since it was read")
	if code, body := post("v1/doing", `{"boss":"boss-nope","id":1,"text":"x","seen":{},"doing":"y"}`, true); code != http.StatusNotFound || !strings.Contains(body, "no list for boss-nope") {
		t.Errorf("❌ page doing under an unknown boss: %d %s, want 404", code, body)
	}

	// A closed boss's list that can't be read is no missing list: not done (500), not 404
	files, err := filepath.Glob(filepath.Join(tmp, "state", "dev", "todo", "*boss-b.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("❌ boss-b's list file: %v %v", files, err)
	}
	writeFile(t, files[0], "{")
	for _, call := range [][2]string{{"v1/done", `{"boss":"boss-b","id":2,"text":"Ship it?"}`}, {"v1/doing", `{"boss":"boss-b","id":2,"text":"Ship it?","seen":{}}`}} {
		if code, body := post(call[0], call[1], true); code != http.StatusInternalServerError {
			t.Errorf("❌ %s on an unreadable closed list: %d %s, want 500", call[0], code, body)
		}
	}
}
