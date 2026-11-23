package inbox

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/screen"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
	"github.com/ppf/dev/internal/wait"
)

const (
	// StuckWorking is how long a working worker goes without progress: no hook (its or a subagent's tool
	// call) and no change on its screen.
	StuckWorking = 15 * time.Minute
	// StuckDecision is how long a decision waits unanswered.
	StuckDecision = 3 * time.Minute
	// StuckIdlePR is how long a worker sits idle before its open PR is checked for merge conflicts and CI.
	StuckIdlePR = 15 * time.Minute
	// AskAgain is when an unanswered question is first repeated, after it was asked; then at 2×, 4×, … AskAgain.
	AskAgain = 30 * time.Minute
	// ContextFull is the share of its context window (percent) a worker's high context note starts at.
	ContextFull = 80
)

// paneRow is one list-panes row for Watch and WorkerHook.
type paneRow struct {
	Session    string `tmux:"session_name"`
	Window     int    `tmux:"window_index"`
	Pane       string `tmux:"pane_id"`
	Sidebar    string `tmux:"@sidebar_pane"`
	StartedBy  string `tmux:"@dev_started_by"`
	Outside    string `tmux:"@dev_started_outside"`
	Herd       string `tmux:"@dev_herd"` // set when the worker is itself a boss
	Command    string `tmux:"pane_current_command"`
	State      string `tmux:"@agent_state"`
	StateCmd   string `tmux:"@agent_cmd"`
	WaitingFor string `tmux:"@agent_waiting_for"`
	Since      string `tmux:"@agent_since"`
	Active     string `tmux:"@agent_active"`
	Context    string `tmux:"@agent_context"`
	PR         string `tmux:"@dev_pr"`
	Path       string `tmux:"pane_current_path"`
	Prompts    string `tmux:"@agent_prompts"`
	// A rate-limited worker's scheduled resume (scheduleResume)
	ResumeAt      string `tmux:"@dev_resume_at"`
	ResumePrompts string `tmux:"@dev_resume_prompts"`
	ResumeSession string `tmux:"@dev_resume_session"`
	AgentSession  string `tmux:"@agent_session"`
	Resumes       string `tmux:"@dev_resumes"`
	// Its planned wait (dev agent wait-until)
	WaitUntil   string `tmux:"@dev_wait_until"`
	WaitReason  string `tmux:"@dev_wait_reason"`
	WaitPrompts string `tmux:"@dev_wait_prompts"`
	Note        string `tmux:"@dev_boss_note"` // OptNote
}

// teammates is the set of panes in rows that aren't their session's agent pane (agent.Detect, as dev agent
// get picks it), in sessions that have one: an agent team's teammates, which send only their decisions.
func teammates(rows []paneRow) map[string]bool {
	panes := map[string][]agent.Pane{}
	sidebars := map[string]string{}
	for _, r := range rows {
		panes[r.Session] = append(panes[r.Session], agent.Pane{Session: r.Session, Window: r.Window, ID: r.Pane,
			Command: r.Command, State: r.State, StateCmd: r.StateCmd})
		sidebars[r.Session] = r.Sidebar
	}
	mates := map[string]bool{}
	for session, ps := range panes {
		a, ok := agent.Detect(ps, sidebars[session])
		for _, p := range ps {
			mates[p.ID] = ok && p.ID != a.Pane.ID
		}
	}
	return mates
}

// unix is a pane option's unix seconds, 0 when unset or malformed.
func unix(v string) int64 {
	sec, _ := strconv.ParseInt(v, 10, 64)
	return sec
}

// resting reports a trusted state that is idle, or waiting only because Claude sat idle a minute.
func resting(state, waitingFor string) bool {
	return state == agent.Idle || state == agent.Waiting && waitingFor == agent.IdlePrompt
}

// sight is what one Watch round saw of a worker's agent pane.
type sight struct {
	paneRow
	trusted string // agent.Trusted
	screen  string // a hash of its screen.Snapshot, while it works
	parked  bool   // working with Claude's input box back: its turn waits on background tasks
	waiting bool   // its planned wait holds: no stuck note while it rests or waits on background tasks, no asks repeat
	waitEnd int64  // its planned wait ended this round while it worked: its stuck clock starts there
	managed bool   // its boss runs under the live Manager, which a decision of its may be handed to
}

