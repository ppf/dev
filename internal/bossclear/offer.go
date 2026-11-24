package bossclear

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

const (
	// IdleLimit is how long a boss rests before dev offers to clear it (Offer).
	IdleLimit = 30 * time.Minute
	// BigContext is the context share (percent) from which a boss is offered a clear after only QuietFor of rest.
	BigContext = 40
	// QuietFor is how long a boss rests, at BigContext or more, before dev offers its clear.
	QuietFor = 2 * time.Minute
	// OptOffered is the coordinator pane option holding the idle stretch (its @agent_since) a clear was last offered for.
	OptOffered = "@dev_clear_offered"
	// OptChecked is the coordinator pane option holding "<idle stretch> <unix time>" of the last offer refused by Check:
	// the full read waits CheckEvery after it
	OptChecked = "@dev_clear_checked"
	// OptOfferedAt is the coordinator pane option holding the unix time of the last offer: a boss's early offer
	// (QuietFor) waits IdleLimit after it, so a "Not now" isn't asked again after the next turn.
	OptOfferedAt = "@dev_clear_offered_at"
	// CheckEvery is how often a stretch Check refused is read again
	CheckEvery = time.Minute
	// OfferQuestion is the ask Offer puts on the manager's own list, for the user.
	OfferQuestion = "Clear the manager's context?"
)

// offerRow is what Offer reads of each pane of the boss session.
type offerRow struct {
	Pane       string `tmux:"pane_id"`
	Role       string `tmux:"@dev_boss_pane"`
	Session    string `tmux:"session_name"`
	State      string `tmux:"@agent_state"`
	StateCmd   string `tmux:"@agent_cmd"`
	Command    string `tmux:"pane_current_command"`
	WaitingFor string `tmux:"@agent_waiting_for"`
	Since      string `tmux:"@agent_since"`
	Context    string `tmux:"@agent_context"`
	Transcript string `tmux:"@agent_transcript"`
	Offered    string `tmux:"@dev_clear_offered"`    // OptOffered
	OfferedAt  string `tmux:"@dev_clear_offered_at"` // OptOfferedAt
	Checked    string `tmux:"@dev_clear_checked"`    // OptChecked
}

// Offer, run by the watch loop of the boss session pane is in, offers once per idle stretch to clear its boss once it
// has rested IdleLimit (at BigContext or more: QuietFor, unless the manager has one of its asks up) and Check passes without --force: a note to the
// manager that started or adopted it, or for the manager itself an ask on its own list, which the user's NEEDS YOU
// shows (they run dev agent clear from a shell).
// None for a boss without a live manager over it or a recorded transcript, or while a clear runs; a stretch is marked
// offered only once told.
func Offer(c *tmux.Client, clk control.Clock, pane string) error {
	if err := offer(c, clk, pane); err != nil {
		return fmt.Errorf("clear offer: %w", err)
	}
	return nil
}

