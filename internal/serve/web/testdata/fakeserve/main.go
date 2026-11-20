// Command fakeserve serves the phone page with fake /v1 endpoints over an invented board, on loopback, to try the
// page in a browser before dev serve exists: go run ./internal/serve/web/testdata/fakeserve. POST /fake/mac changes
// an item, /fake/version {"version"} names a new dev serve version, /fake/unpair drops the page's session.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ppf/dev/internal/serve/web"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/todo"
)

type server struct {
	mu       sync.Mutex
	board    map[string]any
	unpaired bool
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8765", "loopback address to listen on")
	path := flag.String("board", "internal/serve/web/testdata/board.json", "the invented board to serve")
	unpaired := flag.Bool("unpaired", false, "answer /v1/board with 401 until the page pairs with code 123456")
	flag.Parse()
	b, err := os.ReadFile(*path)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{unpaired: *unpaired}
	if err := json.Unmarshal(b, &s.board); err != nil {
		log.Fatal(err)
	}
	if _, set := s.board["manager_runs"]; !set {
		s.board["manager_runs"] = true
	}
	mux := http.NewServeMux()
	mux.Handle("/", web.Handler())
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		v := s.board["version"]
		s.mu.Unlock()
		reply(w, 200, map[string]any{"ok": true, "version": v})
	})
	mux.HandleFunc("GET /v1/board", s.serveBoard)
	mux.HandleFunc("GET /v1/events", s.events)
	mux.HandleFunc("POST /v1/answer", s.act(s.answer))
	mux.HandleFunc("POST /v1/done", s.act(s.done))
	mux.HandleFunc("POST /v1/start", s.act(s.start))
	mux.HandleFunc("POST /v1/pair", s.pair)
	mux.HandleFunc("POST /fake/mac", s.mac)
	mux.HandleFunc("POST /fake/version", s.version)
	mux.HandleFunc("POST /fake/unpair", func(http.ResponseWriter, *http.Request) {
		s.mu.Lock()
		s.unpaired = true
		s.mu.Unlock()
	})
	log.Printf("fakeserve on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func refuse(w http.ResponseWriter, code int, msg string) {
	reply(w, code, map[string]string{"error": msg})
}

