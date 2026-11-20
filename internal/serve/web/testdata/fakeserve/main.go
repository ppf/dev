// Command fakeserve serves the phone page with fake /v1 endpoints over an invented board, on loopback, to try the
// page in a browser before dev serve exists: go run ./internal/serve/web/testdata/fakeserve
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

	"github.com/ppf/dev/internal/serve/web"
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
	mux := http.NewServeMux()
	mux.Handle("/", web.Handler())
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, 200, map[string]any{"ok": true, "version": "fake"})
	})
	mux.HandleFunc("GET /v1/board", s.serveBoard)
	mux.HandleFunc("POST /v1/answer", s.act(s.answer))
	mux.HandleFunc("POST /v1/done", s.act(s.done))
	mux.HandleFunc("POST /v1/start", s.act(s.start))
	mux.HandleFunc("POST /v1/pair", s.pair)
	mux.HandleFunc("POST /fake/mac", s.mac)
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
	b, err := json.Marshal(s.board)
	if err != nil {
		refuse(w, 500, err.Error())
		return
	}
	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(b))
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
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

// start marks the user's own yours or boss closed item, or one whose start wasn't picked up, starting, sent to its boss.
func (s *server) start(req request, it map[string]any) (int, string) {
	switch {
	case req.Text != it["text"]:
		return 409, fmt.Sprintf("item %d is %q now, not %q: look at the list again", req.ID, it["text"], req.Text)
	case it["start_late"] != true && !slices.Contains([]any{todo.StatusYours, todo.StatusBossClosed}, it["status"]):
		return 409, fmt.Sprintf("item %d is %v: not yours to start", req.ID, it["status"])
	}
	it["status"], it["status_on"] = todo.StatusStarting, req.Boss
	delete(it, "start_late")
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
	counts := map[string]int{"decide": 0, "yours": 0}
	for _, l := range s.board["lists"].([]any) {
		for _, it := range l.(map[string]any)["items"].([]any) {
			it := it.(map[string]any)
			if st, ok := it["status"].(string); ok && it["done"] != true {
				if _, counted := counts[st]; counted {
					counts[st]++
				}
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
