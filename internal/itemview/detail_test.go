package itemview

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

const draft = "Draft ready, not sent\n\nHallo Frau Weber,\nanbei die Rechnung.\n\n- attach the PDF\n  - the March one"

// in is the lines of v between the box's sides, trimmed.
func in(v string) []string {
	var out []string
	for _, l := range strings.Split(v, "\n") {
		out = append(out, strings.TrimSpace(strings.Trim(strings.TrimSpace(l), "│")))
	}
	return out
}

// hasLines reports want in lines, one after another.
func hasLines(lines, want []string) bool {
	for i := range lines {
		if i+len(want) <= len(lines) && strings.Join(lines[i:i+len(want)], "\n") == strings.Join(want, "\n") {
			return true
		}
	}
	return false
}

// To do: the details under the title, their first line on top, a paragraph's lines and a list kept, then the context.
func TestDetailsShown(t *testing.T) {
	it := Item{Boss: "boss-vitay", Shown: todo.Shown{ID: 117, Text: "Send the reminder", Status: sidebar.StatusYours, Details: draft, Context: []string{"template: Mahnung 2"},
		Placement: todo.Placement{Column: sidebar.ColumnTodo, Assignee: todo.Assignee{Kind: sidebar.AssigneeYou}}}}
	lines := in(ansi.Strip(New(it, Actions{}, false).View(Width, 60, now)))
	if !hasLines(lines, []string{"Send the reminder", "Draft ready, not sent", "", "Hallo Frau Weber,", "anbei die Rechnung.", "", "- attach the PDF", "- the March one", "template: Mahnung 2"}) {
		t.Errorf("view:\n%s", strings.Join(lines, "\n"))
	}
}

// A 4000-character draft in a short box: its last lines go first, a line says how many, the title, context and keys stay.
func TestLongDraft(t *testing.T) {
	long := "Draft ready, not sent\n\n" + strings.Repeat("Sehr geehrte Damen und Herren, ", 130)[:3970]
	it := Item{Boss: "boss-vitay", Shown: todo.Shown{ID: 117, Text: "Send the reminder", Status: sidebar.StatusYours, Details: long, Context: []string{"template: Mahnung 2"},
		Placement: todo.Placement{Column: sidebar.ColumnTodo, Assignee: todo.Assignee{Kind: sidebar.AssigneeYou}}}}
	m := New(it, Actions{}, false)
	v := ansi.Strip(m.View(Width, 30, now))
	rows := strings.Split(v, "\n")
	if len(rows) > 30 {
		t.Errorf("%d rows, at most 30", len(rows))
	}
	for _, want := range []string{"Send the reminder", "Draft ready, not sent", "more lines of details: open it on the page", "template: Mahnung 2", "esc close"} {
		if !strings.Contains(v, want) {
			t.Errorf("%q missing:\n%s", want, v)
		}
	}
	for _, r := range rows {
		if w := ansi.StringWidth(r); w != Width {
			t.Errorf("row %d wide, want %d: %q", w, Width, r)
		}
	}
	// An ask's options stay while its draft is cut
	it.Options, it.Status = []string{"Send (Recommended)", "Wait"}, sidebar.StatusDecide
	if v := ansi.Strip(New(it, Actions{}, false).View(Width, 30, now)); !strings.Contains(v, "★ Send") || !strings.Contains(v, "Wait") || !strings.Contains(v, "more lines of details") || len(strings.Split(v, "\n")) > 30 {
		t.Errorf("ask with a long draft:\n%s", v)
	}
	if full := ansi.Strip(m.View(Width, 1<<16, now)); !strings.Contains(full, "Herren,") || strings.Contains(full, "more lines of details") {
		t.Errorf("uncut, the draft whole:\n%s", full)
	}
}

