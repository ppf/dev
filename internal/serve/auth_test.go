package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type authed struct {
	h   http.Handler
	dir string
	now *time.Time
}

func newAuthed(t *testing.T) authed {
	now, l := time.Unix(1700000000, 0), lists
	a := &Auth{Dir: filepath.Join(t.TempDir(), "serve"), Now: func() time.Time { return now }}
	cfg := Config{Version: "v1", Now: a.Now, Lists: func() (json.RawMessage, error) { return json.RawMessage(l), nil },
		Guard: a.Session, Pair: http.HandlerFunc(a.Pair),
		Answer: func(string, int, string, string) error { return nil }, Done: func(string, int, string) error { return nil }}
	return authed{Handler(cfg), a.Dir, &now}
}

func (s authed) do(method, path, body string, cookie *http.Cookie, header bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://192.168.1.5:8417"+path, strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if header {
		r.Header.Set("X-Dev-Page", "1")
	}
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	return w
}

func (s authed) code(t *testing.T) string {
	t.Helper()
	code, err := NewCode(s.dir, *s.now)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func (s authed) pair(code, device string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"code": code, "device": device})
	return s.do(http.MethodPost, "/v1/pair", string(b), nil, true)
}

const actBody = `{"boss":"b","id":1,"question":"Q","option":"A","text":"T"}`

func refused(t *testing.T, what string, w *httptest.ResponseRecorder, code int, why string) {
	t.Helper()
	var e struct{ Error string }
	if w.Code != code || json.Unmarshal(w.Body.Bytes(), &e) != nil || !strings.Contains(e.Error, why) {
		t.Errorf("%s: %d %s, want %d with %q", what, w.Code, w.Body, code, why)
	}
}

// A cheap code hash keeps the tests fast; BenchmarkCodeGuess times the real one.
func init() { codeIter = 1000 }

// One guess at the real cost: go test ./internal/serve -run - -bench CodeGuess
func BenchmarkCodeGuess(b *testing.B) {
	p := pending{Salt: strings.Repeat("ab", 16), Iter: defaultCodeIter}
	for b.Loop() {
		if _, err := p.derive("12345678"); err != nil {
			b.Fatal(err)
		}
	}
}

// The code file holds a salted PBKDF2 hash with its parameters, never the code or its plain SHA-256; a record this
// dev serve can't check (an older format, a bad salt or iteration count) is dropped, not counted as a wrong code.
func TestCodeFile(t *testing.T) {
	s := newAuthed(t)
	code := strings.ReplaceAll(s.code(t), " ", "")
	raw, err := os.ReadFile(filepath.Join(s.dir, codeFile))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	keys := slices.Sorted(maps.Keys(fields))
	if strings.Join(keys, ",") != "code_pbkdf2_sha256,expires,iterations,salt" {
		t.Errorf("code file fields %v", keys)
	}
	var p pending
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Hash == hash(code) || len(p.Salt) != 32 || p.Iter != codeIter {
		t.Errorf("code file %s", raw)
	}
	if got, err := p.derive(code); err != nil || got != p.Hash {
		t.Errorf("derive: %s %v, want the stored hash", got, err)
	}
	if got, _ := (pending{Salt: strings.Repeat("0", 32), Iter: p.Iter}).derive(code); got == p.Hash {
		t.Error("the salt doesn't change the hash")
	}

	for name, bad := range map[string]string{
		"old format":      `{"code_sha256":"` + hash(code) + `","expires":%d}`,
		"no iterations":   `{"code_pbkdf2_sha256":"` + p.Hash + `","salt":"` + p.Salt + `","expires":%d}`,
		"huge iterations": `{"code_pbkdf2_sha256":"` + p.Hash + `","salt":"` + p.Salt + `","iterations":2000000000,"expires":%d}`,
		"short salt":      `{"code_pbkdf2_sha256":"` + p.Hash + `","salt":"00","iterations":1000,"expires":%d}`,
	} {
		if err := os.WriteFile(filepath.Join(s.dir, codeFile), fmt.Appendf(nil, bad, p.Expires), 0o600); err != nil {
			t.Fatal(err)
		}
		refused(t, name, s.pair(code, "p"), 401, "run dev serve pair on the Mac again")
		refused(t, name+", then", s.pair(code, "p"), 401, "no pairing code is pending")
	}
}

