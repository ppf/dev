package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ppf/dev/internal/serve"
)

func TestInstallAgent(t *testing.T) {
	home := t.TempDir()
	a := serve.Agent{Bin: "/bin/dev", Log: filepath.Join(home, "serve.log")}
	refused := a
	refused.Addr = "0.0.0.0:8417"
	var out bytes.Buffer
	if err := installAgent(&out, home, "gui/501", refused); err == nil {
		t.Fatal("0.0.0.0 installed")
	}
	if _, err := os.Stat(serve.PlistPath(home)); !os.IsNotExist(err) {
		t.Fatalf("a refused --addr wrote the plist: %v", err)
	}
	if err := installAgent(&out, home, "gui/501", a); err != nil {
		t.Fatal(err)
	}
	path := serve.PlistPath(home)
	want := "wrote " + path + ": /bin/dev serve starts at each login (log: " + a.Log + ")\n" +
		"start it now: launchctl bootout gui/501/dev.serve 2>/dev/null; sleep 1; launchctl bootstrap gui/501 \"" + path + "\"\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestLaunchEnv(t *testing.T) {
	set := map[string]string{"PATH": "/opt/homebrew/bin:/bin", "XDG_STATE_HOME": "/s", "HOME": "/h"}
	env := launchEnv(func(k string) string { return set[k] })
	if len(env) != 2 || env["PATH"] != "/opt/homebrew/bin:/bin" || env["XDG_STATE_HOME"] != "/s" {
		t.Fatalf("got %v", env)
	}
}
