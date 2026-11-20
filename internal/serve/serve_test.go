package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

const lists = `[{"boss":"boss-a","items":[{"id":1,"text":"Merge?","done":false,"options":["Merge","Wait"],"status":"decide"},` +
	`{"id":2,"text":"Read","done":false,"status":"yours"},{"id":3,"text":"Fix","done":false,"on":"w","status":"working","status_on":"w"}],"updated":1,"stale":false}]`

func noGuard(h http.Handler) http.Handler { return h }

func handler(now *time.Time, lists *string) http.Handler {
	return Handler(Config{
		Version: "v1.2.3",
		Now:     func() time.Time { return *now },
		Lists:   func() (json.RawMessage, error) { return json.RawMessage(*lists), nil },
		Guard:   noGuard,
		Pair:    http.NotFoundHandler(),
	})
}

func get(t *testing.T, h http.Handler, path, inm string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8417"+path, nil)
	if inm != "" {
		r.Header.Set("If-None-Match", inm)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The board counts decide and yours, keeps its ETag while only the clock moves, answers 304 to it, and a new one
// when a list changes.
func TestBoardETag(t *testing.T) {
	now, l := time.Unix(1700000000, 0), lists
	h := handler(&now, &l)
	w := get(t, h, "/v1/board", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"generated": 1700000000`) || !strings.Contains(w.Body.String(), `"decide": 1,`) || !strings.Contains(w.Body.String(), `"yours": 1`) {
		t.Fatalf("board %d:\n%s", w.Code, w.Body)
	}
	etag := w.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("ETag %q, want a weak one: generated differs", etag)
	}
	now = now.Add(time.Minute)
	if w := get(t, h, "/v1/board", etag); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Errorf("unchanged board a minute on: %d %q, want 304", w.Code, w.Body)
	}
	if w := get(t, h, "/v1/board", strings.TrimPrefix(etag, "W/")); w.Code != http.StatusNotModified {
		t.Errorf("etag without W/: %d, want 304", w.Code)
	}
	if w := get(t, h, "/v1/board", `"other", `+etag); w.Code != http.StatusNotModified {
		t.Errorf("etag in a list: %d, want 304", w.Code)
	}
	l = strings.Replace(lists, "Read", "Read it", 1)
	w = get(t, h, "/v1/board", etag)
	if w.Code != 200 || w.Header().Get("ETag") == etag {
		t.Errorf("changed list: %d, ETag %s, want 200 and a new ETag", w.Code, w.Header().Get("ETag"))
	}
}

// Health and the placeholder answer, a non-IP Host gets 421, other paths 404.
func TestRoutes(t *testing.T) {
	now, l := time.Unix(0, 0), lists
	h := handler(&now, &l)
	if w := get(t, h, "/v1/health", ""); w.Code != 200 || strings.Join(strings.Fields(w.Body.String()), "") != `{"ok":true,"version":"v1.2.3"}` {
		t.Errorf("health: %d %s", w.Code, w.Body)
	}
	if w := get(t, h, "/", ""); w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("placeholder: %d %s", w.Code, w.Body)
	}
	for host, want := range map[string]int{"evil.example:8417": 421, "evil.example": 421, "localhost:8417": 200, "[::1]:8417": 200, "192.168.1.5:8417": 200} {
		r := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %s: %d, want %d (DNS rebinding)", host, w.Code, want)
		}
	}
	if w := get(t, h, "/x", ""); w.Code != 404 {
		t.Errorf("/x: %d, want 404", w.Code)
	}
	for _, p := range []string{"/v1/answer", "/v1/done", "/v1/start", "/v1/pair"} {
		if w := get(t, h, p, ""); w.Code != 405 || w.Header().Get("Allow") != "POST" {
			t.Errorf("GET %s: %d Allow %q, want 405 POST", p, w.Code, w.Header().Get("Allow"))
		}
	}
}

// The board says whether the manager runs: the page offers Start on a closed boss's item only then.
func TestBoardManagerRuns(t *testing.T) {
	for name, runs := range map[string]func() bool{"nil": nil, "down": func() bool { return false }, "up": func() bool { return true }} {
		h := Handler(Config{Now: time.Now, Guard: noGuard, Pair: http.NotFoundHandler(), ManagerRuns: runs,
			Lists: func() (json.RawMessage, error) { return json.RawMessage(`[]`), nil }})
		want := `"manager_runs": ` + fmt.Sprint(name == "up")
		if w := get(t, h, "/v1/board", ""); !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %s lacks %s", name, w.Body, want)
		}
	}
}

// A board that can't be read is a 500 with a sentence, not the reason (it may name a list).
func TestBoardError(t *testing.T) {
	h := Handler(Config{Now: time.Now, Guard: noGuard, Pair: http.NotFoundHandler(), Lists: func() (json.RawMessage, error) { return nil, errors.New("boom") }})
	logged := captureLog(t)
	if w := get(t, h, "/v1/board", ""); w.Code != 500 || strings.Contains(w.Body.String(), "boom") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	if !strings.Contains(logged.String(), "boom") {
		t.Errorf("log %q lacks the reason", logged)
	}
}

func TestCheck(t *testing.T) {
	for addr, want := range map[string]Kind{"127.0.0.1:1": Loopback, "[::1]:1": Loopback, "100.100.1.2:1": Tailnet,
		"10.0.0.2:1": LAN, "172.16.0.1:1": LAN, "192.168.1.5:8417": LAN, "[::ffff:192.168.1.5]:1": LAN} {
		if _, kind, err := Check(addr); err != nil || kind != want {
			t.Errorf("Check(%s) = %d, %v; want %d", addr, kind, err, want)
		}
	}
	for addr, why := range map[string]string{"0.0.0.0:1": "every interface", "[::]:1": "every interface",
		"8.8.8.8:1": "refused", "100.128.0.1:1": "refused", "[fd00::1]:1": "refused", "169.254.1.1:1": "refused",
		"localhost:1": "want ip:port", ":8417": "want ip:port", "10.0.0.2": "want ip:port"} {
		if _, _, err := Check(addr); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("Check(%s) = %v, want refused: %s", addr, err, why)
		}
	}
}

func TestDefault(t *testing.T) {
	ipnet := func(s string) net.Addr {
		p := netip.MustParsePrefix(s)
		return &net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())}
	}
	lan := func() (netip.Addr, error) { return netip.MustParseAddr("192.168.1.5"), nil }
	addrs := []net.Addr{ipnet("127.0.0.1/8"), ipnet("192.168.1.5/24"), ipnet("100.101.102.103/32")}
	if ap, kind, err := Default(addrs, lan); err != nil || kind != Tailnet || ap.String() != "100.101.102.103:8417" {
		t.Errorf("with a tailnet address: %s %d %v", ap, kind, err)
	}
	cgnat := append(addrs[:2:2], ipnet("100.72.1.2/10"))
	if ap, kind, err := Default(cgnat, lan); err != nil || kind != LAN || ap.String() != "192.168.1.5:8417" {
		t.Errorf("carrier-grade NAT taken for a tailnet: %s %d %v", ap, kind, err)
	}
	if ap, kind, err := Default(addrs[:2], lan); err != nil || kind != LAN || ap.String() != "192.168.1.5:8417" {
		t.Errorf("LAN: %s %d %v", ap, kind, err)
	}
	vpn := func() (netip.Addr, error) { return netip.MustParseAddr("10.8.0.2"), nil }
	if _, _, err := Default(append(addrs[:2:2], ipnet("10.8.0.2/32")), vpn); err == nil || !strings.Contains(err.Error(), "pass --addr") {
		t.Errorf("full-tunnel VPN route: %v, want refused with how to pass --addr", err)
	}
	public := func() (netip.Addr, error) { return netip.MustParseAddr("8.8.8.8"), nil }
	noRoute := func() (netip.Addr, error) { return netip.Addr{}, errors.New("network is unreachable") }
	for name, route := range map[string]func() (netip.Addr, error){"public": public, "no route": noRoute} {
		if _, _, err := Default(addrs[:1], route); err == nil || !strings.Contains(err.Error(), "pass --addr") {
			t.Errorf("%s: %v, want refused with how to pass --addr", name, err)
		}
	}
}

func captureLog(t *testing.T) *strings.Builder {
	var b strings.Builder
	log.SetOutput(&b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &b
}

// A page answers "/" and its own files below it without a session, so Pair can show; the API's routes stay the API's.
func TestPage(t *testing.T) {
	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("page " + r.URL.Path)) })
	auth := &Auth{Dir: t.TempDir(), Now: time.Now}
	h := Handler(Config{Version: "v1.2.3", Now: time.Now, Guard: auth.Session, Pair: http.HandlerFunc(auth.Pair), Page: page,
		Lists: func() (json.RawMessage, error) { return json.RawMessage(lists), nil }})
	for path, want := range map[string]string{"/": "page /", "/app.js": "page /app.js", "/app.css": "page /app.css", "/v1/health": `"ok"`} {
		if w := get(t, h, path, ""); w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %s, want %s", path, w.Code, w.Body, want)
		}
	}
	if w := get(t, h, "/v1/board", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("/v1/board unpaired: %d, want 401", w.Code)
	}
}
