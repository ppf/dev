package cmd

import (
	"encoding/json"
	"testing"
)

// ask --for links the ask to an open item, shown on both in --json; --replaces with --for alone links it anew.
func TestTodoAskFor(t *testing.T) {
	todoEnv(t)
	t.Setenv("CLAUDECODE", "1")
	run(t, "", "todo", "add", "Contract", "--details", "x")
	run(t, "", "todo", "add", "Other", "--details", "x")
	run(t, "", "todo", "ask", "Start it now?", "--option", "Yes (Recommended)", "--option", "No", "--details", "x", "--for", "1")
	type linked struct {
		ID      int `json:"id"`
		For     int `json:"for"`
		WaitsOn *struct {
			ID int `json:"id"`
		} `json:"waits_on"`
	}
	read := func() map[int]linked {
		var l struct{ Items []linked }
		if err := json.Unmarshal([]byte(run(t, "", "todo", "--json")), &l); err != nil {
			t.Fatal(err)
		}
		out := map[int]linked{}
		for _, it := range l.Items {
			out[it.ID] = it
		}
		return out
	}
	if got := read(); got[3].For != 1 || got[1].WaitsOn == nil || got[1].WaitsOn.ID != 3 || got[2].WaitsOn != nil {
		t.Errorf("--for 1: %+v", got)
	}
	run(t, "", "todo", "ask", "--replaces", "3", "--for", "2")
	if got := read(); got[3].For != 2 || got[2].WaitsOn == nil || got[1].WaitsOn != nil {
		t.Errorf("--replaces 3 --for 2: %+v", got)
	}
	run(t, "", "todo", "ask", "--replaces", "3", "--for", "0")
	if got := read(); got[3].For != 0 || got[2].WaitsOn != nil {
		t.Errorf("--for 0 unlinks: %+v", got)
	}
	if _, _, err := execute(t, "", "todo", "ask", "--replaces", "3", "--for", "-1"); err == nil {
		t.Error("--for -1: no error")
	}
	if _, _, err := execute(t, "", "todo", "ask", "Q?", "--option", "a", "--option", "b", "--details", "x", "--for", "3"); err == nil {
		t.Error("--for an ask: no error")
	}
}
