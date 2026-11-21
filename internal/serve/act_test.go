package serve

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/todo"
)

type call struct {
	boss       string
	id         int
	text, pick string
}

func actHandler(calls *[]call, err error) http.Handler {
	return Handler(Config{
		Answer: func(boss string, id int, question, answer string) error {
			*calls = append(*calls, call{boss, id, question, answer})
			return err
		},
		Done: func(boss string, id int, text string) error {
			*calls = append(*calls, call{boss, id, text, ""})
			return err
		},
		Start: func(boss string, id int, text string) error {
			*calls = append(*calls, call{boss, id, text, "start"})
			return err
		},
		Doing: func(boss string, id int, text string, seen todo.Seen, doing string, stop bool) error {
			if stop {
				doing = "stop"
			}
			if seen.Doing != nil {
				doing += " after " + seen.Doing.Text
			}
			*calls = append(*calls, call{boss, id, text, "doing " + doing})
			return err
		},
		Guard: noGuard,
		Pair:  http.NotFoundHandler(),
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
	h := actHandler(&calls, nil)
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
	if w := post(h, "/v1/start", `{"boss":"boss-a","id":3,"text":"Write the notes"}`, true); w.Code != http.StatusOK {
		t.Errorf("start = %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`{"boss":"boss-a","id":4,"text":"Send the cert","seen":{"doing":null,"started":null},"doing":"mailing ops"}`,
		`{"boss":"boss-a","id":4,"text":"Send the cert","seen":{"doing":{"text":"mailing ops","at":5},"started":null},"stop":true}`} {
		if w := post(h, "/v1/doing", body, true); w.Code != http.StatusOK {
			t.Errorf("doing %s = %d %s", body, w.Code, w.Body)
		}
	}
	want := []call{{"boss-a", 1, "Merge?", "Merge"}, {"boss-a", 1, "Merge?", "after the API one"}, {"boss-a", 2, "Read", ""}, {"boss-a", 3, "Write the notes", "start"},
		{"boss-a", 4, "Send the cert", "doing mailing ops"}, {"boss-a", 4, "Send the cert", "doing stop after mailing ops"}}
	if !slices.Equal(calls, want) {
		t.Errorf("calls = %+v, want %+v", calls, want)
	}
}

// Without the page's header, with a malformed body, or with no item text, nothing runs.
func TestActRefused(t *testing.T) {
	cases := []struct {
		name, path, body string
		page             bool
		code             int
	}{
		{"no header", "/v1/answer", `{"boss":"b","id":1,"question":"Q","option":"A"}`, false, http.StatusForbidden},
		{"no header, done", "/v1/done", `{"boss":"b","id":1,"text":"T"}`, false, http.StatusForbidden},
		{"no header, start", "/v1/start", `{"boss":"b","id":1,"text":"T"}`, false, http.StatusForbidden},
		{"desktop boss, start", "/v1/start", `{"boss":"local_1","id":1,"text":"T"}`, true, http.StatusBadRequest},
		{"no text, start", "/v1/start", `{"boss":"b","id":1}`, true, http.StatusBadRequest},
		{"no header, doing", "/v1/doing", `{"boss":"b","id":1,"text":"T","seen":{},"doing":"x"}`, false, http.StatusForbidden},
		{"no seen, doing", "/v1/doing", `{"boss":"b","id":1,"text":"T","doing":"x"}`, true, http.StatusBadRequest},
		{"no text, doing", "/v1/doing", `{"boss":"b","id":1,"doing":"x"}`, true, http.StatusBadRequest},
		{"doing and stop", "/v1/doing", `{"boss":"b","id":1,"text":"T","doing":"x","stop":true}`, true, http.StatusBadRequest},
		{"desktop boss, doing", "/v1/doing", `{"boss":"local_1","id":1,"text":"T"}`, true, http.StatusBadRequest},
		{"no JSON", "/v1/answer", `option=A`, true, http.StatusBadRequest},
		{"no question", "/v1/answer", `{"boss":"b","id":1,"option":"A"}`, true, http.StatusBadRequest},
		{"option and text", "/v1/answer", `{"boss":"b","id":1,"question":"Q","option":"A","text":"B"}`, true, http.StatusBadRequest},
		{"no answer", "/v1/answer", `{"boss":"b","id":1,"question":"Q","text":" "}`, true, http.StatusBadRequest},
		{"no boss", "/v1/done", `{"id":1,"text":"T"}`, true, http.StatusBadRequest},
		{"desktop boss", "/v1/done", `{"boss":"local_1","id":1,"text":"T"}`, true, http.StatusBadRequest},
		{"no text", "/v1/done", `{"boss":"b","id":1}`, true, http.StatusBadRequest},
		{"no id", "/v1/done", `{"boss":"b","text":"T"}`, true, http.StatusBadRequest},
		{"trailing data", "/v1/done", `{"boss":"b","id":1,"text":"T"} {}`, true, http.StatusBadRequest},
		{"too big", "/v1/done", `{"boss":"b","id":1,"text":"` + strings.Repeat("x", maxAct) + `"}`, true, http.StatusBadRequest},
	}
	for _, c := range cases {
		var calls []call
		w := post(actHandler(&calls, nil), c.path, c.body, c.page)
		if w.Code != c.code || len(calls) != 0 || !strings.Contains(w.Body.String(), `"error"`) {
			t.Errorf("%s: %d %s, calls %v; want %d", c.name, w.Code, w.Body, calls, c.code)
		}
	}
}

// A refusal answers its code with its reason; any other error is logged and answers 500 without it.
func TestActErrors(t *testing.T) {
	var calls []call
	stale := Refused{Code: http.StatusConflict, Err: errors.New(`item 1 is answered already: Merge`)}
	w := post(actHandler(&calls, stale), "/v1/answer", `{"boss":"b","id":1,"question":"Q","option":"A"}`, true)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "answered already: Merge") {
		t.Errorf("refused = %d %s", w.Code, w.Body)
	}
	again := Refused{Code: http.StatusConflict, Err: errors.New(`item 1: already sent to boss-a 2m ago`)}
	w = post(actHandler(&calls, again), "/v1/start", `{"boss":"b","id":1,"text":"T"}`, true)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already sent to boss-a") {
		t.Errorf("start refused = %d %s", w.Code, w.Body)
	}
	other := Refused{Code: http.StatusConflict, Err: errors.New(`item 1 is on fix-login, not the user's`)}
	w = post(actHandler(&calls, other), "/v1/doing", `{"boss":"b","id":1,"text":"T","seen":{},"doing":"x"}`, true)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "not the user's") {
		t.Errorf("doing refused = %d %s", w.Code, w.Body)
	}
	logged := captureLog(t)
	w = post(actHandler(&calls, errors.New("boom")), "/v1/done", `{"boss":"b","id":1,"text":"T"}`, true)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "boom") || !strings.Contains(logged.String(), "boom") {
		t.Errorf("error = %d %s, log %q", w.Code, w.Body, logged)
	}
}
