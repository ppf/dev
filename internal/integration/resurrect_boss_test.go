//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A saved boss comes back as a boss: marked, its coordinator resumed into its conversation with the
// coordinator prompt, its previews and sidebar back, so `dev boss` attaches it. A plain session named
// like a boss stays plain.
func TestResurrectBoss(t *testing.T) {
	home := tmuxHome(t)
	restore := filepath.Join(home, "plugins/tmux-resurrect/scripts/restore.sh")
	save := filepath.Join(home, "plugins/tmux-resurrect/scripts/save.sh")
	requireFiles(t, restore, save, filepath.Join(home, "plugins/tmux-resurrect/scripts/helpers.sh"))
	dir, resurrectDir := tempDir(t), tempDir(t)
	d := newDevServer(t, resurrectConf(resurrectDir))
	env := d.scriptEnv(writeProjects(t, [2]string{"app", dir}))
	display := func(target, format string) string {
		out, _ := d.Try("display", "-t", target, "-p", format)
		return out
	}
	openBoss := func(desc string) {
		t.Helper()
		cmd := exec.Command(devBin, "boss", "app")
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dev boss app (%s): %v: %s", desc, err, out)
		}
		check(t, "client on the boss ("+desc+")", d.T("list-clients", "-F", "#{client_session}"), "boss-app")
	}

	openBoss("new")
	coordinator := display("=boss-app:1.2", "#{pane_id}")
	waitFor(t, "previews running", func() bool { return display("=boss-app:1.3", "#{pane_current_command}") == "dev" })
	transcript := filepath.Join(tempDir(t), "b1.jsonl")
	writeFile(t, transcript, "{}\n")
	hook := exec.Command(devBin, "state", "idle")
	hook.Env = d.scriptEnv("TMUX_PANE=" + coordinator)
	hook.Stdin = strings.NewReader(`{"session_id":"b1","transcript_path":"` + transcript + `"}`)
	if out, err := hook.CombinedOutput(); err != nil {
		t.Fatalf("dev state idle in the coordinator: %v\n%s", err, out)
	}
	d.T("new-session", "-d", "-s", "worker", "-c", dir)
	d.T("set-option", "-t", "=worker:", "@dev_started_by", "boss-app")
	d.T("new-session", "-d", "-s", "boss-zzz")
	d.T("run-shell", save+" quiet")
	last, _ := os.Readlink(filepath.Join(resurrectDir, "last"))
	state, _ := os.ReadFile(filepath.Join(resurrectDir, last+".dev"))
	check(t, "boss saved", string(state), "boss-app\tapp\t1.1\n:started_by\tworker\tboss-app\n:boss\tboss-app\t1.2\t1.3\n"+
		":agent\tboss-app\t1.2\tclaude\tb1\t"+transcript+"\t"+dir+"\n")
	if err := os.Truncate(d.stubLog, 0); err != nil {
		t.Fatal(err)
	}

	d.kill()
	d.T("start-server", ";", "set-option", "-g", "@dev_restored", "1", ";", "run-shell", restore)
	sessions := strings.Split(d.T("ls", "-F", "#{session_name}"), "\n")
	sort.Strings(sessions)
	check(t, "sessions restored", strings.Join(sessions, " "), "boot boss-app boss-zzz worker")
	check(t, "boss marked, the look-alike not", d.T("show-option", "-qv", "-t", "=boss-app:", "@dev_herd")+"/"+
		d.T("show-option", "-qv", "-t", "=boss-zzz:", "@dev_herd"), "1/")
	coordinator, previews := display("=boss-app:1.2", "#{pane_id}"), display("=boss-app:1.3", "#{pane_id}")
	waitFor(t, "coordinator resumed", func() bool {
		log, _ := os.ReadFile(d.stubLog)
		return strings.Contains(string(log), coordinator+" claude")
	})
	log, _ := os.ReadFile(d.stubLog)
	if line := strings.TrimSpace(string(log)); !strings.HasPrefix(line, coordinator+" claude --resume b1 --append-system-prompt You are the coordinator") ||
		!strings.HasSuffix(line, "--remote-control=boss-app") {
		t.Errorf("coordinator command: %q", line)
	}
	waitFor(t, "previews restarted", func() bool { return display(previews, "#{pane_current_command}") == "dev" })
	check(t, "previews command", boolStr(strings.Contains(display(previews, "#{pane_start_command}"), "boss --previews")), "1")
	check(t, "pane roles", d.T("show-option", "-pqv", "-t", coordinator, "@dev_boss_pane")+" "+
		d.T("show-option", "-pqv", "-t", previews, "@dev_boss_pane"), "coordinator previews")
	check(t, "coordinator in its directory", display(coordinator, "#{pane_current_path}"), dir)
	sidebar := d.T("show-option", "-qv", "-t", "=boss-app:", "@sidebar_pane")
	check(t, "sidebar rebuilt", devSidebarStart(display(sidebar, "#{pane_start_command}")), "true")
	check(t, "worker still the boss's", d.T("show-option", "-qv", "-t", "=worker:", "@dev_started_by"), "boss-app")
	check(t, "no restore warnings", d.T("show", "-gqv", "@dev_resurrect_warnings"), "")

	d.outer.T("send-keys", "-t", "out", "TMUX= tmux -L "+d.socket+" attach -t boot", "Enter")
	waitFor(t, "client attached again", func() bool {
		c, _ := d.Try("list-clients", "-F", "#{client_session}")
		return c == "boot"
	})
	openBoss("restored")
	check(t, "attached, not recreated", display("=boss-app:1.2", "#{pane_id}"), coordinator)

	d.T("send-keys", "-t", previews, "C-c")
	waitFor(t, "restored boss ends with its previews", func() bool {
		_, err := d.Try("has-session", "-t", "=boss-app:")
		return err != nil
	})
}
