package sidebar

import (
	"strconv"
	"strings"

	"github.com/ppf/dev/internal/tmux"
)

// optWidth is the one sidebar width (columns) every sidebar shows; unset means defaultWidth.
const (
	optWidth     = "@sidebar_width"
	defaultWidth = 40
)

// storedWidth reads an @sidebar_width value; unset or invalid is defaultWidth.
func storedWidth(v string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		return n
	}
	return defaultWidth
}

// StoredWidth is the width new sidebars are created with.
func StoredWidth(c *tmux.Client) (int, error) {
	v, err := c.Run("show-option", "-gqv", optWidth)
	if err != nil {
		return 0, err
	}
	return storedWidth(v), nil
}

// widthState is what a sidebar saw on its last tick, to tell a manual resize from an automatic one.
type widthState struct {
	seen     bool
	width    int  // own pane
	winWidth int  // own window
	panes    int  // own window's pane count: closing a neighbour widens the sidebar without the user
	stored   int  // @sidebar_width
	resized  bool // it resized itself last tick: whatever width tmux gave it is the new baseline
}

// syncWidth decides, from the last tick and this one, whether this sidebar resizes itself to the stored
// width (resizeTo) or a manual resize of it sets the stored width for every sidebar (store).
//   - First look, the stored width changed (another sidebar was resized), or the window was resized or
//     gained or lost a pane (tmux reflows panes with it): take the stored width.
//   - Only its own width changed: the user resized this sidebar, so that is the width for all.
//
// Otherwise it leaves its width alone, so a width tmux can't give (a too-narrow window) isn't retried
// every tick. A zoomed window (compact mode hides the sidebar) is skipped, and the first look after the
// unzoom takes the stored width, as the layout may have been reflowed meanwhile.
func syncWidth(prev widthState, width, winWidth, panes, stored int, zoomed bool) (next widthState, resizeTo, store int) {
	if zoomed {
		return widthState{}, 0, 0
	}
	next = widthState{seen: true, width: width, winWidth: winWidth, panes: panes, stored: stored}
	switch {
	case !prev.seen || stored != prev.stored || winWidth != prev.winWidth || panes != prev.panes:
		if width != stored {
			next.resized = true
			return next, stored, 0
		}
	case prev.resized:
		// Its own resize settling, possibly short of the stored width in a narrow window: not the user's
	case width != prev.width:
		next.stored = width
		return next, 0, width
	}
	return next, 0, 0
}
