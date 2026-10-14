package compact

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

func TestDecide(t *testing.T) {
	agent := window{Index: 1, Name: "Agent", Panes: 3, Active: "%1"}
	tests := []struct {
		name  string
		width int
		w     window
		want  action
	}{
		{"narrow Agent zooms", 100, agent, zoom},
		{"narrow Files zooms", 139, window{Name: "Files", Panes: 2}, zoom},
		{"narrow already ours", 100, window{Name: "Agent", Panes: 3, Ours: "%2"}, none},
		{"narrow manual zoom kept", 100, window{Name: "Agent", Panes: 3, Zoomed: true}, none},
		{"narrow single pane", 100, window{Name: "Agent", Panes: 1}, none},
		{"narrow other window", 100, window{Name: "Shell", Panes: 2}, none},
		{"wide not ours", 140, window{Name: "Agent", Panes: 3, Zoomed: true, Active: "%2"}, none},
		{"wide our zoom unzooms", 140, window{Name: "Agent", Zoomed: true, Active: "%2", Ours: "%2"}, unzoom},
		{"wide zoom moved unmarks", 200, window{Name: "Files", Zoomed: true, Active: "%3", Ours: "%2"}, unmark},
		{"wide manual unzoom unmarks", 200, window{Name: "Files", Active: "%2", Ours: "%2"}, unmark},
	}
	for _, tt := range tests {
		if got := decide(tt.width, tt.w); got != tt.want {
			t.Errorf("%s: decide = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestZoomTarget(t *testing.T) {
	tests := []struct {
		name, window string
		panes        []string
		sidebar      string
		want         string
	}{
		{"Agent skips sidebar", "Agent", []string{"%1", "%2", "%3"}, "%1", "%2"},
		{"Agent sidebar not first", "Agent", []string{"%2", "%1"}, "%1", "%2"},
		{"Agent no sidebar", "Agent", []string{"%1", "%2"}, "", "%1"},
		{"Agent only sidebar", "Agent", []string{"%1"}, "%1", ""},
		{"Files first pane", "Files", []string{"%4", "%5"}, "%4", "%4"},
		{"other window", "Shell", []string{"%6", "%7"}, "", ""},
		{"no panes", "Files", nil, "", ""},
	}
	for _, tt := range tests {
		if got := zoomTarget(tt.window, tt.panes, tt.sidebar); got != tt.want {
			t.Errorf("%s: zoomTarget = %q, want %q", tt.name, got, tt.want)
		}
	}
}

const clientsFormat = "#{client_name}\x1f#{client_width}\x1f#{client_session}\x1f"

const windowsFormat = "#{window_index}\x1f#{window_name}\x1f#{window_panes}\x1f#{window_zoomed_flag}\x1f#{pane_id}\x1f#{@dev_compact_zoomed}\x1f"

var errFake = errors.New("exit status 1")

// fakeTmux records every call and answers it with respond.
func fakeTmux(respond func(args []string) (string, error)) (*tmux.Client, *[][]string) {
	var calls [][]string
	return tmux.NewFake(func(args []string) (string, error) {
		calls = append(calls, args)
		return respond(args)
	}), &calls
}

// devc answers the queries of a run for client /dev/ttys1 viewing devc at width (and a wide /dev/ttys0
// viewing other), with windows rows.
func devc(width string, windows string, override func(args []string) (string, bool, error)) func([]string) (string, error) {
	return func(args []string) (string, error) {
		if override != nil {
			if out, ok, err := override(args); ok {
				return out, err
			}
		}
		switch strings.Join(args, " ") {
		case "list-clients -F " + clientsFormat:
			return tmux.FormatRow("/dev/ttys0", "200", "other") + tmux.FormatRow("/dev/ttys1", width, "devc"), nil
		case "show-option -qv -t =devc: @project":
			return "devc\n", nil
		case "show-option -qv -t =devc: @sidebar_pane":
			return "%1\n", nil
		case "list-windows -t =devc: -F " + windowsFormat:
			return windows, nil
		case "list-panes -t =devc:1 -F #{pane_id}":
			return "%1\n%2\n%3\n", nil
		case "list-panes -t =devc:2 -F #{pane_id}":
			return "%4\n%5\n", nil
		}
		return "", nil
	}
}

var (
	callInfo     = []string{"list-clients", "-F", clientsFormat}
	callProject  = []string{"show-option", "-qv", "-t", "=devc:", "@project"}
	callSidebar  = []string{"show-option", "-qv", "-t", "=devc:", "@sidebar_pane"}
	callWindows  = []string{"list-windows", "-t", "=devc:", "-F", windowsFormat}
	devcWindows  = tmux.FormatRow("1", "Agent", "3", "0", "%2", "") + tmux.FormatRow("2", "Files", "2", "0", "%5", "") + tmux.FormatRow("3", "Shell", "2", "0", "%6", "")
	callWarnings = func(msg string) []string { return []string{"set-option", "-ga", "@dev_compact_warnings", msg + "; "} }
)

func TestRunZoomsNarrow(t *testing.T) {
	c, calls := fakeTmux(devc("100", devcWindows, nil))
	if err := Run(c, "/dev/ttys1"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		callInfo, callProject, callSidebar, callWindows,
		{"list-panes", "-t", "=devc:1", "-F", "#{pane_id}"},
		{"resize-pane", "-Z", "-t", "%2", ";", "set-option", "-wq", "-t", "=devc:1", "@dev_compact_zoomed", "%2"},
		{"list-panes", "-t", "=devc:2", "-F", "#{pane_id}"},
		{"resize-pane", "-Z", "-t", "%4", ";", "set-option", "-wq", "-t", "=devc:2", "@dev_compact_zoomed", "%4"},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Errorf("calls:\n got %q\nwant %q", *calls, want)
	}
}

func TestRunWide(t *testing.T) {
	windows := tmux.FormatRow("1", "Agent", "3", "1", "%2", "%2") + tmux.FormatRow("2", "Files", "2", "1", "%5", "%4") +
		tmux.FormatRow("3", "Shell", "2", "1", "%6", "")
	c, calls := fakeTmux(devc("200", windows, nil))
	if err := Run(c, "/dev/ttys1"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		callInfo, callProject, callSidebar, callWindows,
		{"resize-pane", "-Z", "-t", "=devc:1", ";", "set-option", "-wqu", "-t", "=devc:1", "@dev_compact_zoomed"},
		{"set-option", "-wqu", "-t", "=devc:2", "@dev_compact_zoomed"},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Errorf("calls:\n got %q\nwant %q", *calls, want)
	}
}

func TestRunWarnsPerWindow(t *testing.T) {
	// The zoom of window 1 fails and window 2's panes can't be listed: both warn, Run still succeeds
	c, calls := fakeTmux(devc("100", devcWindows, func(args []string) (string, bool, error) {
		switch args[0] {
		case "resize-pane":
			return "", true, &tmux.Error{Stderr: "can't find pane: %2", Err: errFake}
		case "list-panes":
			if args[2] == "=devc:2" {
				return "", true, errFake
			}
		}
		return "", false, nil
	}))
	if err := Run(c, "/dev/ttys1"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		callInfo, callProject, callSidebar, callWindows,
		{"list-panes", "-t", "=devc:1", "-F", "#{pane_id}"},
		{"resize-pane", "-Z", "-t", "%2", ";", "set-option", "-wq", "-t", "=devc:1", "@dev_compact_zoomed", "%2"},
		{"display-message", "-c", "/dev/ttys1", "⚠ dev compact: zoom =devc:1: can't find pane: %2"},
		callWarnings("zoom =devc:1: can't find pane: %2"),
		{"list-panes", "-t", "=devc:2", "-F", "#{pane_id}"},
		{"display-message", "-c", "/dev/ttys1", "⚠ dev compact: list-panes failed for =devc:2"},
		callWarnings("list-panes failed for =devc:2"),
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Errorf("calls:\n got %q\nwant %q", *calls, want)
	}
}

func TestRunUnmarkWarns(t *testing.T) {
	windows := tmux.FormatRow("2", "Files", "2", "0", "%5", "%4")
	c, calls := fakeTmux(devc("200", windows, func(args []string) (string, bool, error) {
		return "", args[0] == "set-option" && args[1] == "-wqu", &tmux.Error{Stderr: "no such window: =devc:2", Err: errFake}
	}))
	if err := Run(c, "/dev/ttys1"); err != nil {
		t.Fatal(err)
	}
	got := (*calls)[len(*calls)-2:]
	want := [][]string{
		{"display-message", "-c", "/dev/ttys1", "⚠ dev compact: unmark =devc:2: no such window: =devc:2"},
		callWarnings("unmark =devc:2: no such window: =devc:2"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("calls:\n got %q\nwant %q", got, want)
	}
}

func TestRunStops(t *testing.T) {
	tests := []struct {
		name     string
		client   string
		width    string
		override func(args []string) (string, bool, error)
		wantErr  string
		want     [][]string
	}{
		{
			name: "no client", client: "", width: "100", wantErr: "no hook client passed",
			want: [][]string{
				{"display-message", "⚠ dev compact: no hook client passed"},
				callWarnings("no hook client passed"),
			},
		},
		{
			name: "client detached", client: "/dev/ttys2", width: "100",
			want: [][]string{callInfo},
		},
		{
			name: "list-clients fails", client: "/dev/ttys1", width: "100", wantErr: "list-clients failed: no server",
			override: func(args []string) (string, bool, error) {
				return "", args[0] == "list-clients", &tmux.Error{Stderr: "no server", Err: errFake}
			},
			want: [][]string{
				callInfo,
				{"display-message", "-c", "/dev/ttys1", "⚠ dev compact: list-clients failed: no server"},
				callWarnings("list-clients failed: no server"),
			},
		},
		{
			name: "no project", client: "/dev/ttys1", width: "100",
			override: func(args []string) (string, bool, error) { return "\n", args[len(args)-1] == "@project", nil },
			want:     [][]string{callInfo, callProject},
		},
		{
			name: "session gone", client: "/dev/ttys1", width: "100",
			override: func(args []string) (string, bool, error) { return "", args[len(args)-1] == "@project", errFake },
			want:     [][]string{callInfo, callProject},
		},
		{
			name: "list-windows fails", client: "/dev/ttys1", width: "100", wantErr: "list-windows failed for devc",
			override: func(args []string) (string, bool, error) { return "", args[0] == "list-windows", errFake },
			want: [][]string{
				callInfo, callProject, callSidebar, callWindows,
				{"display-message", "-c", "/dev/ttys1", "⚠ dev compact: list-windows failed for devc"},
				callWarnings("list-windows failed for devc"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, calls := fakeTmux(devc(tt.width, "", tt.override))
			err := Run(c, tt.client)
			if gotErr := errString(err); gotErr != tt.wantErr {
				t.Errorf("err = %q, want %q", gotErr, tt.wantErr)
			}
			if !reflect.DeepEqual(*calls, tt.want) {
				t.Errorf("calls:\n got %q\nwant %q", *calls, tt.want)
			}
		})
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
