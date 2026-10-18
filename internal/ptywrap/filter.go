// Package ptywrap runs a command on its own pty and wraps the kitty graphics APCs (ESC _G … ESC \) in its
// output for tmux passthrough, which Claude Code doesn't do: tmux drops a raw APC (and makes it the pane
// title), but passes ESC Ptmux; <APC with each ESC doubled> ESC \ to the terminal when allow-passthrough
// is on. Everything else is copied byte for byte.
package ptywrap

import "bytes"

const esc = 0x1b

var (
	openWrap  = []byte("\x1bPtmux;\x1b\x1b_G")
	closeWrap = []byte("\x1b\x1b\\\x1b\\")
	endWrap   = []byte("\x1b\\")
)

type state int

const (
	ground         state = iota
	sawEsc               // ESC held: it may start ESC _G
	sawEscAPC            // ESC _ held
	graphics             // in a wrapped ESC _G body
	graphicsEsc          // ESC in the body held: ST, or an ESC that aborts the APC
	passthrough          // in a DCS, tmux's own passthrough included: copied as is
	passthroughEsc       // ESC in a DCS held: ESC ESC is an escaped ESC (tmux), ESC \ ends it
)

// filter rewrites a byte stream chunk by chunk; a sequence may be split anywhere between chunks. Only an
// ESC (or ESC _) that ends a chunk is held back, until the next chunk says what it starts: a terminal
// shows nothing for it before that anyway.
type filter struct {
	state state
}

// Write appends p, rewritten, to dst.
func (f *filter) Write(dst, p []byte) []byte {
	for i := 0; i < len(p); i++ {
		b := p[i]
		switch f.state {
		case ground:
			var found bool
			dst, i, found = copyToEsc(dst, p, i)
			if found {
				f.state = sawEsc
			}
		case sawEsc:
			switch b {
			case '_':
				f.state = sawEscAPC
			case 'P':
				dst = append(dst, esc, 'P')
				f.state = passthrough
			case esc:
				dst = append(dst, esc)
			default:
				dst = append(dst, esc, b)
				f.state = ground
			}
		case sawEscAPC:
			if b == 'G' {
				dst = append(dst, openWrap...)
				f.state = graphics
				continue
			}
			dst = append(dst, esc, '_')
			f.state = ground
			i-- // b starts afresh: it may be an ESC
		case graphics:
			var found bool
			dst, i, found = copyToEsc(dst, p, i)
			if found {
				f.state = graphicsEsc
			}
		case graphicsEsc:
			if b == '\\' {
				dst = append(dst, closeWrap...)
				f.state = ground
				continue
			}
			// An ESC that isn't ST aborts the APC: end the wrap, the ESC starts the next sequence
			dst = append(dst, endWrap...)
			f.state = sawEsc
			i--
		case passthrough:
			var found bool
			dst, i, found = copyToEsc(dst, p, i)
			if found {
				f.state = passthroughEsc
			}
		case passthroughEsc:
			switch b {
			case esc:
				dst = append(dst, esc, esc)
				f.state = passthrough
			case '\\':
				dst = append(dst, esc, b)
				f.state = ground
			default:
				// An ESC that ends the DCS otherwise starts the next sequence, which may be ESC _G
				f.state = sawEsc
				i--
			}
		}
	}
	return dst
}

// copyToEsc appends p[i:] up to its next ESC to dst and returns the ESC's index, found=false at p's end.
func copyToEsc(dst, p []byte, i int) ([]byte, int, bool) {
	j := bytes.IndexByte(p[i:], esc)
	if j < 0 {
		return append(dst, p[i:]...), len(p), false
	}
	return append(dst, p[i:i+j]...), i + j, true
}

// Flush appends the held-back bytes to dst as they are: no more output comes to say they start an APC.
func (f *filter) Flush(dst []byte) []byte {
	switch f.state {
	case sawEsc, passthroughEsc:
		dst = append(dst, esc)
	case sawEscAPC:
		dst = append(dst, esc, '_')
	default:
		return dst
	}
	f.state = ground
	return dst
}