func offer(c *tmux.Client, clk control.Clock, pane string) error {
	rows, err := tmux.Query[offerRow](c, "list-panes", "-s", "-t", pane)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(rows, func(r offerRow) bool { return r.Role == "coordinator" })
	if i < 0 {
		return nil
	}
	r, now := rows[i], clk.Now()
	// Cheap checks first: this runs every watch round. Unset: no hook yet, or no dev statusline
	if r.Since == "" || r.Context == "" || r.Transcript == "" || r.Offered == r.Since {
		return nil
	}
	since, err := strconv.ParseInt(r.Since, 10, 64)
	if err != nil {
		return fmt.Errorf("%s @agent_since: %w", r.Session, err)
	}
	used, err := strconv.Atoi(r.Context)
	if err != nil {
		return fmt.Errorf("%s %s: %w", r.Session, agent.OptContext, err)
	}
	idle := now.Sub(time.Unix(since, 0))
	st := agent.Trusted(r.State, r.StateCmd, r.Command)
	manager := r.Session == sidebar.Manager
	limit, early := IdleLimit, false
	if at, err := strconv.ParseInt(r.OfferedAt, 10, 64); used >= BigContext && (err != nil || now.Sub(time.Unix(at, 0)) >= IdleLimit) {
		limit, early = QuietFor, true
	}
	if idle < limit || used < MinContext || st != agent.Idle && (st != agent.Waiting || agent.Deciding(st, r.WaitingFor)) {
		return nil
	}
	// Read captures every worker's screen: a stretch Check refused waits a while
	if stretch, at, ok := strings.Cut(r.Checked, " "); ok && stretch == r.Since {
		if t, err := strconv.ParseInt(at, 10, 64); err == nil && now.Sub(time.Unix(t, 0)) < CheckEvery {
			return nil
		}
	}
	var m inbox.Boss
	if !manager {
		var ok bool
		if m, ok, err = inbox.ManagerOver(c, r.Session); !ok || err != nil {
			return err
		}
	}
	if held, err := inbox.ClearHeld(c, r.Pane); held || err != nil {
		return err
	}
	// A boss's early offer waits for the manager to finish asking the user one of its asks
	if early && !manager && idle < IdleLimit {
		asking, err := inbox.ManagerAsking(c, now)
		if err != nil || slices.ContainsFunc(asking, func(a inbox.Asking) bool { return a.Boss == r.Session }) {
			return err
		}
	}
	a, err := control.Resolve(c, r.Session)
	if err != nil {
		return err
	}
	b, workers, err := Read(c, a, now)
	if err != nil {
		return err
	}
	if Check(b, workers, false) != nil {
		_, err := c.Run("set-option", "-p", "-t", r.Pane, OptChecked, r.Since+" "+strconv.FormatInt(now.Unix(), 10))
		return err
	}
	text := fmt.Sprintf("idle %s, no worker running, context %d%%", inbox.Span(idle), *b.Context)
	if manager {
		err = offerUser(c, r.Pane, text, now)
	} else {
		_, err = m.Box.Add(inbox.Note{Worker: r.Session, Kind: inbox.ClearIt, Key: inbox.ClearIt + "\x00" + r.Since,
			Text: text + ": clear it? (dev agent clear " + r.Session + ", in the background)"}, now)
	}
	if err != nil {
		return err
	}
	// Marked once queued: a failed delivery leaves it queued for the next
	if _, err := c.Run("set-option", "-p", "-t", r.Pane, OptOfferedAt, strconv.FormatInt(now.Unix(), 10)); err != nil {
		return err
	}
	if _, err := c.Run("set-option", "-p", "-t", r.Pane, OptOffered, r.Since); err != nil || manager {
		return err
	}
	return inbox.Deliver(c, clk, m)
}

// offerOptions are OfferQuestion's options: with its text, they tell dev's offer from an ask someone wrote alike.
var offerOptions = []string{"Clear it | you run dev agent clear manager in a plain shell (Recommended)", "Not now | the manager keeps its context"}

// isOffer reports it is dev's open offer on the manager's list.
func isOffer(it todo.Shown) bool {
	return !it.Done && it.Text == OfferQuestion && slices.Equal(it.Options, offerOptions)
}

// offerUser puts OfferQuestion on the list of the manager's pane for the user, with why (text), unless it is open still.
func offerUser(c *tmux.Client, pane, text string, now time.Time) error {
	s, err := todo.ForPane(c, pane)
	if err != nil {
		return err
	}
	l, err := s.Read(now)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(l.Items, isOffer) {
		return nil
	}
	_, err = s.Add(todo.Item{
		Text:    OfferQuestion,
		Options: offerOptions,
		Context: []string{text, "In a plain shell (not a Claude session): dev agent clear manager; it takes minutes"},
		Details: "dev offers this once per idle stretch (" + text + "). The clear saves a handoff, clears " +
			"its context and checks a proof (dev agent clear --help); the manager never clears itself, so you run it. " +
			"Done when you cleared it or picked Not now.",
	}, now)
	return err
}

// answerOffer answers the open OfferQuestion on the manager's list (pane's) with "Clear it": the user just cleared it.
func answerOffer(c *tmux.Client, pane string, now time.Time) error {
	s, err := todo.ForPane(c, pane)
	if err != nil {
		return err
	}
	l, err := s.Read(now)
	if err != nil {
		return err
	}
	for _, it := range l.Items {
		if isOffer(it) {
			if _, err := s.Answer(it.ID, OfferQuestion, "Clear it", 0, now); err != nil {
				return err
			}
		}
	}
	return nil
}
