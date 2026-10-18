// Package resurrect keeps what tmux-resurrect doesn't restore — each dev session's @project, its
// sidebar and the boss that started it, and each agent pane's conversation — in a .dev file next to every snapshot, and rebuilds them
// after a restore (dev-resurrect.sh). Each function runs as a resurrect hook; every failure is warned in
// tmux before it is returned.
package resurrect

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/ptywrap"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

// Save writes <snapshot>.dev: a "session<TAB>project<TAB>window.pane" line per session with a @project,
// the position empty while its sidebar is hidden, then an agentPrefix line per live agent pane with a
// recorded session, and a startedByPrefix line per worker a boss started. A snapshot identical to the last one is deleted by save.sh, so its state goes to the
// last one's .dev instead. A failure keeps the previous file.
func Save(c *tmux.Client, snapshot string) error {
	if info, err := os.Stat(snapshot); err != nil || !info.Mode().IsRegular() {
		return fail(c, "snapshot '"+snapshot+"' missing, dev state not saved", err)
	}
	sessions, err := tmux.Query[struct {
		Name      string `tmux:"session_name"`
		Project   string `tmux:"@project"`
		Sidebar   string `tmux:"@sidebar_pane"`
		ID        string `tmux:"session_id"`
		StartedBy string `tmux:"@dev_started_by"`
		Herd      string `tmux:"@dev_herd"`
	}](c, "list-sessions")
	if err != nil {
		return fail(c, "list-sessions failed, dev state not saved", err)
	}
	panes, err := tmux.Query[struct {
		Session    string `tmux:"session_name"`
		ID         string `tmux:"pane_id"`
		Window     string `tmux:"window_index"`
		Index      string `tmux:"pane_index"`
		Command    string `tmux:"pane_current_command"`
		Path       string `tmux:"pane_current_path"`
		AgentCmd   string `tmux:"@agent_cmd"`
		AgentID    string `tmux:"@agent_session"`
		Transcript string `tmux:"@agent_transcript"`
	}](c, "list-panes", "-a")
	if err != nil {
		return fail(c, "list-panes failed, dev state not saved", err)
	}
	// Keyed by session too: a window linked into another session lists its panes there at another index
	type sessionPane struct{ session, id string }
	pos := map[sessionPane]string{}
	for _, p := range panes {
		pos[sessionPane{p.Session, p.ID}] = p.Window + "." + p.Index
	}
	var b strings.Builder
	for _, s := range sessions {
		if s.Project == "" {
			continue
		}
		// A stale id means the sidebar is gone, which is the same as hidden
		fmt.Fprintf(&b, "%s\t%s\t%s\n", s.Name, s.Project, pos[sessionPane{s.Name, s.Sidebar}])
	}
	bosses := map[string]string{} // id → name
	for _, s := range sessions {
		if s.Herd != "" {
			bosses[s.ID] = s.Name
		}
	}
	for _, s := range sessions {
		// A session id means nothing after a restart: an older version's id of a live boss becomes its name
		boss := s.StartedBy
		if sidebar.IsSessionID(boss) {
			boss = bosses[boss]
		}
		if boss != "" {
			fmt.Fprintf(&b, "%s\t%s\t%s\n", startedByPrefix, s.Name, boss)
		}
	}
	saved := map[string]bool{}
	for _, p := range panes {
		// Once per pane, under the first session listing a linked window; a crash's leftover isn't live
		if p.AgentID == "" || p.Command != p.AgentCmd || saved[p.ID] {
			continue
		}
		saved[p.ID] = true
		fmt.Fprintf(&b, "%s\t%s\t%s.%s\t%s\t%s\t%s\t%s\n", agentPrefix, p.Session, p.Window, p.Index,
			agent.Claude, p.AgentID, p.Transcript, p.Path)
	}
	dev := devPath(snapshot)
	err = os.WriteFile(dev+".tmp", []byte(b.String()), 0o644)
	if err == nil {
		err = os.Rename(dev+".tmp", dev)
	}
	if err != nil {
		return fail(c, "writing "+dev+" failed", err)
	}
	return nil
}

