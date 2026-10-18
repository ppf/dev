// Package open starts or attaches a dev session with the default layout (project.sh).
package open

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/ptywrap"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/worktree"
)

// ErrShown wraps failures Run already printed (and paused on); the caller exits 1 without printing again.
var ErrShown = errors.New("open failed")

// Options select the session to open.
type Options struct {
	Path     string // project directory, ~ already expanded
	Name     string // project key: session base name and @project
	Worktree string // worktree name under .claude/worktrees; empty = main checkout
	ForceNew bool   // picker path: suffix -2, -3, ... instead of attaching to an existing session
	Detach   bool   // dev agent start: create the session if missing; no restore, title or attach; not with ForceNew
	Agent    string // agent.Claude, agent.Codex or agent.Grok, typed into the Agent pane; empty = claude
}

// ensureSidebar makes sure an existing session shows its sidebar before it is attached.
func ensureSidebar(c *tmux.Client, session string) {
	// Best effort, like bash's `>/dev/null 2>&1 || true`: a missing origin only skips the focus
	// give-back, and a failing sidebar never blocks opening the session.
	from, _ := sidebar.ResolveOrigin(c, "")
	_ = sidebar.Ensure(c, session, from)
}

var (
	ensureWorktree  = worktree.Ensure
	stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
)

// Run opens the session: worktree, first-run restore, force-new suffix, title, then attach to an
// existing session or create the layout and attach. out receives user-facing messages.
func Run(c *tmux.Client, opts Options, out io.Writer) error {
	home := os.Getenv("HOME")
	inTmux := os.Getenv("TMUX") != ""
	path := opts.Path
	session := SessionName(opts.Name, opts.Worktree)

	if opts.Worktree != "" {
		// Warnings must outlive the tmux popup this often runs in, hence display-message.
		notify := func(msg string) {
			fmt.Fprintln(out, msg)
			if inTmux {
				if _, err := c.Run("display-message", msg); err != nil {
					fmt.Fprintln(out, err)
				}
			}
		}
		wtPath, err := ensureWorktree(path, opts.Worktree, notify)
		if err != nil {
			fmt.Fprintln(out, "❌ "+err.Error())
			Pause(out, "Press enter to close…")
			return fmt.Errorf("%w: %w", ErrShown, err)
		}
		path = wtPath
	}

	width, height := terminalSize(sizeSources(c, inTmux, os.Getenv)...)

	// Detached: tmux-resurrect's restore switches clients, which would pull the user's client away
	if !opts.Detach {
		if err := restoreOnce(c, home, out); err != nil {
			return err
		}
	}

	exists := func(name string) (bool, error) { return hasSession(c, name) }
	if opts.ForceNew {
		var err error
		if session, err = uniqueName(session, exists); err != nil {
			return err
		}
	}

	if !opts.Detach {
		fmt.Fprintf(out, "\033]2;%s\007", session)
	}

	found, err := exists(session)
	if err != nil {
		return err
	}
	agentCmd, err := agentCommand(cmp.Or(opts.Agent, agent.Claude))
	if err != nil {
		return err
	}
	if opts.Detach {
		if found {
			return nil
		}
		return createLayout(c, session, opts.Name, path, agentCmd, width, height)
	}
	if found {
		ensureSidebar(c, session)
	} else if err := createLayout(c, session, opts.Name, path, agentCmd, width, height); err != nil {
		return err
	}
	return attach(c, session, inTmux)
}

// SessionName is the session opening project name's worktree wt gets; wt "" is the main checkout.
func SessionName(name, wt string) string {
	if wt == "" {
		return name
	}
	return name + "-" + strings.ReplaceAll(wt, "/", "-")
}

// sizeSources is project.sh's order: tmux client, stty on /dev/tty, COLUMNS/LINES, tput.
func sizeSources(c *tmux.Client, inTmux bool, getenv func(string) string) []sizeSource {
	return []sizeSource{tmuxSize(c, inTmux), ttySize, envSize(getenv), tputSize}
}

// sizeSource reports a terminal size; ok=true ends the chain even when the values are unusable.
type sizeSource func() (width, height string, ok bool)

// terminalSize takes the first source that answers; a missing or non-numeric dimension becomes 80x24.
func terminalSize(sources ...sizeSource) (width, height int) {
	for _, src := range sources {
		if w, h, ok := src(); ok {
			return parseDim(w, 80), parseDim(h, 24)
		}
	}
	return 80, 24
}

func parseDim(s string, def int) int {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// tmuxSize asks for the current client's size; inside tmux it is the only source consulted.
func tmuxSize(c *tmux.Client, inTmux bool) sizeSource {
	return func() (string, string, bool) {
		if !inTmux {
			return "", "", false
		}
		// An unanswerable query falls through to 80x24 like bash's 2>/dev/null.
		res, _ := c.Run("display-message", "-p", "#{client_width}"+tmux.Sep+"#{client_height}")
		w, h, _ := strings.Cut(strings.TrimSpace(res), tmux.Sep)
		return w, h, true
	}
}

var ttySize sizeSource = func() (string, string, bool) {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return "", "", false
	}
	defer func() { _ = tty.Close() }()
	w, h, err := term.GetSize(int(tty.Fd()))
	if err != nil {
		return "", "", false
	}
	return strconv.Itoa(w), strconv.Itoa(h), true
}

func envSize(getenv func(string) string) sizeSource {
	return func() (string, string, bool) {
		w, h := getenv("COLUMNS"), getenv("LINES")
		return w, h, w != "" && h != ""
	}
}

