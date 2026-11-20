package serve

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

var (
	// checkEvery is how often the board is read while a stream is open: the sidebars read it every 2 s.
	checkEvery = time.Second
	// beatEvery is how often a stream says it is alive (the page takes one quiet for 40 s as dropped) and checks its
	// session again, so an unpaired phone's stream ends.
	beatEvery = 15 * time.Second
)

// writeWait bounds each write on a stream, in place of the server's WriteTimeout, which would end it.
const writeWait = 10 * time.Second

// watch shares one board read among the open streams, made only while one is open; each stream gets the board's
// ETag when it changes ("" while it can't be read).
type watch struct {
	read    func() (board, string, error)
	mu      sync.Mutex
	subs    map[chan string]struct{}
	running bool
	tag     string
}

func newWatch(read func() (board, string, error)) *watch {
	return &watch{read: read, subs: map[chan string]struct{}{}}
}

// subscribe gives a stream the board's ETag now, then each change; the func ends it.
func (w *watch) subscribe() (<-chan string, func()) {
	ch := make(chan string, 1)
	w.mu.Lock()
	if !w.running {
		w.running = true
		// Unlocked: a slow board read mustn't hold up the other streams
		w.mu.Unlock()
		tag := w.etag("")
		w.mu.Lock()
		w.tag = tag
		go w.run()
	}
	w.subs[ch] = struct{}{}
	ch <- w.tag
	w.mu.Unlock()
	return ch, func() {
		w.mu.Lock()
		delete(w.subs, ch)
		w.mu.Unlock()
	}
}

func (w *watch) run() {
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	for range t.C {
		w.mu.Lock()
		if len(w.subs) == 0 {
			w.running = false
			w.mu.Unlock()
			return
		}
		was := w.tag
		w.mu.Unlock()
		tag := w.etag(was)
		w.mu.Lock()
		if tag != w.tag {
			w.tag = tag
			for ch := range w.subs {
				select { // only the latest counts
				case <-ch:
				default:
				}
				ch <- tag
			}
		}
		w.mu.Unlock()
	}
}

// etag is the board's ETag, "" when it can't be read: logged once, when it was readable before (was).
func (w *watch) etag(was string) string {
	_, tag, err := w.read()
	if err != nil && was != "" {
		log.Printf("dev serve: %v", err)
	}
	return tag
}

// events is GET /v1/events, a server-sent event stream: "board" with the board's ETag at once and on each change,
// "ping" every beatEvery. It ends when the session is gone or the server shuts down (the request's context).
func (cfg Config) events(wt *watch) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		send := func(event, data string) bool {
			_ = rc.SetWriteDeadline(time.Now().Add(writeWait))
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
				return false
			}
			return rc.Flush() == nil
		}
		ch, done := wt.subscribe()
		defer done()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		beat := time.NewTicker(beatEvery)
		defer beat.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case tag := <-ch:
				if !send("board", tag) {
					return
				}
			case <-beat.C:
				if !cfg.paired(r) || !send("ping", "") {
					return
				}
			}
		}
	})
}

// paired is whether r still passes the Guard.
func (cfg Config) paired(r *http.Request) bool {
	ok := false
	cfg.Guard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ok = true })).ServeHTTP(discard{}, r)
	return ok
}

// discard is a ResponseWriter that keeps nothing.
type discard struct{}

func (discard) Header() http.Header         { return http.Header{} }
func (discard) Write(b []byte) (int, error) { return len(b), nil }
func (discard) WriteHeader(int)             {}
