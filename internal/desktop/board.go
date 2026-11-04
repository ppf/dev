// Package desktop reads what the Claude desktop app keeps of a boss session's workers: the app's session
// records, Claude's live session files, the workers' transcripts and Claude Code's model catalog. All are
// private, undocumented formats, read leniently: unknown fields are ignored, a file that fails to parse is
// reported (the boss's own record must parse).
package desktop

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/ppf/dev/internal/inbox"
)

// Paths are the directories the board reads.
type Paths struct {
	// Records holds the app's session records, <account>/<org>/local_*.json.
	Records string
	// Live holds Claude's live session files, <pid>.json.
	Live string
	// Projects holds the transcripts, <cwd-slug>/<session id>.jsonl.
	Projects string
	// Models holds Claude Code's model catalog, *.json, for the models' context windows.
	Models string
	// Hooks holds the sessions' hook files (Hook), <session id>.json; "" for none.
	Hooks string
}

// DefaultPaths are the macOS locations under home and Claude's config directory (~/.claude when "").
func DefaultPaths(home, config string) Paths {
	if config == "" {
		config = filepath.Join(home, ".claude")
	}
	return Paths{
		Records:  filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions"),
		Live:     filepath.Join(config, "sessions"),
		Projects: filepath.Join(config, "projects"),
		Models:   filepath.Join(config, "cache", "model-catalog"),
	}
}

// Worker states, the first that holds: busy runs a turn, blocked ended its last turn waiting on the person
// (running or not), idle runs between turns, stopped has no live Claude.
const (
	StateBusy    = "busy"
	StateBlocked = "blocked"
	StateIdle    = "idle"
	StateStopped = "stopped"
)

// PR is a pull request the app tracks for a worker.
type PR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
}

// Worker is one tile of the board.
type Worker struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Title    string `json:"title"`
	Branch   string `json:"branch"`
	Worktree string `json:"worktree"`
	PRs      []PR   `json:"prs"`
	State    string `json:"state"`
	// CreatedAt and LastActivityAt are the app's times, in milliseconds since the epoch.
	CreatedAt      int64 `json:"createdAt"`
	LastActivityAt int64 `json:"lastActivityAt"`
	// Verdict is the app's summary of the last finished turn, e.g. completed, blocked or review_ready.
	Verdict     string `json:"verdict"`
	Detail      string `json:"detail"`
	NeedsAction string `json:"needsAction"`
	// LastLines are the last lines of the newest assistant text.
	LastLines []string `json:"lastLines"`
	// Tool is the tool call in flight while busy.
	Tool string `json:"tool"`
	// Context is the share of the model's context window in use, in whole percent; nil when unknown.
	Context *int `json:"context"`
	// Notes are its health notes (stuck, unanswered decision, high context, rate-limited, resume due).
	Notes []Note `json:"notes"`
	// Status is finer than State (needs you, rate-limited, stuck, working, waiting, blocked, review ready,
	// idle, stopped); StatusText is what it says: the question, the tool, what it waits on.
	Status     string `json:"status"`
	StatusText string `json:"statusText"`
	// Since is how long it has been in its live state (busy, idle) or, not running, since the app's last
	// activity, e.g. 12m; "" when unknown.
	Since string `json:"since"`
	// Plan is its task list (update_plan), in order.
	Plan []Step `json:"plan"`
}

type record struct {
	SessionID      string `json:"sessionId"`
	CLISessionID   string `json:"cliSessionId"`
	Title          string `json:"title"`
	Branch         string `json:"branch"`
	WorktreePath   string `json:"worktreePath"`
	IsArchived     bool   `json:"isArchived"`
	CreatedAt      int64  `json:"createdAt"`
	LastActivityAt int64  `json:"lastActivityAt"`
	SpawnedFrom    *struct {
		SessionID string `json:"sessionId"`
		Name      string `json:"name"`
		UserFork  bool   `json:"userFork"`
	} `json:"spawnedFrom"`
	// InterruptedAwaitingPerson marks a session the app quit on while a dialog waited on the person.
	InterruptedAwaitingPerson bool `json:"interruptedAwaitingPerson"`
	PRs                       []struct {
		Number int    `json:"prNumber"`
		URL    string `json:"url"`
		State  string `json:"state"`
	} `json:"prs"`
	Summary *struct {
		Category    string `json:"status_category"`
		Detail      string `json:"status_detail"`
		NeedsAction string `json:"needs_action"`
	} `json:"postTurnSummary"`
}