// A code pairs once: the cookie reads the board, the files are 0600 and hold no secret.
func TestPair(t *testing.T) {
	s := newAuthed(t)
	code := s.code(t)
	w := s.pair(code, "  iPhone\x1b[31m\u202e  ")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"device": "iPhone[31m"`) {
		t.Fatalf("pair: %d %s", w.Code, w.Body)
	}
	c := w.Result().Cookies()[0]
	if c.Name != "dev_session_8417" || len(c.Value) != 64 || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge <= 0 || c.Secure {
		t.Errorf("cookie %+v", c)
	}
	if w := s.do(http.MethodGet, "/v1/board", "", c, false); w.Code != 200 || !strings.Contains(w.Body.String(), `"decide": 1`) {
		t.Errorf("board with the cookie: %d %s", w.Code, w.Body)
	}
	if w := s.do(http.MethodPost, "/v1/done", actBody, c, true); w.Code != 200 {
		t.Errorf("done with cookie and header: %d %s", w.Code, w.Body)
	}
	refused(t, "the code again", s.pair(code, "other"), 401, "no pairing code is pending")
	stored, err := os.ReadFile(filepath.Join(s.dir, sessionFile))
	if err != nil || strings.Contains(string(stored), c.Value) || !strings.Contains(string(stored), hash(c.Value)) {
		t.Errorf("sessions file %s, %v: want the token's hash only", stored, err)
	}
	for _, name := range []string{sessionFile, "lock"} {
		if fi, err := os.Stat(filepath.Join(s.dir, name)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v, want 0600", name, fi.Mode(), err)
		}
	}
	if fi, err := os.Stat(s.dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir: %v %v, want 0700", fi.Mode(), err)
	}
}

// Cookies ignore the port, so a second dev serve on the host must not read or replace this one's session.
func TestCookiePerPort(t *testing.T) {
	s := newAuthed(t)
	c := s.pair(s.code(t), "phone").Result().Cookies()[0]
	other := &http.Cookie{Name: "dev_session_8418", Value: c.Value}
	refused(t, "a cookie named for another port", s.do(http.MethodGet, "/v1/board", "", other, false), 401, "isn't paired")
}

// A Host without a numeric port (a proxy, a hostile header) gets the plain name, a valid cookie name.
func TestCookieNameHost(t *testing.T) {
	for host, want := range map[string]string{"h:8417": "dev_session_8417", "[::1]:8417": "dev_session_8417", "h": "dev_session", "h:8 4": "dev_session", "h:;": "dev_session"} {
		if got := cookieName(&http.Request{Host: host}); got != want {
			t.Errorf("%q: %q, want %q", host, got, want)
		}
	}
}

// Each refusal answers its status with a sentence.
func TestAuthRefusals(t *testing.T) {
	s := newAuthed(t)
	refused(t, "board without cookie", s.do(http.MethodGet, "/v1/board", "", nil, false), 401, "isn't paired")
	refused(t, "no code pending", s.pair("12345678", "p"), 401, "no pairing code is pending")
	code := s.code(t)
	c := s.pair(code, "phone").Result().Cookies()[0]
	wrong := &http.Cookie{Name: "dev_session_8417", Value: strings.Repeat("0", 64)}
	refused(t, "board with a wrong cookie", s.do(http.MethodGet, "/v1/board", "", wrong, false), 401, "isn't paired")
	for _, path := range []string{"/v1/answer", "/v1/done"} {
		refused(t, path+" without cookie", s.do(http.MethodPost, path, actBody, nil, true), 401, "isn't paired")
		refused(t, path+" with a wrong cookie", s.do(http.MethodPost, path, actBody, wrong, true), 401, "isn't paired")
		refused(t, path+" without header", s.do(http.MethodPost, path, actBody, c, false), 403, "from the page only")
	}
	refused(t, "pair not JSON", s.do(http.MethodPost, "/v1/pair", "code=1", nil, true), 400, "send {")
	refused(t, "pair without device", s.pair(s.code(t), " \n"), 400, "name the device")
	refused(t, "pair body too big", s.do(http.MethodPost, "/v1/pair", `{"device":"`+strings.Repeat("x", 2000)+`"}`, nil, true), 400, "send {")

	// A cross-site form (text/plain, no X-Dev-Page) is refused before it costs the code a try
	code = s.code(t)
	for range maxMisses {
		refused(t, "cross-site pair", s.do(http.MethodPost, "/v1/pair", `{"code":"0","device":"p"}`, nil, false), 400, "application/json")
	}
	r := httptest.NewRequest(http.MethodPost, "http://192.168.1.5:8417/v1/pair", strings.NewReader(`{"code":"`+code+`","device":"p"}`))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	w := httptest.NewRecorder()
	if s.h.ServeHTTP(w, r); w.Code != 200 {
		t.Errorf("JSON pair after cross-site tries: %d %s", w.Code, w.Body)
	}

	code = s.code(t)
	*s.now = s.now.Add(CodeTTL)
	refused(t, "expired code", s.pair(code, "p"), 401, "expired")
	refused(t, "expired code again", s.pair(code, "p"), 401, "no pairing code is pending")

	code = s.code(t)
	for i := 1; i < maxMisses; i++ {
		refused(t, "wrong code", s.pair("00000000", "p"), 401, "wrong code")
	}
	refused(t, "a miss too many", s.pair("00000000", "p"), 429, "too many wrong codes")
	refused(t, "the right code after the limit", s.pair(code, "p"), 401, "no pairing code is pending")

	// Misses count per code: a new code gets every try
	code = s.code(t)
	refused(t, "a new code's first miss", s.pair("00000000", "p"), 401, "wrong code")
	if w := s.pair(code, "p"); w.Code != 200 {
		t.Errorf("new code: %d %s", w.Code, w.Body)
	}
}

// Unpair drops one device, or every one; re-pairing a device replaces its session.
func TestUnpair(t *testing.T) {
	s := newAuthed(t)
	pair := func(device string) *http.Cookie {
		w := s.pair(s.code(t), device)
		if w.Code != 200 {
			t.Fatalf("pair %s: %d %s", device, w.Code, w.Body)
		}
		return w.Result().Cookies()[0]
	}
	old, phone, pad := pair("phone"), pair("phone"), pair("pad")
	board := func(c *http.Cookie) int { return s.do(http.MethodGet, "/v1/board", "", c, false).Code }
	if board(old) != 401 || board(phone) != 200 || board(pad) != 200 {
		t.Errorf("re-paired: old %d phone %d pad %d, want 401 200 200", board(old), board(phone), board(pad))
	}
	if got, err := Unpair(s.dir, "phone"); err != nil || strings.Join(got, ",") != "phone" {
		t.Errorf("unpair phone: %v %v", got, err)
	}
	if board(phone) != 401 || board(pad) != 200 {
		t.Errorf("after unpair phone: phone %d pad %d", board(phone), board(pad))
	}
	if got, err := Unpair(s.dir, " \u200b"); err == nil || got != nil || board(pad) != 200 {
		t.Errorf("unpair a blank name: %v %v, want refused, pad kept", got, err)
	}
	if got, err := Unpair(s.dir, "watch"); err != nil || got != nil {
		t.Errorf("unpair unknown: %v %v", got, err)
	}
	if got, err := Unpair(s.dir, ""); err != nil || strings.Join(got, ",") != "pad" || board(pad) != 401 {
		t.Errorf("unpair all: %v %v", got, err)
	}
}

func TestServed(t *testing.T) {
	local := func(netip.Addr) bool { return true }
	health := func(w http.ResponseWriter, body string, code int) { w.WriteHeader(code); _, _ = w.Write([]byte(body)) }
	for _, tt := range []struct {
		name, body string
		code       int
		version    string
		running    bool
	}{
		{"dev serve", `{"ok":true,"version":"v0.72.0"}`, 200, "v0.72.0", true},
		{"no version", `{"ok":true}`, 200, "", false},
		{"another program", `hello`, 200, "", false},
		{"500", `{"ok":true}`, 500, "", false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { health(w, tt.body, tt.code) }))
		dir := t.TempDir()
		ap := netip.MustParseAddrPort(srv.Listener.Addr().String())
		if _, err := RecordAddr(dir, ap, false); err != nil {
			t.Fatal(err)
		}
		got, err := Served(dir, local, time.Second)
		if tt.running && (err != nil || !slices.Equal(got, []Running{{ap, tt.version}})) || !tt.running && !errors.Is(err, ErrNotRunning) {
			t.Errorf("%s: %v %v", tt.name, got, err)
		}
		srv.Close()
		if _, err := Served(dir, local, time.Second); !errors.Is(err, ErrNotRunning) {
			t.Errorf("%s closed: %v", tt.name, err)
		}
	}
	if _, err := Served(t.TempDir(), local, time.Second); !errors.Is(err, ErrNotRunning) {
		t.Errorf("no record: %v", err)
	}
	for _, rec := range []string{`"127.0.`, `{}`} {
		bad := t.TempDir()
		if err := os.WriteFile(filepath.Join(bad, legacyAddrFile), []byte(rec), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(bad, addrDir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bad, addrDir, "127.0.0.1:1.json"), []byte(rec), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Served(bad, local, time.Second); !errors.Is(err, ErrNotRunning) {
			t.Errorf("corrupt record %s: %v", rec, err)
		}
	}
}

