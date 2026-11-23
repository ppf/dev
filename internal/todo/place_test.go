package todo

import (
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/wait"
)

// SetStatus places each item by the board model's table, first match winning, and keeps its old status word.
func TestPlace(t *testing.T) {
	merged := &sidebar.Check{Kind: sidebar.CheckMerged, PR: 7}
	link := func(n string) *Session { return &Session{ID: n, Name: n} }
	doing := &Doing{Text: "sending the cert", At: 1}
	workers := []sidebar.Session{
		{Name: "busy", Agent: "Claude", State: agent.Working},
		{Name: "asking", Agent: "Claude", State: agent.Waiting, Deciding: true},
		{Name: "asked", Agent: "Claude", State: agent.Idle, Note: sidebar.NoteAsks + ": A or B?", BossRuns: true},
		{Name: "asked-alone", Agent: "Claude", State: agent.Idle, Note: sidebar.NoteAsks + ": A or B?"},
		{Name: "planned", Agent: "Claude", State: agent.Idle, Wait: wait.Wait{Until: time.Now().Add(time.Hour)}},
		{Name: "resting", Agent: "Claude", State: agent.Idle},
	}
	live := Owner{Name: "boss-x", Workers: workers}
	closed := Owner{Name: "boss-x", Closed: true}
	unread := Owner{Name: "boss-x", Unread: true}
	you := Assignee{Kind: sidebar.AssigneeYou}
	boss := Assignee{Kind: sidebar.AssigneeBoss, Name: "boss-x", Running: true}
	bossClosed := Assignee{Kind: sidebar.AssigneeBoss, Name: "boss-x"} // closed or unread: not running
	worker := func(n string) Assignee { return Assignee{Kind: sidebar.AssigneeWorker, Name: n, Running: true} }
	for _, tt := range []struct {
		row        string
		owner      Owner
		it         Shown
		column     string
		assignee   Assignee
		waitingFor string
		needsYou   bool
		status, on string
	}{
		{"0 ticked", live, Shown{Done: true, On: "busy", Session: link("busy")}, sidebar.ColumnDone, worker("busy"), "", false, "", ""},
		{"1 started, own", live, Shown{Started: &Started{To: "boss-x"}}, sidebar.ColumnProgress, boss, "", false, StatusStarting, "boss-x"},
		{"1 started late, closed", closed, Shown{Started: &Started{To: sidebar.Manager}, StartLate: true}, sidebar.ColumnProgress, bossClosed, "", true, StatusStarting, sidebar.Manager},
		{"1 started, not own", live, Shown{On: "busy", Session: link("busy"), Started: &Started{To: "boss-x"}}, sidebar.ColumnProgress, boss, "", false, StatusWorking, "busy"},
		{"2a closed, PR moved, own", closed, Shown{Check: merged}, sidebar.ColumnReview, you, "", true, StatusCheck, ""},
		{"2a closed, PR moved, a session's", closed, Shown{On: "busy", Session: link("busy"), Check: merged}, sidebar.ColumnReview, bossClosed, "", false, StatusCheck, "busy"},
		{"2b closed, own ask", closed, Shown{Options: []string{"a", "b"}}, sidebar.ColumnTodo, you, "", false, StatusBossClosed, ""},
		{"2c closed, on boss", closed, Shown{On: Boss}, sidebar.ColumnTodo, bossClosed, "", false, StatusBossClosed, Boss},
		{"2c closed, on a session", closed, Shown{On: "busy", Session: link("busy")}, sidebar.ColumnTodo, bossClosed, "", false, StatusBossClosed, "busy"},
		{"2d closed, on a person", closed, Shown{On: "Ola"}, sidebar.ColumnWaiting, bossClosed, "Ola", false, StatusBossClosed, "Ola"},
		{"3a own ask, PR moved", live, Shown{Options: []string{"a", "b"}, Check: merged}, sidebar.ColumnTodo, you, "", true, StatusDecide, ""},
		{"3b own, doing", live, Shown{Doing: doing}, sidebar.ColumnProgress, you, "", false, StatusYours, ""},
		{"3c own, PR moved", live, Shown{Check: merged}, sidebar.ColumnReview, you, "", true, StatusCheck, ""},
		{"3d own", live, Shown{}, sidebar.ColumnTodo, you, "", false, StatusYours, ""},
		{"3d own, on the boss's own name", live, Shown{On: "Boss-X"}, sidebar.ColumnTodo, you, "", false, StatusYours, ""},
		{"4a on boss, PR moved", live, Shown{On: Boss, Check: merged, Doing: doing}, sidebar.ColumnReview, boss, "", false, StatusCheck, "boss-x"},
		{"4a on boss, PR moved, no doing", live, Shown{On: Boss, Check: merged}, sidebar.ColumnReview, boss, "", false, StatusCheck, "boss-x"},
		{"4b on boss, doing", live, Shown{On: Boss, Doing: doing}, sidebar.ColumnProgress, boss, "", false, StatusWorking, "boss-x"},
		{"4c on boss", live, Shown{On: Boss}, sidebar.ColumnTodo, boss, "", false, StatusParked, "boss-x"},
		{"4c on boss, workers unread", unread, Shown{On: Boss}, sidebar.ColumnTodo, bossClosed, "", false, StatusParked, "boss-x"},
		{"1 on boss, started", live, Shown{On: Boss, Started: &Started{To: "boss-x"}}, sidebar.ColumnProgress, boss, "", false, StatusIdle, "boss-x"},
		{"5a unread, linked", unread, Shown{On: "busy", Session: link("busy")}, sidebar.ColumnProgress, Assignee{Kind: sidebar.AssigneeWorker, Name: "busy"}, "", false, "", ""},
		{"5b unread, no session", unread, Shown{On: "Ola"}, sidebar.ColumnWaiting, bossClosed, "Ola", false, "", ""},
		{"6 gone, PR moved", live, Shown{On: "old", Session: link("old"), Check: merged}, sidebar.ColumnTodo, boss, "", false, StatusGone, "old"},
		{"7 blocked, PR moved", live, Shown{On: "asking", Session: link("asking"), Check: merged}, sidebar.ColumnProgress, worker("asking"), "", true, StatusBlocked, "asking"},
		{"7 asks its boss", live, Shown{On: "asked", Session: link("asked")}, sidebar.ColumnProgress, worker("asked"), "", false, StatusAsks, "asked"},
		{"7 a question no running boss answers", live, Shown{On: "asked-alone", Session: link("asked-alone")}, sidebar.ColumnProgress, worker("asked-alone"), "", true, StatusBlocked, "asked-alone"},
		{"7 asks its boss, PR moved", live, Shown{On: "asked", Session: link("asked"), Check: merged, PRState: PROpen}, sidebar.ColumnProgress, worker("asked"), "", false, StatusAsks, "asked"},
		{"8 PR moved, a worker's", live, Shown{On: "busy", Session: link("busy"), Check: merged}, sidebar.ColumnReview, worker("busy"), "", false, StatusCheck, "busy"},
		{"8 PR moved, a person's", live, Shown{On: "Ola", Check: merged}, sidebar.ColumnReview, boss, "Ola", false, StatusCheck, "Ola"},
		{"9 on a person", live, Shown{On: "Ola"}, sidebar.ColumnWaiting, boss, "Ola", false, StatusWaiting, "Ola"},
		{"10a working", live, Shown{On: "busy", Session: link("busy")}, sidebar.ColumnProgress, worker("busy"), "", false, StatusWorking, "busy"},
		{"10b planned wait", live, Shown{On: "planned", Session: link("planned")}, sidebar.ColumnWaiting, worker("planned"), "", false, StatusWaiting, "planned"},
		{"10c idle, PR open", live, Shown{On: "resting", Session: link("resting"), PRState: PROpen}, sidebar.ColumnReview, worker("resting"), "", false, StatusIdle, "resting"},
		{"10d idle", live, Shown{On: "resting", Session: link("resting"), PRState: PRMerged}, sidebar.ColumnProgress, worker("resting"), "", false, StatusIdle, "resting"},
		{"3b over 3c: own, doing, PR moved", live, Shown{Doing: doing, Check: merged}, sidebar.ColumnProgress, you, "", false, StatusCheck, ""},
		{"2a over 2b: closed, own ask, PR moved", closed, Shown{Options: []string{"a", "b"}, Check: merged}, sidebar.ColumnReview, you, "", true, StatusCheck, ""},
		{"0 ticked, own", live, Shown{Done: true}, sidebar.ColumnDone, you, "", false, "", ""},
		{"10a over 10c: working, PR open", live, Shown{On: "busy", Session: link("busy"), PRState: PROpen}, sidebar.ColumnProgress, worker("busy"), "", false, StatusWorking, "busy"},
		{"ask handed on", live, Shown{Options: []string{"a", "b"}, On: "busy", Session: link("busy")}, sidebar.ColumnProgress, worker("busy"), "", false, StatusWorking, "busy"},
		{"quiet", live, Shown{Quiet: true, On: "Ola"}, sidebar.ColumnWaiting, boss, "Ola", false, StatusWaiting, "Ola"},
		{"desktop boss, on boss", Owner{}, Shown{On: Boss}, sidebar.ColumnTodo, Assignee{Kind: sidebar.AssigneeBoss, Name: Boss, Running: true}, "", false, StatusParked, Boss},
		{"desktop boss, own", Owner{}, Shown{}, sidebar.ColumnTodo, you, "", false, StatusYours, ""},
	} {
		l := List{Items: []Shown{tt.it}}
		l.SetStatus(tt.owner)
		got := l.Items[0]
		if got.Column != tt.column || got.Assignee != tt.assignee || got.WaitingFor != tt.waitingFor || got.NeedsYou != tt.needsYou {
			t.Errorf("%s: column %q, assignee %+v, waiting for %q, needs you %v; want %q, %+v, %q, %v", tt.row, got.Column, got.Assignee,
				got.WaitingFor, got.NeedsYou, tt.column, tt.assignee, tt.waitingFor, tt.needsYou)
		}
		if got.Status != tt.status || got.StatusOn != tt.on {
			t.Errorf("%s: status %q on %q, want %q on %q", tt.row, got.Status, got.StatusOn, tt.status, tt.on)
		}
		if want := tt.status == StatusParked; got.Parked != want {
			t.Errorf("%s: parked %v, want %v", tt.row, got.Parked, want)
		}
	}
}