type live struct {
	PID           int    `json:"pid"`
	StartedAt     int64  `json:"startedAt"`
	Kind          string `json:"kind"`
	SessionID     string `json:"sessionId"`
	HostSessionID string `json:"hostSessionId"`
	Status        string `json:"status"`
	// StatusUpdatedAt is when Status last changed, in milliseconds since the epoch.
	StatusUpdatedAt int64 `json:"statusUpdatedAt"`
}

// Board lists boss's unarchived workers, oldest first, with their notes at now; warnings name files it could
// not read, err that boss has no readable app record.
func Board(p Paths, boss string, now time.Time) (workers []Worker, warnings []error, err error) {
	recs, warnings, err := records(p.Records, boss)
	if err != nil || len(recs) == 0 {
		return []Worker{}, warnings, err
	}
	lives, liveWarnings := liveSessions(p.Live)
	warnings = append(warnings, liveWarnings...)
	windows, err := contextWindows(p.Models)
	if err != nil {
		warnings = append(warnings, err)
	}
	workers = make([]Worker, 0, len(recs))
	for _, r := range recs {
		w, err := worker(p, r, lives, windows, now)
		if err != nil {
			warnings = append(warnings, err)
		}
		workers = append(workers, w)
	}
	slices.SortStableFunc(workers, func(a, b Worker) int { return cmp.Compare(a.CreatedAt, b.CreatedAt) })
	return workers, warnings, nil
}

// records reads the app records spawned from boss and not archived; one that fails to read is a warning.
func records(dir, boss string) ([]record, []error, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*", "*", "local_*.json"))
	if err != nil {
		return nil, nil, err
	}
	bossAt, err := createdAt(dir, paths, boss)
	if err != nil {
		return nil, nil, err
	}
	id := []byte(boss)
	var recs []record
	var warnings []error
	for _, path := range paths {
		// Records run to 100+ MB in all, read every few seconds: a worker's was written after the boss
		// started, and only those naming the boss are parsed
		info, err := os.Stat(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				warnings = append(warnings, err)
			}
			continue
		}
		if info.ModTime().Before(bossAt) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, err)
			continue
		}
		if !bytes.Contains(data, id) {
			continue
		}
		var r record
		if err := json.Unmarshal(data, &r); err != nil {
			warnings = append(warnings, fmt.Errorf("%s: %w", path, err))
			continue
		}
		// A fork the person made (/btw) is theirs, not a worker
		if r.SpawnedFrom != nil && r.SpawnedFrom.SessionID == boss && !r.SpawnedFrom.UserFork && !r.IsArchived {
			recs = append(recs, r)
		}
	}
	return recs, warnings, nil
}

// createdAt is when the app created boss's record, found among paths.
func createdAt(dir string, paths []string, boss string) (time.Time, error) {
	for _, path := range paths {
		if filepath.Base(path) != boss+".json" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return time.Time{}, err
		}
		var r record
		if err := json.Unmarshal(data, &r); err != nil {
			return time.Time{}, fmt.Errorf("%s: %w", path, err)
		}
		return time.UnixMilli(r.CreatedAt), nil
	}
	return time.Time{}, fmt.Errorf("no app session record for %s in %s", boss, dir)
}

// liveSessions reads the live session files whose Claude still runs (a crash leaves its file behind).
func liveSessions(dir string) ([]live, []error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, []error{err}
	}
	var lives []live
	var warnings []error
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			// Claude removes its file on exit
			if !errors.Is(err, os.ErrNotExist) {
				warnings = append(warnings, err)
			}
			continue
		}
		var l live
		if err := json.Unmarshal(data, &l); err != nil {
			warnings = append(warnings, fmt.Errorf("%s: %w", path, err))
			continue
		}
		// Only a session's own Claude, not one it runs (claude -p inherits its host session id)
		if l.Kind != "" && l.Kind != "interactive" {
			continue
		}
		// Fields renamed or a status this board doesn't know: every worker would show the wrong state
		if l.PID <= 0 || l.SessionID == "" || (l.Status != StateBusy && l.Status != StateIdle) {
			warnings = append(warnings, fmt.Errorf("%s: unexpected live session (pid %d, session %q, status %q)",
				path, l.PID, l.SessionID, l.Status))
			continue
		}
		if alive(l) {
			lives = append(lives, l)
		}
	}
	return lives, warnings
}

