package todo

import (
	"time"

	"github.com/ppf/dev/internal/tmux"
)

// Closed are the lists with open items of the bosses closed on c's server, and those not read (Err), as All reads them.
func Closed(c *tmux.Client, now time.Time) ([]BossList, error) {
	return All(c, now, func(string) bool { return false }, true)
}

// ClosedStore is the list of boss when it is a boss closed on c's server with open items; ok false when it isn't.
func ClosedStore(c *tmux.Client, boss string, now time.Time) (s Store, ok bool, err error) {
	lists, err := Closed(c, now)
	if err != nil {
		return Store{}, false, err
	}
	for _, l := range lists {
		if l.Boss == boss {
			return l.Store, true, l.Err
		}
	}
	return Store{}, false, nil
}
