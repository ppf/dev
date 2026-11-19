package serve

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Label is the launchd agent's label, and its plist's name in ~/Library/LaunchAgents.
const Label = "dev.serve"

// LaunchEnv is the environment dev reads that launchd does not give an agent: tmux is found on PATH, and the
// state folder (pairing, lists) must be the one dev serve pair uses.
var LaunchEnv = []string{"PATH", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "TMUX_TMPDIR", "CLAUDE_CONFIG_DIR"}

// Agent is what the launchd agent runs: Bin serve [--addr Addr] [--awake=false], with Env, logging to Log.
type Agent struct {
	Bin, Addr, Log string
	Awake          bool
	Env            map[string]string
}

// PlistPath is the agent's plist under home.
func PlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

// Plist is the agent's launchd plist: started at login and again whenever it fails (launchd waits 10 s between
// starts, so a bind that fails before Wi-Fi is up is retried).
func Plist(a Agent) []byte {
	var b strings.Builder
	str := func(s string) string {
		var e strings.Builder
		_ = xml.EscapeText(&e, []byte(s))
		return "<string>" + e.String() + "</string>"
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>` + str(Label) + `
	<key>ProgramArguments</key>
	<array>
`)
	args := []string{a.Bin, "serve"}
	if a.Addr != "" {
		args = append(args, "--addr", a.Addr)
	}
	if !a.Awake {
		args = append(args, "--awake=false")
	}
	for _, arg := range args {
		b.WriteString("\t\t" + str(arg) + "\n")
	}
	b.WriteString("\t</array>\n\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	keys := make([]string, 0, len(a.Env))
	for k := range a.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("\t\t<key>" + k + "</key>" + str(a.Env[k]) + "\n")
	}
	b.WriteString("\t</dict>\n\t<key>RunAtLoad</key><true/>\n\t<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n")
	b.WriteString("\t<key>StandardOutPath</key>" + str(a.Log) + "\n\t<key>StandardErrorPath</key>" + str(a.Log) + "\n")
	b.WriteString("</dict>\n</plist>\n")
	return []byte(b.String())
}

// Install writes a's plist under home (replacing one there) and makes its log's folder.
func Install(home string, a Agent) (string, error) {
	path := PlistPath(home)
	if err := os.MkdirAll(filepath.Dir(a.Log), 0o700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, Plist(a), 0o644)
}

// Uninstall removes the agent's plist under home; an error when there is none.
func Uninstall(home string) (string, error) {
	path := PlistPath(home)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("dev serve is not installed: no %s", path)
		}
		return "", err
	}
	return path, nil
}

// OnBattery says whether pmset -g ps's output has the Mac drawing from its battery.
func OnBattery(pmset string) bool {
	return strings.Contains(pmset, "'Battery Power'")
}