// alive is whether l's Claude still runs: its pid does, and started by l's startedAt (a pid reused after a
// crash started later).
func alive(l live) bool {
	if err := syscall.Kill(l.PID, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	started, ok := procStart(l.PID)
	return !ok || l.StartedAt == 0 || started.Unix() <= l.StartedAt/1000
}

// worker joins r with its live session, transcript and hook file; the error is what it could not read of them.
func worker(p Paths, r record, lives []live, windows map[string]int, now time.Time) (Worker, error) {
	w := Worker{
		ID:             r.SessionID,
		Name:           r.SpawnedFrom.Name,
		Title:          r.Title,
		Branch:         r.Branch,
		Worktree:       r.WorktreePath,
		PRs:            []PR{},
		CreatedAt:      r.CreatedAt,
		LastActivityAt: r.LastActivityAt,
		LastLines:      []string{},
		Notes:          []Note{},
		Plan:           []Step{},
	}
	for _, pr := range r.PRs {
		w.PRs = append(w.PRs, PR(pr))
	}
	if r.Summary != nil {
		w.Verdict, w.Detail, w.NeedsAction = r.Summary.Category, r.Summary.Detail, r.Summary.NeedsAction
	}
	cli := r.CLISessionID
	l, running := findLive(lives, r)
	if running {
		// The record's id lags behind a /clear or /resume; the live file's is current
		cli = l.SessionID
	}
	switch {
	case running && l.Status == StateBusy:
		w.State = StateBusy
	case w.Verdict == StateBlocked:
		w.State = StateBlocked
	case running:
		w.State = StateIdle
	default:
		w.State = StateStopped
	}
	sinceAt := r.LastActivityAt
	if running {
		sinceAt = l.StatusUpdatedAt
	}
	if sinceAt > 0 {
		w.Since = inbox.Span(now.Sub(time.UnixMilli(sinceAt)))
	}
	if cli == "" {
		w.Status, w.StatusText = status(w, Hook{}, r.InterruptedAwaitingPerson, now)
		return w, nil
	}
	var h Hook
	var hookErr error
	if p.Hooks != "" {
		path := filepath.Join(p.Hooks, cli+".json")
		h, hookErr = ReadHook(path)
		if _, err := os.Stat(path); running && errors.Is(err, os.ErrNotExist) {
			// Claude's hooks run a dev without desktop support (or none), or none ran since it was installed
			hookErr = fmt.Errorf("%s: no hook file for %s: its decision and rate-limit notes are missing until a dev state hook writes it", path, w.Title)
		}
	}
	t, err := readTranscript(p.Projects, cli)
	w.LastLines = t.lines
	if t.plan != nil {
		w.Plan = t.plan
	}
	if w.State == StateBusy {
		w.Tool = t.tool
	}
	window := windows[t.model]
	if window > 0 && t.tokens > 0 {
		pct := min(100, (t.tokens*100+window/2)/window)
		w.Context = &pct
	}
	if err == nil && window == 0 && t.tokens > 0 && windows != nil {
		err = fmt.Errorf("no context window for model %s in the model catalog", t.model)
	}
	seen := t.written.Unix()
	if running {
		seen = max(seen, l.StatusUpdatedAt/1000)
	}
	w.Notes = notes(w, cli, h, seen, now)
	w.Status, w.StatusText = status(w, h, r.InterruptedAwaitingPerson, now)
	return w, errors.Join(err, hookErr)
}

func findLive(lives []live, r record) (live, bool) {
	for _, l := range lives {
		if l.HostSessionID == r.SessionID {
			return l, true
		}
	}
	if r.CLISessionID == "" {
		return live{}, false
	}
	for _, l := range lives {
		if l.SessionID == r.CLISessionID {
			return l, true
		}
	}
	return live{}, false
}