// devPath is where snapshot's state goes: next to it, or next to the last snapshot when save.sh will
// delete snapshot as identical to that one (it compares them right after this hook).
func devPath(snapshot string) string {
	last := filepath.Join(filepath.Dir(snapshot), "last")
	target, err := os.Readlink(last)
	if err != nil {
		return snapshot + ".dev"
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(last), target)
	}
	a, errA := os.ReadFile(snapshot)
	b, errB := os.ReadFile(target)
	// Like save.sh's cmp, an unreadable file counts as different
	if target == snapshot || errA != nil || errB != nil || !bytes.Equal(a, b) {
		return snapshot + ".dev"
	}
	return target + ".dev"
}

// Prune deletes .dev files whose snapshot is gone (save.sh deletes one identical to the last).
func Prune(c *tmux.Client) error {
	if err := prune(); err != nil {
		warn(c, "prune: "+err.Error())
		return fmt.Errorf("resurrect: prune: %w", err)
	}
	return nil
}

func prune() error {
	dir, err := helper("resurrect_dir")
	if err != nil {
		return err
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".dev") {
			continue
		}
		state := filepath.Join(dir, f.Name())
		_, err := os.Stat(strings.TrimSuffix(state, ".dev"))
		if errors.Is(err, fs.ErrNotExist) {
			err = os.Remove(state)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// PreRestore records the live sessions, which Restore must leave alone.
func PreRestore(c *tmux.Client) error {
	live, err := livePath()
	if err != nil {
		return fail(c, "state dir unknown, sidebars will not be rebuilt", err)
	}
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		return fail(c, "cannot create "+filepath.Dir(live), err)
	}
	sessions, err := c.Run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		return fail(c, "list-sessions failed, sidebars will not be rebuilt", err)
	}
	if err := os.WriteFile(live, []byte(sessions), 0o644); err != nil {
		return fail(c, "writing "+live+" failed", err)
	}
	return nil
}

// Restore restarts the agents, then sets @project, undoes restored zooms and rebuilds the sidebar of each
// session in the last snapshot's .dev file that the restore created, preserving its active window and focus.
func Restore(c *tmux.Client) error {
	last, err := helper("last_resurrect_file")
	if err != nil {
		return fail(c, "last snapshot unknown, sidebars not rebuilt", err)
	}
	link, err := os.Readlink(last)
	if err != nil {
		// Like bash's readlink: no snapshot link, so no dev state to restore
		return nil
	}
	if !filepath.IsAbs(link) {
		link = filepath.Join(filepath.Dir(last), link)
	}
	state := link + ".dev"
	// Snapshots taken before these hooks existed have no dev state
	if info, err := os.Stat(state); errors.Is(err, fs.ErrNotExist) || err == nil && !info.Mode().IsRegular() {
		return nil
	}
	data, err := os.ReadFile(state)
	if err != nil {
		return fail(c, "reading "+state+" failed", err)
	}
	live, err := livePath()
	if err != nil {
		return fail(c, "state dir unknown, sidebars not rebuilt", err)
	}
	liveData, err := os.ReadFile(live)
	if err != nil {
		return fail(c, "pre-restore hook did not run, sidebars not rebuilt", err)
	}
	shell, err := c.Run("show-option", "-gv", "default-shell")
	if err != nil {
		return fail(c, "default-shell unknown, sidebars not rebuilt", err)
	}
	shell = filepath.Base(strings.TrimSpace(shell))

	liveSessions := strings.Split(string(liveData), "\n")
	entries, agents, startedBy := parseState(string(data))
	var errs []error
	// First: rebuilding a sidebar renumbers its window's panes, and agents are saved by position
	if err := resumeAgents(c, agents, liveSessions, shell); err != nil {
		errs = append(errs, err)
	}
	for _, e := range entries {
		// Sessions that were live before the restore are untouched by it — leave them alone
		if slices.Contains(liveSessions, e.Session) {
			continue
		}
		if err := restoreSession(c, e, shell); err != nil {
			warn(c, e.Session+": "+err.Error())
			errs = append(errs, err)
		}
	}
	for _, w := range startedBy {
		if slices.Contains(liveSessions, w.Session) {
			continue
		}
		if _, err := c.Run("has-session", "-t", "="+w.Session); err != nil {
			continue
		}
		if _, err := c.Run("set-option", "-t", tmux.SessionTarget(w.Session), "@dev_started_by", w.Boss); err != nil {
			warn(c, w.Session+": @dev_started_by not set")
			errs = append(errs, err)
		}
	}
	if err := os.Remove(live); err != nil {
		warn(c, err.Error())
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("resurrect: %w", err)
	}
	return nil
}

