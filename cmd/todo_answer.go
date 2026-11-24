package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// answerTick ticks ask id on boss name's list with the user's answer given via (todo.Store.AnswerPick) and returns it
// with the boss to send the note to and its pane; resolving the boss first, so an answer it can't send stays open.
// claim, when set, runs right before ticking and gives the user's pick; its undo runs when the tick is refused.
func answerTick(c *tmux.Client, name string, id int, question, answer, via string, before int64, claim func() (picked string, undo func() error, err error)) (todo.Item, inbox.Boss, string, error) {
	b, err := inbox.Recipient(c, name)
	if err != nil {
		return todo.Item{}, b, "", err
	}
	pane, err := control.BossPane(c, name)
	if err != nil {
		return todo.Item{}, b, pane, err
	}
	s, err := todo.ForPane(c, pane)
	if err != nil {
		return todo.Item{}, b, pane, err
	}
	var picked string
	undo := func() error { return nil }
	if claim != nil {
		if picked, undo, err = claim(); err != nil {
			return todo.Item{}, b, pane, err
		}
	}
	it, err := s.AnswerPick(id, question, answer, picked, via, before, time.Now())
	var already todo.Answered
	if errors.As(err, &already) && already.Same {
		// The pick is spent on the user's own answer, given elsewhere: nothing to undo
		return it, b, pane, err
	}
	if err != nil {
		return it, b, pane, errors.Join(err, undo())
	}
	return it, b, pane, nil
}

// tellAbove tells the boss above the one in pane (a boss under a boss, as under the manager) that the user answered
// item it there, note being what that boss got, so the one above drops its question for it; not for a test item. A
// failure is only reported, on warn: the item is answered.
func tellAbove(c *tmux.Client, pane string, it todo.Item, note inbox.Note, warn func(string)) {
	if it.Quiet {
		return
	}
	queued, err := inbox.AnsweredUp(c, control.RealClock, pane, it.ID, note)
	switch {
	case err != nil && queued:
		warn("queued for the boss above, delivered when it next rests: " + err.Error())
	case err != nil:
		warn("the boss above wasn't told: " + err.Error())
	}
}

// settleAsk settles ask id of boss name's list, reading question, with its recommended option pick as the manager
// (todo.Store.Settle) and sends the boss the note; only from the manager's coordinator pane.
func settleAsk(c *tmux.Client, out, stderr io.Writer, name string, id int, question, pick string) error {
	if err := inManagerPane(c); err != nil {
		return err
	}
	b, err := inbox.Recipient(c, name)
	if errors.Is(err, inbox.ErrNotBoss) {
		return fmt.Errorf("%w: --recommended settles only a marked ask on a boss's list, never a permission prompt or dialog", err)
	}
	if err != nil {
		return err
	}
	pane, err := control.BossPane(c, name)
	if err != nil {
		return err
	}
	s, err := todo.ForPane(c, pane)
	if err != nil {
		return err
	}
	it, err := s.Settle(id, question, pick, time.Now())
	if err != nil {
		return err
	}
	queued, err := b.Send(c, control.RealClock, inbox.AnswerSettled(it.ID, it.Text, it.Answer))
	switch {
	case !queued:
		return fmt.Errorf("#%d settled with %q and ticked, not sent to %s (tell it yourself): %w", id, it.Answer, name, err)
	case err != nil:
		fmt.Fprintf(stderr, "⚠ dev todo: #%d queued in %s's inbox, delivered when it next rests: %v\n", id, name, err)
	}
	_, err = fmt.Fprintf(out, "#%d settled with %q, sent to %s\n", id, it.Answer, name)
	return err
}

// inManagerPane refuses a run outside the manager's coordinator pane.
func inManagerPane(c *tmux.Client) error {
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		self, ok, err := inbox.Coordinator(c, pane)
		if err != nil {
			return err
		}
		if ok && self.Name == inbox.Manager {
			return nil
		}
	}
	return errors.New("--via manager runs only in the manager's coordinator pane")
}
