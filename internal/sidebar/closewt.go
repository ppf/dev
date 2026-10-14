package sidebar

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/worktree"
)

// CloseArgs are close-worktree's arguments, carried on the popup's command line.
type CloseArgs struct {
	Client   string // the client the sidebar acted for; "" = tmux's current client
	Current  string // the sidebar's session: switched away from first when it is Target
	Target   string
	MainRoot string // the worktree's main checkout
	Worktree string // <MainRoot>/.claude/worktrees/<name>
}

// command is the popup's command line: `dev sidebar close-worktree --client C -- current target main worktree`.
func (a CloseArgs) command() (string, error) {
	return selfCommand("sidebar", "close-worktree", "--client", a.Client, "--", a.Current, a.Target, a.MainRoot, a.Worktree)
}

// CloseWorktree runs in the 70×8 popup (close_worktree_popup): it prints the prompt, reads one key,
// then y closes Target and removes its worktree, n closes Target and keeps it, anything else cancels.
// The popup is a tmux server child, so it outlives the sidebar when Target is the sidebar's session.
func CloseWorktree(c *tmux.Client, a CloseArgs, out io.Writer, readKey func() (byte, error)) error {
	name := a.Worktree
	if _, after, ok := strings.Cut(a.Worktree, "/.claude/worktrees/"); ok {
		name = after
	}
	if _, err := fmt.Fprintf(out, "\n  Close session %s?\n\n"+
		"  [y] close + remove worktree %s (discards uncommitted changes)\n"+
		"  [n] close, keep worktree\n"+
		"  [Esc] cancel\n", a.Target, name); err != nil {
		return fmt.Errorf("sidebar: %w", err)
	}
	key, err := readKey()
	if err != nil {
		return err
	}
	switch key {
	case 'y', 'Y':
		killed, err := killNow(c, a.Client, a.Current, a.Target)
		if err != nil || !killed {
			return err
		}
		// Only now: panes inside the worktree would block its removal
		if err := worktree.Remove(a.MainRoot, a.Worktree); err != nil {
			showMessage(c, a.Client, "❌ worktree remove failed: "+err.Error())
			return nil
		}
		showMessage(c, a.Client, "Removed worktree "+name)
	case 'n', 'N':
		_, err := killNow(c, a.Client, a.Current, a.Target)
		return err
	}
	return nil
}

// ReadKey reads one key press from terminal f without echo (bash read -r -s -n1).
func ReadKey(f *os.File) (byte, error) {
	fd := int(f.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return 0, fmt.Errorf("sidebar: %w", err)
	}
	var b [1]byte
	_, readErr := io.ReadFull(f, b[:])
	if err := term.Restore(fd, state); err != nil {
		return 0, fmt.Errorf("sidebar: %w", err)
	}
	if readErr != nil {
		return 0, fmt.Errorf("sidebar: %w", readErr)
	}
	return b[0], nil
}
