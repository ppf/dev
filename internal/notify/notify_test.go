package notify

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ppf/dev/internal/tmux"
)

func TestDecide(t *testing.T) {
	long := strings.Repeat("é", 150)
	tests := []struct {
		name string
		e    event
		want note
		ok   bool
	}{
		{"permission", event{Name: "Notification", Title: "Permission needed", Message: "Claude needs your permission to use Bash"},
			note{"Permission needed", "Claude needs your permission to use Bash", true}, true},
		{"notification title cleaned", event{Name: "Notification", Title: "(x) T", Message: "m"}, note{"x) T", "m", true}, true},
		{"notification without title", event{Name: "Notification", Message: "m"}, note{"Needs input", "m", true}, true},
		{"notification without message", event{Name: "Notification"}, note{"Needs input", "Needs input", true}, true},
		{"notification collapses whitespace", event{Name: "Notification", Message: "  Pick\n\none   option.  "}, note{"Needs input", "Pick one option.", true}, true},
		{"notification truncates by rune", event{Name: "Notification", Message: long}, note{"Needs input", strings.Repeat("é", 99) + "…", true}, true},
		{"notification strips plist delimiters", event{Name: "Notification", Message: `["(x) pick`}, note{"Needs input", "x) pick", true}, true},
		{"notification strips a leading bullet", event{Name: "Notification", Message: "- Allow X\n- Allow Y"}, note{"Needs input", "Allow X - Allow Y", true}, true},
		{"notification strips a leading minus", event{Name: "Notification", Message: "-1 option left"}, note{"Needs input", "1 option left", true}, true},
		{"notification only delimiters", event{Name: "Notification", Message: "[[( -"}, note{"Needs input", "Needs input", true}, true},
		{"finished turn", event{Name: "Stop"}, note{}, false},
		{"codex approval", event{Name: "PermissionRequest", Tool: "Bash", ToolInput: json.RawMessage(`{"description":"May I run touch?"}`)},
			note{"Needs approval", "May I run touch?", true}, true},
		{"codex approval of a patch", event{Name: "PermissionRequest", Tool: "apply_patch", ToolInput: json.RawMessage(`"*** Begin Patch"`)},
			note{"Needs approval", "apply_patch", true}, true},
		{"codex approval without description", event{Name: "PermissionRequest", Tool: "Bash"}, note{"Needs approval", "Bash", true}, true},
		{"codex approval without tool", event{Name: "PermissionRequest"}, note{"Needs approval", "Needs approval", true}, true},
		{"codex interrupt", event{Name: "Interrupt"}, note{}, false},
		{"failure", event{Name: "StopFailure", Error: "rate_limit"}, note{"Failed", "rate_limit", false}, true},
		{"failure without error", event{Name: "StopFailure"}, note{"Failed", "Stopped on an error", false}, true},
		{"session start", event{Name: "SessionStart"}, note{}, false},
		{"tool use", event{Name: "PostToolUse"}, note{}, false},
		{"prompt", event{Name: "UserPromptSubmit"}, note{}, false},
		{"no event name", event{}, note{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := decide(tt.e)
			if got != tt.want || ok != tt.ok {
				t.Errorf("decide(%+v) = %+v, %v; want %+v, %v", tt.e, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// fakeTmux answers the buildArgs query with one pane row and the given client rows.
func fakeTmux(pane paneView, clients ...clientView) (*tmux.Client, *[]string) {
	var got []string
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	return tmux.NewFake(func(args []string) (string, error) {
		got = args
		out := tmux.FormatRow(pane.Notify, pane.Activate, b(pane.FocusEvents), pane.Session, b(pane.WindowActive),
			b(pane.PaneActive), b(pane.Zoomed), pane.Socket, pane.ServerPID)
		for _, c := range clients {
			out += tmux.FormatRow(c.Session, c.Flags)
		}
		return out, nil
	}), &got
}

var failed = note{Subtitle: "Failed", Message: "rate_limit"}

func TestBuildArgsVisibility(t *testing.T) {
	shown := paneView{FocusEvents: true, Session: "web", WindowActive: true, PaneActive: true, Socket: "/tmp/tmux-1/s"}
	focused := clientView{Session: "web", Flags: "attached,focused,UTF-8"}
	with := func(f func(*paneView)) paneView { p := shown; f(&p); return p }
	tests := []struct {
		name    string
		pane    paneView
		clients []clientView
		ok      bool
	}{
		{"pane in view", shown, []clientView{focused}, false},
		{"no client", shown, nil, true},
		{"client unfocused", shown, []clientView{{Session: "web", Flags: "attached,UTF-8"}}, true},
		{"focused client on another session", shown, []clientView{{Session: "api", Flags: "attached,focused"}}, true},
		{"flag substring is not focused", shown, []clientView{{Session: "web", Flags: "attached,unfocused"}}, true},
		{"other pane active, window in view", with(func(p *paneView) { p.PaneActive = false }), []clientView{focused}, false},
		{"zoomed onto another pane", with(func(p *paneView) { p.PaneActive, p.Zoomed = false, true }), []clientView{focused}, true},
		{"zoomed onto this pane", with(func(p *paneView) { p.Zoomed = true }), []clientView{focused}, false},
		{"window not active", with(func(p *paneView) { p.WindowActive = false }), []clientView{focused}, true},
		{"focus-events off: focus unknown", with(func(p *paneView) { p.FocusEvents = false }), []clientView{focused}, true},
		{"on", with(func(p *paneView) { p.WindowActive, p.Notify = false, "on" }), nil, true},
		{"off", with(func(p *paneView) { p.WindowActive, p.Notify = false, "off" }), nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := fakeTmux(tt.pane, tt.clients...)
			_, ok, err := buildArgs(c, "%3", failed, "/bin/tmux", "")
			if err != nil || ok != tt.ok {
				t.Errorf("buildArgs ok = %v, %v; want %v", ok, err, tt.ok)
			}
		})
	}
}

func TestBuildArgsBadNotifyValue(t *testing.T) {
	c, _ := fakeTmux(paneView{Notify: "0", Session: "web"})
	if _, ok, err := buildArgs(c, "%3", failed, "/bin/tmux", ""); ok || err == nil || !strings.Contains(err.Error(), `@dev_notify is "0", want on or off`) {
		t.Errorf("buildArgs = %v, %v; want the @dev_notify error", ok, err)
	}
}

func TestBuildArgsContent(t *testing.T) {
	pane := paneView{Session: "web", Socket: "/tmp/tmux 1/it's", ServerPID: "42"}
	c, query := fakeTmux(pane)
	args, ok, err := buildArgs(c, "%3", note{Subtitle: "Needs input", Message: "Needs Bash", Sound: true}, "/opt/bin/tmux", "com.mitchellh.ghostty")
	if err != nil || !ok {
		t.Fatalf("buildArgs = %v, %v", ok, err)
	}
	want := []string{"-title", "web", "-subtitle", "Needs input", "-message", "Needs Bash", "-group", "dev-42-%3",
		"-execute", `'/opt/bin/tmux' '-S' '/tmp/tmux 1/it'"'"'s' 'switch-client' '-t' '%3'`,
		"-activate", "com.mitchellh.ghostty", "-sound", "default"}
	if !slices.Equal(args, want) {
		t.Errorf("buildArgs =\n%q\nwant\n%q", args, want)
	}
	if q := strings.Join(*query, " "); !strings.HasPrefix(q, "display -p -t %3 ") || !strings.Contains(q, " ; list-clients -F ") {
		t.Errorf("query = %q", q)
	}

	pane.Activate = "com.example.term"
	c, _ = fakeTmux(pane)
	args, _, _ = buildArgs(c, "%3", failed, "/opt/bin/tmux", "com.mitchellh.ghostty")
	if i := slices.Index(args, "-activate"); i < 0 || args[i+1] != "com.example.term" || slices.Contains(args, "-sound") {
		t.Errorf("@dev_notify_activate override, no sound without Sound: %q", args)
	}

	pane.Activate = ""
	c, _ = fakeTmux(pane)
	if args, _, _ = buildArgs(c, "%3", failed, "/opt/bin/tmux", ""); slices.Contains(args, "-activate") {
		t.Errorf("no app to activate: %q", args)
	}
}

func TestBuildArgsTmuxError(t *testing.T) {
	boom := errors.New("can't find pane")
	c := tmux.NewFake(func([]string) (string, error) { return "", boom })
	if _, ok, err := buildArgs(c, "%3", failed, "tmux", ""); ok || !errors.Is(err, boom) {
		t.Errorf("buildArgs = %v, %v; want %v", ok, err, boom)
	}
}

func TestRunSkipsNonNotifyingEvents(t *testing.T) {
	c := tmux.NewFake(func([]string) (string, error) { t.Fatal("tmux queried for a non-notifying event"); return "", nil })
	for _, input := range []string{`{"hook_event_name":"PostToolUse"}`, `{"session_id":"s"}`, `{"x":{"hook_event_name":"Stop"}}`, `{"hook_event_name":"Stop","last_assistant_message":"done"}`,
		`{"hook_event_name":"PostToolUseFailure","error":{"code":1}}`} {
		if err := Run(c, "%3", []byte(input), ""); err != nil {
			t.Errorf("Run(%s) = %v", input, err)
		}
	}
	if err := Run(c, "%3", []byte(`{"hook_event_name":"StopFailure","error":5}`), ""); err == nil ||
		!strings.HasPrefix(err.Error(), "notify: hook input: ") {
		t.Errorf("Run(StopFailure with a numeric error) = %v; want a hook input error", err)
	}
}