// fakeServe is a dev serve for TestServedMany: its health answer and record.
type fakeServe struct {
	srv    *httptest.Server
	ap     netip.AddrPort
	forget func() error
}

func startFake(t *testing.T, dir string) *fakeServe {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"version":"v1"}`))
	}))
	t.Cleanup(srv.Close)
	f := &fakeServe{srv: srv, ap: netip.MustParseAddrPort(srv.Listener.Addr().String())}
	var err error
	if f.forget, err = RecordAddr(dir, f.ap, false); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fakeServe) stop(t *testing.T) {
	t.Helper()
	f.srv.Close()
	if err := f.forget(); err != nil {
		t.Fatal(err)
	}
}

func addrsOf(rs []Running) []netip.AddrPort {
	var aps []netip.AddrPort
	for _, r := range rs {
		aps = append(aps, r.Addr)
	}
	return aps
}

func deadPid(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid
}

// #146: any number of dev serves on one state folder, stopped in any order or crashed: Served lists the ones that answer.
func TestServedMany(t *testing.T) {
	local := func(netip.Addr) bool { return true }
	want := func(t *testing.T, dir string, live ...*fakeServe) {
		t.Helper()
		var aps []netip.AddrPort
		for _, f := range live {
			aps = append(aps, f.ap)
		}
		slices.SortFunc(aps, netip.AddrPort.Compare)
		got, err := Served(dir, local, time.Second)
		if len(live) == 0 && !errors.Is(err, ErrNotRunning) || len(live) > 0 && (err != nil || !slices.Equal(addrsOf(got), aps)) {
			t.Fatalf("got %v %v, want %v", addrsOf(got), err, aps)
		}
	}
	for _, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		t.Run(fmt.Sprint("stop order ", order), func(t *testing.T) {
			dir := t.TempDir()
			serves := []*fakeServe{startFake(t, dir), startFake(t, dir), startFake(t, dir)}
			want(t, dir, serves...)
			for i, n := range order {
				serves[n].stop(t)
				var live []*fakeServe
				for _, m := range order[i+1:] {
					live = append(live, serves[m])
				}
				want(t, dir, live...)
			}
		})
	}
	t.Run("crash", func(t *testing.T) {
		dir := t.TempDir()
		a, b := startFake(t, dir), startFake(t, dir)
		b.srv.Close() // no forget
		want(t, dir, a)
		crashed := filepath.Join(dir, addrDir, b.ap.String()+".json")
		if err := writeFile(filepath.Join(dir, addrDir), b.ap.String()+".json", addrRecord{Addr: b.ap.String(), Pid: deadPid(t)}); err != nil {
			t.Fatal(err)
		}
		want(t, dir, a)
		startFake(t, dir) // drops the record of a process gone
		if _, err := os.Stat(crashed); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("crashed record kept: %v", err)
		}
	})
	t.Run("same address, next serve", func(t *testing.T) {
		dir := t.TempDir()
		a := startFake(t, dir)
		a.srv.Close()
		// Another process bound a's address after a closed its listener
		next := addrRecord{Addr: a.ap.String(), Pid: os.Getppid()}
		if err := writeFile(filepath.Join(dir, addrDir), a.ap.String()+".json", next); err != nil {
			t.Fatal(err)
		}
		if err := a.forget(); err != nil {
			t.Fatal(err)
		}
		var got addrRecord
		if err := readFile(filepath.Join(dir, addrDir), a.ap.String()+".json", &got); err != nil || got != next {
			t.Errorf("the next serve's record: %v %v, want kept", got, err)
		}
	})
	t.Run("another machine's address", func(t *testing.T) {
		dir := t.TempDir()
		a := startFake(t, dir)
		if _, err := Served(dir, func(netip.Addr) bool { return false }, time.Second); !errors.Is(err, ErrNotRunning) ||
			!strings.Contains(err.Error(), "runs on "+a.ap.String()+", no longer this Mac's address") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("reload re-bound elsewhere", func(t *testing.T) {
		dir := t.TempDir()
		a, b := startFake(t, dir), startFake(t, dir) // b: the same pid before its reload's exec
		if _, err := RecordAddr(dir, netip.AddrPortFrom(b.ap.Addr(), b.ap.Port()+1), true); err != nil {
			t.Fatal(err)
		}
		names, err := recordNames(filepath.Join(dir, addrDir))
		if err != nil || len(names) != 1 || names[0] != netip.AddrPortFrom(b.ap.Addr(), b.ap.Port()+1).String()+".json" {
			t.Errorf("records %q %v, want the new address only (a %s is this pid's too)", names, err, a.ap)
		}
	})
	t.Run("live address, process gone", func(t *testing.T) {
		dir := t.TempDir()
		a := startFake(t, dir)
		if err := writeFile(filepath.Join(dir, addrDir), a.ap.String()+".json", addrRecord{Addr: a.ap.String(), Pid: deadPid(t)}); err != nil {
			t.Fatal(err)
		}
		want(t, dir)
	})
	t.Run("unreadable record", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, addrDir), 0o700); err != nil {
			t.Fatal(err)
		}
		bad := filepath.Join(dir, addrDir, "127.0.0.1:1.json")
		if err := os.WriteFile(bad, []byte(`{}`), 0o000); err != nil {
			t.Fatal(err)
		}
		if _, err := os.ReadFile(bad); err == nil {
			t.Skip("mode 000 doesn't stop this user reading it (root)")
		}
		if _, err := Served(dir, local, time.Second); err == nil || errors.Is(err, ErrNotRunning) {
			t.Errorf("alone: %v, want its read error", err)
		}
		startFake(t, t.TempDir()) // nor does it stop a start beside it
		if _, err := RecordAddr(dir, netip.MustParseAddrPort("127.0.0.1:2"), false); err != nil {
			t.Errorf("a start beside it: %v", err)
		}
		a := startFake(t, t.TempDir())
		if err := writeFile(filepath.Join(dir, addrDir), a.ap.String()+".json", addrRecord{Addr: a.ap.String(), Pid: os.Getpid()}); err != nil {
			t.Fatal(err)
		}
		want(t, dir, a) // beside a live one: that one
	})
	t.Run("ipv6", func(t *testing.T) {
		ln, err := net.Listen("tcp", "[::1]:0")
		if err != nil {
			t.Skipf("no ::1: %v", err)
		}
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true,"version":"v1"}`))
		}))
		srv.Listener = ln
		srv.Start()
		defer srv.Close()
		dir := t.TempDir()
		ap := netip.MustParseAddrPort(ln.Addr().String())
		forget, err := RecordAddr(dir, ap, false)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := Served(dir, local, time.Second); err != nil || !slices.Equal(addrsOf(got), []netip.AddrPort{ap}) {
			t.Fatalf("got %v %v", got, err)
		}
		if err := forget(); err != nil {
			t.Fatal(err)
		}
		want(t, dir)
	})
	t.Run("damaged record beside a live one", func(t *testing.T) {
		dir := t.TempDir()
		a := startFake(t, dir)
		if err := os.WriteFile(filepath.Join(dir, addrDir, "127.0.0.1:1.json"), []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
		want(t, dir, a)
		startFake(t, dir) // a damaged record doesn't stop a start
	})
	t.Run("legacy record", func(t *testing.T) {
		dir := t.TempDir()
		old := startFake(t, t.TempDir()) // a v0.73.0 dev serve: its one record is addr.json
		if err := writeFile(dir, legacyAddrFile, old.ap.String()); err != nil {
			t.Fatal(err)
		}
		a := startFake(t, dir) // another address: the legacy record stays
		want(t, dir, a, old)
		if err := writeFile(dir, legacyAddrFile, a.ap.String()); err != nil { // and a's own address once
			t.Fatal(err)
		}
		want(t, dir, a)
		if err := writeFile(dir, legacyAddrFile, old.ap.String()); err != nil {
			t.Fatal(err)
		}
		old.srv.Close()
		if _, err := RecordAddr(dir, old.ap, false); err != nil { // a dev serve on its address now
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, legacyAddrFile)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("legacy record of its own address kept: %v", err)
		}
	})
}

func TestPhoneFirst(t *testing.T) {
	rs := []Running{{Addr: netip.MustParseAddrPort("127.0.0.1:8417")}, {Addr: netip.MustParseAddrPort("192.168.1.9:8417")},
		{Addr: netip.MustParseAddrPort("100.64.0.2:8417")}}
	slices.SortFunc(rs, phoneFirst)
	if got := fmt.Sprint(addrsOf(rs)); got != "[100.64.0.2:8417 192.168.1.9:8417 127.0.0.1:8417]" {
		t.Errorf("got %s", got)
	}
}

func TestAlive(t *testing.T) {
	dir := t.TempDir()
	if alive, err := AnyAlive(dir); err != nil || alive {
		t.Fatalf("none: %v %v", alive, err)
	}
	first, err := Alive(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Alive(dir) // a second dev serve on the same folder
	if err != nil {
		t.Fatal(err)
	}
	if alive, err := AnyAlive(dir); err != nil || !alive {
		t.Fatalf("held: %v %v", alive, err)
	}
	_ = first.Close()
	_ = second.Close()
	if alive, err := AnyAlive(dir); err != nil || alive {
		t.Fatalf("released: %v %v", alive, err)
	}
}
