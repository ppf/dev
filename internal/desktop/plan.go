package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
)

// The desktop app's task list tools: update_plan sets and moves the list, wrap_up checks it off.
const (
	updatePlanTool = "mcp__ccd_turn__update_plan"
	wrapUpTool     = "mcp__ccd_turn__wrap_up"
)

// Step statuses, as update_plan has them.
const (
	StepDone    = "completed"
	StepCurrent = "in_progress"
	StepPending = "pending"
)

// planChunk and planScanBytes bound the search before a transcript's tail for the list's last items call.
const (
	planChunk     = 1 << 20
	planScanBytes = 16 << 20
)

// Step is one step of a worker's task list.
type Step struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// planOp is one update_plan or wrap_up call, by its tool use id: items replaces the list (hasItems), advance
// completes the current step and starts the next pending one, done completes steps and start starts one
// (1-based), wrapUp completes them all. One with failed is no call but call id's error result: that call
// changed nothing (rejected, interrupted).
type planOp struct {
	id       string
	failed   bool
	hasItems bool
	items    []Step
	advance  bool
	done     []int
	start    int
	wrapUp   bool
}

// planOps are the task list calls among a row's blocks, and the tool calls whose results are errors.
func planOps(blocks []block) []planOp {
	var ops []planOp
	for _, b := range blocks {
		if b.Type == "tool_result" && b.IsError {
			ops = append(ops, planOp{id: b.ToolUseID, failed: true})
		}
		if b.Type != "tool_use" {
			continue
		}
		switch b.Name {
		case wrapUpTool:
			ops = append(ops, planOp{id: b.ID, wrapUp: true})
		case updatePlanTool:
			var in struct {
				Items   *[]Step `json:"items"`
				Advance bool    `json:"advance"`
				Done    []int   `json:"done"`
				Start   int     `json:"start"`
			}
			if json.Unmarshal(b.Input, &in) != nil {
				continue
			}
			op := planOp{id: b.ID, advance: in.Advance, done: in.Done, start: in.Start}
			if in.Items != nil {
				op.hasItems, op.items = true, *in.Items
			}
			ops = append(ops, op)
		}
	}
	return ops
}

// failedCalls are the ids of the calls ops say failed.
func failedCalls(ops []planOp) map[string]bool {
	failed := map[string]bool{}
	for _, op := range ops {
		if op.failed {
			failed[op.id] = true
		}
	}
	return failed
}

// hasItems reports whether ops set the list.
func hasItems(ops []planOp) bool {
	failed := failedCalls(ops)
	return slices.ContainsFunc(ops, func(op planOp) bool { return op.hasItems && !failed[op.id] })
}

// replay is the task list ops leave, from their last items call on; none without one.
func replay(ops []planOp) []Step {
	steps := []Step{}
	failed := failedCalls(ops)
	for _, op := range ops {
		if op.failed || failed[op.id] {
			continue
		}
		if op.hasItems {
			steps = append([]Step{}, op.items...)
		}
		if op.wrapUp {
			for i := range steps {
				steps[i].Status = StepDone
			}
		}
		for _, n := range op.done {
			if n >= 1 && n <= len(steps) {
				steps[n-1].Status = StepDone
			}
		}
		if op.advance {
			next := 0
			for i, s := range steps {
				if s.Status == StepCurrent {
					steps[i].Status, next = StepDone, i+1
					break
				}
			}
			for i := next; i < len(steps); i++ {
				if steps[i].Status == StepPending {
					steps[i].Status = StepCurrent
					break
				}
			}
		}
		if op.start >= 1 && op.start <= len(steps) {
			// One step runs at a time
			for i, s := range steps {
				if s.Status == StepCurrent {
					steps[i].Status = StepPending
				}
			}
			steps[op.start-1].Status = StepCurrent
		}
	}
	return steps
}

// planOpsBefore reads path backward from end, a row boundary, for the task list calls back to the chunk holding
// the last items call (replay drops those before it), at most planScanBytes back, and counts the task list rows
// that did not parse; all it found when it found no items call.
func planOpsBefore(path string, end int64) (ops []planOp, broken int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	// carry is the start of a row cut by the chunk after, which ends in the chunk before
	var carry []byte
	for floor := max(0, end-planScanBytes); end > floor; {
		start := max(0, end-planChunk)
		buf := make([]byte, end-start, end-start+int64(len(carry)))
		n, err := f.ReadAt(buf, start)
		if err != nil && !errors.Is(err, io.EOF) {
			return ops, broken, err
		}
		buf = append(buf[:n], carry...)
		carry = nil
		if start > 0 {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				carry, end = buf, start
				continue
			}
			carry, buf = buf[:i], buf[i+1:]
		}
		var chunk []planOp
		for line := range bytes.SplitSeq(buf, []byte{'\n'}) {
			if !bytes.Contains(line, []byte(`"mcp__ccd_turn__`)) && !bytes.Contains(line, []byte(`"is_error":true`)) {
				continue
			}
			var r row
			var blocks []block
			if err := json.Unmarshal(line, &r); err != nil {
				broken++
				continue
			}
			if r.IsSidechain || (r.Type != "assistant" && r.Type != "user") {
				continue
			}
			if json.Unmarshal(r.Message.Content, &blocks) != nil {
				broken++
				continue
			}
			chunk = append(chunk, planOps(blocks)...)
		}
		ops = append(chunk, ops...)
		// A call's error result may be in a chunk read before
		if hasItems(ops) {
			return ops, broken, nil
		}
		end = start
	}
	return ops, broken, nil
}
