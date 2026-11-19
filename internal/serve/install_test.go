package serve

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlist(t *testing.T) {
	p := string(Plist(Agent{Bin: "/a b/dev", Addr: "192.168.1.5:8417", Log: "/s/serve.log", Env: map[string]string{"PATH": "/x&y:/bin", "TMUX_TMPDIR": "/t"}}))
	for d := xml.NewDecoder(strings.NewReader(p)); ; {
		if _, err := d.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("not XML: %v\n%s", err, p)
		}
	}
	for _, want := range []string{
		"<key>Label</key><string>dev.serve</string>",
		"<string>/a b/dev</string>\n\t\t<string>serve</string>\n\t\t<string>--addr</string>\n\t\t<string>192.168.1.5:8417</string>\n",
		"<key>PATH</key><string>/x&amp;y:/bin</string>\n\t\t<key>TMUX_TMPDIR</key><string>/t</string>",
		"<key>RunAtLoad</key><true/>", "<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>",
		"<key>StandardErrorPath</key><string>/s/serve.log</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q:\n%s", want, p)
		}
	}
	if !strings.Contains(p, "<string>192.168.1.5:8417</string>\n\t\t<string>--awake=false</string>") {
		t.Errorf("not awake, plist lacks --awake=false:\n%s", p)
	}
	if p := string(Plist(Agent{Bin: "/dev", Log: "/l", Awake: true})); strings.Contains(p, "--addr") || strings.Contains(p, "--awake") {
		t.Errorf("no --addr, awake: plist has a flag:\n%s", p)
	}
}

func TestInstallUninstall(t *testing.T) {
	home := t.TempDir()
	log := filepath.Join(home, "state", "dev", "serve", "serve.log")
	if _, err := Uninstall(home); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("uninstall with none: %v", err)
	}
	path, err := Install(home, Agent{Bin: "/dev", Log: log})
	if err != nil || path != filepath.Join(home, "Library", "LaunchAgents", "dev.serve.plist") {
		t.Fatalf("install: %s %v", path, err)
	}
	if st, err := os.Stat(filepath.Dir(log)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("log folder: %v %v", st, err)
	}
	if _, err = Install(home, Agent{Bin: "/dev", Addr: "127.0.0.1:1", Log: log}); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "127.0.0.1:1") {
		t.Fatalf("reinstall kept the old plist:\n%s", b)
	}
	if got, err := Uninstall(home); err != nil || got != path {
		t.Fatalf("uninstall: %s %v", got, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plist still there: %v", err)
	}
}

func TestOnBattery(t *testing.T) {
	if !OnBattery("Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t80%; discharging") {
		t.Error("battery not seen")
	}
	if OnBattery("Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t100%; charged") {
		t.Error("AC seen as battery")
	}
}
