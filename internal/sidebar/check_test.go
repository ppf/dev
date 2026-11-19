package sidebar

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestCheckText(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	tests := []struct {
		c           Check
		short, long string
	}{
		{Check{Kind: CheckApproved, PR: 412, Since: ago(2 * time.Hour)}, "#412 approved 2h", "PR #412 approved 2h ago"},
		{Check{Kind: CheckMerged, PR: 418, Since: ago(40 * time.Minute)}, "#418 merged 40m", "PR #418 merged 40m ago"},
		{Check{Kind: CheckUnchanged, Since: ago(5*24*time.Hour + 3*time.Hour)}, "unchanged 5d", "unchanged 5d: check it at its source"},
		{Check{Kind: CheckBossClosed, Since: ago(time.Hour)}, "boss closed", "boss closed"},
	}
	for _, tt := range tests {
		if got := tt.c.Short(now); got != tt.short {
			t.Errorf("%s Short = %q, want %q", tt.c.Kind, got, tt.short)
		}
		if got := tt.c.Long(now); got != tt.long {
			t.Errorf("%s Long = %q, want %q", tt.c.Kind, got, tt.long)
		}
	}
	if got := AgeSpan(18*time.Hour + 19*time.Minute); got != "18h 19m" {
		t.Errorf("AgeSpan(18h19m) = %q", got)
	}
	if got := AgeSpan(47 * time.Hour); got != "47h" {
		t.Errorf("AgeSpan(47h) = %q", got)
	}
}

func TestParsePRView(t *testing.T) {
	p, err := ParsePRView([]byte(`{"state":"MERGED","mergedAt":"2026-11-18T14:20:00Z","closedAt":"2026-11-18T14:20:00Z","reviewDecision":"APPROVED",
		"statusCheckRollup":[{"name":"test","status":"COMPLETED","conclusion":"SUCCESS"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.State != "MERGED" || p.Review != "APPROVED" || p.Checks != CIPassed || p.Ended.Unix() != time.Date(2026, 11, 18, 14, 20, 0, 0, time.UTC).Unix() {
		t.Errorf("view = %+v", p)
	}
	p, err = ParsePRView([]byte(`{"state":"OPEN","mergedAt":null,"closedAt":null,"reviewDecision":"","statusCheckRollup":[]}`))
	if err != nil || p.State != "OPEN" || !p.Ended.IsZero() || p.Checks != "" {
		t.Errorf("open view = %+v, %v", p, err)
	}
	if _, err := ParsePRView([]byte("nope")); err == nil {
		t.Error("bad JSON read")
	}
}

// A closed boss's list shows on the manager's board and the sidebar's own rows, on no other board; a row carries its
// item's check, and a stale list's row its item's own age, not the list's.
func TestNeedRowsChecks(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	sessions := Order([]Session{
		{Name: "manager", Group: "manager", ID: "$1", Boss: true},
		{Name: "boss-orbit", Group: "orbit", ID: "$2", Boss: true, StartedBy: "$1"},
		{Name: "boss-harbor", Group: "harbor", ID: "$3", Boss: true},
	})
	unchanged := &Check{Kind: CheckUnchanged, Since: now.Add(-5 * 24 * time.Hour).Unix()}
	lists := []NeedList{
		{Boss: "boss-orbit", Items: []NeedItem{{ID: 1, Text: "Attach the DPA", Check: unchanged, Status: StatusYours}, {ID: 2, Text: "Review copy", Added: now.Add(-time.Hour).Unix(), Status: StatusYours}}},
		{Boss: "boss-harbor", Stale: true, Updated: now.Add(-2 * time.Hour).Unix(), Items: []NeedItem{{ID: 1, Text: "VAT", Added: now.Add(-(18*time.Hour + 19*time.Minute)).Unix(), Status: StatusYours}}},
		{Boss: "boss-kite", Closed: true, Items: []NeedItem{{ID: 3, Text: "Ask Mara", Check: &Check{Kind: CheckBossClosed}, Status: StatusBossClosed}}},
	}
	rows := func(root string) []string {
		var out []string
		for _, r := range NeedRows(sessions, lists, root) {
			out = append(out, r.Project+" "+r.Text+" "+r.Age(now))
		}
		return out
	}
	if got, want := strings.Join(rows("manager"), "|"), "orbit Attach the DPA unchanged 5d|orbit Review copy |kite Ask Mara boss closed"; got != want {
		t.Errorf("manager rows = %q, want %q", got, want)
	}
	if got, want := strings.Join(rows("boss-harbor"), "|"), "harbor VAT 18h 19m"; got != want {
		t.Errorf("harbor rows = %q, want %q", got, want)
	}
	if got := strings.Join(rows(""), "|"); !strings.Contains(got, "kite Ask Mara boss closed") {
		t.Errorf("all rows lack the closed list: %q", got)
	}
}

// The sidebar's row ends with its check, in the check's colour.
func TestNeedLineCheck(t *testing.T) {
	now := time.Now()
	m := Model{}
	r := NeedRow{Kind: NeedPlain, Project: "kite", Text: "Ask Mara for the key", Check: &Check{Kind: CheckBossClosed}}
	line := m.needLine(r, 44, false)
	if !strings.Contains(line, red+"boss closed") || !strings.HasSuffix(strings.TrimRight(ansi.Strip(line), " "), "boss closed") {
		t.Errorf("line %q", line)
	}
	r.Check = &Check{Kind: CheckUnchanged, Since: now.Add(-72 * time.Hour).Unix()}
	if line := m.needLine(r, 44, false); !strings.Contains(line, gold+"unchanged 3d") {
		t.Errorf("line %q", line)
	}
}