// restoreSession sets e's @project, undoes its zooms and rebuilds its sidebar. The problems bash
// warned about are warned here; it returns the tmux failures bash didn't check.
func restoreSession(c *tmux.Client, e entry, shell string) error {
	target := tmux.SessionTarget(e.Session)
	// Resurrect didn't create it (it existed or failed to restore): nothing to rebuild
	if _, err := c.Run("has-session", "-t", "="+e.Session); err != nil {
		return nil
	}
	if _, err := c.Run("set-option", "-t", target, "@project", e.Project); err != nil {
		warn(c, e.Session+": @project not set")
		return nil
	}
	windows, err := tmux.Query[struct {
		Index  string `tmux:"window_index"`
		Zoomed bool   `tmux:"window_zoomed_flag"`
	}](c, "list-windows", "-t", target)
	if err != nil {
		return &stepError{"list-windows", err}
	}
	// Zooms come back unmarked (compact mode would never undo them); the next client decides again
	for _, w := range windows {
		if !w.Zoomed {
			continue
		}
		if _, err := c.Run("resize-pane", "-Z", "-t", target+w.Index); err != nil {
			return &stepError{"unzoom " + target + w.Index, err}
		}
	}
	if e.Pos == "" {
		return nil
	}
	return rebuildSidebar(c, e, shell)
}

// rebuildSidebar replaces the bare shell resurrect restored in e's sidebar slot with a live sidebar.
func rebuildSidebar(c *tmux.Client, e entry, shell string) error {
	target := tmux.SessionTarget(e.Session)
	index, _, _ := strings.Cut(e.Pos, ".")
	window, slot := target+index, target+e.Pos
	out, err := display(c, slot, "#{pane_current_command}"+tmux.Sep+"#{pane_id}")
	command, slotID, _ := strings.Cut(out, tmux.Sep)
	if err != nil || command != shell {
		warn(c, e.Session+":"+e.Pos+" is not the old sidebar's bare shell, sidebar not rebuilt")
		return nil
	}
	active, err := display(c, target, "#{window_index}")
	if err != nil {
		return &stepError{"active window", err}
	}
	focus, err := display(c, window, "#{pane_id}")
	if err != nil {
		return &stepError{"active pane of " + window, err}
	}
	if focus == slotID {
		focus = ""
	}
	if _, err := c.Run("kill-pane", "-t", slot); err != nil {
		return &stepError{"kill-pane " + slot, err}
	}
	// Ensure splits the current window and focuses the new sidebar
	if _, err := c.Run("select-window", "-t", window); err != nil {
		return &stepError{"select-window " + window, err}
	}
	from, err := resolveOrigin(c, "")
	if err != nil {
		// Like bash's display-message 2>/dev/null: no origin, so focus isn't given back to one
		from = sidebar.Origin{}
	}
	if err := ensureSidebar(c, e.Session, from); err != nil {
		warn(c, e.Session+": sidebar not created")
	}
	if focus != "" {
		if _, err := c.Run("select-pane", "-t", focus); err != nil {
			return &stepError{"select-pane " + focus, err}
		}
	}
	if _, err := c.Run("select-window", "-t", target+active); err != nil {
		return &stepError{"select-window " + target + active, err}
	}
	return nil
}

// stagger spaces agent restarts, so many agents don't start at once.
var stagger = 100 * time.Millisecond

