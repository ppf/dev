package itemview

import (
	"strings"
	"testing"
	"time"

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
}

// The boss's own item: only its doing line, since when.
func TestBossBlock(t *testing.T) {
	it := Item{Boss: "boss-dev", Shown: todo.Shown{ID: 19, Text: "Release v0.83.0", On: todo.Boss, Status: sidebar.StatusWorking,
		Doing:     &todo.Doing{Text: "waiting on CI for #285", At: now.Add(-time.Minute).Unix()},
		Placement: todo.Placement{Column: sidebar.ColumnProgress, Assignee: todo.Assignee{Kind: sidebar.AssigneeBoss, Name: "boss-dev"}}}}
	v := ansi.Strip(New(it, Actions{}, false).View(Width, 40, now))
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
