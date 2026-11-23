package cmd

import (
	"errors"
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
	queued, err := inbox.AnsweredUp(c, control.RealClock, pane, note)
	switch {
	case err != nil && queued:
		warn("queued for the boss above, delivered when it next rests: " + err.Error())
	case err != nil:
		warn("the boss above wasn't told: " + err.Error())
	}
}
