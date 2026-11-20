package cmd

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/serve"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// pageAnswer is dev todo answer --question from the phone page: its note says so. $CLAUDECODE's refusal can't apply
// to a server; the Guard is what keeps an agent on the Mac from answering as the user (S2's session cookie).
func pageAnswer(boss string, id int, question, answer string) error {
	c := tmux.New()
	if !running(c, boss) {
		return serve.Refused{Code: http.StatusNotFound, Err: fmt.Errorf("%s isn't running: its asks can't be answered", boss)}
	}
	it, b, err := answerTick(c, boss, id, question, answer, 0, nil)
	if err != nil {
		return refused(err)
	}
	queued, err := b.Send(c, control.RealClock, inbox.AnswerPage(it.ID, it.Text, it.Answer))
	switch {
	case !queued:
		err = fmt.Errorf("#%d answered and ticked, not sent to %s (tell it yourself): %w", id, boss, err)
		log.Printf("dev serve: %v", err)
		return serve.Refused{Code: http.StatusInternalServerError, Err: err}
	case err != nil:
		log.Printf("dev serve: #%d queued in %s's inbox, delivered when it next rests: %v", id, boss, err)
	}
	return nil
}

// pageDone is dev todo done --text from the phone page.
func pageDone(stderr io.Writer) func(boss string, id int, text string) error {
	return func(boss string, id int, text string) error {
		s, _, err := listStore(boss, true, stderr)
		switch {
		case err != nil && !running(tmux.New(), boss):
			// A closed boss's list leaves once its last item is ticked
			return serve.Refused{Code: http.StatusNotFound, Err: fmt.Errorf("%s is closed and its list has no open item", boss)}
		case err != nil:
			return err
		}
		return refused(s.DoneText(id, text, time.Now()))
	}
}

// running is whether boss's session is on c's server.
func running(c *tmux.Client, boss string) bool {
	_, err := c.Run("has-session", "-t", tmux.SessionTarget(boss))
	return err == nil
}

// refused is err as a 409 when the list refused it, else err as is: the page's 500, logged.
func refused(err error) error {
	if errors.As(err, new(todo.Refused)) {
		return serve.Refused{Code: http.StatusConflict, Err: err}
	}
	return err
}
