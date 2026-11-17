package inbox

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func messages(t *testing.T) Messages {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, err := OpenMessages("/tmp/tmux-501/default")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// sent fails t on a refused message, else returns it.
func sent(t *testing.T) func(Msg, error) Msg {
	return func(msg Msg, err error) Msg {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
}

// A thread: a first message, replies between its two bosses with the line each inbox gets, ended by --done.
func TestMessageThread(t *testing.T) {
	s := messages(t)
	now := time.Unix(1_700_000_000, 0)
	first := sent(t)(s.Post("boss-a", "boss-b", "Is it\n  released?", false, now))
	if want := "m1 from boss-a: Is it released?"; first.Line() != want || first.To != "boss-b" || first.Thread != "m1" {
		t.Errorf("first = %+v, line %q, want %q", first, first.Line(), want)
	}
	reply := sent(t)(s.Reply("boss-b", "m1", "Not yet", false, now))
	if want := "m2 from boss-b (thread m1, reply to m1): Not yet"; reply.Line() != want || reply.To != "boss-a" {
		t.Errorf("reply = %+v, line %q, want %q to boss-a", reply, reply.Line(), want)
	}
	// The first sender follows up on its own message: to the other boss still
	again := sent(t)(s.Reply("boss-a", "m1", "Thanks", true, now))
	if want := "m3 from boss-a (thread m1, reply to m1, done: the thread is closed): Thanks"; again.Line() != want || again.To != "boss-b" {
		t.Errorf("follow-up = %+v, line %q", again, again.Line())
	}
	if _, err := s.Reply("boss-b", "m3", "one more", false, now); err == nil || !strings.Contains(err.Error(), "ended with --done") {
		t.Errorf("reply after --done = %v, want refused", err)
	}
	ts, err := s.Threads("boss-a", now)
	if err != nil || len(ts) != 1 || ts[0].State != ThreadDone || len(ts[0].Messages) != 3 {
		t.Errorf("threads = %+v, %v", ts, err)
	}
}

func TestMessageRefusals(t *testing.T) {
	s := messages(t)
	now := time.Unix(1_700_000_000, 0)
	sent(t)(s.Post("boss-a", "boss-b", "fyi: deployed", true, now))
	sent(t)(s.Post("boss-a", "boss-b", "question", false, now))
	for _, tt := range []struct {
		name string
		err  func() error
		want string
	}{
		{"to --fyi", func() error { _, err := s.Reply("boss-b", "m1", "ok", false, now); return err }, "m1 was sent --fyi: it takes no reply"},
		{"not in the thread", func() error { _, err := s.Reply("boss-c", "m2", "me too", false, now); return err }, "boss-c isn't in thread m2"},
		{"the manager outside its thread", func() error { _, err := s.Reply(Manager, "m2", "hi", false, now); return err }, "manager isn't in thread m2"},
		{"unknown id", func() error { _, err := s.Reply("boss-b", "m99", "?", false, now); return err }, "no message m99"},
		{"to itself", func() error { _, err := s.Post("boss-a", "boss-a", "hi", false, now); return err }, "can't message itself"},
		{"empty", func() error { _, err := s.Post("boss-a", "boss-b", " \n", false, now); return err }, "empty message"},
		{"too long", func() error {
			_, err := s.Post("boss-a", "boss-b", strings.Repeat("x", maxMessage+1), false, now)
			return err
		}, "message too long (1001 characters, at most 1000)"},
	} {
		if err := tt.err(); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.want)
		}
	}
	ts, _ := s.Threads("", now)
	if len(ts) != 2 || ts[0].ID != "m2" || ts[0].State != ThreadOpen || ts[1].State != ThreadFYI {
		t.Errorf("refusals stored something: %+v", ts)
	}
}

// 6 messages per thread, the first included; 10 per hour per sender, over a rolling hour.
func TestMessageCaps(t *testing.T) {
	s := messages(t)
	now := time.Unix(1_700_000_000, 0)
	sent(t)(s.Post("boss-a", "boss-b", "1", false, now))
	for i := 2; i <= ThreadCap; i++ {
		from := map[bool]string{true: "boss-b", false: "boss-a"}[i%2 == 0]
		sent(t)(s.Reply(from, "m"+strconv.Itoa(i-1), strconv.Itoa(i), false, now))
	}
	if _, err := s.Reply("boss-a", "m6", "7", false, now); err == nil || err.Error() != "thread m1 holds 6 messages, the cap: ask the user instead" {
		t.Errorf("7th in a thread = %v", err)
	}
	// boss-a sent 3 of them: 7 more within the hour, then refused
	for i := range 7 {
		sent(t)(s.Post("boss-a", "boss-c", "n"+strconv.Itoa(i), false, now.Add(time.Duration(i)*time.Minute)))
	}
	if _, err := s.Post("boss-a", "boss-c", "11th", false, now.Add(10*time.Minute)); err == nil || err.Error() != "boss-a sent 10 messages in the last hour, the cap: ask the user instead" {
		t.Errorf("11th in an hour = %v", err)
	}
	sent(t)(s.Post("boss-b", "boss-c", "another sender", false, now.Add(10*time.Minute)))
	// The first three left the hour
	sent(t)(s.Post("boss-a", "boss-c", "later", false, now.Add(time.Hour)))
}

