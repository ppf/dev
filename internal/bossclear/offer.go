package bossclear

import (
	"fmt"
	"slices"
	"strconv"
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
	// OptOffered is the coordinator pane option holding the idle stretch (its @agent_since) a clear was last offered for.
	OptOffered = "@dev_clear_offered"
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
	Offered    string `tmux:"@dev_clear_offered"`
}

// Offer, run by the watch loop of the boss session pane is in, offers once per idle stretch to clear its boss once it
// has rested IdleLimit and Check passes without --force: a note to the manager that started or adopted it, or for the
// manager itself an ask on its own list, which the user's NEEDS YOU shows (they run dev agent clear from a shell).
// None for a boss without a live manager over it, or while a clear runs; a stretch is marked offered only once told.
func Offer(c *tmux.Client, clk control.Clock, pane string) error {
	rows, err := tmux.Query[offerRow](c, "list-panes", "-s", "-t", pane)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(rows, func(r offerRow) bool { return r.Role == "coordinator" })
	if i < 0 {
		return nil
	}
	r, now := rows[i], clk.Now()
	// Cheap checks first: this runs every watch round
	st := agent.Trusted(r.State, r.StateCmd, r.Command)
	since, serr := strconv.ParseInt(r.Since, 10, 64)
	used, cerr := strconv.Atoi(r.Context)
	idle := now.Sub(time.Unix(since, 0))
	if serr != nil || cerr != nil || r.Offered == r.Since || idle < IdleLimit || used < MinContext ||
		st != agent.Idle && (st != agent.Waiting || agent.Deciding(st, r.WaitingFor)) {
		return nil
	}
	manager := r.Session == sidebar.Manager
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
	a, err := control.Resolve(c, r.Session)
	if err != nil {
		return err
	}
	b, workers, err := Read(c, a, now)
	if err != nil {
		return err
	}
	if Check(b, workers, false) != nil {
		return nil
	}
	text := fmt.Sprintf("idle %s, no worker running, context %d%%", inbox.Span(idle), *b.Context)
	if manager {
		err = offerUser(c, r.Pane, text, now)
	} else {
		err = inbox.ToManager(c, clk, m, inbox.Note{Worker: r.Session, Kind: inbox.ClearIt, Key: r.Since,
			Text: text + ": clear it? (dev agent clear " + r.Session + ", in the background)"})
	}
	if err != nil {
		return err
	}
	_, err = c.Run("set-option", "-p", "-t", r.Pane, OptOffered, r.Since)
	return err
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
	if slices.ContainsFunc(l.Items, func(it todo.Shown) bool { return !it.Done && it.Text == OfferQuestion }) {
		return nil
	}
	_, err = s.Add(todo.Item{
		Text: OfferQuestion,
		Options: []string{"Clear it | you run dev agent clear manager in a plain shell (Recommended)",
			"Not now | the manager keeps its context"},
		Context: []string{text, "In a plain shell (not a Claude session): dev agent clear manager; it takes minutes"},
		Details: "dev offers this once per idle stretch (" + text + "). The clear saves a handoff, clears " +
			"its context and checks a proof (dev agent clear --help); the manager never clears itself, so you run it. " +
			"Done when you cleared it or picked Not now.",
	}, now)
	return err
}
