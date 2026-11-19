package serve

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type call struct {
	boss       string
	id         int
	text, pick string
}

func actHandler(kind Kind, calls *[]call, err error) http.Handler {
	return Handler(Config{
		Answer: func(boss string, id int, question, answer string) error {
			*calls = append(*calls, call{boss, id, question, answer})
			return err
		},
		Done: func(boss string, id int, text string) error {
			*calls = append(*calls, call{boss, id, text, ""})
			return err
		},
		Guard: LoopbackOnly(kind),
	})
}

func post(h http.Handler, path, body string, page bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8417"+path, strings.NewReader(body))
	if page {
		r.Header.Set("X-Dev-Page", "1")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// An answer passes the option or own words through; a tick its text; each answers ok.
func TestActOK(t *testing.T) {
	var calls []call
	h := actHandler(Loopback, &calls, nil)
	for _, body := range []string{
		`{"boss":"boss-a","id":1,"question":"Merge?","option":"Merge"}`,
		`{"boss":"boss-a","id":1,"question":"Merge?","text":"after the API one"}`,
	} {
		if w := post(h, "/v1/answer", body, true); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok": true`) {
			t.Errorf("answer %s = %d %s", body, w.Code, w.Body)
		}
	}
	if w := post(h, "/v1/done", `{"boss":"boss-a","id":2,"text":"Read"}`, true); w.Code != http.StatusOK {
		t.Errorf("done = %d %s", w.Code, w.Body)
	}
	want := []call{{"boss-a", 1, "Merge?", "Merge"}, {"boss-a", 1, "Merge?", "after the API one"}, {"boss-a", 2, "Read", ""}}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] || calls[2] != want[2] {
		t.Errorf("calls = %+v, want %+v", calls, want)
	}
}

// Without the page's header, off loopback, with a malformed body, or with no item text, nothing runs.
func TestActRefused(t *testing.T) {
	cases := []struct {
		name, path, body string
		kind             Kind
		page             bool
		code             int
	}{
		{"no header", "/v1/answer", `{"boss":"b","id":1,"question":"Q","option":"A"}`, Loopback, false, http.StatusForbidden},
		{"no header, done", "/v1/done", `{"boss":"b","id":1,"text":"T"}`, Loopback, false, http.StatusForbidden},
		{"off loopback", "/v1/answer", `{"boss":"b","id":1,"question":"Q","option":"A"}`, Tailnet, true, http.StatusForbidden},
		{"no JSON", "/v1/answer", `option=A`, Loopback, true, http.StatusBadRequest},
		{"no question", "/v1/answer", `{"boss":"b","id":1,"option":"A"}`, Loopback, true, http.StatusBadRequest},
		{"option and text", "/v1/answer", `{"boss":"b","id":1,"question":"Q","option":"A","text":"B"}`, Loopback, true, http.StatusBadRequest},
		{"no answer", "/v1/answer", `{"boss":"b","id":1,"question":"Q","text":" "}`, Loopback, true, http.StatusBadRequest},
		{"no boss", "/v1/done", `{"id":1,"text":"T"}`, Loopback, true, http.StatusBadRequest},
		{"desktop boss", "/v1/done", `{"boss":"local_1","id":1,"text":"T"}`, Loopback, true, http.StatusBadRequest},
		{"no text", "/v1/done", `{"boss":"b","id":1}`, Loopback, true, http.StatusBadRequest},
		{"no id", "/v1/done", `{"boss":"b","text":"T"}`, Loopback, true, http.StatusBadRequest},
		{"trailing data", "/v1/done", `{"boss":"b","id":1,"text":"T"} {}`, Loopback, true, http.StatusBadRequest},
		{"too big", "/v1/done", `{"boss":"b","id":1,"text":"` + strings.Repeat("x", maxAct) + `"}`, Loopback, true, http.StatusBadRequest},
	}
	for _, c := range cases {
		var calls []call
		w := post(actHandler(c.kind, &calls, nil), c.path, c.body, c.page)
		if w.Code != c.code || len(calls) != 0 || !strings.Contains(w.Body.String(), `"error"`) {
			t.Errorf("%s: %d %s, calls %v; want %d", c.name, w.Code, w.Body, calls, c.code)
		}
	}
}

// A refusal answers its code with its reason; any other error is logged and answers 500 without it.
func TestActErrors(t *testing.T) {
	var calls []call
	stale := Refused{Code: http.StatusConflict, Err: errors.New(`item 1 is answered already: Merge`)}
	w := post(actHandler(Loopback, &calls, stale), "/v1/answer", `{"boss":"b","id":1,"question":"Q","option":"A"}`, true)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "answered already: Merge") {
		t.Errorf("refused = %d %s", w.Code, w.Body)
	}
	logged := captureLog(t)
	w = post(actHandler(Loopback, &calls, errors.New("boom")), "/v1/done", `{"boss":"b","id":1,"text":"T"}`, true)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "boom") || !strings.Contains(logged.String(), "boom") {
		t.Errorf("error = %d %s, log %q", w.Code, w.Body, logged)
	}
}
