package cmd

import (
	"errors"
	"time"

	"github.com/ppf/dev/internal/control"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/todo"
)

// answerTick ticks ask id on boss name's list with the user's answer (todo.Store.AnswerPick) and returns it with the
// boss to send the note to; resolving the boss first, so an answer it can't send stays open. claim, when set, runs
// right before ticking and gives the user's pick; its undo runs when the tick is refused.
func answerTick(c *tmux.Client, name string, id int, question, answer string, before int64, claim func() (picked string, undo func() error, err error)) (todo.Item, inbox.Boss, error) {
	b, err := inbox.Recipient(c, name)
	if err != nil {
		return todo.Item{}, b, err
	}
	pane, err := control.BossPane(c, name)
	if err != nil {
		return todo.Item{}, b, err
	}
	s, err := todo.ForPane(c, pane)
	if err != nil {
		return todo.Item{}, b, err
	}
	var picked string
	undo := func() error { return nil }
	if claim != nil {
		if picked, undo, err = claim(); err != nil {
			return todo.Item{}, b, err
		}
	}
	it, err := s.AnswerPick(id, question, answer, picked, before, time.Now())
	if err != nil {
		return it, b, errors.Join(err, undo())
	}
	return it, b, nil
}