// Read gives an open item its linked GitHub PR's last looked-up state, none for another link or before a lookup, and
// copies the stored Doing and PartOf.
func TestReadPRStateDoing(t *testing.T) {
	s := Store{path: t.TempDir() + "/boss-x.json"}
	now := time.Unix(1700000000, 0)
	pr := "https://github.com/o/r/pull/9"
	for _, it := range []Item{{Text: "a", Link: pr, Doing: &Doing{Text: "on it", At: 5}}, {Text: "b", Link: "https://example.com/x", PartOf: 1}} {
		if _, err := s.Add(it, now); err != nil {
			t.Fatal(err)
		}
	}
	l, err := s.Read(now)
	if err != nil {
		t.Fatal(err)
	}
	if l.Items[0].PRState != "" {
		t.Errorf("before a lookup: pr_state %q, want none", l.Items[0].PRState)
	}
	// A fact for the PR an item linked before its link changed gives none
	if err := s.writeChecks(map[string]fact{"1:1700000000": {Link: "https://github.com/o/r/pull/8", Last: prSeen{State: "MERGED"}}}); err != nil {
		t.Fatal(err)
	}
	if l, err = s.Read(now); err != nil || l.Items[0].PRState != "" {
		t.Errorf("fact of another PR: pr_state %q (%v), want none", l.Items[0].PRState, err)
	}
	if err := s.writeChecks(map[string]fact{"1:1700000000": {Link: pr, Last: prSeen{State: "MERGED"}}}); err != nil {
		t.Fatal(err)
	}
	if l, err = s.Read(now); err != nil || l.Items[0].PRState != PRMerged {
		t.Errorf("merged: pr_state %q (%v), want merged", l.Items[0].PRState, err)
	}
	if err := s.writeChecks(map[string]fact{"1:1700000000": {Link: pr, Last: prSeen{State: "OPEN"}}, "2:1700000000": {Link: "https://example.com/x", Last: prSeen{State: "OPEN"}}}); err != nil {
		t.Fatal(err)
	}
	if l, err = s.Read(now); err != nil {
		t.Fatal(err)
	}
	if a, b := l.Items[0], l.Items[1]; a.PRState != PROpen || b.PRState != "" || a.Doing == nil || *a.Doing != (Doing{Text: "on it", At: 5}) || a.PartOf != 2 || b.PartOf != 1 {
		t.Errorf("read: a %q %+v part of %d, b %q; want open, its doing, 2 (from b's side), none", a.PRState, a.Doing, a.PartOf, b.PRState)
	}
}

// An item on the boss itself stays the boss's, with no Now, when a worker is named boss too.
func TestPlaceWorkerNamedBoss(t *testing.T) {
	l := List{Items: []Shown{{On: Boss}}}
	l.SetStatus(Owner{Name: "boss-x", Workers: []sidebar.Session{{Name: Boss, Agent: "Claude", State: agent.Working, Doing: "x"}}})
	if got := l.Items[0]; got.Column != sidebar.ColumnTodo || got.Assignee.Kind != sidebar.AssigneeBoss || got.Status != StatusParked || got.Now != "" {
		t.Errorf("on boss beside a worker named boss: %s %+v %s now %q; want todo, the boss's, parked, no now", got.Column, got.Assignee, got.Status, got.Now)
	}
}
