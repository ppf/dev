package inbox

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// How a boss's session ended, as its last-session file says.
const (
	EndedClosed      = "closed"
	EndedCleared     = "cleared (/clear)"
	EndedGoneStart   = "gone without a close (crash or killed), found at its next start"
	EndedGoneManager = "gone without a close (crash or killed), found at the manager's start"
)

// LastSessionDir is the manager folder's directory of the bosses' last-session files.
func LastSessionDir() (string, error) {
	dir, err := config.ManagerDir()
	return filepath.Join(dir, "bosses"), err
}

// LastSessionPath is boss's last-session file: <manager folder>/bosses/<boss>.md.
func LastSessionPath(boss string) (string, error) {
	dir, err := LastSessionDir()
	return filepath.Join(dir, strings.ReplaceAll(boss, "/", "_")+".md"), err
}

// EndBossSession writes boss's last-session file over the one before from list s's session (todo.EndSession), which
// ended as how says, from (unix seconds, 0 for none) its start when the list has none; with next, a new one starts.
// The manager has none.
func EndBossSession(c *tmux.Client, s todo.Store, boss, how string, from int64, next bool, now time.Time) (wrote bool, err error) {
	if boss == Manager {
		return false, nil
	}
	return s.EndSession(now, from, next, func(f todo.SessionFacts) error {
		workers, err := sessionWorkers(c, boss, f)
		if err != nil {
			return err
		}
		msgs, err := sessionMessages(c, boss, f)
		if err != nil {
			return err
		}
		path, err := LastSessionPath(boss)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(LastSessionText(boss, how, f, workers, msgs)), 0o600); err != nil {
			return err
		}
		return os.Rename(tmp, path)
	})
}

// BossClosing writes the last-session file of boss session, made at created (unix seconds), as it closes
// (sidebar.BossEnding).
func BossClosing(c *tmux.Client, session string, created int64) error {
	s, err := todo.ForPane(c, tmux.SessionTarget(session))
	if err != nil {
		return err
	}
	_, err = EndBossSession(c, s, session, EndedClosed, created, false, time.Now())
	return err
}

// WriteGone writes the last-session file of each boss closed on c's server without one (todo.GoneUnwritten), as the
// manager starts; it returns the bosses written.
func WriteGone(c *tmux.Client, now time.Time) ([]string, error) {
	gone, err := todo.GoneUnwritten(c)
	var wrote []string
	for _, s := range gone {
		ok, werr := EndBossSession(c, s, s.Boss(), EndedGoneManager, 0, false, now)
		if ok {
			wrote = append(wrote, s.Boss())
		}
		err = errors.Join(err, werr)
	}
	return wrote, err
}

// Worker is a session a boss started, as its last-session file lists it: Closed zero while still open.
type Worker struct {
	Name            string
	Started, Closed time.Time
	Note            string
}

// sessionWorkers are the sessions boss started that closed in session f (archive records) or are open now.
func sessionWorkers(c *tmux.Client, boss string, f todo.SessionFacts) ([]Worker, error) {
	records, err := sidebar.Records()
	if err != nil {
		return nil, err
	}
	var ws []Worker
	for _, r := range records {
		if r.StartedBy == boss && r.At.Unix() >= f.Start && r.At.Unix() <= f.End {
			ws = append(ws, Worker{Name: r.Session, Started: r.StartedAt, Closed: r.At, Note: r.Note})
		}
	}
	type session struct {
		Name      string `tmux:"session_name"`
		ID        string `tmux:"session_id"`
		Created   string `tmux:"session_created"`
		StartedBy string `tmux:"@dev_started_by"`
	}
	sessions, err := tmux.Query[session](c, "list-sessions")
	if err != nil && !tmux.IsNoServer(err) {
		return nil, err
	}
	var id string
	if i := slices.IndexFunc(sessions, func(s session) bool { return s.Name == boss }); i >= 0 {
		id = sessions[i].ID
	}
	for _, s := range sessions {
		if !sidebar.Names(s.StartedBy, id, boss, true) {
			continue
		}
		created, err := strconv.ParseInt(s.Created, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s's creation time %q: %w", s.Name, s.Created, err)
		}
		ws = append(ws, Worker{Name: s.Name, Started: time.Unix(created, 0)})
	}
	// By close, the still open ones last
	slices.SortFunc(ws, func(a, b Worker) int {
		ca, cb := a.Closed, b.Closed
		if ca.IsZero() {
			ca = time.Unix(1<<40, 0)
		}
		if cb.IsZero() {
			cb = time.Unix(1<<40, 0)
		}
		return cmp.Or(ca.Compare(cb), strings.Compare(a.Name, b.Name))
	})
	return ws, nil
}

