package cmd

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/desktop"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
	"github.com/ppf/dev/internal/wait"
)

// takeOverTmux is the tmux server whose bosses' lists take-over sees; tests replace it.
var takeOverTmux = tmux.New

// mainCheckout is the main checkout of the folder dev runs in (a worktree's too), the folder itself outside git: what
// a T3 Code boss's list stores as its dir. Tests replace it.
var mainCheckout = func() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && strings.Contains(string(exit.Stderr), "not a git repository") {
		return cwd, nil
	}
	if err != nil {
		return "", fmt.Errorf("the main checkout of %s: %w", cwd, err)
	}
	return filepath.Dir(strings.TrimSpace(string(out))), nil
}

func newTodoTakeOverCmd(boss *string) *cobra.Command {
	var asJSON, anyProject bool
	cmd := &cobra.Command{
		Use:   "take-over [<list>]",
		Short: "Take over another boss's to do list for this project (without <list>: what this boss could take)",
		Long: `Take over another boss's to do list for this project: its items (who does each, links, --until, --done-on,
context, doing, open asks), its notes, PR checks and finished items move to this boss's list: into an empty one
numbers kept, else after its own items, renumbered. The old list is left as a marker: its boss's writes are refused, saying where the list
went; a dev boss opened under its name after the move starts a fresh list.
Without <list>, print the lists this boss could take, newest change first, and why one can't be taken.
<list> is a dev boss's name (boss-dev), a desktop app session's local_… id, or t3:<thread id> (8 or more
characters of it). Refused: this boss's own list, a dev boss or desktop app session still running (close it
first), another project's list, the manager's or one of no known project (--any-project), a list moved or empty,
over 12 notes together.
A T3 Code thread's list is taken without a running check (dev can't see T3 Code's threads): archive that thread.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if *boss != "" {
				return errors.New("take-over takes into the list of the boss that runs it: leave out --boss")
			}
			now := time.Now()
			own, ownProject, cands, err := takeOverCandidates(cmd, anyProject, now)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(args) == 0 {
				return printCandidates(out, own, ownProject, cands, anyProject, asJSON, now)
			}
			c, err := findCandidate(cands, args[0])
			if err != nil {
				return err
			}
			if err := c.Refusal(); err != nil {
				return err
			}
			t, err := own.TakeOver(c.Store(), now)
			if err != nil {
				return err
			}
			return printTaken(out, own, ownProject, c, t, asJSON)
		},
	}
	cmd.Flags().BoolVar(&anyProject, "any-project", false, "lists of other projects, the manager's, and lists of no known project too")
	cmd.Flags().BoolVar(&asJSON, "json", false, `print a JSON object: without <list>, lists (each with list, kind: tmux, desktop or t3, project, state: running, closed, stopped or unknown, open, asks, notes, updated, takeable, why_not) and other_projects; with it, from, to ({list, kind, project} each), mode: move or merge, items and notes ({from, to} each), asks, on_workers, dropped_state_note ({id, kind, text} or null), warnings`)
	return cmd
}

// takeOverCandidates are this boss's list, its project ("" unknown) and every list beside it (todo.Candidates).
func takeOverCandidates(cmd *cobra.Command, anyProject bool, now time.Time) (todo.Store, string, []todo.Candidate, error) {
	own, err := todoStore("", false, cmd.ErrOrStderr())
	if err != nil {
		return own, "", nil, err
	}
	pane, id, err := todoBoss("")
	if err != nil {
		return own, "", nil, err
	}
	ownProject, cands, err := candidatesOf(cmd, own, pane, id, anyProject, now)
	return own, ownProject, cands, err
}

// candidatesOf are the project ("" unknown) of own, the list of the boss in tmux pane, else of desktop app or T3 Code
// session id, and every list beside it (todo.Candidates).
func candidatesOf(cmd *cobra.Command, own todo.Store, pane, id string, anyProject bool, now time.Time) (string, []todo.Candidate, error) {
	project, err := projectFinder(cmd)
	if err != nil {
		return "", nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, err
	}
	paths := desktop.DefaultPaths(home, os.Getenv("CLAUDE_CONFIG_DIR"))
	desktopStates := func(ids []string) (map[string]todo.DesktopState, error) {
		states, err := desktop.States(paths, ids)
		out := map[string]todo.DesktopState{}
		for id, st := range states {
			out[id] = todo.DesktopState{Dir: st.OriginCwd, Running: st.Running}
		}
		return out, err
	}
	var ownProject string
	switch {
	case pane != "":
		if own.Boss() != sidebar.Manager {
			ownProject = strings.TrimPrefix(own.Boss(), "boss-")
		}
	case strings.HasPrefix(id, t3Boss):
		dir, err := mainCheckout()
		if err != nil {
			return "", nil, err
		}
		ownProject = project(dir)
	default:
		states, err := desktopStates([]string{id})
		if err != nil {
			return "", nil, err
		}
		if dir := states[id].Dir; dir != "" {
			ownProject = project(dir)
		}
	}
	c := takeOverTmux()
	socket, err := tmuxSocket(c)
	if err != nil {
		return "", nil, err
	}
	looks := todo.Looks{
		Socket: socket,
		Sessions: func() ([]string, error) {
			type session struct {
				Name string `tmux:"session_name"`
			}
			rows, err := tmux.Query[session](c, "list-sessions")
			if tmux.IsNoServer(err) {
				return nil, nil
			}
			names := make([]string, len(rows))
			for i, r := range rows {
				names[i] = r.Name
			}
			return names, err
		},
		Desktop: desktopStates,
		Project: project,
	}
	cands, err := todo.Candidates(own, ownProject, looks, anyProject, now)
	return ownProject, cands, err
}

// tmuxSocket is c's server's socket path; with no server running, the one TMUX names, else tmux's default.
func tmuxSocket(c *tmux.Client) (string, error) {
	out, err := c.Run("display-message", "-p", "#{socket_path}")
	switch {
	case err == nil:
		return strings.TrimSpace(out), nil
	case errors.Is(err, exec.ErrNotFound):
		// No tmux, no dev boss: no tmux list is anyone's live one
	case !tmux.IsNoServer(err):
		return "", err
	}
	if env := os.Getenv("TMUX"); env != "" {
		return strings.Split(env, ",")[0], nil
	}
	dir, err := filepath.EvalSymlinks(cmp.Or(os.Getenv("TMUX_TMPDIR"), "/tmp"))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), "default"), nil
}

// projectFinder is the project of a folder: the projects.toml project it is in (the deepest), else the folder itself.
func projectFinder(cmd *cobra.Command) (func(dir string) string, error) {
	path, err := config.DefaultPath()
	if err != nil {
		return nil, err
	}
	projects, err := config.Load(path, reservedNames(cmd))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	real := func(dir string) string {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return r
		}
		return filepath.Clean(dir)
	}
	type root struct{ name, dir string }
	var roots []root
	for _, p := range projects {
		dir, err := p.Dir()
		if err != nil {
			return nil, err
		}
		roots = append(roots, root{p.Name, real(dir)})
	}
	return func(dir string) string {
		dir = real(dir)
		best := root{dir: ""}
		for _, r := range roots {
			if (dir == r.dir || strings.HasPrefix(dir, r.dir+string(filepath.Separator))) && len(r.dir) > len(best.dir) {
				best = r
			}
		}
		return cmp.Or(best.name, dir)
	}, nil
}

// findCandidate is the list arg names: its name, a desktop app session's local_… id or t3:<thread id>, either of 8
// characters or more.
func findCandidate(cands []todo.Candidate, arg string) (todo.Candidate, error) {
	var exact []todo.Candidate
	for _, c := range cands {
		if c.List == arg || c.Short() == arg || c.ID() == arg {
			exact = append(exact, c)
		}
	}
	switch len(exact) {
	case 1:
		return exact[0], nil
	case 0:
	default:
		return todo.Candidate{}, ambiguous(arg, exact)
	}
	var kind, id string
	switch {
	case strings.HasPrefix(arg, t3Boss):
		kind, id = todo.KindT3, strings.TrimPrefix(arg, t3Boss)
	case strings.HasPrefix(arg, "local_"):
		kind, id = todo.KindDesktop, strings.TrimPrefix(arg, "local_")
	default:
		return todo.Candidate{}, fmt.Errorf("no list %s: dev todo take-over lists those this boss can take", arg)
	}
	if len(id) < 8 {
		return todo.Candidate{}, fmt.Errorf("%s: give 8 or more characters of its id", arg)
	}
	want := arg
	if kind == todo.KindT3 {
		want = todo.T3ID(id)
	}
	var found []todo.Candidate
	for _, c := range cands {
		if c.Kind == kind && strings.HasPrefix(c.ID(), want) {
			found = append(found, c)
		}
	}
	switch len(found) {
	case 0:
		return todo.Candidate{}, fmt.Errorf("no list %s: dev todo take-over lists those this boss can take", arg)
	case 1:
		return found[0], nil
	}
	return todo.Candidate{}, ambiguous(arg, found)
}

// ambiguous refuses arg naming more than one list, found.
func ambiguous(arg string, found []todo.Candidate) error {
	names := make([]string, len(found))
	for i, c := range found {
		names[i] = c.ID()
	}
	return fmt.Errorf("%s matches %d lists (%s): give more of its id", arg, len(found), strings.Join(names, ", "))
}

// kindLabel is how the listing names a list's kind.
func kindLabel(kind string) string {
	if kind == todo.KindT3 {
		return "T3 Code"
	}
	return kind
}

// ago is how long ago unix time at was, as the listing says it.
func ago(at int64, now time.Time) string {
	if at == 0 {
		return "never changed"
	}
	d := now.Sub(time.Unix(at, 0))
	switch {
	case d < time.Minute:
		return "changed just now"
	case d >= 48*time.Hour:
		return fmt.Sprintf("changed %dd ago", int(d.Hours()/24))
	}
	return "changed " + strings.Fields(wait.Span(d))[0] + " ago"
}

// printCandidates prints the lists this boss, own of project ownProject, can take over (todo.Candidate.Listed), and
// how many of other projects it leaves out.
func printCandidates(out io.Writer, own todo.Store, ownProject string, cands []todo.Candidate, anyProject, asJSON bool, now time.Time) error {
	rows := []todo.Candidate{}
	var others []string
	for _, c := range cands {
		switch {
		case c.Listed, anyProject && c.Other:
			rows = append(rows, c)
		case c.Other:
			others = append(others, c.Short())
		}
	}
	if asJSON {
		return printJSON(out, struct {
			Lists         []todo.Candidate `json:"lists"`
			OtherProjects int              `json:"other_projects"`
		}{rows, len(others)})
	}
	who := own.Name() + ", " + cmp.Or(ownProject, "no known project")
	if len(rows) == 0 {
		fmt.Fprintf(out, "No list this boss (%s) can take over.\n", who)
	} else {
		fmt.Fprintf(out, "Lists this boss (%s) can take over, newest change first:\n", who)
	}
	takeable := false
	for _, c := range rows {
		open := fmt.Sprintf("%2d open", c.Open)
		if c.Asks > 0 {
			open += ", " + todo.Plural(c.Asks, "ask")
		}
		notes := "notes"
		if c.Notes == 1 {
			notes = "note"
		}
		line := fmt.Sprintf("  %-15s %-8s %-9s%-17s%-9s  %s", c.Short(), kindLabel(c.Kind), c.State, open, fmt.Sprintf("%2d %s", c.Notes, notes), ago(c.Updated, now))
		if !c.Takeable {
			line += "   ✗ " + c.WhyNot
		}
		takeable = takeable || c.Takeable
		fmt.Fprintln(out, line)
	}
	if len(others) > 0 {
		what := todo.Plural(len(others), "list") + " of other projects"
		if len(others) == 1 {
			what = "1 list of another project"
		}
		fmt.Fprintf(out, "%s (%s): --any-project\n", what, strings.Join(others, ", "))
	}
	if takeable {
		fmt.Fprintln(out, "Take one: dev todo take-over <list>")
	}
	return nil
}

// takenList is a list in take-over's JSON.
type takenList struct {
	List    string `json:"list"`
	Kind    string `json:"kind"`
	Project string `json:"project,omitempty"`
}

// renumbered is an item's or note's number before and after the take-over.
type renumbered struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// printTaken prints what own's boss, of project ownProject, took over from c (t).
func printTaken(out io.Writer, own todo.Store, ownProject string, c todo.Candidate, t todo.Taken, asJSON bool) error {
	var warnings []string
	if len(t.OnWorkers) > 0 {
		on := 0
		for _, it := range t.Items {
			if it.Session != nil {
				on++
			}
		}
		warnings = append(warnings, fmt.Sprintf("%d on the old boss's workers (%s): re-check them (dev todo)", on, strings.Join(t.OnWorkers, ", ")))
	}
	if t.Asks > 0 && own.Kind() == todo.KindT3 {
		warnings = append(warnings, todo.Plural(t.Asks, "open ask")+" among them: the boards can't answer a T3 Code boss's asks: name them in your next reply")
	}
	if c.Kind == todo.KindT3 {
		warnings = append(warnings, "dev can't see T3 Code's threads: archive "+c.List+" in T3 Code; its dev todo writes are refused from now")
	}
	if t.DroppedState != nil {
		warnings = append(warnings, fmt.Sprintf("older state note dropped (one per list): %s", t.DroppedState.Text))
	}
	mode := "move"
	if t.Merged {
		mode = "merge"
	}
	if asJSON {
		type dropped struct {
			ID   int    `json:"id"`
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		v := struct {
			From      takenList    `json:"from"`
			To        takenList    `json:"to"`
			Mode      string       `json:"mode"`
			Items     []renumbered `json:"items"`
			Notes     []renumbered `json:"notes"`
			Asks      int          `json:"asks"`
			OnWorkers []string     `json:"on_workers"`
			Dropped   *dropped     `json:"dropped_state_note"`
			Warnings  []string     `json:"warnings"`
		}{
			From: takenList{c.List, c.Kind, c.Project}, To: takenList{own.Name(), own.Kind(), ownProject}, Mode: mode,
			Items: []renumbered{}, Notes: []renumbered{}, Asks: t.Asks, OnWorkers: append([]string{}, t.OnWorkers...), Warnings: append([]string{}, warnings...),
		}
		for _, it := range t.Items {
			v.Items = append(v.Items, renumbered{it.From, it.ID})
		}
		for _, n := range t.Notes {
			v.Notes = append(v.Notes, renumbered{n.From, n.ID})
		}
		if d := t.DroppedState; d != nil {
			v.Dropped = &dropped{d.ID, d.Kind, d.Text}
		}
		return printJSON(out, v)
	}
	var after string
	if t.Merged {
		var yours []string
		if t.OwnItems > 0 {
			yours = append(yours, todo.Plural(t.OwnItems, "item"))
		}
		if t.OwnNotes > 0 {
			yours = append(yours, todo.Plural(t.OwnNotes, "note"))
		}
		after = ", after your " + strings.Join(yours, " and ")
	}
	fmt.Fprintf(out, "✅ Took over %s (%s, %s): %s, %s%s\n", c.Short(), kindLabel(c.Kind), cmp.Or(c.Project, "no known project"), todo.Plural(len(t.Items), "item"), todo.Plural(len(t.Notes), "note"), after)
	for _, it := range t.Items {
		fmt.Fprintf(out, "  %-9s %s\n", fmt.Sprintf("#%d → #%d", it.From, it.ID), it.Text)
	}
	for _, n := range t.Notes {
		fmt.Fprintf(out, "  %-9s %s: %s\n", fmt.Sprintf("n%d → n%d", n.From, n.ID), n.Kind, n.Text)
	}
	for _, w := range warnings {
		fmt.Fprintln(out, "⚠ "+w)
	}
	_, err := fmt.Fprintln(out, "Next: dev todo check")
	return err
}

// takeOverHint is the start-of-session line telling the boss of list own, empty (todo.Store.Empty), that an earlier
// boss's list for its project waits: the newest it can take (candidatesOf's pane and id); "" for none. A failure is
// reported on stderr, no line.
func takeOverHint(cmd *cobra.Command, own todo.Store, pane, id string, now time.Time) string {
	line, err := hintLine(cmd, own, pane, id, now)
	if err != nil {
		noHint(cmd, err)
	}
	return line
}

// hintLine is takeOverHint's line, or why it can't tell.
func hintLine(cmd *cobra.Command, own todo.Store, pane, id string, now time.Time) (string, error) {
	if empty, err := own.Empty(now); err != nil || !empty {
		return "", err
	}
	project, cands, err := candidatesOf(cmd, own, pane, id, false, now)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(cands, func(c todo.Candidate) bool { return c.Takeable })
	if i < 0 {
		return "", nil
	}
	c := cands[i]
	open := fmt.Sprintf("%d open", c.Open)
	if c.Asks > 0 {
		open += ", " + todo.Plural(c.Asks, "ask")
	}
	return fmt.Sprintf("[dev todo] An earlier boss's list for %s waits: %s (%s, %s, %s): dev todo take-over %s. dev todo take-over lists all.",
		project, c.Short(), open, todo.Plural(c.Notes, "note"), ago(c.Updated, now), c.Short()), nil
}

// hintT3 prints takeOverHint on stderr for a T3 Code boss's own list s, which no hook gives it: each bare dev todo and
// dev todo note, while s is empty.
func hintT3(cmd *cobra.Command, boss string, s todo.Store, now time.Time) {
	if boss != "" || s.Kind() != todo.KindT3 {
		return
	}
	_, id, err := todoBoss("")
	if err != nil {
		noHint(cmd, err)
		return
	}
	if line := takeOverHint(cmd, s, "", id, now); line != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), line)
	}
}

// desktopHint is takeOverHint for desktop app session id's list s; none for a worker another session started, which has
// no list.
func desktopHint(cmd *cobra.Command, s todo.Store, id string) string {
	home, err := os.UserHomeDir()
	worker := false
	if err == nil {
		worker, err = desktop.Spawned(desktop.DefaultPaths(home, os.Getenv("CLAUDE_CONFIG_DIR")).Records, id)
	}
	if err != nil {
		noHint(cmd, err)
		return ""
	}
	if worker {
		return ""
	}
	return takeOverHint(cmd, s, "", id, time.Now())
}

// noHint reports on stderr why there's no take-over hint.
func noHint(cmd *cobra.Command, err error) {
	fmt.Fprintln(cmd.ErrOrStderr(), "⚠ dev todo: no take-over hint: "+err.Error())
}
