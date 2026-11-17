package herd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
)

// withThread is board4 at 44 columns, in UTC, with one open thread of two messages, the reply long.
func withThread() Model {
	m := board4()
	m.width = 44
	now := time.Date(2026, 11, 17, 14, 30, 0, 0, time.UTC)
	m.clk.Now = func() time.Time { return now }
	m.snap.threads = []inbox.Thread{{ID: "m14", State: inbox.ThreadOpen, Messages: []inbox.Msg{
		{ID: "m14", Thread: "m14", From: "boss-stargate-vue", To: "boss-dev", Text: "Is dev agent message in 0.56.0?", At: now.Add(-10 * time.Minute).Unix()},
		{ID: "m15", Thread: "m14", ReplyTo: "m14", From: "boss-dev", To: "boss-stargate-vue",
			Text: "Not yet: lands in 0.57.0 (#150). Use dev agent prompt until then, it works the same for now.", At: now.Add(-9 * time.Minute).Unix()},
	}}}
	return m
}

// The block as in the mockup: header with the open count, each message's time, sender → recipient (· reply) and
// text wrapped under them to two rows, cut with …, then the thread's count.
func TestMessageBlock(t *testing.T) {
	m := withThread()
	checkRows(t, m.msgRows(39), []string{
		" messages · 1 open ─────────────────────────",
		" 14:20 boss-stargate-vue → boss-dev         ",
		"       Is dev agent message in 0.56.0?      ",
		" 14:21 boss-dev → boss-stargate-vue · reply ",
		"       Not yet: lands in 0.57.0 (#150). Use ",
		"       dev agent prompt until then, it …    ",
		" open · 2 of 6                              ",
	}, 44)
	if rows := plain(m.msgRows(9)); len(rows) != 3 || strings.TrimSpace(rows[2]) != "+ more: dev agent messages" {
		t.Errorf("9 rows: a third, cut with + more:\n%s", strings.Join(rows, "\n"))
	}
	if rows := m.msgRows(2); rows != nil {
		t.Errorf("2 rows: no room, got %q", rows)
	}
	m.snap.threads = nil
	if rows := m.msgRows(39); rows != nil {
		t.Errorf("no threads: no block, got %q", rows)
	}
}

// The block sits under the board, above the bottom row, and takes its rows from the board.
func TestMessageBlockLayout(t *testing.T) {
	m := withThread()
	before := board4()
	before.width = 44
	lines := m.render()
	if got := ansi.Strip(lines[len(lines)-2]); strings.TrimSpace(got) != "open · 2 of 6" {
		t.Errorf("row above the hints = %q, want the thread's count", got)
	}
	if got := ansi.Strip(lines[len(lines)-8]); !strings.HasPrefix(got, " messages · 1 open ") {
		t.Errorf("block header row = %q", got)
	}
	if m.boardHeight() != before.boardHeight()-7 {
		t.Errorf("board height %d, want %d", m.boardHeight(), before.boardHeight()-7)
	}
}

// Only open threads active within a day show.
func TestOpenThreads(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	thread := func(id, state string, ago time.Duration) inbox.Thread {
		return inbox.Thread{ID: id, State: state, Messages: []inbox.Msg{{ID: id, At: now.Add(-ago).Unix()}}}
	}
	got := openThreads([]inbox.Thread{
		thread("m1", inbox.ThreadOpen, time.Hour), thread("m2", inbox.ThreadDone, time.Minute),
		thread("m3", inbox.ThreadFYI, time.Minute), thread("m4", inbox.ThreadFull, time.Minute),
		thread("m5", inbox.ThreadOpen, 25*time.Hour),
	}, now)
	if len(got) != 1 || got[0].ID != "m1" {
		t.Errorf("openThreads = %+v, want m1 only", got)
	}
}

// Per thread its latest threadShown messages and its count, the most recently active thread first.
func TestMessageBlockThreads(t *testing.T) {
	m := withThread()
	at := m.clk.Now().Add(-time.Minute).Unix()
	long := inbox.Thread{ID: "m20", State: inbox.ThreadOpen}
	for i, from := range []string{"boss-a", "boss-b", "boss-a", "boss-b"} {
		long.Messages = append(long.Messages, inbox.Msg{ID: "m2" + string(rune('0'+i)), From: from, To: "x", Text: "msg " + string(rune('1'+i)), At: at})
	}
	m.snap.threads = append([]inbox.Thread{long}, m.snap.threads...)
	rows := plain(m.msgRows(60))
	got := strings.Join(rows, "\n")
	for _, want := range []string{" messages · 2 open ", "msg 3", "msg 4", " open · 4 of 6", " open · 2 of 6"} {
		if !strings.Contains(got, want) {
			t.Errorf("block lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "msg 1") || strings.Contains(got, "msg 2") || strings.Index(got, "4 of 6") > strings.Index(got, "2 of 6") {
		t.Errorf("want the long thread's last two messages, first:\n%s", got)
	}
}

// A click on the block hits no card; one on the board above it does.
func TestMessageBlockClick(t *testing.T) {
	m := withThread()
	lines := m.render()
	if _, ok := m.hitAt(3, len(lines)-5); ok {
		t.Error("click on the block hit a card")
	}
	top, _ := m.boardSpan(m.height - len(m.bottom()))
	if _, ok := m.hitAt(3, top+1); !ok {
		t.Error("click on the board missed its card")
	}
}

// Threads with no workers grow a collapsed pane to its share; once gone the pane collapses again.
func TestFitThreads(t *testing.T) {
	c := tmux.NewFake(func([]string) (string, error) { return "200\n", nil })
	m := Model{c: c, pane: "%2", size: collapsed}
	for _, st := range []struct {
		name string
		snap snapshot
		want paneSize
	}{
		{"a thread", snapshot{threads: withThread().snap.threads}, shared},
		{"unreadable", snapshot{msgsErr: errors.New("bad")}, shared},
		{"gone", snapshot{}, collapsed},
	} {
		next, _ := m.Update(refreshMsg{snap: st.snap})
		if m = next.(Model); m.size != st.want {
			t.Errorf("%s: size %v, want %v", st.name, m.size, st.want)
		}
	}
}
