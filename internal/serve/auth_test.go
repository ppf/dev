package serve

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
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