// prCheck is a PR's merge and CI state as Watch checks it.
type prCheck struct {
	number   int
	done     bool // conclusive: GitHub knew whether it merges
	conflict bool
	noChecks bool
}

// ghPR asks gh for the PR at url's state, mergeability and checks; tests replace it.
var ghPR = func(url string) ([]byte, error) {
	return sidebar.RunGH("", "pr", "view", url, "--json", "state,mergeable,statusCheckRollup")
}

// checkPR checks pr, the worker's open PR as its pane last looked it up in dir. A repo without GitHub
// Actions workflows (found from dir up to its checkout's root) is expected to have no checks.
func checkPR(pr sidebar.PR, dir string) (prCheck, error) {
	out, err := ghPR(pr.URL)
	if err != nil {
		return prCheck{}, err
	}
	var v struct {
		State     string            `json:"state"`
		Mergeable string            `json:"mergeable"`
		Checks    []json.RawMessage `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return prCheck{}, fmt.Errorf("gh pr view %s: %w", pr.URL, err)
	}
	if v.State != "OPEN" {
		return prCheck{done: true}, nil
	}
	// UNKNOWN: GitHub is still working it out
	return prCheck{number: pr.Number, done: v.Mergeable != "UNKNOWN", conflict: v.Mergeable == "CONFLICTING",
		noChecks: len(v.Checks) == 0 && hasWorkflows(dir)}, nil
}

// hasWorkflows reports a .github/workflows directory in dir or above it, up to the checkout's root (.git).
func hasWorkflows(dir string) bool {
	for {
		if fi, err := os.Stat(filepath.Join(dir, ".github", "workflows")); err == nil && fi.IsDir() {
			return true
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// observe updates the workers' health with one round's sights and queues their notes: stuck (no progress
// while working, an unanswered decision), a repeat of an unanswered question, high context. live is the
// sessions of the boss's workers; others' health goes. It returns the notes holding this round, for the
// panes (OptNote): a stuck note each round it holds, so its minutes stay current, though queued once. A worker
// that is itself a boss queues only the notes up reports.
func (s *state) observe(now time.Time, live map[string]bool, sights []sight) []Note {
	for name := range s.Workers {
		if !live[name] {
			delete(s.Workers, name)
		}
	}
	var holding []Note
	for _, w := range sights {
		queue := func(n Note) {
			n.Key = n.Kind + "\x00" + n.Key
			holding = append(holding, n)
			if w.Herd == "" || up(n.Kind) {
				s.add(n)
			}
		}
		h := s.health(w.Session)
		if w.waitEnd != 0 {
			h.WaitEnd = w.waitEnd
		}
		stuck := func(text, key string, handed bool) {
			n := Note{Worker: w.Session, Kind: Stuck, Text: text, Key: Stuck + "\x00" + key, Outside: w.Outside != "", Handed: handed}
			if handed {
				// The Manager gone later, the boss gets the plain note
				n.Key += "\x00handed"
			}
			holding = append(holding, n)
			if h.Stuck != n.Key && s.add(n) {
				h.Stuck = n.Key
			}
		}
		since := time.Unix(unix(w.Since), 0)
		switch {
		case w.trusted == agent.Working:
			if w.screen != h.Screen {
				h.Screen, h.ScreenAt = w.screen, now.Unix()
			}
			progress := time.Unix(max(unix(w.Active), unix(w.Since), h.ScreenAt, h.WaitEnd), 0)
			if quiet := now.Sub(progress); quiet >= StuckWorking && !(w.waiting && w.parked) {
				text := "no progress for " + Span(quiet)
				if w.parked {
					text += ", waiting on its background tasks"
				}
				stuck(text, "working"+strconv.FormatInt(progress.Unix(), 10), false)
			}
		case unix(w.Since) == 0:
			// No stretch to time
		case agent.Deciding(w.trusted, w.WaitingFor):
			if held := now.Sub(since); held >= StuckDecision {
				// Handed to the Manager, still there: it asks; else the boss relays it, and keeps it
				if !w.managed && h.Handed == w.Since {
					h.Handed = ""
				}
				stuck("waiting on a decision for "+Span(held)+", unanswered", w.trusted+w.Since, w.managed && h.Handed == w.Since)
			}
		case resting(w.trusted, w.WaitingFor) && !w.waiting:
			kind, text, _ := strings.Cut(w.Note, ": ")
			text, _ = sidebar.Unanswered(text)
			recorded := h.Question != "" && text == h.Question
			if kind == Asks && text != "" && !recorded {
				// Its question never reached this box (asked while the inbox was off, or before the boss reopened): now it
				// does. Not held for the pane, which has the note: a prompt since clears it there
				if n := (Note{Worker: w.Session, Kind: Asks, Text: text, Key: Asks + "\x00" + text, Outside: w.Outside != ""}); w.Herd == "" || up(n.Kind) {
					s.add(n)
				}
				h.Question, h.AskedAt, h.Reminded = text, now.Unix(), 0
			} else if asked := now.Sub(time.Unix(h.AskedAt, 0)); (kind == Asks && recorded || h.Question != "" && sidebar.HealthNote(kind)) && asked >= AskAgain<<h.Reminded {
				// Still its question: its note, or a health note that took its place (an answer clears both)
				// Past several (rounds missed while it worked or the previews were closed): one repeat
				for asked >= AskAgain<<h.Reminded {
					h.Reminded++
				}
				queue(Note{Worker: w.Session, Kind: Asks, Text: h.Question + " (unanswered for " + Span(asked) + ")", Key: h.Question + strconv.Itoa(h.Reminded),
					Outside: w.Outside != ""})
			}
		}
		if h.Screen != "" && w.trusted != agent.Working {
			h.Screen, h.ScreenAt = "", 0
		}
		used, err := strconv.Atoi(w.Context)
		switch {
		case err != nil || used < ContextFull:
			h.Full = false
		case !h.Full:
			h.Full = true
			// Keyed by the time: a climb back to the same share is a new note
			queue(Note{Worker: w.Session, Kind: Context, Text: strconv.Itoa(used) + "% used", Key: strconv.FormatInt(now.Unix(), 10), Outside: w.Outside != ""})
		}
	}
	return holding
}

// observePR records worker's PR check for its idle stretch since (once conclusive) and queues its note: a
// stuck note when the PR has merge conflicts or no CI, Outside when outside. ok reports a note.
func (s *state) observePR(now time.Time, worker, since string, outside bool, pr prCheck) (n Note, ok bool) {
	if !pr.done {
		// A conflict found after an early no-CI note would share its key
		return Note{}, false
	}
	h := s.health(worker)
	h.PRChecked = since
	var issues []string
	if pr.conflict {
		issues = append(issues, "merge conflicts")
	}
	if pr.noChecks {
		issues = append(issues, "no CI checks")
	}
	if len(issues) == 0 {
		return Note{}, false
	}
	idle := now.Sub(time.Unix(unix(since), 0))
	n = Note{Worker: worker, Kind: Stuck, Text: fmt.Sprintf("idle for %s, PR #%d has %s", Span(idle), pr.number, strings.Join(issues, " and ")),
		Key: Stuck + "\x00pr" + since, Outside: outside}
	if h.Stuck != n.Key && s.add(n) {
		h.Stuck = n.Key
	}
	return n, true
}

// Watch runs from the boss session pane is in (its previews): it resumes the boss's workers whose rate
// limit reset (resume; a worker that is a boss without a resumed note), wakes those whose planned wait ended (wake), checks on its workers (see state.observe) and queues their notes, also recording them on the worker's pane (OptNote), then delivers
// the inbox if the coordinator is ready. Notes that waited for the coordinator to go idle with an empty
// input box get there this way too. Last, as gh may be slow, it checks the open PRs of workers idle
// StuckIdlePR (see checkPR), once per idle stretch, and delivers what they find, then looks up the PRs its to do
// items link to (lookupList), and queues and delivers its to do notes (nudgeList); the manager's then those of the
// closed bosses' lists (lookupClosed).
func Watch(c *tmux.Client, clk control.Clock, pane string) error {
	b, ok, err := Here(c, pane)
	switch {
	case err != nil:
		return err
	case !ok:
		// Its inbox off: its board still shows its list
		manager, err := lookupList(c, pane, clk.Now())
		if manager {
			err = errors.Join(err, lookupClosed(c, clk.Now()))
		}
		return err
	}
	out, err := c.Run("display-message", "-p", "-t", pane, "#{session_id}")
	if err != nil {
		return err
	}
	id := strings.TrimSpace(out)
	rows, err := tmux.Query[paneRow](c, "list-panes", "-a")
	if err != nil {
		return err
	}
	known, err := b.Box.workers()
	if err != nil {
		return err
	}
	now := clk.Now()
	mates := teammates(rows)
	live := map[string]bool{}
	var sights []sight
	var due []paneRow // idle workers whose PR to check
	m, managed, err := managerOver(c, b)
	errs := []error{err}
	for _, r := range rows {
		if !sidebar.Names(r.StartedBy, id, b.Name, true) {
			continue
		}
		live[r.Session] = true
		w := sight{paneRow: r, trusted: agent.Trusted(r.State, r.StateCmd, r.Command), managed: managed}
		if mates[r.Pane] || w.trusted == "" {
			continue
		}
		// A planned wait set since a rate limit holds its resume back until the wait's time
		if pw := wait.FromOptions(r.WaitUntil, r.WaitReason); r.ResumeAt != "" && !(pw.Held() && !pw.Due(now)) {
			resumed, err := resume(c, clk, r)
			if resumed {
				n := Note{Worker: r.Session, Kind: Resumed, Text: "its rate limit reset, dev sent it: continue", Key: Resumed + "\x00" + r.ResumeAt,
					Outside: r.Outside != ""}
				if r.Herd == "" || up(n.Kind) {
					if _, err := b.Box.Add(n, now); err != nil {
						return err
					}
				}
				errs = append(errs, setNote(c, r.Pane, n.Summary()))
			}
			if err != nil && !strings.Contains(err.Error(), "can't find pane") {
				// The boss was told dev resumes it
				n := Note{Worker: r.Session, Kind: Failed, Text: clean("resuming it after its rate limit: " + err.Error()), Key: Failed + "\x00resume" + r.ResumeAt,
					Outside: r.Outside != ""}
				if _, err := b.Box.Add(n, now); err != nil {
					return err
				}
			}
			if resumed || err != nil {
				continue
			}
		}
		if r.WaitUntil != "" {
			wt := wait.FromOptions(r.WaitUntil, r.WaitReason)
			woke, held, err := wake(c, clk, r)
			if woke {
				n := Note{Worker: r.Session, Kind: Resumed, Text: "its wait " + wt.Phrase(now) + " ended, dev sent it: continue",
					Key: Resumed + "\x00wait" + r.WaitUntil, Outside: r.Outside != ""}
				if r.Herd == "" || up(n.Kind) {
					if _, err := b.Box.Add(n, now); err != nil {
						return err
					}
				}
				errs = append(errs, setNote(c, r.Pane, n.Summary()))
				continue
			}
			if err != nil && strings.Contains(err.Error(), "can't find pane") {
				continue
			}
			if err != nil {
				n := Note{Worker: r.Session, Kind: Failed, Text: clean("waking it after its planned wait: " + err.Error()), Key: Failed + "\x00wait" + r.WaitUntil,
					Outside: r.Outside != ""}
				if _, err := b.Box.Add(n, now); err != nil {
					return err
				}
			}
			w.waiting = held
			if !held && w.trusted == agent.Working && wt.Due(now) {
				w.waitEnd = wt.Until.Unix()
			}
		}
		held := now.Sub(time.Unix(unix(r.Since), 0))
		switch {
		case w.trusted == agent.Working:
			shot, err := c.Run("capture-pane", "-p", "-t", r.Pane)
			if err != nil && strings.Contains(err.Error(), "can't find pane") {
				continue
			}
			if err != nil {
				return err
			}
			if agent.ClaudeCommand(r.Command) && screen.ClaudeInterrupted(shot) {
				// Esc ended its turn, which fired no hook: the check records it idle, moving @agent_since
				st, err := control.ReadChecked(c, r.Pane)
				if err != nil && strings.Contains(err.Error(), "can't find pane") {
					continue
				}
				if err != nil {
					return err
				}
				if st.Since != r.Since {
					continue
				}
			}
			w.screen = fmt.Sprintf("%x", sha256.Sum256([]byte(screen.Snapshot(shot))))
			w.parked = agent.ClaudeCommand(r.Command) && screen.ClaudeState(shot) == agent.Idle
		case unix(r.Since) == 0:
			// No stretch to time
		case agent.Deciding(w.trusted, r.WaitingFor) && held >= StuckDecision:
			// A dialog answered No or Esc fired no hook: it isn't unanswered
			st, err := control.ReadChecked(c, r.Pane)
			if err != nil && strings.Contains(err.Error(), "can't find pane") {
				continue // closed since list-panes
			}
			if err != nil {
				return err
			}
			if !agent.Deciding(st.Trusted(), st.WaitingFor) {
				continue
			}
		case resting(w.trusted, r.WaitingFor) && !w.waiting && held >= StuckIdlePR && (known[r.Session] == nil || known[r.Session].PRChecked != r.Since):
			if sidebar.StoredPR(r.PR, r.Path).State == "open" {
				due = append(due, r)
			}
		}
		sights = append(sights, w)
	}
	// A prompt since list-panes clears an asks note (its hook): read it again, so an answered question isn't sent
	for i, w := range sights {
		if kind, _, _ := strings.Cut(w.Note, ": "); kind != Asks {
			continue
		}
		note, err := c.Run("show-options", "-pqv", "-t", w.Pane, OptNote)
		if err != nil && !strings.Contains(err.Error(), "can't find pane") {
			return err
		}
		sights[i].Note = strings.TrimSpace(note)
	}
	var holding []Note
	if err := b.Box.update(func(s *state) error {
		holding = s.observe(now, live, sights)
		return nil
	}); err != nil {
		return err
	}
	panes := map[string]string{}
	for _, w := range sights {
		panes[w.Session] = w.Pane
	}
	for _, n := range holding {
		if err := setNote(c, panes[n.Worker], n.Summary()); err != nil && !strings.Contains(err.Error(), "can't find pane") {
			errs = append(errs, err)
		}
	}
	if managed {
		errs = append(errs, remindManager(c, clk, b, m, holding))
	}
	errs = append(errs, deliverPending(c, clk, b))
	if len(due) > 0 {
		errs = append(errs, checkPRs(c, clk, b, due))
	}
	manager, err := lookupList(c, pane, now)
	if manager {
		// Its own list failing doesn't hold them up
		errs = append(errs, lookupClosed(c, now))
	}
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	queued, err := nudgeList(c, b, pane, live, now)
	if queued {
		err = errors.Join(err, deliverPending(c, clk, b))
	}
	return errors.Join(append(errs, err)...)
}

// listLookups caps the PR lookups of to do items one watch makes: gh takes up to a second each.
const listLookups = 2

// viewPR looks a to do item's PR up; tests replace it.
var viewPR = sidebar.ViewPR

// lookupList looks up the PRs the boss's open to do items link to, those not looked up within todo.LookupTTL, at
// most listLookups; a failed lookup is kept on its item, not reported, as gh may be down for long. None for a pane
// not in a boss session. manager reports the manager's list, failed or not: it owns the closed bosses' (lookupClosed).
func lookupList(c *tmux.Client, pane string, now time.Time) (manager bool, err error) {
	s, err := todo.ForPane(c, pane)
	if errors.Is(err, todo.ErrNotBoss) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.Boss() == sidebar.Manager, s.Lookup(now, todo.LookupTTL, listLookups, viewPR)
}

// lookupClosed looks up the PRs of the closed bosses' lists as lookupList does, at most listLookups across them, so
// a merged PR shows on its item rather than only boss closed, and a --done-on item ticks. A list not read is skipped: the board shows why.
func lookupClosed(c *tmux.Client, now time.Time) error {
	lists, err := todo.Closed(c, now)
	if err != nil {
		return err
	}
	left := listLookups
	view := func(url string) (sidebar.PRView, error) {
		left--
		return viewPR(url)
	}
	var errs []error
	for _, l := range lists {
		// Lookup's limit 0 is no limit
		if left <= 0 {
			break
		}
		if l.Err == nil {
			errs = append(errs, l.Store.Lookup(now, todo.LookupTTL, left, view))
		}
	}
	return errors.Join(errs...)
}

// nudgeList queues a note for each item on the boss's list that ticked by itself on its PR's move, and each it should
// re-check (todo.Store.Nudges; live is its workers' sessions); queued reports any. An --until tick waits for the boss's
// next reminder. None for a pane not in a boss session. Run only with its inbox on: with it off, a tick waits for the
// boss's next start or /clear, and no re-check note goes out.
func nudgeList(c *tmux.Client, b Boss, pane string, live map[string]bool, now time.Time) (queued bool, err error) {
	s, err := todo.ForPane(c, pane)
	if errors.Is(err, todo.ErrNotBoss) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Taken and told only once queued: a failed queue leaves them for the next watch
	add := func(n Note) error {
		_, err := b.Box.Add(n, now)
		return err
	}
	ticked, err := s.TakeTicked(now, func(t todo.Ticked) bool { return t.PR != 0 }, func(ts []todo.Ticked) error {
		for _, t := range ts {
			if err := add(Note{Kind: ToDo, Text: t.String(), Key: tickKey}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	nudges, err := s.Nudges(now, func() ([]todo.Session, error) {
		var ws []todo.Session
		for name := range live {
			ws = append(ws, todo.Session{ID: name, Name: name})
		}
		return ws, nil
	}, func(ns []todo.Nudge) error {
		for _, n := range ns {
			if err := add(Note{Kind: ToDo, Text: n.String()}); err != nil {
				return err
			}
		}
		return nil
	})
	return len(ticked)+len(nudges) > 0, err
}

// tickKey marks a ToDo note of an item ticked by itself: off the list, so no reminder repeats it.
const tickKey = "ticked"

// deliverPending delivers b's inbox when notes wait.
func deliverPending(c *tmux.Client, clk control.Clock, b Boss) error {
	if pending, err := b.Box.Pending(); !pending || err != nil {
		return err
	}
	return Deliver(c, clk, b)
}

// checkPRs checks the open PRs of due, idle workers' panes, and queues and delivers their notes; a result
// counts only while its worker still rests in the same idle stretch. A failed check is taken as done, so
// a gh that is down costs one try per stretch.
func checkPRs(c *tmux.Client, clk control.Clock, b Boss, due []paneRow) error {
	var errs []error
	queued := false
	for _, r := range due {
		pr, err := checkPR(sidebar.StoredPR(r.PR, r.Path), r.Path)
		if err != nil {
			errs = append(errs, err)
			pr = prCheck{done: true}
		}
		out, err := c.Run("display-message", "-p", "-t", r.Pane, tmux.Format[paneRow]())
		if err != nil && strings.Contains(err.Error(), "can't find pane") {
			continue
		}
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		cur, err := tmux.Parse[paneRow](out)
		if err != nil || len(cur) != 1 {
			return errors.Join(append(errs, err, fmt.Errorf("inbox: %d rows for %s", len(cur), r.Pane))...)
		}
		if st := agent.Trusted(cur[0].State, cur[0].StateCmd, cur[0].Command); !resting(st, cur[0].WaitingFor) || cur[0].Since != r.Since {
			continue
		}
		var n Note
		var ok bool
		if err := b.Box.update(func(s *state) error {
			n, ok = s.observePR(clk.Now(), r.Session, r.Since, r.Outside != "", pr)
			return nil
		}); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if !ok {
			continue
		}
		queued = true
		if err := setNote(c, r.Pane, n.Summary()); err != nil && !strings.Contains(err.Error(), "can't find pane") {
			errs = append(errs, err)
		}
	}
	if queued {
		errs = append(errs, deliverPending(c, clk, b))
	}
	return errors.Join(errs...)
}

// remindManager queues in the Manager's inbox, once each, the stuck notes of decisions handed to it (handOver) that
// still wait, and delivers them: it may have lost the first note to a /clear. Not while that first note still waits
// there: the Manager would get the one dialog twice.
func remindManager(c *tmux.Client, clk control.Clock, b, m Boss, holding []Note) error {
	queued := false
	for _, n := range holding {
		if n.Kind != Stuck || !n.Handed {
			continue
		}
		n.Handed, n.Of = false, b.Name
		var added bool
		if err := m.Box.update(func(s *state) error {
			if !slices.ContainsFunc(s.Notes, func(q Note) bool { return q.Worker == n.Worker && q.Of != "" }) {
				added = s.add(n)
			}
			return nil
		}); err != nil {
			return err
		}
		queued = queued || added
	}
	if !queued {
		return nil
	}
	return Deliver(c, clk, m)
}