// sessionMessages are the messages to or from boss in session f, oldest first.
func sessionMessages(c *tmux.Client, boss string, f todo.SessionFacts) ([]Msg, error) {
	socket, err := c.Run("display-message", "-p", "#{socket_path}")
	if err != nil {
		if tmux.IsNoServer(err) {
			return nil, nil
		}
		return nil, err
	}
	store, err := OpenMessages(strings.TrimSpace(socket))
	if err != nil {
		return nil, err
	}
	m, err := store.read()
	if err != nil {
		return nil, err
	}
	var out []Msg
	for _, msg := range m.Messages {
		if (msg.From == boss || msg.To == boss) && msg.At >= f.Start && msg.At <= f.End {
			out = append(out, msg)
		}
	}
	return out, nil
}

var prRef = regexp.MustCompile(`\bPR #\d+|\b[a-z][\w.-]*#\d+`)

// LastSessionText is boss's last-session file: what its session f did, how it ended, its workers and messages.
func LastSessionText(boss, how string, f todo.SessionFacts, workers []Worker, msgs []Msg) string {
	at := func(t int64) string { return time.Unix(t, 0).Format("02.01 15:04") }
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	section := func(title string) { line("\n## %s", title) }
	none := func(n int) {
		if n == 0 {
			line("None.")
		}
	}
	line("# %s: last session", boss)
	line("")
	line("Session %s → %s, %s. Written by dev at %s, over the session before; your list: dev todo.", at(f.Start), at(f.End), how, at(f.End))

	section("From the boss (state note)")
	if n := f.State; n != nil {
		line("n%d, %s: %s", n.ID, at(n.Added), n.Text)
		if n.Why != "" {
			line("Why: %s", n.Why)
		}
	} else {
		line("None: the boss kept no state note (dev todo note state).")
	}

	section("Done this session")
	for _, it := range f.Ticked {
		text := fmt.Sprintf("- #%d %s (%s", it.ID, short(it.Text), at(it.Done))
		if it.DoneBy != "" {
			text += ", " + it.DoneBy
		}
		text += ")"
		if it.Answer != "" {
			text += ": answered " + short(it.Answer)
		}
		if it.Result != "" {
			text += ": " + it.Result
		}
		line("%s", text)
	}
	none(len(f.Ticked))

	section("Added this session, still open")
	for _, it := range f.Added {
		line("- #%d %s", it.ID, short(it.Text))
	}
	none(len(f.Added))

	section("Open asks to the user")
	for _, it := range f.Asks {
		line("- #%d %s", it.ID, short(it.Text))
	}
	none(len(f.Asks))

	section("Notes added")
	for _, n := range f.Notes {
		line("- n%d %s (%s): %s", n.ID, n.Kind, at(n.Added), short(n.Text))
	}
	none(len(f.Notes))

	section("Workers")
	if len(workers) > 0 {
		line("| worker | started | closed |")
		line("|---|---|---|")
	}
	for _, w := range workers {
		started, closed := "-", "still open"
		if !w.Started.IsZero() {
			started = at(w.Started.Unix())
		}
		if !w.Closed.IsZero() {
			closed = at(w.Closed.Unix())
			if w.Note != "" {
				closed += " (" + w.Note + ")"
			}
		}
		line("| %s | %s | %s |", w.Name, started, closed)
	}
	none(len(workers))

	section("PRs named")
	var prs []string
	for _, it := range slices.Concat(f.Ticked, f.Added) {
		for _, ref := range append(prRef.FindAllString(it.Result, -1), it.Link) {
			if ref != "" && !slices.Contains(prs, ref) {
				prs = append(prs, ref)
			}
		}
	}
	if len(prs) > 0 {
		line("%s", strings.Join(prs, " · "))
	}
	none(len(prs))

	section("Messages")
	for _, m := range msgs {
		who := "to " + m.To
		if m.To == boss {
			who = "from " + m.From
		}
		first, _, _ := strings.Cut(m.Text, "\n")
		line("- %s %s (%s): %s", m.ID, who, at(m.At), short(first))
	}
	none(len(msgs))

	section("Left from the session before, still open")
	for _, it := range f.Left {
		line("- #%d %s", it.ID, short(it.Text))
	}
	none(len(f.Left))
	return b.String()
}

// shortMax is the most runes of an item's or note's text a last-session file keeps: the list holds the rest.
const shortMax = 100

func short(s string) string {
	if utf8.RuneCountInString(s) <= shortMax {
		return s
	}
	return string([]rune(s)[:shortMax-1]) + "…"
}
