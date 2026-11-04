package desktop

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func steps(statuses ...string) []Step {
	s := make([]Step, len(statuses))
	for i, st := range statuses {
		s[i] = Step{Content: string(rune('a' + i)), Status: st}
	}
	return s
}

func statuses(s []Step) []string {
	out := make([]string, len(s))
	for i, st := range s {
		out[i] = st.Status
	}
	return out
}

func TestReplay(t *testing.T) {
	items := planOp{hasItems: true, items: steps(StepCurrent, StepPending, StepPending)}
	tests := []struct {
		name string
		ops  []planOp
		want []string
	}{
		{"no list", nil, []string{}},
		{"no items call: no list", []planOp{{advance: true}, {wrapUp: true}}, []string{}},
		{"items", []planOp{items}, []string{StepCurrent, StepPending, StepPending}},
		{"advance", []planOp{items, {advance: true}}, []string{StepDone, StepCurrent, StepPending}},
		{"advance past the end", []planOp{items, {advance: true}, {advance: true}, {advance: true}}, []string{StepDone, StepDone, StepDone}},
		{"advance with none current starts the first pending", []planOp{{hasItems: true, items: steps(StepDone, StepPending)}, {advance: true}},
			[]string{StepDone, StepCurrent}},
		{"done and start out of order", []planOp{items, {done: []int{1, 2}, start: 3}}, []string{StepDone, StepDone, StepCurrent}},
		{"start moves the current step", []planOp{items, {start: 3}}, []string{StepPending, StepPending, StepCurrent}},
		{"indexes out of range are ignored", []planOp{items, {done: []int{0, 9}, start: 4}}, []string{StepCurrent, StepPending, StepPending}},
		{"wrap_up checks it off", []planOp{items, {wrapUp: true}}, []string{StepDone, StepDone, StepDone}},
		{"a new items call replaces the list", []planOp{items, {wrapUp: true}, {hasItems: true, items: steps(StepCurrent)}}, []string{StepCurrent}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statuses(replay(tt.ops)); !slices.Equal(got, tt.want) {
				t.Errorf("replay = %v, want %v", got, tt.want)
			}
		})
	}
	// The items call's list is not changed under it
	replay([]planOp{items, {wrapUp: true}})
	if items.items[0].Status != StepCurrent {
		t.Errorf("replay changed its input")
	}
}

// testdata/plan.jsonl: an earlier turn's list, wrapped up; this turn's list, advanced, then moved out of order;
// a subagent's list in between.
func TestTranscriptPlan(t *testing.T) {
	data, err := os.ReadFile("testdata/plan.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	projects := t.TempDir()
	write(t, filepath.Join(projects, "-wt", "cli-plan.jsonl"), string(data))
	tr, err := readTranscript(projects, "cli-plan")
	if err != nil {
		t.Fatal(err)
	}
	want := []Step{
		{"Read the code", StepDone}, {"Write Go", StepDone}, {"Write the mod", StepPending},
		{"Tests", StepCurrent}, {"Open PR", StepPending},
	}
	if !slices.Equal(tr.plan, want) {
		t.Errorf("plan = %v, want %v", tr.plan, want)
	}
}

// A list set before the transcript's tail is found by reading back, with the calls after it.
func TestTranscriptPlanBeforeTail(t *testing.T) {
	filler := func(n int) map[string]any {
		return map[string]any{"type": "attachment", "attachment": strings.Repeat("x", n)}
	}
	call := func(id string, input map[string]any) map[string]any {
		return assistant("claude-opus-5-5", map[string]any{"type": "tool_use", "id": id, "name": updatePlanTool, "input": input})
	}
	rows := []any{
		call("t1", map[string]any{"items": []map[string]any{{"content": "one", "status": "pending"}}}),
		call("t2", map[string]any{"items": []map[string]any{
			{"content": "one", "status": "in_progress"}, {"content": "two", "status": "pending"}, {"content": "three", "status": "pending"},
		}}),
	}
	// Rows of 300 KB: chunks cut through them
	for range 8 {
		rows = append(rows, filler(300_000))
	}
	rows = append(rows, call("t3", map[string]any{"advance": true}))
	for range 6 {
		rows = append(rows, filler(300_000))
	}
	rows = append(rows, call("t4", map[string]any{"advance": true}), assistant("claude-opus-5-5", text("Working.")))
	projects := t.TempDir()
	write(t, filepath.Join(projects, "-wt", "cli-long.jsonl"), lines(t, rows...))
	tr, err := readTranscript(projects, "cli-long")
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses(tr.plan); !slices.Equal(got, []string{StepDone, StepDone, StepCurrent}) {
		t.Errorf("plan = %v, want the list from before the tail, advanced twice", got)
	}
}

// A transcript with no list: read back to the cap, no list.
func TestTranscriptNoPlan(t *testing.T) {
	var rows []any
	for range 8 {
		rows = append(rows, map[string]any{"type": "attachment", "attachment": strings.Repeat("x", 300_000)})
	}
	projects := t.TempDir()
	write(t, filepath.Join(projects, "-wt", "cli-none.jsonl"), lines(t, rows...))
	tr, err := readTranscript(projects, "cli-none")
	if err != nil {
		t.Fatal(err)
	}
	if tr.plan == nil || len(tr.plan) != 0 {
		t.Errorf("plan = %#v, want an empty list", tr.plan)
	}
}
