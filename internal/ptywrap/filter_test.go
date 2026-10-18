package ptywrap

import (
	"bytes"
	"testing"
)

const (
	transmit = "\x1b_Ga=T,U=1,q=1,f=100,t=f,i=7,c=4,r=2;L3RtcC9hLnBuZw==\x1b\\"
	wrapped  = "\x1bPtmux;\x1b\x1b_Ga=T,U=1,q=1,f=100,t=f,i=7,c=4,r=2;L3RtcC9hLnBuZw==\x1b\x1b\\\x1b\\"
	del      = "\x1b_Ga=d,d=I,i=7,q=2\x1b\\"
	delWrap  = "\x1bPtmux;\x1b\x1b_Ga=d,d=I,i=7,q=2\x1b\x1b\\\x1b\\"
)

func run(chunks ...string) (string, bool) {
	var f filter
	var out []byte
	for _, c := range chunks {
		out = f.Write(out, []byte(c))
	}
	return string(out), len(f.Flush(nil)) > 0
}

func TestFilter(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain text", "hello\r\nworld", "hello\r\nworld"},
		{"CSI and OSC untouched", "\x1b[31mred\x1b[0m\x1b]0;title\x07\x1b]8;;u\x1b\\l", "\x1b[31mred\x1b[0m\x1b]0;title\x07\x1b]8;;u\x1b\\l"},
		{"transmit wrapped", "a" + transmit + "b", "a" + wrapped + "b"},
		{"delete wrapped", del, delWrap},
		{"two in a row", transmit + del, wrapped + delWrap},
		{"non-graphics APC untouched", "\x1b_Xabc\x1b\\", "\x1b_Xabc\x1b\\"},
		{"ESC ESC _G wraps the second", "\x1b\x1b_Gq=2\x1b\\", "\x1b\x1bPtmux;\x1b\x1b_Gq=2\x1b\x1b\\\x1b\\"},
		{"ESC _ ESC", "\x1b_\x1b[m", "\x1b_\x1b[m"},
		{"already wrapped passes as is", wrapped + "x", wrapped + "x"},
		{"tmux passthrough of OSC 52", "\x1bPtmux;\x1b\x1b]52;c;eA==\x07\x1b\\x", "\x1bPtmux;\x1b\x1b]52;c;eA==\x07\x1b\\x"},
		{"DCS query untouched", "\x1bP+q544e\x1b\\\x1b_Gi=1\x1b\\", "\x1bP+q544e\x1b\\\x1bPtmux;\x1b\x1b_Gi=1\x1b\x1b\\\x1b\\"},
		{"ESC aborting a DCS starts an APC", "\x1bPq#0\x1b_Gi=1\x1b\\", "\x1bPq#0\x1bPtmux;\x1b\x1b_Gi=1\x1b\x1b\\\x1b\\"},
		{"ESC aborting an APC ends the wrap", "\x1b_Gi=1\x1b[m", "\x1bPtmux;\x1b\x1b_Gi=1\x1b\\\x1b[m"},
		{"UTF-8 and placeholders untouched", "\U0010EEEE̅̅ ✅", "\U0010EEEE̅̅ ✅"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, pending := run(tt.in)
			if got != tt.want || pending {
				t.Errorf("got %q (pending %v), want %q", got, pending, tt.want)
			}
		})
	}
}

// Every split of the input into two or three chunks gives the same output as one chunk.
func TestFilterSplits(t *testing.T) {
	in := "x\x1b[1m" + transmit + "\x1b_Xq\x1b\\" + del + "\x1bPtmux;\x1b\x1b]52;c;eA==\x07\x1b\\" + "\x1b\x1b_Gi=2\x1b\\y" + "\x1bPq\x1b_Gi=3\x1b\\"
	want, _ := run(in)
	for i := 0; i <= len(in); i++ {
		for j := i; j <= len(in); j++ {
			if got, _ := run(in[:i], in[i:j], in[j:]); got != want {
				t.Fatalf("split %d,%d: got %q, want %q", i, j, got, want)
			}
		}
	}
}

func TestFilterHoldsOnlyAmbiguousTail(t *testing.T) {
	var f filter
	out := f.Write(nil, []byte("abc\x1b"))
	if string(out) != "abc" {
		t.Fatalf("got %q, want abc held ESC", out)
	}
	out = f.Write(out, []byte("_"))
	if string(out) != "abc" {
		t.Fatalf("got %q, want abc held ESC _", out)
	}
	if out = f.Flush(out); string(out) != "abc\x1b_" {
		t.Fatalf("flush: got %q", out)
	}
	// The body streams: the start of the wrap and the body go out at once
	out = f.Write(nil, []byte("\x1b_Ga=T;QUJD"))
	if string(out) != "\x1bPtmux;\x1b\x1b_Ga=T;QUJD" {
		t.Fatalf("body: got %q", out)
	}
}

func BenchmarkFilterPlain(b *testing.B) {
	chunk := bytes.Repeat([]byte("\x1b[38;5;123mhello world \x1b[0m"), 1000)
	var f filter
	buf := make([]byte, 0, len(chunk)*2)
	b.SetBytes(int64(len(chunk)))
	for b.Loop() {
		buf = f.Write(buf[:0], chunk)
	}
}
