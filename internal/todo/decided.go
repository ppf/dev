package todo

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"
)

// Who decided an ask for the user (Decided.By).
const (
	DecidedManager = "manager" // the manager settled a boss's ask (Settle)
	DecidedBoss    = "boss"    // the list's boss settled it itself (Decide)
)

// NotObvious is a replace's Obvious that removes the mark (dev todo ask --replaces <n> --obvious -).
const NotObvious = "-"

// Decided is an ask settled for the user with its recommended option: by whom (DecidedManager or DecidedBoss), the
// option picked and the others it was picked over (as stored), why it was obvious or easy, and when, unix seconds.
type Decided struct {
	By   string   `json:"by"`
	Pick string   `json:"pick"`
	Over []string `json:"over,omitempty"`
	Why  string   `json:"why"`
	At   int64    `json:"at"`
}

// OverLabels is the labels of the options d was picked over.
func (d Decided) OverLabels() []string {
	labels := make([]string, len(d.Over))
	for i, o := range d.Over {
		labels[i] = Split(o).Label
	}
	return labels
}

// CheckObvious refuses a why over MaxResult characters, or options without exactly one "(Recommended)": it is what
// gets picked. An empty why (not marked) passes.
func CheckObvious(why string, options []string) error {
	if why == "" {
		return nil
	}
	if n := utf8.RuneCountInString(why); n > MaxResult {
		return fmt.Errorf("the why of --obvious or --decided is %d characters, at most %d: one line", n, MaxResult)
	}
	if n := recommended(options); n != 1 {
		return fmt.Errorf("an ask marked --obvious or --decided needs exactly one option marked \"(Recommended)\", it has %d", n)
	}
	return nil
}

// recommended counts the options marked "(Recommended)".
func recommended(options []string) int {
	n := 0
	for _, o := range options {
		if Split(o).Recommended {
			n++
		}
	}
	return n
}

// decide ticks ask it with its one recommended option, decided for the user by by, ticked by doneBy.
func (it *Item) decide(by, doneBy, why string, at int64) error {
	if recommended(it.Options) != 1 {
		return fmt.Errorf("item %d has no one recommended option: the user answers it", it.ID)
	}
	i := slices.IndexFunc(it.Options, func(o string) bool { return Split(o).Recommended })
	d := &Decided{By: by, Pick: it.Options[i], Why: why, At: at}
	for j, o := range it.Options {
		if j != i {
			d.Over = append(d.Over, o)
		}
	}
	it.tick(at, doneBy)
	// Its why moves into Decided: a decided ask is no longer marked
	it.Answer, it.Decided, it.Obvious = answerOf(it.Options, it.Options[i]), d, ""
	return nil
}

// Settle ticks the user's own open ask id with its recommended option, decided for the user by the manager for its
// boss's obvious mark (Item.Obvious) or, unmarked, for why, and returns it. pick names the option the manager checked:
// refused unless it is still the recommended one. It is also refused, changing nothing, for an item that isn't such an
// ask, has no one recommended option or that handOn reports, an unmarked one with one option or without why, a marked
// one with why, one reading other than question (numbers restart), or one answered already (an Answered error).
func (s Store) Settle(id int, question, pick, why string, handOn func(Item) bool, now time.Time) (Item, error) {
	if question == "" {
		return Item{}, errors.New("settling an ask needs its text")
	}
	var it Item
	var unread error
	// answered is the refusal for id once it left the list or its number went to a new item
	answered := func(f *file, other error) error {
		was, err := s.answeredItem(f, id, question, now)
		switch {
		case err != nil:
			unread = err
			return err
		case was != nil:
			return Answered{Item: *was}
		}
		return other
	}
	err := s.update(now, refusing(func(f *file) error {
		i := slices.IndexFunc(f.Items, func(it Item) bool { return it.ID == id })
		if i < 0 {
			return answered(f, fmt.Errorf("no open item %d", id))
		}
		p := &f.Items[i]
		switch {
		case p.Text != question:
			return answered(f, fmt.Errorf("item %d is %q now, not %q: look at the list again", id, p.Text, question))
		case p.Done != 0 && p.Answer != "":
			return Answered{Item: *p}
		case p.Done != 0:
			return fmt.Errorf("no open item %d", id)
		case len(p.Options) == 0:
			return fmt.Errorf("item %d is no question (dev todo ask)", id)
		case !Own(p.On, p.Session, s.boss):
			return fmt.Errorf("item %d is on %s, not the user's to answer", id, p.On)
		case handOn(*p):
			return fmt.Errorf("item %d is a hand-on question: the user answers it", id)
		case recommended(p.Options) != 1:
			return fmt.Errorf("item %d has no one recommended option: the user answers it", id)
		case p.Obvious != "" && why != "":
			return fmt.Errorf("item %d is marked obvious by its boss (%s): leave out --why", id, p.Obvious)
		case p.Obvious != "":
		case len(p.Options) < 2:
			return fmt.Errorf("item %d has one option, none to overrule it with: the user answers it", id)
		case why == "":
			return fmt.Errorf("item %d isn't marked obvious by its boss (dev todo ask --obvious): settle it with --why '<one line>' only when it is cheap, reversible and inside what the user agreed, else the user answers it", id)
		case utf8.RuneCountInString(why) > MaxResult:
			return fmt.Errorf("--why is %d characters, at most %d: one line", utf8.RuneCountInString(why), MaxResult)
		}
		want, err := Pick(p.Options, pick)
		if err != nil {
			return err
		}
		if i := slices.IndexFunc(p.Options, func(o string) bool { return Split(o).Recommended }); i >= 0 && answerOf(p.Options, p.Options[i]) != want {
			return fmt.Errorf("item %d's recommended option is %q now, not %q: look at the list again", id, answerOf(p.Options, p.Options[i]), want)
		}
		if err := p.decide(DecidedManager, DecidedManager, cmp.Or(p.Obvious, why), now.Unix()); err != nil {
			return err
		}
		p.Via = ViaManager
		it = *p
		return nil
	}))
	if unread != nil {
		// A failed read of the finished items is no refusal
		return it, unread
	}
	return it, err
}

// Decide puts ask it on the list ticked at once with its one recommended option, decided by the list's boss itself
// for why (an easy decision), and returns its number: it shows under Done, for the user to overrule.
func (s Store) Decide(it Item, why string, now time.Time) (int, error) {
	if why == "" {
		return 0, errors.New("deciding an ask needs why it is easy")
	}
	if err := CheckObvious(why, it.Options); err != nil {
		return 0, err
	}
	if err := checkAsk(it); err != nil {
		return 0, err
	}
	var id int
	err := s.update(now, func(f *file) error {
		if len(f.Items) == 0 {
			f.Next = 1
		}
		it.ID, it.Added, it.On, it.Session, it.For, it.Obvious = max(f.Next, 1), now.Unix(), "", nil, 0, ""
		if err := it.decide(DecidedBoss, cmp.Or(s.boss, Boss), why, now.Unix()); err != nil {
			return err
		}
		f.Items = append(f.Items, it)
		f.Next, id = it.ID+1, it.ID
		return nil
	})
	return id, err
}
