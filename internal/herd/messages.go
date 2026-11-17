package herd

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/sidebar"
)

const (
	// threadShown is how many of a thread's messages, its latest, the block shows.
	threadShown = 2
	// textRows is how many rows a message's text wraps to.
	textRows = 2
	// threadIdle is how long an open thread goes quiet before the block drops it.
	threadIdle = 24 * time.Hour
	msgIndent  = "       "
)

// openThreads are the threads of ts the block shows at now: open and active within threadIdle.
func openThreads(ts []inbox.Thread, now time.Time) []inbox.Thread {
	var open []inbox.Thread
	for _, t := range ts {
		if t.State == inbox.ThreadOpen && now.Sub(time.Unix(t.Last().At, 0)) < threadIdle {
			open = append(open, t)
		}
	}
	return open
}

// msgRows is the block of open message threads under the board, in at most a third of the rem rows left by the to do
// block: header (messages · n open ──), then per thread, the most recently active first, its latest messages (time,
// sender → recipient, · reply on a reply, text) and its count (open · 2 of 6); "+ more" when cut. An alert row if unreadable; none
// without open threads.
func (m Model) msgRows(rem int) []string {
	w, room := m.width, rem/3
	if room < 1 {
		return nil
	}
	if m.snap.msgsErr != nil {
		return []string{sidebar.AlertRow("❌ messages: "+m.snap.msgsErr.Error(), w)}
	}
	ts := m.snap.threads
	if len(ts) == 0 {
		return nil
	}
	head := " " + sidebar.Muted + "messages · " + strconv.Itoa(len(ts)) + " open" + reset + " "
	head = ansi.Truncate(head, w, "…")
	rows := []string{head + rule + strings.Repeat("─", max(0, w-ansi.StringWidth(head))) + reset}
	for _, t := range ts {
		for _, msg := range t.Messages[max(0, len(t.Messages)-threadShown):] {
			to := msg.To
			if msg.ReplyTo != "" {
				to += " · reply"
			}
			at := time.Unix(msg.At, 0).In(m.clk.Now().Location()).Format("15:04")
			rows = append(rows, padRow(" "+sidebar.Muted+at+reset+" "+bold+msg.From+reset+sidebar.Muted+" → "+to+reset, w))
			rows = append(rows, wrapText(msg.Text, w)...)
		}
		rows = append(rows, padRow(" "+sidebar.Muted+"open · "+strconv.Itoa(len(t.Messages))+" of "+strconv.Itoa(inbox.ThreadCap)+reset, w))
	}
	if len(rows) > room {
		// The header, then as many rows as fit with the hint
		rows = append(rows[:max(1, room-1)], sidebar.HintRow("  + more: dev agent messages", w))
	}
	return rows[:min(len(rows), max(room, 1))]
}

// wrapText is text on one line, wrapped under msgIndent to at most textRows rows, … ending a cut one.
func wrapText(text string, w int) []string {
	width := max(1, w-len(msgIndent)-1)
	lines := strings.Split(ansi.Wordwrap(strings.Join(strings.Fields(text), " "), width, ""), "\n")
	if len(lines) > textRows {
		lines = lines[:textRows]
		lines[textRows-1] += " …"
	}
	rows := make([]string, len(lines))
	for i, l := range lines {
		rows[i] = padRow(msgIndent+ansi.Truncate(l, width, "…"), w)
	}
	return rows
}

// padRow is row cut to w columns and padded to them.
func padRow(row string, w int) string {
	row = ansi.Truncate(row, w, "…") + reset
	return row + strings.Repeat(" ", max(0, w-ansi.StringWidth(row)))
}
