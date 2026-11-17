package wait

import (
	"strings"
	"testing"
	"time"

	"github.com/ppf/dev/internal/tmux"
)

func TestParse(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	now := time.Date(2026, 11, 17, 14, 2, 30, 0, loc)
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 11, day, hour, minute, 0, 0, loc) }
	tests := []struct {
		arg  string
		now  time.Time
		want time.Time
		err  string
	}{
		{"16:30", now, at(17, 16, 30), ""},
		{"9:05", time.Date(2026, 11, 17, 7, 0, 0, 0, loc), at(17, 9, 5), ""},
		{"18:02", now, at(17, 18, 2), ""},
		{"18:03", now, time.Time{}, "18:03 is more than 4h away"},
		{"14:02", now, time.Time{}, "14:02 has passed (tomorrow's is more than 4h away)"},
		{"9:00", now, time.Time{}, "9:00 has passed"},
		{"01:30", time.Date(2026, 11, 17, 23, 50, 0, 0, loc), at(18, 1, 30), ""},
		{"+90m", now, now.Add(90 * time.Minute), ""},
		{"+1h30m", now, now.Add(90 * time.Minute), ""},
		{"+4h", now, now.Add(4 * time.Hour), ""},
		{"+4h1m", now, time.Time{}, "more than 4h"},
		{"+0s", now, time.Time{}, "not ahead of now"},
		{"+soon", now, time.Time{}, "want +<duration>"},
		{"24:00", now, time.Time{}, "no such time"},
		{"4pm", now, time.Time{}, "want HH:MM"},
	}
	for _, tt := range tests {
		got, err := Parse(tt.arg, tt.now)
		if tt.err != "" {
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("Parse(%q) = %v, %v; want error with %q", tt.arg, got, err, tt.err)
			}
			continue
		}
		if err != nil || !got.Equal(tt.want) {
			t.Errorf("Parse(%q) = %v, %v; want %v", tt.arg, got, err, tt.want)
		}
	}
}

func TestNew(t *testing.T) {
	until := time.Unix(1_000, 0)
	if w, err := New(until, "  Codex limit\n resets "); err != nil || w.Reason != "Codex limit resets" || !w.Until.Equal(until) {
		t.Errorf("New = %+v, %v; want the reason on one line", w, err)
	}
	for _, reason := range []string{"", " \n", "reset;", "limit\x1freset", "a\x00b"} {
		if _, err := New(until, reason); err == nil {
			t.Errorf("New(%q) = nil error, want one", reason)
		}
	}
	if _, err := New(time.Time{}, "x"); err == nil {
		t.Error("New without a time: nil error")
	}
}

func TestWaitText(t *testing.T) {
	now := time.Date(2026, 11, 17, 14, 0, 0, 0, time.Local)
	w := Wait{Until: time.Date(2026, 11, 17, 16, 30, 0, 0, time.Local), Reason: "Codex limit resets"}
	if got := w.Text(now); got != "until 16:30 · Codex limit resets" {
		t.Errorf("Text = %q", got)
	}
	if got := w.Phrase(now); got != "until 16:30 (Codex limit resets)" {
		t.Errorf("Phrase = %q", got)
	}
	w.Until = w.Until.AddDate(0, 0, 1)
	if got := w.Text(now); got != "until Nov 18 16:30 · Codex limit resets" {
		t.Errorf("next day's Text = %q", got)
	}
	if !w.Held() || w.Due(now) || !w.Due(w.Until) || (Wait{}).Held() || (Wait{}).Due(now) {
		t.Error("Held/Due")
	}
}

func TestFromOptions(t *testing.T) {
	if w := FromOptions("1700000000", "x"); !w.Held() || w.Until.Unix() != 1700000000 || w.Reason != "x" {
		t.Errorf("FromOptions = %+v", w)
	}
	for _, until := range []string{"", "0", "soon"} {
		if w := FromOptions(until, "x"); w.Held() {
			t.Errorf("FromOptions(%q) = %+v, want none", until, w)
		}
	}
	if w := FromOptions("1700000000", ""); w.Held() {
		t.Errorf("FromOptions without a reason = %+v, want none", w)
	}
}

func TestSetUnset(t *testing.T) {
	var ran []string
	c := tmux.NewFake(func(args []string) (string, error) {
		ran = append(ran, strings.Join(args, " "))
		return "", nil
	})
	if err := Set(c, "%3", Wait{Until: time.Unix(1700000000, 0), Reason: "Codex limit resets"}); err != nil {
		t.Fatal(err)
	}
	if err := Unset(c, "%3"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"set-option -p -t %3 @dev_wait_until 1700000000 ; set-option -p -t %3 @dev_wait_reason Codex limit resets ; set-option -pF -t %3 @dev_wait_prompts #{@agent_prompts}",
		"set-option -pu -t %3 @dev_wait_until ; set-option -pu -t %3 @dev_wait_reason ; set-option -pu -t %3 @dev_wait_prompts",
	}
	if strings.Join(ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(ran, "\n"), strings.Join(want, "\n"))
	}
}
