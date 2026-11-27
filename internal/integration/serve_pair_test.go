//go:build integration

package integration

import (
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/serve"
)

// dev serve pair --popup, run by the manager's Claude, opens dev serve pair in a popup on the user's client: the code
// shows there and pairs a second device, and never reaches the command's output, the manager's pane, tmux or a file.
// Once a prompt of the user's; refused with no prompt and with no client showing the manager.
func TestServePairPopup(t *testing.T) {
	ip := lanIP(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	dir := filepath.Join(stateDir, "dev", "serve")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A dev serve a phone can reach, as far as pair asks: its /v1/health only, on this machine's own address
	ln, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	health := &httptest.Server{Listener: ln, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"version":"test"}`))
	})}}
	health.Start()
	t.Cleanup(health.Close)
	if _, err := serve.RecordAddr(dir, netip.MustParseAddrPort(ln.Addr().String()), false); err != nil {
		t.Fatal(err)
	}

	s := newServer(t, "set -g base-index 1\nset -g @dev_notify off\n", "XDG_STATE_HOME="+stateDir)
	s.T("new-session", "-d", "-s", "manager", "sleep 600")
	s.T("set-option", "-t", "=manager:", "@dev_herd", "1")
	manager := s.T("display", "-t", "=manager:", "-p", "#{pane_id}")
	dev := func(stdin string, argv ...string) (string, string, error) {
		t.Helper()
		cmd := exec.Command(devBin, argv...)
		cmd.Env = s.scriptEnv("TMUX_PANE="+manager, "CLAUDECODE=1")
		cmd.Stdin = strings.NewReader(stdin)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
	refused := func(what, want string) {
		t.Helper()
		if out, errOut, err := dev("", "serve", "pair", "--popup"); err == nil || !strings.Contains(errOut, want) {
			t.Errorf("❌ %s: %q %q %v, want refused with %q", what, out, errOut, err, want)
		}
	}
	if _, errOut, err := dev(`{"hook_event_name":"SessionStart"}`, "state", "idle"); err != nil {
		t.Fatalf("❌ register the manager: %v %s", err, errOut)
	}
	s.T("set-option", "-p", "-t", manager, "@agent_cmd", "sleep")
	refused("before the user prompted", "the user hasn't prompted the manager yet")
	if _, errOut, err := dev(`{"hook_event_name":"UserPromptSubmit","prompt":"pair my phone"}`, "state", "working"); err != nil {
		t.Fatalf("❌ the user's prompt: %v %s", err, errOut)
	}
	refused("no client on the manager", "no client shows the manager")

	user := newServer(t, "")
	user.T("new-session", "-d", "-s", "out", "-x", "160", "-y", "40")
	user.T("send-keys", "-t", "out", "TMUX= tmux -L "+s.socket+" attach -t manager", "Enter")
	waitFor(t, "the user's client attached", func() bool { c, _ := s.Try("list-clients", "-F", "#{client_name}"); return c != "" })

	out, errOut, err := dev("", "serve", "pair", "--popup")
	if err != nil || !strings.HasPrefix(out, "pair popup opened on ") {
		t.Fatalf("❌ pair --popup: %q %q %v", out, errOut, err)
	}
	codeRe := regexp.MustCompile(`enter the code (\d{4}) (\d{4})`)
	var m []string
	waitFor(t, "the code in the popup", func() bool {
		m = codeRe.FindStringSubmatch(user.T("capture-pane", "-p", "-t", "out"))
		return m != nil
	})
	shown := user.T("capture-pane", "-p", "-t", "out")
	if !strings.Contains(shown, "http://"+ln.Addr().String()+"/") || !strings.Contains(shown, "Press enter to close") {
		t.Errorf("❌ popup:\n%s", shown)
	}
	forms := []string{m[1] + " " + m[2], m[1] + m[2]}
	leaks := func(where, text string) {
		t.Helper()
		for _, f := range forms {
			if strings.Contains(text, f) {
				t.Errorf("❌ the code %q in %s:\n%s", f, where, text)
			}
		}
	}
	leaks("the command's output", out+errOut)
	leaks("the manager's pane", s.T("capture-pane", "-p", "-J", "-S", "-", "-t", manager))
	leaks("the tmux environment", s.T("show-environment", "-g"))
	leaks("tmux's buffers", s.T("list-buffers", "-F", "#{buffer_sample}"))
	leaks("the clients and panes", s.T("list-clients", "-F", "#{client_name} #{client_termname}")+s.T("list-panes", "-a", "-F", "#{pane_start_command} #{pane_title}"))
	if err := filepath.WalkDir(stateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		leaks(path, string(b))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/pair", strings.NewReader(`{"code":"`+forms[0]+`","device":"phone"}`))
	req.Header.Set("Content-Type", "application/json")
	(&serve.Auth{Dir: dir, Now: time.Now}).Pair(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"device": "phone"`) {
		t.Errorf("❌ pairing with the popup's code: %d %s", rec.Code, rec.Body)
	}
	user.T("send-keys", "-t", "out", "Enter")
	waitFor(t, "the popup closed", func() bool { return !codeRe.MatchString(user.T("capture-pane", "-p", "-t", "out")) })

	refused("a second popup for the same prompt", "paired a phone already")
	if _, errOut, err := dev(`{"hook_event_name":"UserPromptSubmit","prompt":"and my tablet"}`, "state", "working"); err != nil {
		t.Fatalf("❌ the user's next prompt: %v %s", err, errOut)
	}
	if out, errOut, err := dev("", "serve", "pair", "--popup"); err != nil || !strings.HasPrefix(out, "pair popup opened on ") {
		t.Errorf("❌ pair --popup for a new prompt: %q %q %v", out, errOut, err)
	}
	waitFor(t, "the second popup", func() bool { return codeRe.MatchString(user.T("capture-pane", "-p", "-t", "out")) })
	user.T("send-keys", "-t", "out", "Enter")
}

// lanIP is one of this machine's own addresses that isn't loopback, which dev serve pair names to a phone.
func lanIP(t *testing.T) netip.Addr {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP.To4()); ok && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				return ip
			}
		}
	}
	t.Skip("no address but loopback: dev serve pair has no phone URL to show")
	return netip.Addr{}
}
