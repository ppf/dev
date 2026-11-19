// Package serve is dev serve's HTTP service for the phone page: the board as JSON, read on the Mac only.
package serve

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/ppf/dev/internal/todo"
)

// Config is what the server reads the board from.
type Config struct {
	Version string
	Now     func() time.Time
	// Lists is the JSON dev todo --all --json prints: an array of lists, each with its items' status.
	Lists func() (json.RawMessage, error)
	// Guard wraps every /v1 call but health: the auth slot.
	Guard func(http.Handler) http.Handler
	// Page answers "/"; nil answers a placeholder line.
	Page http.Handler
}

// Handler is the service's routes.
func Handler(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": cfg.Version})
	})
	mux.Handle("GET /v1/board", cfg.Guard(http.HandlerFunc(cfg.board)))
	page := cfg.Page
	if page == nil {
		page = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("dev serve: the page isn't built yet; the board is at /v1/board\n"))
		})
	}
	mux.Handle("GET /{$}", page)
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

// LoopbackOnly is the Guard until pairing lands: the board only on a loopback bind, else 403.
func LoopbackOnly(kind Kind) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if kind == Loopback {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusForbidden, "the board is served on loopback only until pairing lands: run dev serve --addr 127.0.0.1:<port>")
		})
	}
}

type counts struct {
	Decide int `json:"decide"`
	Yours  int `json:"yours"`
}

// board is everything but generated, which the ETag leaves out so an unchanged board answers 304.
type board struct {
	Version  string          `json:"version"`
	Statuses []string        `json:"statuses"`
	Counts   counts          `json:"counts"`
	Lists    json.RawMessage `json:"lists"`
}

func (cfg Config) board(w http.ResponseWriter, r *http.Request) {
	lists, err := cfg.Lists()
	if err != nil {
		log.Printf("dev serve: board not read: %v", err)
		writeError(w, http.StatusInternalServerError, "the board couldn't be read: see dev serve's output")
		return
	}
	var statuses []struct {
		Items []struct {
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(lists, &statuses); err != nil {
		log.Printf("dev serve: board not decoded: %v", err)
		writeError(w, http.StatusInternalServerError, "the board couldn't be read: see dev serve's output")
		return
	}
	b := board{Version: cfg.Version, Statuses: todo.Statuses, Lists: lists}
	for _, l := range statuses {
		for _, it := range l.Items {
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
		log.Printf("dev serve: board not encoded: %v", err)
		writeError(w, http.StatusInternalServerError, "the board couldn't be read: see dev serve's output")
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
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

// matches is whether an If-None-Match header names etag (weak comparison, as RFC 9110 has for it).
func matches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == etag || t == "*" {
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
