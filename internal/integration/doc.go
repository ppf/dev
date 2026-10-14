// Package integration holds the local integration suite (build tag integration). Every test runs on
// an isolated tmux server (-L) with temp resurrect dir and XDG_STATE_HOME; it needs tmux 3.8 and the
// exported ~/.tmux scripts and plugins and ~/.claude/hooks/tmux-agent-state.sh. TestMain builds the dev
// binary once; the parity tests run it side by side with the bash scripts:
//
//	go test -tags integration -count=1 ./internal/integration/
//
// Set DEV_TERMINAL_REPO to the ppf/terminal checkout to also fail on stale exports.
package integration
