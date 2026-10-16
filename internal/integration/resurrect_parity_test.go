//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The resurrect hooks outside a real save or restore, dev-resurrect.sh against `dev resurrect` on twin
// servers: save writes <snapshot>.dev; a missing snapshot fails loudly and keeps the old file; prune
// deletes only the .dev files whose snapshot is gone; pre-restore records the live sessions.
func TestResurrectHooksParity(t *testing.T) {
	script := filepath.Join(tmuxHome(t), "sessions/dev-resurrect.sh")
	requireFiles(t, filepath.Join(tmuxHome(t), "plugins/tmux-resurrect/scripts/helpers.sh"))

	got := map[string]string{}
	for _, impl := range []string{"bash", "dev"} {
		d := newDevServer(t, "")
		d.T("new-session", "-d", "-s", "devt")
		d.T("set-option", "-t", "=devt:", "@project", "devt")
		dir := filepath.Join(d.tmp, "resurrect")
		kept, missing := filepath.Join(dir, "tmux_resurrect_1.txt"), filepath.Join(dir, "tmux_resurrect_2.txt")
		writeFile(t, kept, "snapshot\n")
		writeFile(t, missing+".dev", "old\n")
		hook := func(args ...string) string {
			t.Helper()
			name := script
			if impl == "dev" {
				name, args = devBin, append([]string{"resurrect"}, args...)
			}
			out, err := runScript(d.scriptEnv(), name, args...)
			return "status=" + errStatus(err) + " output=" + out
		}
		read := func(path string) string {
			b, err := os.ReadFile(path)
			if err != nil {
				return "missing"
			}
			return strconv.Quote(string(b))
		}

		var b strings.Builder
		b.WriteString("save: " + hook("save", kept) + " file=" + read(kept+".dev") + "\n")
		b.WriteString("save missing: " + hook("save", missing) + " file=" + read(missing+".dev") + "\n")
		b.WriteString("warnings: " + strconv.Quote(d.T("show-option", "-gqv", "@dev_resurrect_warnings")) + "\n")
		b.WriteString("prune: " + hook("prune"))
		entries, err := os.ReadDir(dir)
		b.WriteString(" left=" + dirListing(entries, err) + "\n")
		b.WriteString("pre-restore: " + hook("pre-restore") + " live=" + read(filepath.Join(d.tmp, "state/dev/live-before-restore")))
		got[impl] = strings.ReplaceAll(b.String(), d.tmp, "<tmp>")
		t.Logf("%s:\n%s", impl, got[impl])
	}
	check(t, "hooks (dev = bash)", got["dev"], got["bash"])
	check(t, "dev: hooks", got["dev"], `save: status=0 output= file="devt\tdevt\t\n"
save missing: status=1 output= file="old\n"
warnings: "snapshot '<tmp>/resurrect/tmux_resurrect_2.txt' missing, dev state not saved; "
prune: status=0 output= left=tmux_resurrect_1.txt tmux_resurrect_1.txt.dev
pre-restore: status=0 output= live="boot\ndevt\n"`)
}
