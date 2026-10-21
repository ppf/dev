package inbox

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"time"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/tmux"
)

// sendTimeout bounds a send over Claude's messaging socket, which runs in a worker's hook.
const sendTimeout = 2 * time.Second

// Send posts text as one message to the Claude session listening on sock (Claude Code's cross-session messaging:
// newline-delimited JSON over a unix socket). It sends no auth line, which a local sender on macOS and Linux
// doesn't need, and Claude sends no reply.
func Send(sock, text string) (err error) {
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	id[6], id[8] = id[6]&0x0f|0x40, id[8]&0x3f|0x80
	data, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]string{"role": "user", "content": text},
		"uuid":    fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]),
	})
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", sock, sendTimeout)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	if err := conn.SetDeadline(time.Now().Add(sendTimeout)); err != nil {
		return err
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}

// prompting are the permission modes in which Claude shows a message at once; a session in another mode
// (bypassPermissions) holds it behind a dialog that drops it after 5 minutes.
var prompting = []string{"default", "auto", "acceptEdits", "plan"}

// messaging is a pane's Claude messaging socket and permission mode, as its hooks recorded them.
type messaging struct {
	Socket string `tmux:"@agent_socket"`
	Mode   string `tmux:"@agent_mode"`
}

// socketOf is a's messaging socket when a is a Claude in a prompting mode, else "".
func socketOf(c *tmux.Client, a control.Agent) (string, error) {
	if a.Label != "Claude" {
		return "", nil
	}
	out, err := c.Run("display-message", "-p", "-t", a.Pane, tmux.Format[messaging]())
	if err != nil {
		return "", err
	}
	rows, err := tmux.Parse[messaging](out)
	if err != nil || len(rows) != 1 || !slices.Contains(prompting, rows[0].Mode) {
		return "", err
	}
	return rows[0].Socket, nil
}

// send sends b's pending notes over a's messaging socket (socketOf), one batch in flight at a time, without
// waiting for an idle coordinator or an empty input box: Claude shows a message between tool calls, and leaves
// a draft alone. done=false leaves the notes to the paste: no socket, a lost batch (PasteNext), or a failed send
// (err), whose batch goes back first.
func send(c *tmux.Client, clk control.Clock, b Boss, a control.Agent) (done bool, err error) {
	sock, err := socketOf(c, a)
	if sock == "" || err != nil {
		return false, err
	}
	bt, paste, err := b.Box.TakeForSend(clk.Now())
	if err != nil || paste {
		return false, err
	}
	if len(bt.Notes) == 0 {
		// A batch in flight, or nothing pending
		return true, nil
	}
	if err := Send(sock, bt.Message()); err != nil {
		return false, errors.Join(fmt.Errorf("inbox: send to %s: %w", sock, err), b.Box.Unsend())
	}
	return true, nil
}