var (
	processEntry = regexp.MustCompile(`"[^"]*"|'[^']*'|\S+`)
	namesClaude  = regexp.MustCompile(`\bclaude([^\w-]|$)`)
)

// restartsClaude reports whether resurrect restarts claude itself under procs, its process list: ":all:",
// or an entry whose match part (before "->") names claude.
func restartsClaude(procs string) bool {
	for _, e := range processEntry.FindAllString(procs, -1) {
		e = strings.Trim(e, `"'`)
		match, _, _ := strings.Cut(e, "->")
		if e == ":all:" || namesClaude.MatchString(match) {
			return true
		}
	}
	return false
}

// resumeAgents types each saved agent's resume command into its restored pane, in a session the restore
// created; a pane back in another directory gets a fresh agent, as its conversation is stored by directory.
// Nothing is restarted while resurrect restarts claude itself: two commands would be typed into one pane.
func resumeAgents(c *tmux.Client, agents []agentEntry, live []string, shell string) error {
	if len(agents) == 0 {
		return nil
	}
	// Resurrect restarts both lists
	procs, err := c.Run("show-option", "-gqv", "@resurrect-default-processes", ";", "show-option", "-gqv", "@resurrect-processes")
	if err != nil {
		warn(c, "@resurrect-processes unknown, agents not restarted")
		return &stepError{"show-option @resurrect-processes", err}
	}
	if restartsClaude(procs) {
		warn(c, "@resurrect-processes restarts claude, agents not resumed: remove its claude entry")
		return nil
	}
	argv, err := ptywrap.Argv()
	if err != nil {
		warn(c, "dev executable unknown, agents not restarted")
		return &stepError{"dev pty-wrap", err}
	}
	wrap := tmux.ShellJoin(argv...)
	var errs []error
	started := false
	for _, a := range agents {
		if slices.Contains(live, a.Session) {
			continue
		}
		slot := tmux.SessionTarget(a.Session) + a.Pos
		out, err := display(c, slot, "#{pane_current_command}"+tmux.Sep+"#{pane_id}"+tmux.Sep+"#{pane_current_path}")
		fields := strings.Split(out, tmux.Sep)
		if err != nil || len(fields) != 3 || fields[0] != shell {
			warn(c, a.Session+":"+a.Pos+" is not the agent's bare shell, agent not restarted")
			continue
		}
		pane, path := fields[1], fields[2]
		id := a.ID
		if path != a.Path {
			id = ""
		}
		cmd, resumed, ok := agent.ResumeCommand(a.Kind, id, a.Transcript)
		if !ok {
			warn(c, a.Session+":"+a.Pos+": unknown agent "+a.Kind+", not restarted")
			continue
		}
		if !resumed {
			warn(c, a.Session+":"+a.Pos+": conversation "+a.ID+" not resumed (directory changed or transcript gone), fresh agent")
		}
		if started {
			time.Sleep(stagger)
		}
		started = true
		// Under dev pty-wrap, as a new session's claude, so its images get through tmux
		if _, err := c.Run("send-keys", "-t", pane, "-l", wrap+" "+cmd, ";", "send-keys", "-t", pane, "Enter"); err != nil {
			err = &stepError{"send-keys " + a.Session + ":" + a.Pos, err}
			warn(c, err.Error())
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// stepError is a tmux step of restoreSession that failed. Its text is Restore's warning, so it gives
// what tmux printed rather than the argv (a Query's argv carries a format of \x1f-separated #{…}).
type stepError struct {
	step string
	err  error
}

func (e *stepError) Error() string {
	detail := e.err.Error()
	if te, ok := errors.AsType[*tmux.Error](e.err); ok {
		detail = te.Stderr
		if detail == "" {
			detail = te.Err.Error()
		}
	}
	return e.step + " failed: " + detail
}

func (e *stepError) Unwrap() error { return e.err }

// display returns format expanded for target.
func display(c *tmux.Client, target, format string) (string, error) {
	out, err := c.Run("display-message", "-t", target, "-p", format)
	return strings.TrimSuffix(out, "\n"), err
}

// entry is one line of a .dev file.
type entry struct {
	Session string
	Project string
	Pos     string // window.pane of the sidebar; "" while hidden
}

// agentPrefix starts an agent line of a .dev file: "<prefix><TAB>session<TAB>window.pane<TAB>kind<TAB>id
// <TAB>transcript<TAB>path". tmux session names can't contain ":", so older parsers take it for a session
// that doesn't exist and skip it.
const agentPrefix = ":agent"

// startedByPrefix starts a "<prefix><TAB>session<TAB>boss" line of a .dev file: the boss (by name) whose
// coordinator started session.
const startedByPrefix = ":started_by"

// startedByEntry is one started-by line of a .dev file.
type startedByEntry struct{ Session, Boss string }

// agentEntry is one agent line of a .dev file.
type agentEntry struct {
	Session, Pos, Kind, ID, Transcript, Path string
}

// parseState reads a .dev file's session, agent and started-by lines.
func parseState(data string) ([]entry, []agentEntry, []startedByEntry) {
	var entries []entry
	var agents []agentEntry
	var startedBy []startedByEntry
	for _, line := range strings.Split(data, "\n") {
		session, rest, _ := strings.Cut(line, "\t")
		if session == startedByPrefix {
			if worker, boss, ok := strings.Cut(rest, "\t"); ok && worker != "" && boss != "" {
				startedBy = append(startedBy, startedByEntry{worker, boss})
			}
			continue
		}
		if session == agentPrefix {
			f := strings.Split(rest, "\t")
			if len(f) == 6 {
				agents = append(agents, agentEntry{Session: f[0], Pos: f[1], Kind: f[2], ID: f[3], Transcript: f[4], Path: f[5]})
			}
			continue
		}
		if session == "" {
			continue
		}
		project, pos, _ := strings.Cut(rest, "\t")
		entries = append(entries, entry{Session: session, Project: project, Pos: pos})
	}
	return entries, agents, startedBy
}

// livePath is $XDG_STATE_HOME/dev/live-before-restore, XDG_STATE_HOME defaulting to ~/.local/state.
func livePath() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "live-before-restore"), nil
}

// helper returns the output of a tmux-resurrect helpers.sh function (resurrect_dir,
// last_resurrect_file); a var so unit tests never run bash.
var helper = runHelper

// runHelper sources ~/.tmux/plugins/tmux-resurrect/scripts/helpers.sh in bash and calls fn. helpers.sh
// runs plain tmux, which follows the inherited TMUX: the server whose hook runs dev.
func runHelper(fn string) (string, error) {
	if testing.Testing() {
		return "", errors.New("resurrect: helpers.sh would reach the default tmux server under test")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resurrect: %w", err)
	}
	script := filepath.Join(home, ".tmux", "plugins", "tmux-resurrect", "scripts", "helpers.sh")
	out, err := exec.Command("bash", "-c", `source "$1" && "$2"`, "bash", script, fn).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("resurrect: %s: %w", fn, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// Restore rebuilds sidebars with the Go ensure, in-process; vars so unit tests stub them.
var (
	ensureSidebar = sidebar.Ensure
	resolveOrigin = sidebar.ResolveOrigin
)

// warn shows msg in tmux and appends it to @dev_resurrect_warnings, which the first `dev` prints after
// a cold-start restore (resurrect discards hook output, and no client may be attached).
func warn(c *tmux.Client, msg string) {
	// Errors ignored: this is where failures are reported, there is nowhere left to report its own
	_, _ = c.Run("display-message", "⚠ dev resurrect: "+msg)
	_, _ = c.Run("set-option", "-ga", "@dev_resurrect_warnings", msg+"; ")
}

// fail warns msg and returns it as an error wrapping err (which may be nil).
func fail(c *tmux.Client, msg string, err error) error {
	warn(c, msg)
	if err == nil {
		return errors.New("resurrect: " + msg)
	}
	return fmt.Errorf("resurrect: %s: %w", msg, err)
}
