package todo

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ppf/dev/internal/wait"
)

// A boss keeps at most MaxNotes notes of at most MaxNote characters each (text and why).
const (
	MaxNotes = 12
	MaxNote  = 200
)

// A note's kind: a choice with its why, what the user said that changes the work (it needs an end), an open risk, the
// boss's standing state (one per list: a new one replaces it).
const (
	NoteDecided = "decided"
	NoteSaid    = "said"
	NoteRisk    = "risk"
	NoteState   = "state"
)

// Note is a dated line a boss keeps beside its list. Until, when set, drops it at that time, unix seconds; Item ties
// it to list item n, ItemDone once that item is ticked or its list replaced (item numbers restart, so it stays set).
type Note struct {
	ID       int    `json:"id"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Why      string `json:"why,omitempty"`
	Until    int64  `json:"until,omitempty"`
	Item     int    `json:"item,omitempty"`
	ItemDone bool   `json:"item_done,omitempty"`
	Added    int64  `json:"added"`
}

// Check refuses a note of an unknown kind, no text, a text or why over MaxNote characters, or a said note without an end.
func (n Note) Check() error {
	switch n.Kind {
	case NoteDecided, NoteSaid, NoteRisk, NoteState:
	default:
		return fmt.Errorf("note kind %q: want %s, %s, %s or %s", n.Kind, NoteDecided, NoteSaid, NoteRisk, NoteState)
	}
	if n.Text == "" {
		return fmt.Errorf("empty %s note", n.Kind)
	}
	for _, f := range [...]struct{ name, v string }{{"text", n.Text}, {"why", n.Why}} {
		if c := utf8.RuneCountInString(f.v); c > MaxNote {
			return fmt.Errorf("note %s is %d characters, at most %d: put the details in a file and end the line with its path", f.name, c, MaxNote)
		}
	}
	if n.Kind == NoteSaid && n.Until == 0 && n.Item == 0 {
		return fmt.Errorf("a %s note needs an end: --until or --item", NoteSaid)
	}
	return nil
}

// AddNote stores n and returns its id (1, 2…, shown as n1), never one used before; a state note replaces the list's
// earlier one. Over MaxNotes, an over-long text or why, a said note without Until or Item, or an Item that isn't an
// open item is an error. The list's Updated stays.
func (s Store) AddNote(n Note, now time.Time) (id int, err error) {
	if err := n.Check(); err != nil {
		return 0, err
	}
	err = s.locked(func() error {
		f, err := s.read()
		if err != nil {
			return err
		}
		if err := s.refuseMoved(f); err != nil {
			return err
		}
		f.prune(now)
		n.ID, n.Added = max(f.NextNote, 1), now.Unix()
		// A file an earlier dev wrote has no NextNote: after its highest
		for _, o := range f.Notes {
			n.ID = max(n.ID, o.ID+1)
		}
		notes := f.Notes
		if n.Kind == NoteState {
			notes = slices.DeleteFunc(slices.Clone(notes), func(o Note) bool { return o.Kind == NoteState })
		}
		switch {
		case len(notes) >= MaxNotes:
			return fmt.Errorf("%d notes already, at most %d: drop one first (dev todo note drop <n>)", len(notes), MaxNotes)
		case n.Item != 0 && !f.hasOpen(n.Item):
			return fmt.Errorf("no open item %d", n.Item)
		}
		f.Notes, f.NextNote = append(notes, n), n.ID+1
		id = n.ID
		return s.write(f)
	})
	return id, err
}

// DropNote removes note id; one that isn't there is an error. The list's Updated stays.
func (s Store) DropNote(id int, now time.Time) error {
	return s.locked(func() error {
		f, err := s.read()
		if err != nil {
			return err
		}
		if err := s.refuseMoved(f); err != nil {
			return err
		}
		f.prune(now)
		i := slices.IndexFunc(f.Notes, func(n Note) bool { return n.ID == id })
		if i < 0 {
			return fmt.Errorf("no note n%d", id)
		}
		f.Notes = slices.Delete(f.Notes, i, i+1)
		return s.write(f)
	})
}

// Notes is the notes at now, oldest first, those whose Until has come dropped.
func (s Store) Notes(now time.Time) ([]Note, error) {
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	f.prune(now)
	return f.Notes, nil
}

// endItems marks every note's item done, as its list is replaced: the numbers restart.
func (f *file) endItems() {
	for i := range f.Notes {
		f.Notes[i].ItemDone = f.Notes[i].Item != 0
	}
}

// settleNotes marks done the notes whose item is no longer open.
func (f *file) settleNotes() {
	for i, n := range f.Notes {
		if n.Item != 0 && !f.hasOpen(n.Item) {
			f.Notes[i].ItemDone = true
		}
	}
}

func (f *file) hasOpen(id int) bool {
	return slices.ContainsFunc(f.Items, func(it Item) bool { return it.ID == id && it.Done == 0 })
}

// staleNote is how long a note stands before it is marked to re-check.
const staleNote = 7 * 24 * time.Hour

// NotesText is the notes as a boss reads them, one per line, with their age and what to check; "" for none.
func NotesText(notes []Note, now time.Time) string {
	if len(notes) == 0 {
		return ""
	}
	age := func(n Note) string {
		d := now.Sub(time.Unix(n.Added, 0))
		if d >= 24*time.Hour {
			return strconv.Itoa(int(d.Hours()/24)) + "d"
		}
		return wait.Span(d)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[dev todo] Your notes (%d of %d, oldest %s):", len(notes), MaxNotes, age(notes[0]))
	for _, n := range notes {
		fmt.Fprintf(&b, "\n n%d %s %s: %s", n.ID, time.Unix(n.Added, 0).Format(time.DateOnly), n.Kind, n.Text)
		if n.Why != "" {
			b.WriteString(" (why: " + n.Why + ")")
		}
		if n.Until != 0 {
			b.WriteString(" (until " + time.Unix(n.Until, 0).Format(UntilLayout) + ")")
		}
		switch {
		case n.ItemDone:
			fmt.Fprintf(&b, " (item #%d done)", n.Item)
		case n.Item != 0:
			fmt.Fprintf(&b, " #%d", n.Item)
		}
		if now.Sub(time.Unix(n.Added, 0)) >= staleNote {
			b.WriteString(" (" + age(n) + ", still true?)")
		}
	}
	b.WriteString("\nCheck each still holds: drop what's over (dev todo note drop <n>).")
	return b.String()
}
