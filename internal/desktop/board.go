// Package desktop reads what the Claude desktop app keeps of a boss session's workers: the app's session
// records, Claude's live session files and the workers' transcripts. All three are private, undocumented
// formats, read leniently: unknown fields are ignored, a file that fails to parse is reported.
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
)

// Paths are the directories the board reads.
type Paths struct {
	// Records holds the app's session records, <account>/<org>/local_*.json.
	Records string
	// Live holds Claude's live session files, <pid>.json.
	Live string
	// Projects holds the transcripts, <cwd-slug>/<session id>.jsonl.
	Projects string
	// Models holds the app's model catalog, *.json, for the models' context windows.
	Models string
}

// DefaultPaths are the macOS locations under home.
func DefaultPaths(home string) Paths {
	return Paths{
		Records:  filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions"),
		Live:     filepath.Join(home, ".claude", "sessions"),
		Projects: filepath.Join(home, ".claude", "projects"),
		Models:   filepath.Join(home, ".claude", "cache", "model-catalog"),
	}
}

// Worker states: busy runs a turn, blocked ended its last turn waiting on the person, idle is running
// between turns, stopped has no live Claude.
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
	ID        string `json:"id"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	Branch    string `json:"branch"`
	Worktree  string `json:"worktree"`
	PRs       []PR   `json:"prs"`
	State     string `json:"state"`
	CreatedAt int64  `json:"createdAt"`
	// LastActivityAt is the app's last activity time, in milliseconds since the epoch.
	LastActivityAt int64 `json:"lastActivityAt"`
	// Verdict is the app's summary of the last finished turn: completed, blocked or review_ready.
	Verdict     string `json:"verdict"`
	Detail      string `json:"detail"`
	NeedsAction string `json:"needsAction"`
	// LastLines are the last lines of the newest assistant text.
	LastLines []string `json:"lastLines"`
	// Tool is the tool call in flight while busy.
	Tool string `json:"tool"`
	// Context is the share of the model's context window in use, in whole percent; nil when unknown.
	Context *int `json:"context"`
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
	} `json:"spawnedFrom"`
	PRs []struct {
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
	SessionID     string `json:"sessionId"`
	HostSessionID string `json:"hostSessionId"`
	Status        string `json:"status"`
}

// Board lists boss's unarchived workers, oldest first; warnings name files it could not read, err that boss
// has no readable app record.
func Board(p Paths, boss string) (workers []Worker, warnings []error, err error) {
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
		w, err := worker(p, r, lives, windows)
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
		if info, err := os.Stat(path); err != nil || info.ModTime().Before(bossAt) {
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
		if r.SpawnedFrom != nil && r.SpawnedFrom.SessionID == boss && !r.IsArchived {
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
		if l.PID > 0 && alive(l.PID) {
			lives = append(lives, l)
		}
	}
	return lives, warnings
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// worker joins r with its live session and transcript; the error is a transcript that could not be read.
func worker(p Paths, r record, lives []live, windows map[string]int) (Worker, error) {
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
	if cli == "" {
		return w, nil
	}
	t, err := readTranscript(p.Projects, cli)
	if err != nil {
		return w, err
	}
	w.LastLines = t.lines
	if w.State == StateBusy {
		w.Tool = t.tool
	}
	if window := windows[t.model]; window > 0 && t.tokens > 0 {
		pct := min(100, (t.tokens*100+window/2)/window)
		w.Context = &pct
	}
	return w, nil
}

func findLive(lives []live, r record) (live, bool) {
	for _, l := range lives {
		if l.HostSessionID == r.SessionID {
			return l, true
		}
	}
	for _, l := range lives {
		if r.CLISessionID != "" && l.SessionID == r.CLISessionID {
			return l, true
		}
	}
	return live{}, false
}
