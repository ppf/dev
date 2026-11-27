package inbox

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
)

// listen is a unix listener with a short path (macOS caps them at 104 bytes).
func listen(t *testing.T) (net.Listener, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "dvs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, sock
}

func TestSend(t *testing.T) {
	l, sock := listen(t)
	got := make(chan string, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			got <- err.Error()
			return
		}
		defer func() { _ = conn.Close() }()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		got <- line
	}()
	if err := Send(sock, "[dev boss inbox] 1 worker update(s):\n- a finished"); err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Type    string `json:"type"`
		UUID    string `json:"uuid"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	line := <-got
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatalf("line %q: %v", line, err)
	}
	if msg.Type != "user" || msg.Message.Role != "user" || msg.Message.Content != "[dev boss inbox] 1 worker update(s):\n- a finished" || len(msg.UUID) != 36 {
		t.Errorf("sent %q", line)
	}
}

func TestSendWithoutListener(t *testing.T) {
	if err := Send(filepath.Join(t.TempDir(), "gone.sock"), "x"); err == nil {
		t.Error("send to a missing socket succeeded")
	}
}

// A sent batch stays in flight, holding further sends, until the coordinator's prompt shows it; one that
// goes unshown flightTimeout into a rest comes back ahead of newer notes, for the paste, also after a failed paste.
func TestSendFlight(t *testing.T) {
	b := testBox(t)
	now := time.Unix(1000, 0)
	_, _ = b.Add(note("a", "1"), now)
	bt, paste, _ := b.TakeForSend(now)
	if len(bt.Notes) != 1 || paste {
		t.Fatalf("first send took %d, paste %v", len(bt.Notes), paste)
	}
	_, _ = b.Add(note("b", "2"), now)
	if bt, paste, _ := b.TakeForSend(now); len(bt.Notes) != 0 || paste {
		t.Errorf("sent %d while a batch is in flight, paste %v", len(bt.Notes), paste)
	}
	if pending, _ := b.Pending(); !pending {
		t.Error("in flight isn't pending")
	}
	if err := b.Arrived("some other prompt", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := b.TakeForSend(now); len(got.Notes) != 0 {
		t.Error("another prompt cleared the batch in flight")
	}
	if err := b.Arrived(bt.Message(), time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _, _ := b.TakeForSend(now)
	if got.String() != "[dev boss inbox] 1 worker update(s):\n- b finished: 2" {
		t.Fatalf("after arrival: %q", got)
	}

	_, _ = b.Add(note("c", "3"), now)
	if expired, _ := b.Expire(now.Add(time.Hour), time.Time{}); expired {
		t.Error("expired while the coordinator works")
	}
	// It worked on past the timeout: only its rest counts
	rest := now.Add(time.Hour)
	if expired, _ := b.Expire(rest.Add(flightTimeout-time.Second), rest); expired {
		t.Error("expired before the timeout into its rest")
	}
	if expired, _ := b.Expire(now.Add(flightTimeout-time.Second), now.Add(-time.Hour)); expired {
		t.Error("expired before the timeout after the send")
	}
	if expired, _ := b.Expire(rest.Add(flightTimeout), rest); !expired {
		t.Fatal("not expired after the timeout")
	}
	// Claude drops a repeated message: the lost batch goes by paste
	if got, paste, _ := b.TakeForSend(now); len(got.Notes) != 0 || !paste {
		t.Errorf("after a lost batch: sent %d, paste %v", len(got.Notes), paste)
	}
	got, _ = b.TakeForPaste(now)
	if got.String() != "[dev boss inbox] 2 worker update(s):\n- b finished: 2\n- c finished: 3" {
		t.Errorf("pasted %q", got)
	}
	// The user started typing: the paste waits, and the batch still goes by paste
	if err := b.Restore(got); err != nil {
		t.Fatal(err)
	}
	if got, paste, _ := b.TakeForSend(now); len(got.Notes) != 0 || !paste {
		t.Errorf("after a failed paste: sent %d, paste %v", len(got.Notes), paste)
	}
	_, _ = b.TakeForPaste(now.Add(pasteGap))
	_, _ = b.Add(note("d", "4"), now)
	if got, paste, _ := b.TakeForSend(now); len(got.Notes) != 1 || paste {
		t.Errorf("after the paste: sent %d, paste %v", len(got.Notes), paste)
	}
}

func TestUnsend(t *testing.T) {
	b := testBox(t)
	now := time.Unix(1000, 0)
	_, _ = b.Add(note("a", "1"), now)
	_, _, _ = b.TakeForSend(now)
	_, _ = b.Add(note("b", "2"), now)
	if err := b.Unsend(); err != nil {
		t.Fatal(err)
	}
	got, paste, _ := b.TakeForSend(now)
	if got.String() != "[dev boss inbox] 2 worker update(s):\n- a finished: 1\n- b finished: 2" || paste {
		t.Errorf("after unsend: %q, paste %v", got, paste)
	}
}

// The user's AskUserQuestion pick is their latest prompt, claimed by one relayed answer only; a prompt since isn't
// claimed, nor is a typed one.
func TestPickedClaim(t *testing.T) {
	b := testBox(t)
	at := time.Unix(1700000000, 0)
	if err := b.Picked([]agent.Pick{{Question: "boss-a #1: Merge #148?", Answer: "Merge"}}, at); err != nil {
		t.Fatal(err)
	}
	pick, err := b.UserPrompt()
	want := Prompt{Text: "boss-a #1: Merge #148? picked Merge", At: at.Unix(), Pick: true, Question: "boss-a #1: Merge #148?", Answer: "Merge"}
	if err != nil || pick == nil || !reflect.DeepEqual(*pick, want) {
		t.Fatalf("UserPrompt = %+v, %v; want %+v", pick, err, want)
	}
	if err := b.Claim(*pick); err != nil {
		t.Fatal(err)
	}
	if err := b.Claim(*pick); err == nil || !strings.Contains(err.Error(), "pick was used already") {
		t.Errorf("second Claim: %v, want refused", err)
	}
	if err := b.Arrived("merge it", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := b.Claim(*pick); err == nil || !strings.Contains(err.Error(), "said something since") {
		t.Errorf("Claim after a prompt: %v, want refused", err)
	}
	// Several questions in one pick: the words, no single question
	if err := b.Picked([]agent.Pick{{Question: "A?", Answer: "x"}, {Question: "B?", Answer: "y"}}, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if p, err := b.UserPrompt(); err != nil || p == nil || p.Text != "A? picked x; B? picked y" || p.Question != "" {
		t.Errorf("two picks: %+v, %v", p, err)
	}
}

// A pick of several questions is claimed per question, once each, in both places it is kept; a hand-on question
// among them marks the pick.
func TestPickedSeveral(t *testing.T) {
	b := testBox(t)
	at := time.Unix(1700000000, 0)
	picks := []agent.Pick{{Question: "a #1: A?", Answer: "x"}, {Question: "a #2: B?"}, {Question: "a #3: C?\n\n- its context", Answer: "Other words", Labels: []string{"z"}}}
	if err := b.Picked(picks, at); err != nil {
		t.Fatal(err)
	}
	p, err := b.LatestPick()
	if err != nil || p == nil || len(p.Asks) != 3 || p.HandOnMixed || !p.Asks[2].Typed {
		t.Fatalf("LatestPick = %+v, %v", p, err)
	}
	if got, want := p.Ask(2), (Prompt{Text: "a #3: C? picked Other words", At: at.Unix(), Pick: true, Question: "a #3: C?\n\n- its context", Answer: "Other words", Typed: true}); !reflect.DeepEqual(got, want) {
		t.Errorf("Ask(2) = %+v, want %+v", got, want)
	}
	if err := b.ClaimPick(*p, 0); err != nil {
		t.Fatal(err)
	}
	if err := b.ClaimPick(*p, 0); err == nil || !strings.Contains(err.Error(), "pick on this question was used already") {
		t.Errorf("second ClaimPick of question 1: %v, want refused", err)
	}
	if err := b.ClaimPick(*p, 2); err != nil {
		t.Errorf("ClaimPick of question 3 after question 1: %v", err)
	}
	if err := b.Unclaim(*p, 2); err != nil {
		t.Fatal(err)
	}
	if err := b.ClaimPick(*p, 2); err != nil {
		t.Errorf("ClaimPick of question 3 after Unclaim: %v", err)
	}
	// A typed message since leaves the pick's other questions to relay; a question it hasn't, or the whole pick, is refused
	if err := b.Arrived("and tag it", at.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	for i, want := range map[int]string{-1: "was of several questions", 3: "has no question 4"} {
		if err := b.ClaimPick(*p, i); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ClaimPick(%d) of a pick of 3: %v, want %q", i, err, want)
		}
	}
	if err := b.ClaimPick(*p, 1); err != nil {
		t.Errorf("ClaimPick of question 2 after a typed message: %v", err)
	}
	if err := b.Unclaim(*p, 1); err != nil {
		t.Fatal(err)
	}
	// Another dialog of the same second, words and size, differing in a question left unanswered, is not this one
	other := *p
	other.Asks = slices.Clone(p.Asks)
	other.Asks[1].Question = "a #9: Z?"
	if err := b.ClaimPick(other, 1); err == nil || !strings.Contains(err.Error(), "picked something since") {
		t.Errorf("ClaimPick of a look-alike dialog: %v, want refused", err)
	}
	if err := b.Picked(picks, at); err != nil {
		t.Fatal(err)
	}
	if err := b.ClaimPick(*p, 0); err != nil {
		t.Fatal(err)
	}
	if err := b.ClaimPick(*p, 2); err != nil {
		t.Fatal(err)
	}
	// The same spend shows in the latest prompt the hand-on gate reads
	if q, err := b.UserPrompt(); err != nil || q == nil || !q.Asks[0].Spent || q.Asks[1].Spent || !q.Asks[2].Spent || q.Spent {
		t.Errorf("UserPrompt after the claims = %+v, %v", q, err)
	}
	// A new dialog's pick refuses the older one's questions
	if err := b.Picked([]agent.Pick{{Question: "Hand on to boss-a?", Answer: HandItOn, Labels: []string{HandItOn, DropIt}}, {Question: "a #4: D?", Answer: "y"}}, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := b.ClaimPick(*p, 1); err == nil || !strings.Contains(err.Error(), "picked something since") {
		t.Errorf("ClaimPick of an older dialog's question: %v, want refused", err)
	}
	if q, err := b.LatestPick(); err != nil || q == nil || !q.HandOnMixed {
		t.Errorf("a hand-on question beside an item: %+v, %v; want HandOnMixed", q, err)
	}
	// A hand-on question is known by its first line, whatever its options
	if err := b.Picked([]agent.Pick{{Question: "Hand on to boss-a?\n\nCache.", Answer: "Proceed", Labels: []string{"Proceed", DropIt}}, {Question: "a #4: D?", Answer: "y"}}, at.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	if q, err := b.LatestPick(); err != nil || q == nil || !q.HandOnMixed {
		t.Errorf("a hand-on question offering Proceed beside an item: %+v, %v; want HandOnMixed", q, err)
	}
	// So is one worded otherwise that offers Hand it on, when it isn't an item's ask
	if err := b.Picked([]agent.Pick{{Question: "Send boss-api the brief?", Answer: HandItOn, Labels: []string{HandItOn, DropIt}}, {Question: "a #4: D?", Answer: "y"}}, at.Add(100*time.Second)); err != nil {
		t.Fatal(err)
	}
	if q, err := b.LatestPick(); err != nil || q == nil || !q.HandOnMixed {
		t.Errorf("a hand-on question worded otherwise beside an item: %+v, %v; want HandOnMixed", q, err)
	}
	// An item's ask offering the label counts too: the safe side
	if err := b.Picked([]agent.Pick{{Question: "a #5: Ship X?", Answer: "Keep it", Labels: []string{HandItOn, "Keep it"}}, {Question: "a #4: D?", Answer: "y"}}, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if q, err := b.LatestPick(); err != nil || q == nil || !q.HandOnMixed {
		t.Errorf("an item offering %q beside another: %+v, %v; want HandOnMixed", HandItOn, q, err)
	}
}

// The user's latest pick outlives the typed prompts after it, for ClaimPick only (Claim still wants the latest
// words); one pick is spent by either claim, and a new conversation forgets it.
func TestLatestPick(t *testing.T) {
	at := time.Unix(1700000000, 0)
	picked := func(b Box) Prompt {
		t.Helper()
		if err := b.Picked([]agent.Pick{{Question: "boss-a #1: Merge #148?", Answer: "Merge"}}, at); err != nil {
			t.Fatal(err)
		}
		if err := b.Arrived("yes, and tell the API team", at.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		p, err := b.LatestPick()
		if err != nil || p == nil || p.Answer != "Merge" || p.Spent {
			t.Fatalf("LatestPick after a typed prompt = %+v, %v", p, err)
		}
		return *p
	}

	b := testBox(t)
	p := picked(b)
	if err := b.Claim(p); err == nil || !strings.Contains(err.Error(), "said something since") {
		t.Errorf("Claim after a typed prompt: %v, want refused", err)
	}
	if err := b.ClaimPick(p, -1); err != nil {
		t.Fatalf("ClaimPick after a typed prompt: %v", err)
	}
	if err := b.Arrived("and rebase", at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := b.ClaimPick(p, -1); err == nil || !strings.Contains(err.Error(), "pick was used already") {
		t.Errorf("second ClaimPick: %v, want refused", err)
	}
	if err := b.Unclaim(p, -1); err != nil {
		t.Fatal(err)
	}
	if err := b.ClaimPick(p, -1); err != nil {
		t.Errorf("ClaimPick after Unclaim: %v", err)
	}

	// Spent by Claim while it was the latest words: ClaimPick refuses it after a typed prompt
	b = testBox(t)
	if err := b.Picked([]agent.Pick{{Question: "boss-a #1: Merge #148?", Answer: "Merge"}}, at); err != nil {
		t.Fatal(err)
	}
	latest, _ := b.UserPrompt()
	if err := b.Claim(*latest); err != nil {
		t.Fatal(err)
	}
	if err := b.Arrived("ok", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := b.ClaimPick(*latest, -1); err == nil || !strings.Contains(err.Error(), "pick was used already") {
		t.Errorf("ClaimPick of a pick Claim spent: %v, want refused", err)
	}

	// A pick since refuses the older one; ForgetPrompt drops the pick too
	b = testBox(t)
	p = picked(b)
	if err := b.Picked([]agent.Pick{{Question: "boss-a #2: Tag?", Answer: "Tag"}}, at.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := b.ClaimPick(p, -1); err == nil || !strings.Contains(err.Error(), "picked something since") {
		t.Errorf("ClaimPick of an older pick: %v, want refused", err)
	}
	if err := b.ForgetPrompt(); err != nil {
		t.Fatal(err)
	}
	if p, err := b.LatestPick(); p != nil || err != nil {
		t.Errorf("LatestPick after ForgetPrompt = %+v, %v", p, err)
	}

	// A hand-on question drops the latest prompt, not an item's pick waiting for its relay
	b = testBox(t)
	p = picked(b)
	if err := b.DropPrompt(); err != nil {
		t.Fatal(err)
	}
	if latest, err := b.UserPrompt(); latest != nil || err != nil || b.ClaimPick(p, -1) != nil {
		t.Errorf("after DropPrompt: latest %+v, %v; want none, the pick still claimable", latest, err)
	}
	// ... and drops a pick on an earlier hand-on question
	if err := b.Picked([]agent.Pick{{Question: "\n  Hand  on to boss-a?\n\nCache the CI builds.", Answer: "Hand it on"}}, at); err != nil {
		t.Fatal(err)
	}
	if err := b.DropPrompt(); err != nil {
		t.Fatal(err)
	}
	if p, err := b.LatestPick(); p != nil || err != nil {
		t.Errorf("hand-on pick after DropPrompt = %+v, %v; want none", p, err)
	}
	// ... to a session that is no boss too
	if err := b.Picked([]agent.Pick{{Question: "Hand on to plain?\n\nCache the CI builds.", Answer: "Hand it on"}}, at); err != nil {
		t.Fatal(err)
	}
	if err := b.DropPrompt(); err != nil {
		t.Fatal(err)
	}
	if p, err := b.LatestPick(); p != nil || err != nil {
		t.Errorf("hand-on pick to a non-boss after DropPrompt = %+v, %v; want none", p, err)
	}

	// A file from before the pick slot: its latest prompt, a pick, is claimed as the latest pick
	b = testBox(t)
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.path, []byte(`{"notes":[],"last":{},"prompt":{"text":"x","at":1700000000,"pick":true,"question":"boss-a #1: Merge?","answer":"Merge"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := b.LatestPick(); err != nil || p == nil || b.ClaimPick(*p, -1) != nil {
		t.Errorf("LatestPick of an older file = %+v, %v; want it claimable", p, err)
	}
}

// A pairing code is shown once per prompt of the user's: given back when not shown, refused after a prompt since;
// a new prompt may pair again. Pairing leaves the prompt's pick unspent.
func TestClaimPair(t *testing.T) {
	b := testBox(t)
	at := time.Unix(1700000000, 0)
	if err := b.Arrived("pair my phone", at); err != nil {
		t.Fatal(err)
	}
	p, err := b.UserPrompt()
	if err != nil || p == nil {
		t.Fatalf("UserPrompt = %+v, %v", p, err)
	}
	if err := b.ClaimPair(*p); err != nil {
		t.Fatal(err)
	}
	if err := b.ClaimPair(*p); err == nil || !strings.Contains(err.Error(), "paired a phone already") {
		t.Errorf("second ClaimPair: %v, want refused", err)
	}
	if err := b.Claim(*p); err != nil {
		t.Errorf("Claim after ClaimPair: %v", err)
	}
	if err := b.UnclaimPair(*p); err != nil {
		t.Fatal(err)
	}
	if err := b.ClaimPair(*p); err != nil {
		t.Errorf("ClaimPair after UnclaimPair: %v", err)
	}
	if err := b.Arrived("pair it again", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := b.ClaimPair(*p); err == nil || !strings.Contains(err.Error(), "said something since") {
		t.Errorf("ClaimPair of the old prompt: %v, want refused", err)
	}
	if p, err = b.UserPrompt(); err != nil || p == nil || p.Paired {
		t.Fatalf("new prompt: %+v, %v", p, err)
	}
	if err := b.ClaimPair(*p); err != nil {
		t.Errorf("ClaimPair of the new prompt: %v", err)
	}
}

// The user's latest prompt is kept for an answer the manager relays; a batch or ResumePrompt dev sent isn't one.
func TestArrivedUserPrompt(t *testing.T) {
	b := testBox(t)
	if p, err := b.UserPrompt(); p != nil || err != nil {
		t.Fatalf("before any prompt: %+v, %v", p, err)
	}
	at := time.Unix(1700000000, 0)
	for i, prompt := range []string{"merge 148", Header + " 1 worker update(s):\n- boss-a waits on you: #1 Merge?", ResumePrompt, "  "} {
		if err := b.Arrived(prompt, at.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if p, err := b.UserPrompt(); err != nil || p == nil || !reflect.DeepEqual(*p, Prompt{Text: "merge 148", At: at.Unix()}) {
		t.Errorf("UserPrompt = %+v, %v; want merge 148 at %d", p, err, at.Unix())
	}
	// The user quoting a batch is still their prompt
	quote := "for " + Header + " #1 Merge?: merge"
	if err := b.Arrived(quote, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if p, err := b.UserPrompt(); err != nil || p == nil || p.Text != quote {
		t.Errorf("UserPrompt = %+v, %v; want %q", p, err, quote)
	}
}

// A relayed answer quotes the user's words, cleaned, and stays a "you answered" note for its item.
func TestAnswerVia(t *testing.T) {
	n := AnswerVia(4, "Merge #148?", "Merge", "merge 148,\n  wait on the API one")
	if got := n.String(); got != `you answered #4 "Merge #148?" via manager ("merge 148, wait on the API one"): Merge` || n.Key != Answer(4, "", "").Key {
		t.Errorf("AnswerVia = %q, key %q", got, n.Key)
	}
}

// An answer from the phone page says so, and stays a "you answered" note for its item.
func TestAnswerPage(t *testing.T) {
	n := AnswerPage(4, "Merge #148?", "Merge")
	if got := n.String(); got != `you answered #4 "Merge #148?" on the phone page: Merge` || n.Key != Answer(4, "", "").Key {
		t.Errorf("AnswerPage = %q, key %q", got, n.Key)
	}
}

// A long prompt keeps both ends, where an answer may be; a new conversation forgets it.
func TestPromptClipForget(t *testing.T) {
	long := "merge 148 " + strings.Repeat("x ", 300) + "wait on the API one"
	if got := clip(long); len([]rune(got)) > maxText || !strings.HasPrefix(got, "merge 148") || !strings.HasSuffix(got, "wait on the API one") {
		t.Errorf("clip = %q", got)
	}
	b := testBox(t)
	if err := b.Arrived("merge 148", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := b.ForgetPrompt(); err != nil {
		t.Fatal(err)
	}
	if p, err := b.UserPrompt(); p != nil || err != nil {
		t.Errorf("after ForgetPrompt: %+v, %v", p, err)
	}
}