var tputSize sizeSource = func() (string, string, bool) {
	return tput("cols"), tput("lines"), true
}

func tput(capability string) string {
	res, _ := exec.Command("tput", capability).Output()
	return strings.TrimSpace(string(res))
}

// restoreOnce restores saved sessions on the first dev of a tmux server, so `dev <name>` attaches to
// the restored session instead of creating a new one.
func restoreOnce(c *tmux.Client, home string, out io.Writer) error {
	// An error here is usually "no server running": nothing restored yet either.
	if done, err := c.Run("show-option", "-gqv", "@dev_restored"); err == nil && strings.TrimSpace(done) != "" {
		return nil
	}
	res, err := c.Run("start-server", ";", "set-option", "-g", "@dev_restored", "1",
		";", "run-shell", home+"/.tmux/plugins/tmux-resurrect/scripts/restore.sh")
	fmt.Fprint(out, res)
	if err != nil {
		// Like bash, a failed restore is reported but doesn't stop the session from opening.
		fmt.Fprintln(out, err)
	}
	// A server left with no sessions after the restore exits at once, taking any warnings with it.
	warnings, _ := c.Run("show-option", "-gqv", "@dev_resurrect_warnings")
	if warnings = strings.TrimSpace(warnings); warnings == "" {
		return nil
	}
	if _, err := c.Run("set-option", "-gu", "@dev_resurrect_warnings"); err != nil {
		return err
	}
	fmt.Fprintln(out, "⚠  Session restore: "+warnings)
	Pause(out, "Press enter to continue…")
	return nil
}

// hasSession matches name exactly; any tmux exit status (no such session, no server) means absent.
func hasSession(c *tmux.Client, name string) (bool, error) {
	_, err := c.Run("has-session", "-t", tmux.SessionTarget(name))
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return err == nil, err
}

func uniqueName(base string, exists func(string) (bool, error)) (string, error) {
	name := base
	for suffix := 2; ; suffix++ {
		found, err := exists(name)
		if err != nil || !found {
			return name, err
		}
		name = base + "-" + strconv.Itoa(suffix)
	}
}

// agentCommand is what a new session's Agent pane types to start kind: claude under dev pty-wrap, so its
// images get through tmux.
func agentCommand(kind string) (string, error) {
	if kind != agent.Claude {
		return kind, nil
	}
	argv, err := ptywrap.Argv(kind)
	if err != nil {
		return "", err
	}
	return tmux.ShellJoin(argv...), nil
}

// createLayout builds the default session: Agent (agentCmd | lazygit over a terminal, sidebar on the
// left) and Files (nvim | two terminals), finishing on the agent pane.
func createLayout(c *tmux.Client, session, project, path, agentCmd string, width, height int) error {
	target := tmux.SessionTarget(session)
	sidebarCmd, err := sidebar.PaneCommand()
	if err != nil {
		return err
	}
	run := func(args ...string) string {
		if err != nil {
			return ""
		}
		var res string
		res, err = c.Run(args...)
		return strings.TrimSpace(res)
	}

	run("new-session", "-d", "-x", strconv.Itoa(width), "-y", strconv.Itoa(height),
		"-s", session, "-c", path, "-n", "Agent")
	run("set-option", "-t", target, "@project", project)
	run("send-keys", "-t", target+"1", agentCmd, "Enter")
	claude := run("display-message", "-t", target+"1.1", "-p", "#{pane_id}")
	lazygit := run("split-window", "-t", claude, "-h", "-l", "50%", "-c", path, "-PF", "#{pane_id}")
	run("send-keys", "-t", lazygit, "lazygit status --screen-mode=half", "Enter")
	run("split-window", "-t", lazygit, "-v", "-c", path)

	run("new-window", "-t", target+"2", "-c", path, "-n", "Files")
	nvim := run("display-message", "-t", target+"2.1", "-p", "#{pane_id}")
	run("send-keys", "-t", nvim, "nvim .", "Enter")
	right := run("split-window", "-t", nvim, "-h", "-l", "80", "-c", path, "-PF", "#{pane_id}")
	run("split-window", "-t", right, "-v", "-c", path)
	run("select-pane", "-t", nvim)

	run("select-window", "-t", target+"1")
	sidebarPane := run("split-window", "-t", claude, "-hb", "-l", "40", "-PF", "#{pane_id}", sidebarCmd)
	run("set-option", "-t", target, "@sidebar_pane", sidebarPane)
	run("select-pane", "-t", claude)
	return err
}

func attach(c *tmux.Client, session string, inTmux bool) error {
	if inTmux {
		_, err := c.Run("switch-client", "-t", tmux.SessionTarget(session))
		return err
	}
	return c.Interactive("attach-session", "-t", tmux.SessionTarget(session))
}

// Pause waits for Enter when a person can press it.
func Pause(out io.Writer, prompt string) {
	if !stdinIsTerminal() {
		return
	}
	fmt.Fprint(out, prompt)
	// Any outcome, EOF included, means go on, as with bash's read.
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

// ClientSize is the size a new session gets: the current client's inside tmux, else the terminal's.
func ClientSize(c *tmux.Client) (width, height int) {
	return terminalSize(sizeSources(c, os.Getenv("TMUX") != "", os.Getenv)...)
}

// Attach switches the current client to session inside tmux, else attaches this terminal to it.
func Attach(c *tmux.Client, session string) error {
	return attach(c, session, os.Getenv("TMUX") != "")
}
