package herd

import (
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// section is the board section a worker's card sits in, in the order they show.
type section int

const (
	needsYouSection section = iota
	workingSection
	waitingSection
	restingSection
)

// section is where t's card goes, the first that holds: needs you while a dialog waits on the user; working
// while its agent works; needs you when its turn ended on a question (an asks note; a needs a decision note
// outlives its dialog, so the dialog itself decides); waiting when the newest marker in its last reply is
// WAITING; else resting.
func (t tile) section() section {
	switch {
	case t.deciding:
		return needsYouSection
	case t.sess.State == agent.Working:
		return workingSection
	case noteKind(t.note) == inbox.Asks:
		return needsYouSection
	}
	if st, _, _ := desktop.Marker(t.tail.Lines); st == desktop.StatusWaiting {
		return waitingSection
	}
	return restingSection
}

// noteKind is the kind of a note as inbox.Note.Summary writes it, "<kind>: <text>" or "<kind>".
func noteKind(note string) string {
	kind, _, _ := strings.Cut(note, ":")
	return kind
}

// liveLookups are sidebar's lookups plus the workers' transcripts, each read again only once it changed.
type liveLookups struct {
	*sidebar.Lookups
	mu    sync.Mutex
	tails map[string]cachedTail
	cis   *ciCache
}

// cachedTail is a transcript's tail as read at its size and modification time.
type cachedTail struct {
	size int64
	mod  time.Time
	tail desktop.Tail
}

func newLookups(c *tmux.Client) *liveLookups {
	return &liveLookups{Lookups: sidebar.NewLookups(c), tails: map[string]cachedTail{}, cis: newCICache()}
}

// CI is the checks of the PR at url as last read for pane, read again at most every ciTTL or once gen moved on.
func (l *liveLookups) CI(url, pane, gen string) ci { return l.cis.get(url, pane, gen) }

// Tail is the end of the transcript at path; none written yet is no error. A failed read isn't kept, so the
// next refresh tries again.
func (l *liveLookups) Tail(path string) (desktop.Tail, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return desktop.Tail{}, nil
	}
	if err != nil {
		return desktop.Tail{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if c, ok := l.tails[path]; ok && c.size == info.Size() && c.mod.Equal(info.ModTime()) {
		return c.tail, nil
	}
	t, err := desktop.ReadTail(path)
	if err != nil {
		return t, err
	}
	l.tails[path] = cachedTail{size: info.Size(), mod: info.ModTime(), tail: t}
	return t, nil
}