// In progress on a worker: the session block under the title (state, how long, now, its latest note, PR and check),
// and the facts don't say doing or the check again; a worker asking the user says needs you.
func TestSessionBlock(t *testing.T) {
	it := Item{Boss: "boss-dev", Shown: todo.Shown{ID: 18, Text: "Item sheet", On: "item-detail-1", Session: &todo.Session{ID: "$4", Name: "item-detail-1"},
		Status: sidebar.StatusWorking, Now: "writing app.js", Note: "finished: mockup in the design folder", Added: now.Add(-42 * time.Minute).Unix(),
		Link: "https://github.com/ppf/dev/pull/285", Check: &sidebar.Check{Kind: sidebar.CheckFailing, PR: 285, Since: now.Unix()}, Details: "Layout A.",
		Placement: todo.Placement{Column: sidebar.ColumnProgress, Assignee: todo.Assignee{Kind: sidebar.AssigneeWorker, Name: "item-detail-1"}}}}
	v := ansi.Strip(New(it, Actions{}, false).View(Width, 40, now))
	lines := in(v)
	if !hasLines(lines, []string{"Item sheet"}) || !strings.Contains(v, "─ item-detail-1 ─") || !strings.Contains(v, "working · on it 42m ─") {
		t.Errorf("block head:\n%s", v)
	}
	for _, want := range []string{"now     writing app.js", "last    finished: mockup in the design folder", "pr      #285 checks failing", "Layout A."} {
		if !strings.Contains(v, want) {
			t.Errorf("%q missing:\n%s", want, v)
		}
	}
	if strings.Contains(v, "check   ") || strings.Contains(v, "doing   ") {
		t.Errorf("facts repeat the block:\n%s", v)
	}
	if strings.Index(v, "item-detail-1 ─") > strings.Index(v, "Layout A.") {
		t.Errorf("the block is under the details:\n%s", v)
	}
	it.Status = sidebar.StatusBlocked
	if v := ansi.Strip(New(it, Actions{}, false).View(Width, 40, now)); !strings.Contains(v, "needs you · on it 42m") {
		t.Errorf("blocked:\n%s", v)
	}
	it.Status, it.StatusOn = sidebar.StatusAsks, "item-detail-1"
	if v := ansi.Strip(New(it, Actions{}, false).View(Width, 40, now)); !strings.Contains(v, "asks its boss · on it 42m") || !strings.Contains(v, "status  asks its boss") || strings.Contains(v, "needs you") {
		t.Errorf("asks its boss:\n%s", v)
	}
}

// A narrow box keeps every row to its width: a long session name, a tick by itself on another day, the cut line.
func TestNarrowBlocks(t *testing.T) {
	worker := Item{Boss: "boss-vitay", Shown: todo.Shown{ID: 3, Text: "Send it", On: "vitay-mail-reminder-worker", Session: &todo.Session{ID: "$4", Name: "vitay-mail-reminder-worker"},
		Status: sidebar.StatusBlocked, Now: "asking", Added: now.Add(-3 * time.Hour).Unix(), Details: strings.Repeat("a line of the draft\n", 80),
		Placement: todo.Placement{Column: sidebar.ColumnProgress, Assignee: todo.Assignee{Kind: sidebar.AssigneeWorker, Name: "vitay-mail-reminder-worker"}}}}
	done := Item{Boss: "boss-vitay", DoneAt: now.Add(-10 * 24 * time.Hour).Unix(), Shown: todo.Shown{ID: 4, Text: "Merged", Done: true, Result: "PR #285 merged", DoneBy: todo.DoneByPR,
		Placement: todo.Placement{Column: sidebar.ColumnDone, Assignee: todo.Assignee{Kind: sidebar.AssigneeYou}}}}
	for _, w := range []int{40, 60} {
		for _, it := range []Item{worker, done} {
			for i, r := range strings.Split(New(it, Actions{}, false).View(w, 20, now), "\n") {
				if got := ansi.StringWidth(r); got != w {
					t.Errorf("width %d, #%d row %d: %d wide: %q", w, it.ID, i, got, ansi.Strip(r))
				}
			}
		}
	}
}

// The boss's parked item: a block saying no one is on it, status parked, Start kept.
func TestParkedBlock(t *testing.T) {
	it := Item{Boss: "boss-dev", Shown: todo.Shown{ID: 136, Text: "Parked: the phone's Done", On: todo.Boss, Status: sidebar.StatusParked, StatusOn: "boss-dev",
		Added: now.Add(-48 * time.Hour).Unix(), Placement: todo.Placement{Column: sidebar.ColumnTodo, Assignee: todo.Assignee{Kind: sidebar.AssigneeBoss, Name: "boss-dev", Running: true}, Parked: true}}}
	v := ansi.Strip(New(it, Actions{}, false).View(Width, 40, now))
	for _, want := range []string{"─ boss-dev ─", "parked · added 2d ago ─", "now     no one on it: boss-dev has not taken it up", "status  parked", "Start"} {
		if !strings.Contains(v, want) {
			t.Errorf("%q missing:\n%s", want, v)
		}
	}
}

