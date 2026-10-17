package sidebar

import "testing"

func TestSyncWidth(t *testing.T) {
	seen := widthState{seen: true, width: 40, winWidth: 120, panes: 3, stored: 40}
	tests := []struct {
		name                           string
		prev                           widthState
		width, winWidth, panes, stored int
		zoomed                         bool
		wantResize, wantStore          int
		wantSeen                       bool
	}{
		{"first look at another width takes the stored one", widthState{}, 52, 120, 3, 40, false, 40, 0, true},
		{"first look at the stored width does nothing", widthState{}, 40, 120, 3, 40, false, 0, 0, true},
		{"steady", seen, 40, 120, 3, 40, false, 0, 0, true},
		{"window resized: tmux scaled it, take the stored width back", seen, 53, 160, 3, 40, false, 40, 0, true},
		{"a neighbour closed: tmux gave it the space, take the stored width back", seen, 80, 120, 2, 40, false, 40, 0, true},
		{"another sidebar was resized: follow it", seen, 40, 120, 3, 30, false, 30, 0, true},
		{"only its own width changed: the user resized it, store it for all", seen, 30, 120, 3, 40, false, 0, 30, true},
		{"after following, its own new width isn't a manual resize", widthState{seen: true, width: 40, winWidth: 120, panes: 3, stored: 30, resized: true}, 30, 120, 3, 30, false, 0, 0, true},
		{"a width tmux can't give isn't the user's either", widthState{seen: true, width: 25, winWidth: 30, panes: 3, stored: 40, resized: true}, 20, 30, 3, 40, false, 0, 0, true},
		{"nor retried every tick", widthState{seen: true, width: 20, winWidth: 30, panes: 3, stored: 40}, 20, 30, 3, 40, false, 0, 0, true},
		{"zoomed: left alone and forgotten", seen, 0, 120, 3, 30, true, 0, 0, false},
		{"first look after the unzoom takes the stored width", widthState{}, 44, 120, 3, 40, false, 40, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, resize, store := syncWidth(tt.prev, tt.width, tt.winWidth, tt.panes, tt.stored, tt.zoomed)
			if resize != tt.wantResize || store != tt.wantStore || next.seen != tt.wantSeen {
				t.Errorf("syncWidth = resize %d store %d next %+v; want resize %d store %d seen %v",
					resize, store, next, tt.wantResize, tt.wantStore, tt.wantSeen)
			}
		})
	}
}

func TestStoredWidth(t *testing.T) {
	for v, want := range map[string]int{"": 40, "32\n": 32, "x": 40, "0": 40, "-5": 40} {
		if got := storedWidth(v); got != want {
			t.Errorf("storedWidth(%q) = %d, want %d", v, got, want)
		}
	}
}
