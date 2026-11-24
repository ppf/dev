package serve

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/ppf/dev/internal/todo"
)

// Refused is an answer, tick or start the list or its boss refused: Code is the HTTP status, Err the reason the page shows.
type Refused struct {
	Code int
	Err  error
}

func (r Refused) Error() string { return r.Err.Error() }

// maxAct caps an answer's, tick's or start's body.
const maxAct = 64 << 10

type answerReq struct {
	Boss     string `json:"boss"`
	ID       int    `json:"id"`
	Question string `json:"question"`
	Option   string `json:"option"`
	Text     string `json:"text"`
}

type doneReq struct {
	Boss string `json:"boss"`
	ID   int    `json:"id"`
	Text string `json:"text"`
}

type overruleReq struct {
	Boss   string `json:"boss"`
	ID     int    `json:"id"`
	Text   string `json:"text"`
	Option string `json:"option"`
}

func (req *overruleReq) check() string {
	if strings.TrimSpace(req.Option) == "" {
		return "no option: send the one to overrule with"
	}
	return item(req.Boss, req.ID, req.Text)
}

type doingReq struct {
	Boss string `json:"boss"`
	ID   int    `json:"id"`
	Text string `json:"text"`
	// Seen is the item's doing and start as the page showed them: required
	Seen  *todo.Seen `json:"seen"`
	Doing string     `json:"doing"`
	Stop  bool       `json:"stop"`
}

// fromPage refuses a state-changing call without the page's X-Dev-Page: 1 header, which a cross-site form can't send.
func fromPage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Dev-Page") != "1" {
			writeError(w, http.StatusForbidden, "answers, ticks and starts come from the page only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (cfg Config) answer(w http.ResponseWriter, r *http.Request) {
	var req answerReq
	if !decode(w, r, &req, req.check) {
		return
	}
	answer := req.Option
	if strings.TrimSpace(answer) == "" {
		answer = req.Text
	}
	act(w, cfg.Answer(req.Boss, req.ID, req.Question, answer))
}

func (cfg Config) done(w http.ResponseWriter, r *http.Request) {
	var req doneReq
	if !decode(w, r, &req, req.check) {
		return
	}
	act(w, cfg.Done(req.Boss, req.ID, req.Text))
}

func (cfg Config) start(w http.ResponseWriter, r *http.Request) {
	var req doneReq
	if !decode(w, r, &req, req.check) {
		return
	}
	act(w, cfg.Start(req.Boss, req.ID, req.Text))
}

func (cfg Config) reopen(w http.ResponseWriter, r *http.Request) {
	var req doneReq
	if !decode(w, r, &req, req.check) {
		return
	}
	act(w, cfg.Reopen(req.Boss, req.ID, req.Text))
}

func (cfg Config) overrule(w http.ResponseWriter, r *http.Request) {
	var req overruleReq
	if !decode(w, r, &req, req.check) {
		return
	}
	act(w, cfg.Overrule(req.Boss, req.ID, req.Text, req.Option))
}

func (cfg Config) doing(w http.ResponseWriter, r *http.Request) {
	var req doingReq
	if !decode(w, r, &req, req.check) {
		return
	}
	act(w, cfg.Doing(req.Boss, req.ID, req.Text, *req.Seen, req.Doing, req.Stop))
}

func (req *doingReq) check() string {
	switch {
	case req.Stop && strings.TrimSpace(req.Doing) != "":
		return "send what you do now or stop, not both"
	case req.Seen == nil:
		return "no seen: send the item's doing and started as the page shows them"
	}
	return item(req.Boss, req.ID, req.Text)
}

func (req *answerReq) check() string {
	if (strings.TrimSpace(req.Option) == "") == (strings.TrimSpace(req.Text) == "") {
		return "send an option or your own words, one of them"
	}
	return item(req.Boss, req.ID, req.Question)
}

func (req *doneReq) check() string { return item(req.Boss, req.ID, req.Text) }

// item is what's wrong with an answer's, tick's or start's item as the page names it, "" for nothing: the text is required,
// as item numbers restart once a list empties.
func item(boss string, id int, text string) string {
	switch {
	case boss == "":
		return "no boss"
	case strings.HasPrefix(boss, "local_"):
		return "a desktop app boss's list isn't on the phone page"
	case id < 1:
		return "no item number"
	case text == "":
		return "no item text: send it as the page shows it"
	}
	return ""
}

// decode reads the body into v and checks it, answering 400 when it is no such call.
func decode(w http.ResponseWriter, r *http.Request, v any, check func() string) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAct))
	if err := dec.Decode(v); err != nil || dec.Decode(new(json.RawMessage)) != io.EOF {
		writeError(w, http.StatusBadRequest, "the body isn't the JSON this call takes")
		return false
	}
	if msg := check(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return false
	}
	return true
}

// act answers what an answer, tick or start did; any other error is logged, not sent: a refusal quotes list text, which
// stays out of the log.
func act(w http.ResponseWriter, err error) {
	var ref Refused
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.As(err, &ref):
		writeError(w, ref.Code, ref.Error())
	default:
		log.Printf("dev serve: %v", err)
		writeError(w, http.StatusInternalServerError, "not done: see dev serve's output")
	}
}
