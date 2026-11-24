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
// to a server. On a tailnet or LAN bind the Guard's session cookie stands in; on loopback the Guard stops only another
// origin's page, not a process on the Mac.
func pageAnswer(boss string, id int, question, answer string) error {
	c := tmux.New()
	if !running(c, boss) {
		return serve.Refused{Code: http.StatusNotFound, Err: fmt.Errorf("%s isn't running: its asks can't be answered", boss)}
	}
	it, b, pane, err := answerTick(c, boss, id, question, answer, todo.ViaPage, 0, nil)
	if err != nil {
		return refused(err)
	}
	note := inbox.AnswerPage(it.ID, it.Text, it.Answer)
	queued, err := b.Send(c, control.RealClock, note)
	switch {
	case !queued:
		err = fmt.Errorf("#%d answered and ticked, not sent to %s (tell it yourself): %w", id, boss, err)
		log.Printf("dev serve: %v", err)
		return serve.Refused{Code: http.StatusInternalServerError, Err: err}
	case err != nil:
		log.Printf("dev serve: #%d queued in %s's inbox, delivered when it next rests: %v", id, boss, err)
	}
	tellAbove(c, pane, it, note, func(s string) { log.Printf("dev serve: %s", s) })
	return nil
}

// pageDone is dev todo done --text from the phone page.
func pageDone(stderr io.Writer) func(boss string, id int, text string) error {
	return func(boss string, id int, text string) error {
		s, closed, err := listStore(boss, true, stderr)
		switch {
		case err != nil && !errors.As(err, new(unreadList)) && !running(tmux.New(), boss):
			// A closed boss's list leaves once its last item is ticked: that and an unknown boss look the same
			log.Printf("dev serve: %v", err)
			return serve.Refused{Code: http.StatusNotFound, Err: fmt.Errorf("no list for %s: not running, and no closed list with an open item", boss)}
		case err != nil:
			return err
		}
		now := time.Now()
		ticked, err := openItems(s, []int{id}, now)
		if err != nil {
			return err
		}
		if err := s.DoneText(id, text, now); err != nil {
			return refused(err)
		}
		tellTicked(tmux.New(), boss, closed, ticked, wherePage, stderr)
		return nil
	}
}

func pageStart(stderr io.Writer) func(boss string, id int, text string) error {
	return func(boss string, id int, text string) error {
		_, err := startItem(tmux.New(), boss, id, text, stderr)
		if errors.As(err, new(notStarted)) {
			return serve.Refused{Code: http.StatusConflict, Err: err}
		}
		return refused(err)
	}
}

// pageDoing is dev todo doing --question from the phone page, refused once the item's doing or start changed since the page read it.
func pageDoing(stderr io.Writer) func(boss string, id int, text string, seen todo.Seen, doing string, stop bool) error {
	return func(boss string, id int, text string, seen todo.Seen, doing string, stop bool) error {
		c := tmux.New()
		_, err := userDoing(c, boss, id, text, &seen, doing, stop, stderr)
		switch {
		case errors.As(err, new(noList)) && !errors.As(err, new(unreadList)) && !running(c, boss):
			log.Printf("dev serve: %v", err)
			return serve.Refused{Code: http.StatusNotFound, Err: fmt.Errorf("no list for %s: not running, and no closed list with an open item", boss)}
		case errors.As(err, new(notTold)):
			log.Printf("dev serve: %v", err)
			return serve.Refused{Code: http.StatusInternalServerError, Err: err}
		}
		return refused(err)
	}
}

// pageReopen is dev todo reopen --question from the phone page.
func pageReopen(stderr io.Writer) func(boss string, id int, text string) error {
	return func(boss string, id int, text string) error {
		_, err := reopenItem(boss, id, text, stderr)
		if errors.As(err, new(noReopenList)) && !errors.As(err, new(unreadList)) && !running(tmux.New(), boss) {
			log.Printf("dev serve: %v", err)
			return serve.Refused{Code: http.StatusNotFound, Err: fmt.Errorf("no list for %s: not running, and no closed list with a ticked item", boss)}
		}
		return refused(err)
	}
}

// pageOverrule is dev todo overrule --question from the phone page: its boss must run, to be told.
func pageOverrule(stderr io.Writer) func(boss string, id int, text, option string) error {
	return func(boss string, id int, text, option string) error {
		c := tmux.New()
		if !running(c, boss) {
			return serve.Refused{Code: http.StatusNotFound, Err: fmt.Errorf("%s isn't running: its decisions can't be overruled", boss)}
		}
		_, err := overruleItem(c, stderr, boss, id, text, option, todo.ViaPage)
		if errors.As(err, new(notTold)) {
			log.Printf("dev serve: %v", err)
			return serve.Refused{Code: http.StatusInternalServerError, Err: err}
		}
		return refused(err)
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