// The boss's own item: only its doing line, since when; its PR's check stays in the facts.
func TestBossBlock(t *testing.T) {
	it := Item{Boss: "boss-dev", Shown: todo.Shown{ID: 19, Text: "Release v0.83.0", On: todo.Boss, Status: sidebar.StatusWorking,
		Doing:     &todo.Doing{Text: "waiting on CI for #285", At: now.Add(-time.Minute).Unix()},
		Placement: todo.Placement{Column: sidebar.ColumnProgress, Assignee: todo.Assignee{Kind: sidebar.AssigneeBoss, Name: "boss-dev"}}}}
	it.Link, it.Check = "https://github.com/ppf/dev/pull/285", &sidebar.Check{Kind: sidebar.CheckFailing, PR: 285, Since: now.Unix()}
	v := ansi.Strip(New(it, Actions{}, false).View(Width, 40, now))
	if !strings.Contains(v, "check   #285 checks failing") {
		t.Errorf("the boss's own item lost its check:\n%s", v)
	}
	if !strings.Contains(v, "doing · since "+now.Add(-time.Minute).Format("15:04")) || !strings.Contains(v, "now     waiting on CI for #285") || strings.Contains(v, "last ") || strings.Contains(v, "doing   ") {
		t.Errorf("view:\n%s", v)
	}
}

// Done: what was done under the title, when and by whom, then the details and context; the facts say ticked.
func TestDoneBlock(t *testing.T) {
	it := Item{Boss: "boss-vitay", DoneAt: now.Add(-time.Hour).Unix(), Shown: todo.Shown{ID: 117, Text: "Send the reminder", Done: true, On: "vitay-mail",
		Result: "Mail sent 12:40 to accounts@hartmann.example, Gmail id 19ab3f0c2e", DoneBy: "boss-vitay", Details: draft, Context: []string{"template: Mahnung 2"},
		Placement: todo.Placement{Column: sidebar.ColumnDone, Assignee: todo.Assignee{Kind: sidebar.AssigneeWorker, Name: "vitay-mail"}}}}
	v := ansi.Strip(New(it, Actions{}, false).View(Width, 50, now))
	at := now.Add(-time.Hour).Format("15:04")
	for _, want := range []string{"─ what was done ─", at + " · by boss-vitay ─", "Mail sent 12:40 to accounts@hartmann.example", "Draft ready, not sent", "template: Mahnung 2",
		"ticked  " + at + " · by boss-vitay"} {
		if !strings.Contains(v, want) {
			t.Errorf("%q missing:\n%s", want, v)
		}
	}
	if strings.Index(v, "Mail sent") > strings.Index(v, "Draft ready") {
		t.Errorf("what was done is under the details:\n%s", v)
	}
	if strings.Contains(v, "Reopen") {
		t.Errorf("a worker's item reopens:\n%s", v)
	}
	it.Result, it.DoneBy = "", todo.DoneByUntil
	if v := ansi.Strip(New(it, Actions{}, false).View(Width, 50, now)); strings.Contains(v, "what was done") || !strings.Contains(v, "ticked  "+at+" · by itself, at its time") {
		t.Errorf("no result:\n%s", v)
	}
}

// An item dev ticked by its rule: one line, the rule's result, when, by dev; no "what was done".
func TestDevClosedLine(t *testing.T) {
	it := Item{Boss: "boss-dev", DoneAt: now.Add(-time.Hour).Unix(), Shown: todo.Shown{ID: 213, Text: "Check v0.83.0 on the iPad", Done: true, DoneBy: todo.DoneByDev,
		Result:    "closed by dev: v0.93.0 replaced v0.83.0 (#310)",
		Placement: todo.Placement{Column: sidebar.ColumnDone, Assignee: todo.Assignee{Kind: sidebar.AssigneeYou}}}}
	v := ansi.Strip(New(it, Actions{}, false).View(Width, 50, now))
	at := now.Add(-time.Hour).Format("15:04")
	if !strings.Contains(v, "─ closed by dev: v0.93.0 replaced v0.83.0 (#310) ─") || !strings.Contains(v, at+" · by dev ─") || strings.Contains(v, "what was done") {
		t.Errorf("❌ dev's line:\n%s", v)
	}
}

