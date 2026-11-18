// Package spent keeps each session's ledger of what ended under it: the last cost of each Claude conversation a
// /clear, an exit or a close took off the panes of it and the sessions it started, so a boss's cost total keeps
// it. A ledger is keyed by its session's socket, id and creation time: a boss reopened under its name starts a
// new one.
package spent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/tmux"
)

// Ledger is each ended conversation's id and its last cost.total_cost_usd.
type Ledger map[string]float64

// Closed is the sum of l's conversations not in live: Claude's --resume brings a conversation's cost back to
// its pane, where it counts live.
func (l Ledger) Closed(live map[string]bool) float64 {
	var usd float64
	for id, cost := range l {
		if !live[id] {
			usd += cost
		}
	}
	return usd
}

var unsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ledgerName is a ledger file's name past its socket: another socket's name may start as this one's.
var ledgerName = regexp.MustCompile(`^_\d+-\d+\.json(\.lock)?$`)

// path is the ledger file of the session id created at created on socket.
func path(socket, id, created string) (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	name := strings.Trim(unsafe.ReplaceAllString(socket+"-"+id+"-"+created, "_"), "_")
	return filepath.Join(dir, "spent", name+".json"), nil
}

// Read is the ledgers of the sessions on socket, each id with its creation time in created, by id, in one look at
// the directory: the views read them every refresh, mostly finding none. An unreadable one's error is in errs.
func Read(socket string, created map[string]string) (ledgers map[string]Ledger, errs map[string]error, err error) {
	dir, err := config.StateDir()
	if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "spent"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	ledgers, errs = map[string]Ledger{}, map[string]error{}
	for id, at := range created {
		p, err := path(socket, id, at)
		if err != nil {
			return nil, nil, err
		}
		if !names[filepath.Base(p)] {
			continue
		}
		if ledgers[id], err = read(p); err != nil {
			errs[id] = err
		}
	}
	return ledgers, errs, nil
}

func read(p string) (Ledger, error) {
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var l Ledger
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("spent: %s: %w", p, err)
	}
	return l, nil
}

// add merges l into the ledger at p under its lock, keeping each conversation's higher cost: a resumed one ends
// again with its cost grown.
func add(p string, l Ledger) error {
	if len(l) == 0 {
		return nil
	}
	return locked(p, func() error {
		have, err := read(p)
		if err != nil {
			return err
		}
		if have == nil {
			have = Ledger{}
		}
		for id, cost := range l {
			have[id] = max(have[id], cost)
		}
		data, err := json.Marshal(have)
		if err != nil {
			return err
		}
		// Whole or not at all: the views read it on every refresh
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return err
		}
		return os.Rename(tmp, p)
	})
}

// locked runs fn under p's lock file, which stays: removing it would let a writer waiting on it and a later one
// run together.
func locked(p string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	return fn()
}

// pane is what a ledger write reads of a pane and its session.
type pane struct {
	Socket       string `tmux:"socket_path"`
	ID           string `tmux:"session_id"`
	Created      string `tmux:"session_created"`
	Herd         string `tmux:"@dev_herd"`
	StartedBy    string `tmux:"@dev_started_by"`
	Cost         string `tmux:"@agent_cost"`
	Conversation string `tmux:"@agent_session"`
}

// Ended adds the last cost of conversation, ending in pane (a /clear or an exit: Claude's SessionEnd), to the
// ledger of pane's session when it is a boss, else of the session that started it: Claude counts the next
// conversation's cost from zero. Nothing without a cost, a conversation or a session to keep it.
func Ended(c *tmux.Client, paneID, conversation string) error {
	out, err := c.Run("display-message", "-p", "-t", paneID, tmux.Format[pane]())
	if err != nil {
		return err
	}
	ps, err := tmux.Parse[pane](out)
	if err != nil {
		return err
	}
	if len(ps) != 1 {
		return fmt.Errorf("spent: %d rows for %s", len(ps), paneID)
	}
	p := ps[0]
	cost, ok := agent.ParseCost(p.Cost)
	if conversation == "" {
		conversation = p.Conversation
	}
	if !ok || conversation == "" {
		return nil
	}
	id, created := p.ID, p.Created
	if p.Herd == "" {
		if p.StartedBy == "" {
			return nil
		}
		ss, err := sessions(c)
		if err != nil {
			return err
		}
		s, ok := starter(ss, p.StartedBy)
		if !ok {
			return fmt.Errorf("%s's last cost isn't kept: the session that started it (%s) is gone", conversation, p.StartedBy)
		}
		id, created = s.ID, s.Created
	}
	at, err := path(p.Socket, id, created)
	if err != nil {
		return err
	}
	return add(at, Ledger{conversation: cost})
}

// Closing moves a session about to be killed's own ledger plus live (its panes' conversations) to its starter's,
// removing its own only once the starter's holds it (or no starter is left), then prunes ledgers a hook wrote
// after their session's close, which nothing reads.
func Closing(c *tmux.Client, socket, id, created, startedBy string, live Ledger) error {
	own, err := path(socket, id, created)
	if err != nil {
		return err
	}
	if _, err := os.Stat(own); errors.Is(err, fs.ErrNotExist) && len(live) == 0 {
		return nil
	}
	ss, err := sessions(c)
	if err != nil {
		return err
	}
	err = locked(own, func() error {
		l, err := read(own)
		if err != nil {
			return err
		}
		if l == nil {
			l = Ledger{}
		}
		for conversation, cost := range live {
			l[conversation] = max(l[conversation], cost)
		}
		if s, ok := starter(ss, startedBy); ok && len(l) > 0 {
			to, err := path(socket, s.ID, s.Created)
			if err != nil {
				return err
			}
			if err := add(to, l); err != nil {
				return err
			}
		}
		if err := os.Remove(own); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return prune(socket, ss)
}

// prune removes the ledgers and lock files on socket of sessions not in ss, unchanged for a minute: a session
// made since ss was read may be writing its first.
func prune(socket string, ss []session) error {
	keep := map[string]bool{}
	for _, s := range ss {
		p, err := path(socket, s.ID, s.Created)
		if err != nil {
			return err
		}
		keep[filepath.Base(p)] = true
	}
	p, err := path(socket, "", "")
	if err != nil {
		return err
	}
	// "<socket>--.json": the socket's names start "<socket>-"
	dir, prefix := filepath.Dir(p), strings.TrimSuffix(filepath.Base(p), "-.json")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !ledgerName.MatchString(name[len(prefix):]) || keep[strings.TrimSuffix(name, ".lock")] {
			continue
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if time.Since(info.ModTime()) < time.Minute {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// session is what a ledger write reads of a tmux session.
type session struct {
	ID      string `tmux:"session_id"`
	Name    string `tmux:"session_name"`
	Created string `tmux:"session_created"`
	Herd    string `tmux:"@dev_herd"`
}

func sessions(c *tmux.Client) ([]session, error) {
	return tmux.Query[session](c, "list-sessions")
}

// starter is the session in ss the @dev_started_by value v names: a session id, or a boss's name.
func starter(ss []session, v string) (session, bool) {
	for _, s := range ss {
		if v != "" && (v == s.ID || s.Herd != "" && v == s.Name) {
			return s, true
		}
	}
	return session{}, false
}