func (s *server) serveBoard(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unpaired {
		refuse(w, 401, "pair this phone first: run dev serve pair on the Mac")
		return
	}
	b, etag, err := s.read()
	if err != nil {
		refuse(w, 500, err.Error())
		return
	}
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// read is the board and its ETag; the caller holds the lock.
func (s *server) read() ([]byte, string, error) {
	b, err := json.Marshal(s.board)
	return b, fmt.Sprintf(`"%x"`, sha256.Sum256(b)), err
}

// events is dev serve's stream, checked every 200 ms: "board" with the ETag at once and on a change, "ping" every
// 15 s; it ends once unpaired.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	last, beat := "", time.Now()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for first := true; ; first = false {
		s.mu.Lock()
		unpaired := s.unpaired
		_, etag, _ := s.read()
		s.mu.Unlock()
		switch {
		case unpaired && first:
			refuse(w, 401, "pair this phone first: run dev serve pair on the Mac")
			return
		case unpaired:
			return
		case first:
			w.Header().Set("Content-Type", "text/event-stream")
		}
		if etag != last {
			last = etag
			fmt.Fprintf(w, "event: board\ndata: %s\n\n", etag)
		}
		if time.Since(beat) >= 15*time.Second {
			beat = time.Now()
			fmt.Fprint(w, "event: ping\ndata: \n\n")
		}
		if http.NewResponseController(w).Flush() != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

// version names a new dev serve version, as an upgrade and restart would.
func (s *server) version(w http.ResponseWriter, r *http.Request) {
	var req struct{ Version string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		refuse(w, 400, "bad JSON: "+err.Error())
		return
	}
	s.mu.Lock()
	s.board["version"] = req.Version
	s.mu.Unlock()
}

type request struct {
	Boss     string `json:"boss"`
	ID       int    `json:"id"`
	Question string `json:"question"`
	Text     string `json:"text"`
	Option   string `json:"option"`
}

// act checks the page's header and finds the open item the request names, then hands it to f under the lock.
func (s *server) act(f func(req request, it map[string]any) (int, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Dev-Page") != "1" {
			refuse(w, 403, "missing X-Dev-Page header")
			return
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			refuse(w, 400, "bad JSON: "+err.Error())
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, l := range s.board["lists"].([]any) {
			l := l.(map[string]any)
			if l["boss"] != req.Boss {
				continue
			}
			for _, it := range l["items"].([]any) {
				it := it.(map[string]any)
				if int(it["id"].(float64)) == req.ID && it["done"] == true {
					refuse(w, 409, fmt.Sprintf("item %d is answered or ticked already", req.ID))
					return
				}
				if int(it["id"].(float64)) == req.ID {
					code, msg := f(req, it)
					if code != 200 {
						refuse(w, code, msg)
						return
					}
					s.recount()
					log.Printf("%s %s #%d", r.URL.Path, req.Boss, req.ID)
					reply(w, 200, map[string]any{"ok": true})
					return
				}
			}
		}
		refuse(w, 404, fmt.Sprintf("no open item %d on %s", req.ID, req.Boss))
	}
}

func (s *server) answer(req request, it map[string]any) (int, string) {
	text := it["text"].(string)
	switch {
	case req.Question != text:
		return 409, fmt.Sprintf("item %d is %q now, not %q: look at the list again", req.ID, text, req.Question)
	case it["status"] != todo.StatusDecide:
		return 409, fmt.Sprintf("item %d is no question", req.ID)
	case (req.Option == "") == (strings.TrimSpace(req.Text) == ""):
		return 400, "give an option or your own words, not both"
	}
	answer := strings.TrimSpace(req.Text)
	if req.Option != "" {
		if !slices.ContainsFunc(it["options"].([]any), func(o any) bool { return o == req.Option || todo.Split(o.(string)).Label == req.Option }) {
			return 400, fmt.Sprintf("item %d has no option %q", req.ID, req.Option)
		}
		answer = req.Option
	}
	it["done"], it["answer"] = true, answer
	return 200, ""
}

func (s *server) done(req request, it map[string]any) (int, string) {
	text := it["text"].(string)
	switch {
	case req.Text != text:
		return 409, fmt.Sprintf("item %d is %q now, not %q: look at the list again", req.ID, text, req.Text)
	case !slices.Contains([]any{todo.StatusYours, todo.StatusCheck, todo.StatusBossClosed}, it["status"]):
		return 409, fmt.Sprintf("item %d is %v: not yours to tick", req.ID, it["status"])
	}
	it["done"] = true
	return 200, ""
}

// start marks a To do card that is no ask started, as dev todo start leaves it; a late start goes again wherever it sits.
func (s *server) start(req request, it map[string]any) (int, string) {
	options, _ := it["options"].([]any)
	a, _ := it["assignee"].(map[string]any)
	mine := a["kind"] == "you"
	switch {
	case req.Text != it["text"]:
		return 409, fmt.Sprintf("item %d is %q now, not %q: look at the list again", req.ID, it["text"], req.Text)
	case req.Boss == sidebar.Manager && !mine:
		return 409, fmt.Sprintf("item %d on the manager's own list isn't yours: ask the manager in its session", req.ID)
	case it["start_late"] != true && (it["column"] != "todo" || len(options) > 0):
		return 409, fmt.Sprintf("item %d is %v: not a To do card to start", req.ID, it["column"])
	}
	if mine {
		it["status"], it["status_on"] = todo.StatusStarting, req.Boss
	}
	it["column"], it["assignee"] = "progress", map[string]any{"kind": "boss", "name": req.Boss, "running": true}
	it["started"] = map[string]any{"at": time.Now().Unix(), "to": req.Boss}
	delete(it, "start_late")
	delete(it, "needs_you")
	return 200, ""
}

// mac changes an item as the Mac's own dev todo would: {"boss","id"} ticks it, with "text" it rewords it.
func (s *server) mac(w http.ResponseWriter, r *http.Request) {
	r.Header.Set("X-Dev-Page", "1")
	s.act(func(req request, it map[string]any) (int, string) {
		if req.Text != "" {
			it["text"] = req.Text
		} else {
			it["done"] = true
		}
		return 200, ""
	})(w, r)
}

func (s *server) recount() {
	counts := map[string]int{"decide": 0, "yours": 0, "needs_you": 0, "mine": 0}
	for _, l := range s.board["lists"].([]any) {
		for _, it := range l.(map[string]any)["items"].([]any) {
			it := it.(map[string]any)
			if it["done"] == true || it["quiet"] == true {
				continue
			}
			if st, ok := it["status"].(string); ok {
				if _, counted := counts[st]; counted {
					counts[st]++
				}
			}
			if it["needs_you"] == true {
				counts["needs_you"]++
			}
			if a, ok := it["assignee"].(map[string]any); ok && a["kind"] == "you" {
				counts["mine"]++
			}
		}
	}
	s.board["counts"] = counts
}

func (s *server) pair(w http.ResponseWriter, r *http.Request) {
	var req struct{ Code, Device string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		refuse(w, 400, "bad JSON: "+err.Error())
		return
	}
	if req.Code != "123456" {
		refuse(w, 401, "that code is wrong or expired: run dev serve pair again")
		return
	}
	s.mu.Lock()
	s.unpaired = false
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "fake_session", Value: "fake", HttpOnly: true, SameSite: http.SameSiteStrictMode, Path: "/"})
	reply(w, 200, map[string]any{"ok": true})
}
