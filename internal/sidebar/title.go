package sidebar

import "regexp"

// titleNoise are normalize_pane_title's perl substitutions, applied in order: real CSI sequences,
// then their caret-escaped and ESC-less leftovers, then any control character.
var titleNoise = []*regexp.Regexp{
	regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`),
	regexp.MustCompile(`\^\[\[[0-9;?]*[A-Za-z~]`),
	regexp.MustCompile(`\^\[O[A-Za-z~]`),
	regexp.MustCompile(`\[\[[0-9;?]*[A-Za-z~]`),
	regexp.MustCompile(`\[O[A-Za-z~]`),
	regexp.MustCompile(`[\x00-\x1F\x7F]`),
}

// titleLeadSymbol is bash's ^[^[:alnum:]][[:space:]]+(.+); macOS's UTF-8 [:alnum:] is letters and
// decimal digits (² and Ⅻ are not alnum there).
var titleLeadSymbol = regexp.MustCompile(`^[^\pL\p{Nd}][[:space:]]+(.+)`)

// codexTitle splits a Codex title at its last " | " (greedy, like bash's =~).
var codexTitle = regexp.MustCompile(`^(.+)[[:space:]]\|[[:space:]](.+)$`)

// cleanTitle strips ANSI/control sequences (escaped or raw) and a leading symbol plus spaces from a
// pane title (normalize_pane_title).
func cleanTitle(title string) string {
	for _, re := range titleNoise {
		title = re.ReplaceAllString(title, "")
	}
	if m := titleLeadSymbol.FindStringSubmatch(title); m != nil {
		return m[1]
	}
	return title
}

// isGenericTitle reports titles that say nothing about the session (is_generic_title); host is the
// hostname tmux gives new panes as their title.
func isGenericTitle(title, session, host string) bool {
	switch title {
	case "", "tmux-session-sidebar", "terminal", "Agent", "Claude Code", session, host:
		return true
	}
	return false
}

// agentTitle rewrites a Codex title "a | b" to "b on a"; other titles are kept (normalize_agent_title).
func agentTitle(label, title string) string {
	if label != "Codex" {
		return title
	}
	if m := codexTitle.FindStringSubmatch(title); m != nil {
		return m[2] + " on " + m[1]
	}
	return title
}
