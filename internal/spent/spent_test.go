package spent

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

const socket = "/tmp/tmux-1/default"

// fake is a server with a boss b ($1) that started worker w ($2) by name, pane %2 showing paneRow.
func fake(paneRow string) *tmux.Client {
	return tmux.NewFake(func(args []string) (string, error) {
		switch args[0] {
		case "display-message":
			return paneRow, nil
		case "list-sessions":
			return tmux.FormatRow("$1", "b", "100", "1") + tmux.FormatRow("$2", "w", "200", ""), nil
		}
		return "", nil
	})
}

func load(t *testing.T, id, created string) Ledger {
	t.Helper()
	ls, errs, err := Read(socket, map[string]string{id: created})
	if err != nil || errs[id] != nil {
		t.Fatal(err, errs[id])
	}
	return ls[id]
}

// A worker's ended conversation goes to its starter's ledger, a boss's own to its own; nothing without a cost.
func TestEnded(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := Ended(fake(tmux.FormatRow(socket, "$2", "200", "", "b", "1.5", "c-old")), "%2", "c1"); err != nil {
		t.Fatal(err)
	}
	if got := load(t, "$1", "100"); !maps.Equal(got, Ledger{"c1": 1.5}) {
		t.Errorf("starter's ledger = %v", got)
	}
	if err := Ended(fake(tmux.FormatRow(socket, "$1", "100", "1", "", "2", "cb")), "%1", ""); err != nil {
		t.Fatal(err)
	}
	if err := Ended(fake(tmux.FormatRow(socket, "$2", "200", "", "b", "", "c2")), "%2", "c2"); err != nil {
		t.Fatal(err)
	}
	if got := load(t, "$1", "100"); !maps.Equal(got, Ledger{"c1": 1.5, "cb": 2}) {
		t.Errorf("boss's ledger = %v, want its own conversation added, none without a cost", got)
	}
	// A worker started by a worker, by session id
	if err := Ended(fake(tmux.FormatRow(socket, "$3", "300", "", "$2", "0.75", "c3")), "%3", "c3"); err != nil {
		t.Fatal(err)
	}
	if got := load(t, "$2", "200"); !maps.Equal(got, Ledger{"c3": 0.75}) {
		t.Errorf("starter by id's ledger = %v", got)
	}
	// Its starter gone: said, not dropped silently
	if err := Ended(fake(tmux.FormatRow(socket, "$4", "400", "", "$9", "4", "c4")), "%4", "c4"); err == nil || !strings.Contains(err.Error(), "($9) is gone") {
		t.Errorf("starter gone: %v", err)
	}
	// Started by no one: nothing to keep
	if err := Ended(fake(tmux.FormatRow(socket, "$4", "400", "", "", "4", "c4")), "%4", "c4"); err != nil {
		t.Errorf("no starter: %v", err)
	}
}

// A closing session's live conversations and its own ledger move to its starter's; a resumed conversation keeps
// its higher cost.
func TestClosing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c := fake("")
	p, err := path(socket, "$2", "200")
	if err != nil {
		t.Fatal(err)
	}
	if err := add(p, Ledger{"old": 3}); err != nil {
		t.Fatal(err)
	}
	if err := add(p, Ledger{"old": 2}); err != nil {
		t.Fatal(err)
	}
	if err := Closing(c, socket, "$2", "200", "b", Ledger{"live": 5}); err != nil {
		t.Fatal(err)
	}
	if got := load(t, "$1", "100"); !maps.Equal(got, Ledger{"old": 3, "live": 5}) {
		t.Errorf("starter's ledger = %v", got)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("closed session's ledger stays: %v", err)
	}
	// Nothing spent: no file
	if err := Closing(c, socket, "$5", "500", "b", nil); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(p), "*500*")); len(left) != 0 {
		t.Errorf("nothing spent left %v", left)
	}
	// No starter left: its ledger goes, written nowhere
	if err := add(p, Ledger{"orphan": 1}); err != nil {
		t.Fatal(err)
	}
	if err := Closing(c, socket, "$2", "200", "gone", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("ledger without a starter stays: %v", err)
	}
	if got := load(t, "$1", "100"); got["orphan"] != 0 {
		t.Errorf("starter's ledger = %v", got)
	}
}

// A close that can't reach its starter's ledger keeps its own: a retry moves it all.
func TestClosingFailedKeeps(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	p, err := path(socket, "$2", "200")
	if err != nil {
		t.Fatal(err)
	}
	if err := add(p, Ledger{"old": 3}); err != nil {
		t.Fatal(err)
	}
	down := tmux.NewFake(func([]string) (string, error) { return "", errors.New("server down") })
	if err := Closing(down, socket, "$2", "200", "b", Ledger{"live": 5}); err == nil {
		t.Fatal("Closing with tmux down: no error")
	}
	if got := load(t, "$2", "200"); !maps.Equal(got, Ledger{"old": 3}) {
		t.Errorf("own ledger after a failed close = %v", got)
	}
	if err := Closing(fake(""), socket, "$2", "200", "b", Ledger{"live": 5}); err != nil {
		t.Fatal(err)
	}
	if got := load(t, "$1", "100"); !maps.Equal(got, Ledger{"old": 3, "live": 5}) {
		t.Errorf("starter's ledger after the retry = %v", got)
	}
}

// Writers at once: none loses another's conversation.
func TestAddConcurrent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	p, err := path(socket, "$1", "100")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Go(func() { errs <- add(p, Ledger{fmt.Sprint(i): 1}) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := load(t, "$1", "100"); len(got) != 20 {
		t.Errorf("ledger has %d conversations, want 20", len(got))
	}
}

// Read gives each session's ledger, none for one without, and an unreadable one's error naming its file.
func TestRead(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	p, err := path(socket, "$1", "100")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	ls, errs, err := Read(socket, map[string]string{"$1": "100", "$2": "200"})
	if err != nil {
		t.Fatal(err)
	}
	if errs["$1"] == nil || !strings.Contains(errs["$1"].Error(), p) || errs["$2"] != nil || ls["$2"] != nil {
		t.Errorf("Read = %v, %v; want $1's error naming %s, nothing for $2", ls, errs, p)
	}
}

func TestClosedLeavesLive(t *testing.T) {
	if got := (Ledger{"a": 1, "b": 2}).Closed(map[string]bool{"b": true}); got != 1 {
		t.Errorf("Closed = %v, want 1", got)
	}
	if got := Ledger(nil).Closed(nil); got != 0 {
		t.Errorf("nil Closed = %v", got)
	}
}

// A close removes the ledgers and lock files of sessions gone, once a minute old; a live session's and a new one
// stay, and another socket's are not its own.
func TestClosingPrunes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ledger := func(socket, id, created string, age time.Duration) string {
		t.Helper()
		p, err := path(socket, id, created)
		if err != nil {
			t.Fatal(err)
		}
		if err := add(p, Ledger{"c": 1}); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		for _, f := range []string{p, p + ".lock"} {
			if err := os.Chtimes(f, old, old); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	gone, fresh := ledger(socket, "$7", "700", time.Hour), ledger(socket, "$8", "800", 0)
	other, boss := ledger(socket+"-2", "$7", "700", time.Hour), ledger(socket, "$1", "100", time.Hour)
	if err := Closing(fake(""), socket, "$2", "200", "b", Ledger{"live": 5}); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{gone: false, gone + ".lock": false, fresh: true, other: true, boss: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s there = %v, want %v", filepath.Base(p), err == nil, want)
		}
	}
}
