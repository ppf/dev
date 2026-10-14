// Package integration holds the local integration suite (build tag integration). Every test runs on
// an isolated tmux server (-L) with temp resurrect dir and XDG_STATE_HOME, in a UTF-8 locale, and with
// HOME at a temp dir holding the bash reference. It needs tmux 3.8, git, go, rg and fzf (used by the
// bash scripts), the tmux plugins in ~/.tmux/plugins and a ppf/terminal clone containing bashRef, at
// ~/PhpstormProjects/ppf/terminal or $DEV_TERMINAL_REPO. TestMain builds the dev binary once; the
// parity tests run it side by side with the bash scripts:
//
//	go test -tags integration -count=1 ./internal/integration/
package integration
