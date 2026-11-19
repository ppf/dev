package todo

import (
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/wait"
)

// OwnerOf takes the sessions the boss started, by id or name, not itself nor another boss's.
func TestOwnerOf(t *testing.T) {
	live := []sidebar.Session{
		{Name: "boss-x", ID: "$1", Boss: true},
		{Name: "w1", StartedBy: sidebar.Starter("$1", "boss-x", true)},
		{Name: "w2", StartedBy: "boss-x"},
		{Name: "other", StartedBy: sidebar.Starter("$9", "boss-y", true)},
	}
	o := OwnerOf(live, "boss-x", true)
	if o.Name != "boss-x" || !o.Closed || o.Unread || len(o.Workers) != 2 || o.Workers[0].Name != "w1" || o.Workers[1].Name != "w2" {
		t.Errorf("OwnerOf = %+v, want boss-x closed with w1 and w2", o)
	}
	// A boss not listed: an open one's workers can't be told, a closed one's needn't be
	if o := OwnerOf(live, "boss-gone", false); !o.Unread || len(o.Workers) != 0 {
		t.Errorf("OwnerOf(open boss not listed) = %+v, want unread", o)
	}
	if o := OwnerOf(nil, "boss-gone", true); o.Unread || !o.Closed {
		t.Errorf("OwnerOf(closed boss) = %+v, want closed, not unread", o)
	}
}

// Status takes the first that holds, where several do.
func TestStatus(t *testing.T) {
	merged := &sidebar.Check{Kind: sidebar.CheckMerged, PR: 7}
	unchanged := &sidebar.Check{Kind: sidebar.CheckUnchanged}
	link := func(n string) *Session { return &Session{ID: n, Name: n} }
	held := wait.Wait{Until: time.Now().Add(time.Hour)}
	workers := []sidebar.Session{
		{Name: "busy", Agent: "Claude", State: agent.Working},
		{Name: "asking", Agent: "Claude", State: agent.Waiting, Deciding: true},
		{Name: "asked", Agent: "Claude", State: agent.Idle, Note: sidebar.NoteAsks + ": which one?"},
		{Name: "failed", Agent: "Claude", State: agent.Idle, Note: sidebar.NoteFailed + ": API error"},
		{Name: "stuck", Agent: "Claude", State: agent.Working, Note: sidebar.NoteStuck + ": no progress for 20m"},
		{Name: "planned", Agent: "Claude", State: agent.Idle, Wait: held},
		{Name: "limited", Agent: "Claude", State: agent.Idle, Resume: true, Note: sidebar.NoteFailed + ": rate limit"},
		{Name: "resting", Agent: "Claude", State: agent.Idle},
		{Name: "shell"},
	}
	live := Owner{Name: "boss-x", Workers: workers}
	for _, tt := range []struct {
		name     string
		owner    Owner
		it       Shown
		want, on string
	}{
		{"ticked", live, Shown{Done: true, On: "busy"}, "", ""},
		{"closed", Owner{Name: "boss-x", Closed: true}, Shown{On: "Ola", Check: &sidebar.Check{Kind: sidebar.CheckBossClosed}}, StatusBossClosed, "Ola"},
		{"closed, own name, PR merged", Owner{Name: "boss-x", Closed: true}, Shown{On: "boss-x", Check: merged}, StatusCheck, ""},
		{"closed, PR merged", Owner{Name: "boss-x", Closed: true}, Shown{On: "Ola", Check: merged}, StatusCheck, "Ola"},
		{"closed ask", Owner{Name: "boss-x", Closed: true}, Shown{Options: []string{"a", "b"}}, StatusBossClosed, ""},
		{"ask", live, Shown{Options: []string{"a", "b"}, Check: merged}, StatusDecide, ""},
		{"ask handed to a worker", live, Shown{Options: []string{"a", "b"}, On: "busy", Session: link("busy")}, StatusWorking, "busy"},
		{"linked to a worker named as the boss, case aside", Owner{Name: "boss-x", Workers: []sidebar.Session{{Name: "Boss-X", Agent: "Claude", State: agent.Working}}},
			Shown{On: "Boss-X", Session: link("Boss-X")}, StatusWorking, "Boss-X"},
		{"no one", live, Shown{}, StatusYours, ""},
		{"no one, unchanged", live, Shown{Check: unchanged}, StatusYours, ""},
		{"no one, PR merged", live, Shown{Check: merged}, StatusCheck, ""},
		{"its own boss", live, Shown{On: "Boss-X"}, StatusYours, ""},
		{"own boss, workers unread", Owner{Name: "boss-x", Unread: true}, Shown{On: "boss-x"}, StatusYours, ""},
		{"on someone, workers unread", Owner{Unread: true}, Shown{On: "busy", Session: link("busy")}, "", ""},
		{"linked, gone", live, Shown{On: "old", Session: link("old"), Check: merged}, StatusGone, "old"},
		{"linked to a session it didn't start", live, Shown{On: "other", Session: link("other")}, StatusGone, "other"},
		{"dialog", live, Shown{On: "asking", Session: link("asking"), Check: merged}, StatusBlocked, "asking"},
		{"turn ended on a question", live, Shown{On: "asked"}, StatusBlocked, "asked"},
		{"failed turn", live, Shown{On: "failed", Session: link("failed")}, StatusBlocked, "failed"},
		{"stuck note", live, Shown{On: "stuck", Session: link("stuck")}, StatusWorking, "stuck"},
		{"working, PR merged", live, Shown{On: "busy", Session: link("busy"), Check: merged}, StatusCheck, "busy"},
		{"working", live, Shown{On: "busy", Session: link("busy"), Check: unchanged}, StatusWorking, "busy"},
		{"unlinked worker by name", live, Shown{On: " BUSY"}, StatusWorking, "busy"},
		{"planned wait", live, Shown{On: "planned", Session: link("planned")}, StatusWaiting, "planned"},
		{"rate-limit resume", live, Shown{On: "limited", Session: link("limited")}, StatusWaiting, "limited"},
		{"resting", live, Shown{On: "resting", Session: link("resting")}, StatusIdle, "resting"},
		{"no agent", live, Shown{On: "shell", Session: link("shell")}, StatusIdle, "shell"},
		{"unlinked, no agent: a person", live, Shown{On: "shell"}, StatusWaiting, "shell"},
		{"person", live, Shown{On: "Ola"}, StatusWaiting, "Ola"},
		{"person, PR merged", live, Shown{On: "Ola", Check: merged}, StatusCheck, "Ola"},
	} {
		if got, on := tt.owner.Status(tt.it); got != tt.want || on != tt.on {
			t.Errorf("%s: Status = %q, %q; want %q, %q", tt.name, got, on, tt.want, tt.on)
		}
	}
}

// SetStatus fills each item's Status and StatusOn.
func TestSetStatus(t *testing.T) {
	l := List{Items: []Shown{{ID: 1}, {ID: 2, On: "Ola"}, {ID: 3, Done: true}}}
	l.SetStatus(Owner{Name: "boss-x"})
	for i, want := range [][2]string{{StatusYours, ""}, {StatusWaiting, "Ola"}, {"", ""}} {
		if got := [2]string{l.Items[i].Status, l.Items[i].StatusOn}; got != want {
			t.Errorf("item %d = %v, want %v", i+1, got, want)
		}
	}
}
