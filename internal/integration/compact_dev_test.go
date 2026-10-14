//go:build integration

package integration

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestCompactDev is TestCompact against `dev compact`: the real ~/.tmux.conf with its client-* hooks
// pointed at the dev binary, and the sidebar flows run through `dev sidebar`.
func TestCompactDev(t *testing.T) {
	requireFiles(t, filepath.Join(filepath.Dir(tmuxHome(t)), ".tmux.conf"))
	requireExported(t, map[string]string{"tmux/.tmux.conf": ".tmux.conf"})
	tmp := t.TempDir()
	env := "XDG_STATE_HOME=" + filepath.Join(tmp, "state")
	hook := `'run-shell "` + devBin + ` compact #{q:hook_client}"'`
	in := newServer(t, `source-file ~/.tmux.conf
set -g @resurrect-dir '`+tmp+`/resurrect'
set -g @continuum-save-interval '0'
set -g default-command /bin/sh
set-hook -g client-attached `+hook+`
set-hook -g client-resized `+hook+`
set-hook -g client-active `+hook+`
set-hook -g client-session-changed `+hook+`
`, env)
	out := newServer(t, "", env)

	zoom := func(w string) string { return in.T("display", "-t", "devc:"+w, "-p", "#{window_zoomed_flag}") }
	active := func(w string) string { return in.T("display", "-t", "devc:"+w, "-p", "#{pane_id}") }
	flag := func(w string) string { return in.T("show-option", "-wqv", "-t", "devc:"+w, "@dev_compact_zoomed") }
	// Wait until both dev windows reach the expected zoom state, then let the remaining hooks run
	settle := func(want string) {
		t.Helper()
		for range 20 {
			if zoom("1")+zoom("2") == want {
				time.Sleep(500 * time.Millisecond)
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		check(t, "zoom state settled", zoom("1")+zoom("2"), want)
	}
	resize := func(x string) { out.T("resize-window", "-t", "out", "-x", x, "-y", "40") }
	narrow := func(want string) { t.Helper(); resize("100"); settle(want) }
	wide := func(want string) { t.Helper(); resize("200"); settle(want) }

	// dev-like layout: Agent = sidebar | claude | right column, Files = nvim | terminal
	devSession := func(s string) (claude, nvim string) {
		in.T("new-session", "-d", "-s", s, "-x", "200", "-y", "50", "-n", "Agent")
		in.T("set-option", "-t", s+":", "@project", s)
		claude = in.T("display", "-t", s+":1.1", "-p", "#{pane_id}")
		in.T("split-window", "-t", claude, "-h")
		sidebar := in.T("split-window", "-t", claude, "-hb", "-l", "40", "-PF", "#{pane_id}")
		in.T("set-option", "-t", s+":", "@sidebar_pane", sidebar)
		in.T("new-window", "-t", s+":2", "-n", "Files")
		nvim = in.T("display", "-t", s+":2.1", "-p", "#{pane_id}")
		in.T("split-window", "-t", nvim, "-h")
		return claude, nvim
	}
	claude, nvim := devSession("devc")
	check(t, "setup: sidebar is the Agent window's first pane",
		in.T("display", "-t", "devc:1.1", "-p", "#{pane_id}"), in.T("show-option", "-qv", "-t", "devc:", "@sidebar_pane"))
	check(t, "setup: client hooks run dev compact",
		strconv.FormatBool(strings.Contains(in.T("show-hooks", "-g", "client-attached"), devBin+" compact")), "true")
	devSession("other")                      // dev session nobody views: must be left alone
	in.T("new-session", "-d", "-s", "plain") // not a dev session: must be left alone

	out.T("new-session", "-d", "-s", "out", "-x", "100", "-y", "40")
	out.T("set", "-g", "window-size", "manual")
	out.T("send-keys", "-t", "out", "TMUX= tmux -L "+in.socket+" attach -t devc", "Enter")
	clientWidth := func() string { w, _ := in.Try("list-clients", "-F", "#{client_width}"); return w }
	for range 20 {
		if clientWidth() == "100" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if clientWidth() != "100" {
		t.Fatal("❌ test client did not attach at 100 cols")
	}
	settle("11")
	check(t, "narrow attach zooms Agent on claude", zoom("1")+"/"+active("1"), "1/"+claude)
	check(t, "narrow attach zooms Files on nvim", zoom("2")+"/"+active("2"), "1/"+nvim)
	check(t, "unviewed dev session untouched", in.T("display", "-t", "other:1", "-p", "#{window_zoomed_flag}"), "0")
	check(t, "non-dev session untouched", in.T("display", "-t", "plain:1", "-p", "#{window_zoomed_flag}"), "0")

	wide("00")
	check(t, "wide client unzooms both and clears flags", zoom("1")+zoom("2")+"/"+flag("1")+flag("2"), "00/")

	// A zoom set by hand while wide survives a narrow → wide round trip
	in.T("resize-pane", "-Z", "-t", "devc:2.2")
	narrow("11")
	wide("01")
	check(t, "manual wide zoom kept", zoom("2")+"/"+active("2"), "1/"+in.T("display", "-t", "devc:2.2", "-p", "#{pane_id}"))
	in.T("resize-pane", "-Z", "-t", "devc:2.2")

	// While narrow: unzoom by hand stays unzoomed, and a zoom moved to another pane survives going wide
	narrow("11")
	in.T("resize-pane", "-Z", "-t", "devc:1")
	resize("101") // another narrow resize fires the hooks again
	settle("01")
	check(t, "manual unzoom while narrow not undone", zoom("1"), "0")
	in.T("resize-pane", "-Z", "-t", "devc:2.2", ";", "resize-pane", "-Z", "-t", "devc:2.2") // unzoom nvim, zoom the terminal
	wide("01")
	check(t, "zoom moved to another pane kept when wide", zoom("2")+"/"+active("2"), "1/"+in.T("display", "-t", "devc:2.2", "-p", "#{pane_id}"))
	check(t, "flags cleared when wide", flag("1")+flag("2"), "")
	in.T("resize-pane", "-Z", "-t", "devc:2.2")

	// Sidebar flows while narrow must keep the compact zoom: switching to a session (ensure) and hiding
	// all sidebars (toggle). Run them with the pane env a key binding in devc would give them.
	narrow("11")
	sidebar := func(args ...string) {
		t.Helper()
		if out, err := runScript(in.scriptEnv("TMUX_PANE="+claude), devBin, append([]string{"sidebar"}, args...)...); err != nil {
			t.Fatalf("dev sidebar %v: %v\n%s", args, err, out)
		}
	}
	sidebar("ensure", "devc")
	check(t, "ensure on a compact session keeps the zoom", zoom("1")+"/"+active("1"), "1/"+claude)
	sidebar("toggle")
	check(t, "hiding sidebars keeps the zoom", zoom("1")+"/"+active("1")+"/"+in.T("show", "-gqv", "@sidebar_hidden"), "1/"+claude+"/1")
	check(t, "no compact warnings", in.T("show", "-gqv", "@dev_compact_warnings"), "")
}
