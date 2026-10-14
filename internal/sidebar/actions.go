package sidebar

import (
	"errors"
	"os/exec"
	"slices"
	"strings"

	"github.com/ppf/dev/internal/picker"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/worktree"
)

// focusContent selects the first non-sidebar pane of session's active window (focus_content).
func focusContent(c *tmux.Client, client, session string) error {
	pane, err := firstContentPane(c, session)
	if err != nil {
		return err
	}
	if pane != "" {
		_, err = c.Run("select-pane", "-t", pane)
	}
	resetKeyTable(c, client)
	return err
}

// OpenPicker opens the dev picker in an 80%×60% rounded popup on client, "" meaning tmux's current
// client (open_new_popup): the sidebar's d/+ and `dev new`.
func OpenPicker(c *tmux.Client, client string) error {
	resetKeyTable(c, client)
	cmd, err := selfCommand()
	if err != nil {
		return err
	}
	args := append([]string{"display-popup"}, clientFlag("-c", client)...)
	_, err = c.Run(append(args, "-E", "-w", "80%", "-h", "60%", "-b", "rounded", "-e", picker.PopupGuard+"=1", "--", cmd)...)
	// display-popup exits with the picker's status, and the picker showed its own error in the popup;
	// its output goes to the popup, so stderr is tmux's own (e.g. a client gone since the snapshot).
	if e, ok := errors.AsType[*tmux.Error](err); ok && e.Stderr == "" {
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil
		}
	}
	return err
}

// kill closes target for client: refuses the last session; a worktree no other session uses is asked
// about in the 70×8 close-worktree popup, which owns the rest (kill_session_by_name).
func kill(c *tmux.Client, client, current, target string) error {
	if target == "" {
		return nil
	}
	order, err := LoadOrder(c)
	if err != nil || len(order) == 0 {
		return err
	}
	if !slices.Contains(order, target) {
		return setSelected(c, current)
	}
	if len(order) <= 1 {
		showMessage(c, client, "Refusing to kill the last tmux session")
		return nil
	}
	mainRoot, wt, ok, err := sessionWorktree(c, target)
	if err != nil {
		return err
	}
	if ok {
		// A worktree another session still uses (the picker allows reusing one) is closed plainly.
		shared, err := worktreeShared(c, target, wt)
		if err != nil {
			return err
		}
		ok = !shared
	}
	if !ok {
		_, err := killNow(c, client, current, target)
		return err
	}
	cmd, err := CloseArgs{Client: client, Current: current, Target: target, MainRoot: mainRoot, Worktree: wt}.command()
	if err != nil {
		return err
	}
	args := append([]string{"display-popup"}, clientFlag("-c", client)...)
	_, err = c.Run(append(args, "-E", "-w", "70", "-h", "8", "-b", "rounded", "--", cmd)...)
	return err
}

// killNow kills target, first switching client to the first other session in Order when target is
// current, and moves the selection (kill_session_now). killed=false means target is still there (no
// replacement to switch to), so nothing that needs it gone, like removing its worktree, may follow.
func killNow(c *tmux.Client, client, current, target string) (killed bool, err error) {
	order, err := LoadOrder(c)
	if err != nil {
		return false, err
	}
	replacement := current
	if target == current {
		i := slices.IndexFunc(order, func(name string) bool { return name != target })
		if i < 0 {
			showMessage(c, client, "No replacement session available")
			return false, nil
		}
		replacement = order[i]
		// Like bash's `|| true`: kill-session below still runs and has-session reports the outcome.
		_, _ = c.Run(append(append([]string{"switch-client"}, clientFlag("-c", client)...), "-t", tmux.SessionTarget(replacement))...)
		resetKeyTable(c, client)
		// Now, because killing the sidebar's own session kills this process with it.
		if err := setSelected(c, replacement); err != nil {
			return false, err
		}
	}
	// Like bash's `|| true`: has-session below decides whether it is gone.
	_, _ = c.Run("kill-session", "-t", tmux.SessionTarget(target))
	_, err = c.Run("has-session", "-t", tmux.SessionTarget(target))
	killed = err != nil

	if replacement != "" {
		if _, err := c.Run("has-session", "-t", tmux.SessionTarget(replacement)); err == nil {
			return killed, setSelected(c, replacement)
		}
	}
	order, err = LoadOrder(c)
	if err != nil || len(order) == 0 {
		return killed, err
	}
	return killed, setSelected(c, order[0])
}

// sessionWorktree returns the main checkout and worktree of session's first content pane when it
// runs in a .claude/worktrees worktree (session_worktree).
func sessionWorktree(c *tmux.Client, session string) (mainRoot, wtPath string, ok bool, err error) {
	pane, err := firstContentPane(c, session)
	if err != nil || pane == "" {
		return "", "", false, err
	}
	path, err := c.Run("display-message", "-t", pane, "-p", "#{pane_current_path}")
	if err != nil {
		return "", "", false, err
	}
	mainRoot, wtPath, ok = worktree.SessionWorktree(strings.TrimSuffix(path, "\n"))
	return mainRoot, wtPath, ok, nil
}

// worktreeShared reports whether a pane of another session runs in wtPath or below it.
func worktreeShared(c *tmux.Client, session, wtPath string) (bool, error) {
	panes, err := tmux.Query[struct {
		Session string `tmux:"session_name"`
		Path    string `tmux:"pane_current_path"`
	}](c, "list-panes", "-a")
	if err != nil {
		return false, err
	}
	for _, p := range panes {
		if p.Session != session && (p.Path == wtPath || strings.HasPrefix(p.Path, wtPath+"/")) {
			return true, nil
		}
	}
	return false, nil
}

// rename opens client's command prompt, prefilled with target's name, to rename it (rename_selected_session).
func rename(c *tmux.Client, client, target string) error {
	if target == "" {
		return nil
	}
	args := append([]string{"command-prompt"}, clientFlag("-t", client)...)
	// The template is parsed as a tmux command, so the target is quoted for it; '' quoting nests in tmux as in sh.
	template := "rename-session -t " + tmux.ShellJoin(tmux.SessionTarget(target)) + " -- '%%'"
	_, err := c.Run(append(args, "-I", target, "-p", "Rename session:", template)...)
	return err
}

// clientFlag is flag client, or nothing when client is "" so tmux picks its current client.
func clientFlag(flag, client string) []string {
	if client == "" {
		return nil
	}
	return []string{flag, client}
}

// resetKeyTable puts client back in the root key table, as bash did after each action.
func resetKeyTable(c *tmux.Client, client string) {
	// Like bash's `|| true`: a client that detached meanwhile has no key table to reset.
	_, _ = c.Run(append(append([]string{"switch-client"}, clientFlag("-c", client)...), "-T", "root")...)
}
