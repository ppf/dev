// Package serve is dev serve's HTTP service for the phone page: the board as JSON, read on the Mac only.
package serve

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

// Config is what the server reads the board from.
type Config struct {
	Version string
	Now     func() time.Time
	// Lists is the JSON dev todo --all --json prints: an array of lists, each with its items' status.
	Lists func() (json.RawMessage, error)
	// ManagerRuns is whether the manager's session is open: it takes a closed boss's starts. nil: it isn't.
	ManagerRuns func() bool
	// Answer answers ask id of boss's list as the user while it still reads question; Done ticks the user's own item
	// id while it still reads text; Start asks for that item to be started while it still reads text. Refused tells
	// the page why not.
	Answer func(boss string, id int, question, answer string) error
	Done   func(boss string, id int, text string) error
	Start  func(boss string, id int, text string) error
	// Doing marks the user's own item id in progress with doing, or ends it (stop), while it still reads text, and tells
	// its boss (the manager for a closed one).
	Doing func(boss string, id int, text, doing string, stop bool) error
	// Guard wraps every /v1 call but health and pair: Auth.Session, or SameOrigin on a loopback bind. Required.
	Guard func(http.Handler) http.Handler
	// Pair answers POST /v1/pair, which needs no X-Dev-Page (the code guards it): Auth.Pair. Required.
	Pair http.Handler
	// Page answers "/" and the paths below it the routes above don't take; nil answers a placeholder line at "/".
	Page http.Handler
}

// Handler is the service's routes.
func Handler(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": cfg.Version})
	})
	mux.Handle("GET /v1/board", cfg.Guard(http.HandlerFunc(cfg.board)))
	mux.Handle("GET /v1/events", cfg.Guard(cfg.events(newWatch(cfg.read))))
	mux.Handle("POST /v1/answer", cfg.Guard(fromPage(http.HandlerFunc(cfg.answer))))
	mux.Handle("POST /v1/done", cfg.Guard(fromPage(http.HandlerFunc(cfg.done))))
	mux.Handle("POST /v1/start", cfg.Guard(fromPage(http.HandlerFunc(cfg.start))))
	mux.Handle("POST /v1/doing", cfg.Guard(fromPage(http.HandlerFunc(cfg.doing))))
	mux.Handle("POST /v1/pair", cfg.Pair)
	for _, route := range []string{"/v1/answer", "/v1/done", "/v1/start", "/v1/doing", "/v1/pair"} { // else "GET /" answers the page's 404
		mux.HandleFunc("GET "+route, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "POST only")
		})
	}
	if cfg.Page != nil {
		mux.Handle("GET /", cfg.Page) // the page's own files sit beside it
	} else {
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("dev serve: the page isn't built yet; the board is at /v1/board\n"))
		})
	}
	return ipHost(mux)
}

// ipHost refuses a request whose Host isn't an IP address or localhost: a site rebinding its name to this address
// (DNS rebinding) would otherwise read the board as same-origin.
func ipHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err != nil && host != "localhost" {
			writeError(w, http.StatusMisdirectedRequest, "open the page by the Mac's IP address, as dev serve prints it")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SameOrigin is the Guard on a loopback bind, in place of a paired session: it refuses a request, reads too, that a
// browser marks as sent from another site (Sec-Fetch-Site) or whose Origin isn't this server.
func SameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		site, origin := r.Header.Get("Sec-Fetch-Site"), r.Header.Get("Origin")
		if (site != "" && site != "same-origin" && site != "none") || (origin != "" && origin != "http://"+r.Host) {
			writeError(w, http.StatusForbidden, "another site's request: open the page at its own address")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// counts are the open items, test items left out: by the old status words, needing the user, the user's.
type counts struct {
	Decide   int `json:"decide"`
	Yours    int `json:"yours"`
	NeedsYou int `json:"needs_you"`
	Mine     int `json:"mine"`
}

// board is everything but generated, which the ETag leaves out so an unchanged board answers 304.
type board struct {
	Version  string   `json:"version"`
	Statuses []string `json:"statuses"`
	Counts   counts   `json:"counts"`
	// ManagerRuns: Start on a closed boss's item needs it
	ManagerRuns bool            `json:"manager_runs"`
	Lists       json.RawMessage `json:"lists"`
}

func (cfg Config) board(w http.ResponseWriter, r *http.Request) {
	b, etag, err := cfg.read()
	if err != nil {
		log.Printf("dev serve: %v", err)
		writeError(w, http.StatusInternalServerError, "the board couldn't be read: see dev serve's output")
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if matches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Generated int64 `json:"generated"`
		board
	}{cfg.Now().Unix(), b})
}

// read is the board and its ETag.
func (cfg Config) read() (board, string, error) {
	lists, err := cfg.Lists()
	if err != nil {
		return board{}, "", fmt.Errorf("board not read: %w", err)
	}
	var statuses []struct {
		Items []struct {
			Status   string        `json:"status"`
			Quiet    bool          `json:"quiet"`
			Done     bool          `json:"done"`
			NeedsYou bool          `json:"needs_you"`
			Assignee todo.Assignee `json:"assignee"`
		} `json:"items"`
	}
	if err := json.Unmarshal(lists, &statuses); err != nil {
		return board{}, "", fmt.Errorf("board not decoded: %w", err)
	}
	b := board{Version: cfg.Version, Statuses: todo.Statuses, Lists: lists, ManagerRuns: cfg.ManagerRuns != nil && cfg.ManagerRuns()}
	for _, l := range statuses {
		for _, it := range l.Items {
			if it.Quiet || it.Done {
				continue
			}
			if it.NeedsYou {
				b.Counts.NeedsYou++
			}
			if it.Assignee.Kind == sidebar.AssigneeYou {
				b.Counts.Mine++
			}
			switch it.Status {
			case todo.StatusDecide:
				b.Counts.Decide++
			case todo.StatusYours:
				b.Counts.Yours++
			}
		}
	}
	body, err := json.Marshal(b)
	if err != nil {
		return board{}, "", fmt.Errorf("board not encoded: %w", err)
	}
	sum := sha256.Sum256(body)
	// Weak: two 200s of one board differ in generated
	return b, `W/"` + hex.EncodeToString(sum[:16]) + `"`, nil
}

// matches is whether an If-None-Match header names etag (weak comparison, as RFC 9110 has for it).
func matches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == strings.TrimPrefix(etag, "W/") || t == "*" {
			return true
		}
	}
	return false
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}
