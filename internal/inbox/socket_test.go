package inbox

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if p, err := b.UserPrompt(); err != nil || p == nil || *p != (Prompt{Text: "merge 148", At: at.Unix()}) {
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