// An ask decided for the user: who decided and when, the pick (★) over the others and why, in place of what was done.
func TestDecidedBlock(t *testing.T) {
	it := Item{Boss: "boss-dev", DoneAt: now.Add(-time.Hour).Unix(), Shown: todo.Shown{ID: 34, Text: "Re-run CI on #301?", Done: true, DoneBy: todo.DecidedManager, Answer: "Re-run the job",
		Decided:   &todo.Decided{By: todo.DecidedManager, Pick: "Re-run the job | known flake (Recommended)", Over: []string{"Look into it first | reads the log"}, Why: "known flake, 2 of the last 10 runs", At: now.Add(-time.Hour).Unix()},
		Placement: todo.Placement{Column: sidebar.ColumnDone, Assignee: todo.Assignee{Kind: sidebar.AssigneeYou}}}}
	v := ansi.Strip(New(it, Actions{}, false).View(Width, 50, now))
	at := now.Add(-time.Hour).Format("15:04")
	for _, want := range []string{"─ decided for you: manager ─", at + " · by manager ─", "picked  ★ Re-run the job", "over    Look into it first", "why     known flake, 2 of the last 10 runs"} {
		if !strings.Contains(v, want) {
			t.Errorf("%q missing:\n%s", want, v)
		}
	}
	if strings.Contains(v, "what was done") {
		t.Errorf("a decided ask shows what was done:\n%s", v)
	}
}

// decidedItem is ask #34 settled for the user by the manager, ticked at doneAt, options in the ask's order.
func decidedItem(doneAt time.Time) Item {
	options := []string{"Re-run the job | known flake (Recommended)", "Look into it first | reads the log", "Skip it"}
	return Item{Boss: "boss-dev", DoneAt: doneAt.Unix(), Shown: todo.Shown{ID: 34, Text: "Re-run CI on #301?", Done: true, DoneBy: todo.DecidedManager,
		Answer: "Re-run the job", Options: options,
		Decided:   &todo.Decided{By: todo.DecidedManager, Pick: options[0], Over: options[1:], Why: "known flake", At: doneAt.Unix()},
		Placement: todo.Placement{Column: sidebar.ColumnDone, Assignee: todo.Assignee{Kind: sidebar.AssigneeYou}}}}
}

// An ask decided for the user lists each other option as an Overrule, keyed by its number in the ask, in Reopen's
// place; its key runs dev todo overrule with the option as stored.
func TestOverruleEntries(t *testing.T) {
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = time.Now })
	var got []string
	acts := Actions{Overrule: func(boss string, id int, text, option string) (string, error) {
		got = append(got, fmt.Sprintf("%s#%d %s=%s", boss, id, text, option))
		return "overruled", nil
	}}
	m := New(decidedItem(now.Add(-time.Hour)), acts, false)
	v := ansi.Strip(m.View(Width, 50, now))
	for _, want := range []string{"▸ 2  Look into it first   overrule: reopens #34 with this, tells boss-dev", "  3  Skip it", "2/3 overrule · j/k ⏎ run · g go to boss · esc close"} {
		if !strings.Contains(v, want) {
			t.Errorf("%q missing:\n%s", want, v)
		}
	}
	if strings.Contains(v, "Reopen") {
		t.Errorf("Reopen beside Overrule:\n%s", v)
	}
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m, _ = m.Update(cmd())
	if !m.Done || m.Result != "overruled" || len(got) != 1 || got[0] != "boss-dev#34 Re-run CI on #301?=Skip it" {
		t.Errorf("3 = done %v %q, calls %q", m.Done, m.Result, got)
	}
	// Ticked over a day ago, a closed boss's: Reopen's line, no Overrule
	for name, it := range map[string]Item{"old": decidedItem(now.Add(-25 * time.Hour)), "closed": func() Item { it := decidedItem(now); it.Closed = true; return it }()} {
		v := ansi.Strip(New(it, acts, false).View(Width, 50, now))
		if strings.Contains(v, "overrule") || !strings.Contains(v, "r  Reopen") {
			t.Errorf("%s:\n%s", name, v)
		}
	}
	// Open past the day: its key says so and the list drops it
	m = New(decidedItem(now.Add(-23*time.Hour)), acts, false)
	timeNow = func() time.Time { return now.Add(2 * time.Hour) }
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if cmd != nil || !strings.Contains(m.msg, "over a day ago") || strings.Contains(ansi.Strip(m.View(Width, 50, now)), "overrule:") {
		t.Errorf("late 2 = %q, cmd %v", m.msg, cmd != nil)
	}
}