// The manager writes at most once in a thread a boss started with it; in its own threads, like any boss.
func TestMessageManager(t *testing.T) {
	s := messages(t)
	now := time.Unix(1_700_000_000, 0)
	sent(t)(s.Post("boss-a", Manager, "can you ask boss-b?", false, now))
	sent(t)(s.Reply(Manager, "m1", "asking", false, now))
	if _, err := s.Reply(Manager, "m1", "again", false, now); err == nil || !strings.Contains(err.Error(), "the manager already wrote in thread m1") {
		t.Errorf("manager's second message = %v", err)
	}
	sent(t)(s.Post(Manager, "boss-b", "boss-a asks", false, now))
	sent(t)(s.Reply("boss-b", "m3", "yes", false, now))
	sent(t)(s.Reply(Manager, "m4", "thanks", false, now))
	sent(t)(s.Post("boss-b", "boss-c", "other", false, now.Add(time.Minute)))
	for viewer, want := range map[string][]string{Manager: {"m6", "m3", "m1"}, "": {"m6", "m3", "m1"}, "boss-b": {"m6", "m3"}, "boss-a": {"m1"}, "boss-d": {}} {
		ts, err := s.Threads(viewer, now)
		var got []string
		for _, th := range ts {
			got = append(got, th.ID)
		}
		if err != nil || strings.Join(got, " ") != strings.Join(want, " ") || ts == nil {
			t.Errorf("Threads(%q) = %v, %v, want %v", viewer, got, err, want)
		}
	}
}

// A thread goes keepThreads after its last message; ids keep counting.
func TestMessagePrune(t *testing.T) {
	s := messages(t)
	now := time.Unix(1_700_000_000, 0)
	sent(t)(s.Post("boss-a", "boss-b", "old", false, now))
	sent(t)(s.Post("boss-a", "boss-b", "kept", false, now))
	sent(t)(s.Reply("boss-b", "m2", "recent", false, now.Add(2*24*time.Hour)))
	later := now.Add(keepThreads + time.Hour)
	// Gone from a read too, before any write prunes the file; a reply doesn't bring it back
	if ts, _ := s.Threads("", later); len(ts) != 1 || ts[0].ID != "m2" {
		t.Errorf("read after a week: %+v", ts)
	}
	if _, err := s.Reply("boss-b", "m1", "late", false, later); err == nil || !strings.Contains(err.Error(), "no message m1") {
		t.Errorf("reply to a thread gone = %v", err)
	}
	msg := sent(t)(s.Post("boss-a", "boss-b", "new", false, later))
	ts, _ := s.Threads("", later)
	if msg.ID != "m4" || len(ts) != 2 || ts[0].ID != "m4" || ts[1].ID != "m2" {
		t.Errorf("after prune: %s, %+v", msg.ID, ts)
	}
}

// A message note keeps its line, skips the dedupe, and its batch says how to reply.
func TestMessageNote(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	b, err := Open("/tmp/s", "$1-1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	fin := Note{Worker: "boss-a", Kind: Finished, Text: "done", Key: Finished + "\x00done"}
	for _, n := range []Note{fin, {Worker: "boss-a", Kind: Message, Text: "m1 from boss-a: hi", Key: "m1"}, fin} {
		if _, err := b.Add(n, now); err != nil {
			t.Fatal(err)
		}
	}
	bt, err := b.Take()
	if err != nil || len(bt.Notes) != 2 {
		t.Fatalf("took %+v, %v: the repeated finished note is still a repeat", bt, err)
	}
	want := "[dev boss inbox] 2 worker update(s):\n- boss-a finished: done\n- message m1 from boss-a: hi\n" + messageHint
	if got := bt.String(); got != want {
		t.Errorf("batch:\n%s\nwant:\n%s", got, want)
	}
	if got := (Batch{Notes: []Note{fin}}).String(); strings.Contains(got, "dev agent reply") {
		t.Errorf("batch without messages has the hint: %q", got)
	}
}
