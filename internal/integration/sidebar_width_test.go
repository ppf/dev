//go:build integration

package integration

import (
	"testing"
)

// Every sidebar shows one width (@sidebar_width): resizing one sidebar resizes them all, a window resize
// (tmux scaling its panes) doesn't change it, and new sidebars are created with it.
func TestSidebarWidthShared(t *testing.T) {
	d := newDevServer(t, "")
	sidebar := map[string]string{}
	for _, s := range []string{"a", "b"} {
		d.T("new-session", "-d", "-s", s, "-x", "120", "-y", "30")
		from := d.T("display", "-t", "="+s+":", "-p", "#{pane_id}")
		if out, err := runScript(d.scriptEnv("TMUX_PANE="+from), devBin, "sidebar", "ensure", s); err != nil {
			t.Fatalf("dev sidebar ensure %s: %v\n%s", s, err, out)
		}
		sidebar[s] = d.T("show-option", "-qv", "-t", "="+s+":", "@sidebar_pane")
	}
	width := func(pane string) string { return d.T("display", "-t", pane, "-p", "#{pane_width}") }
	check(t, "new sidebars at the default width", width(sidebar["a"])+" "+width(sidebar["b"]), "40 40")

	// Manual: the user resizes a's sidebar; b's follows and the width is stored
	d.T("resize-pane", "-t", sidebar["a"], "-x", "30")
	waitFor(t, "b's sidebar follows a's resize", func() bool { return width(sidebar["b"]) == "30" })
	check(t, "a keeps the width it was given", width(sidebar["a"]), "30")
	check(t, "width stored", d.T("show-option", "-gqv", "@sidebar_width"), "30")

	// Automatic: b's window shrinks (tmux squeezes its panes) and grows again; the sidebar takes the stored
	// width back, and the squeeze isn't taken for a manual resize
	d.T("resize-window", "-t", "=b:", "-x", "50")
	waitFor(t, "b's window squeezed", func() bool { return d.T("display", "-t", "=b:", "-p", "#{window_width}") == "50" })
	d.T("resize-window", "-t", "=b:", "-x", "180")
	waitFor(t, "b's sidebar back at the stored width after the window resize", func() bool {
		return d.T("display", "-t", "=b:", "-p", "#{window_width}") == "180" && width(sidebar["b"]) == "30"
	})
	check(t, "an automatic resize doesn't change the stored width", d.T("show-option", "-gqv", "@sidebar_width"), "30")
	check(t, "a untouched", width(sidebar["a"]), "30")

	// New sidebars use the stored width
	d.T("new-session", "-d", "-s", "c", "-x", "120", "-y", "30")
	from := d.T("display", "-t", "=c:", "-p", "#{pane_id}")
	if out, err := runScript(d.scriptEnv("TMUX_PANE="+from), devBin, "sidebar", "ensure", "c"); err != nil {
		t.Fatalf("dev sidebar ensure c: %v\n%s", err, out)
	}
	check(t, "new sidebar at the stored width", width(d.T("show-option", "-qv", "-t", "=c:", "@sidebar_pane")), "30")
}
