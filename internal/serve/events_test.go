package serve

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fast makes the board check and the ping quick for a test.
func fast(t *testing.T, check, beat time.Duration) {
	c, b := checkEvery, beatEvery
	checkEvery, beatEvery = check, beat
	t.Cleanup(func() { checkEvery, beatEvery = c, b })
}

// stream is an open /v1/events: events has each one as {event, data}, closed once it ended.
type stream struct {
	res    *http.Response
	events chan [2]string
}

func open(t *testing.T, url string) *stream {
	t.Helper()
	res, err := http.Get(url + "/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events: %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	s := &stream{res: res, events: make(chan [2]string, 64)}
	go func() {
		defer close(s.events)
		sc, ev := bufio.NewScanner(res.Body), [2]string{}
		for sc.Scan() {
			switch line := sc.Text(); {
			case strings.HasPrefix(line, "event: "):
				ev[0] = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev[1] = strings.TrimPrefix(line, "data: ")
			case line == "":
				s.events <- ev
				ev = [2]string{}
			}
		}
	}()
	return s
}

// next is the stream's next event of kind, skipping others; ok false when the stream ended or within is up.
func (s *stream) next(kind string, within time.Duration) (string, bool) {
	deadline := time.After(within)
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				return "", false
			}
			if ev[0] == kind {
				return ev[1], true
			}
		case <-deadline:
			return "", false
		}
	}
}

// ended is whether the stream closed within d.
func (s *stream) ended(d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case _, ok := <-s.events:
			if !ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// server serves h with timeouts far shorter than the test waits: a stream outlives them.
func server(t *testing.T, h http.Handler) *httptest.Server {
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ReadTimeout, srv.Config.WriteTimeout = 200*time.Millisecond, 200*time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// A stream needs a session, as /v1/board does.
func TestEventsUnpaired(t *testing.T) {
	auth := &Auth{Dir: t.TempDir(), Now: time.Now}
	h := Handler(Config{Now: time.Now, Guard: auth.Session, Pair: http.HandlerFunc(auth.Pair),
		Lists: func() (json.RawMessage, error) { return json.RawMessage(lists), nil }})
	if w := get(t, h, "/v1/events", ""); w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "event:") {
		t.Errorf("unpaired: %d %s, want 401", w.Code, w.Body)
	}
}

// A stream names the board's ETag at once (the one /v1/board answers), again on a change only, pings, and outlives
// the server's read and write timeouts; with no stream open the board isn't read.
func TestEventsOnChange(t *testing.T) {
	fast(t, 20*time.Millisecond, 100*time.Millisecond)
	var mu sync.Mutex
	l := lists
	var reads atomic.Int32
	now := time.Unix(1700000000, 0)
	h := Handler(Config{Version: "v1", Now: func() time.Time { return now }, Guard: noGuard, Pair: http.NotFoundHandler(),
		Lists: func() (json.RawMessage, error) {
			reads.Add(1)
			mu.Lock()
			defer mu.Unlock()
			return json.RawMessage(l), nil
		}})
	srv := server(t, h)
	s := open(t, srv.URL)
	tag, ok := s.next("board", time.Second)
	etag := get(t, h, "/v1/board", "").Header().Get("ETag")
	if !ok || tag != etag {
		t.Fatalf("first event %q %v, want the board's ETag %q", tag, ok, etag)
	}
	if _, ok := s.next("ping", time.Second); !ok {
		t.Fatal("no ping")
	}
	time.Sleep(400 * time.Millisecond) // past both timeouts, the board unchanged
	mu.Lock()
	l = strings.Replace(lists, "Read", "Read it", 1)
	mu.Unlock()
	changed, ok := s.next("board", time.Second)
	if !ok || changed == tag || changed != get(t, h, "/v1/board", "").Header().Get("ETag") {
		t.Fatalf("after a change: %q %v, want the new ETag", changed, ok)
	}
	select {
	case ev := <-s.events:
		if ev[0] == "board" {
			t.Errorf("a second board event %q with no change", ev[1])
		}
	case <-time.After(150 * time.Millisecond):
	}
	_ = s.res.Body.Close()
	if !settles(&reads, 2*time.Second) {
		t.Fatal("the board is still read with no stream open")
	}
	// The watch starts again for new streams, and tells each of them
	a, b := open(t, srv.URL), open(t, srv.URL)
	for _, s := range []*stream{a, b} {
		if tag, ok := s.next("board", time.Second); !ok || tag != changed {
			t.Fatalf("reopened: first event %q %v, want %q", tag, ok, changed)
		}
	}
	mu.Lock()
	l = lists
	mu.Unlock()
	for i, s := range []*stream{a, b} {
		if tag, ok := s.next("board", time.Second); !ok || tag != etag {
			t.Errorf("stream %d after a second change: %q %v, want %q", i, tag, ok, etag)
		}
	}
}

// settles is whether n stops growing (for 5 checks in a row) within d.
func settles(n *atomic.Int32, d time.Duration) bool {
	for deadline, last, still := time.Now().Add(d), n.Load(), 0; time.Now().Before(deadline); {
		time.Sleep(checkEvery)
		if now := n.Load(); now != last {
			last, still = now, 0
		} else if still++; still == 5 {
			return true
		}
	}
	return false
}

// A stream whose session is dropped ends at its next ping.
func TestEventsSessionGone(t *testing.T) {
	fast(t, 20*time.Millisecond, 50*time.Millisecond)
	var paired atomic.Bool
	paired.Store(true)
	guard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !paired.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	srv := server(t, Handler(Config{Now: time.Now, Guard: guard, Pair: http.NotFoundHandler(),
		Lists: func() (json.RawMessage, error) { return json.RawMessage(lists), nil }}))
	s := open(t, srv.URL)
	if _, ok := s.next("ping", time.Second); !ok {
		t.Fatal("no ping while paired")
	}
	paired.Store(false)
	if !s.ended(time.Second) {
		t.Error("the stream outlived its session")
	}
}
