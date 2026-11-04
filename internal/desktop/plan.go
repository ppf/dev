package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// The desktop app's task list tools: update_plan sets and moves the list, wrap_up checks it off.
const (
	updatePlan = "mcp__ccd_turn__update_plan"
	wrapUp     = "mcp__ccd_turn__wrap_up"
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

// planOp is one update_plan or wrap_up call: items replaces the list (hasItems), advance completes the current
// step and starts the next pending one, done completes steps and start starts one (1-based), wrapUp
// completes them all.
type planOp struct {
	hasItems bool
	items    []Step
	advance  bool
	done     []int
	start    int
	wrapUp   bool
}

// planOps are the task list calls among an assistant row's blocks.
func planOps(blocks []block) []planOp {
	var ops []planOp
	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		switch b.Name {
		case wrapUp:
			ops = append(ops, planOp{wrapUp: true})
		case updatePlan:
			var in struct {
				Items   *[]Step `json:"items"`
				Advance bool    `json:"advance"`
				Done    []int   `json:"done"`
				Start   int     `json:"start"`
			}
			if json.Unmarshal(b.Input, &in) != nil {
				continue
			}
			op := planOp{advance: in.Advance, done: in.Done, start: in.Start}
			if in.Items != nil {
				op.hasItems, op.items = true, *in.Items
			}
			ops = append(ops, op)
		}
	}
	return ops
}

// hasItems reports whether ops set the list.
func hasItems(ops []planOp) bool {
	for _, op := range ops {
		if op.hasItems {
			return true
		}
	}
	return false
}

// replay is the task list ops leave, from their last items call on; none without one.
func replay(ops []planOp) []Step {
	steps := []Step{}
	for _, op := range ops {
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

// planOpsBefore reads path backward from end, a row boundary, for the task list calls from the last items call
// on, at most planScanBytes back; all it found when it found no items call.
func planOpsBefore(path string, end int64) ([]planOp, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var ops []planOp
	// carry is the start of a row cut by the chunk after, which ends in the chunk before
	var carry []byte
	for scanned := int64(0); end > 0 && scanned < planScanBytes; {
		start := max(0, end-planChunk)
		buf := make([]byte, end-start, end-start+int64(len(carry)))
		n, err := f.ReadAt(buf, start)
		if err != nil && !errors.Is(err, io.EOF) {
			return ops, err
		}
		buf = append(buf[:n], carry...)
		carry = nil
		if start > 0 {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				scanned += end - start
				carry, end = buf, start
				continue
			}
			carry, buf = buf[:i], buf[i+1:]
		}
		var chunk []planOp
		for line := range bytes.SplitSeq(buf, []byte{'\n'}) {
			if !bytes.Contains(line, []byte(`"mcp__ccd_turn__`)) || !bytes.Contains(line, []byte(`"type":"assistant"`)) {
				continue
			}
			var r row
			var blocks []block
			if json.Unmarshal(line, &r) != nil || r.IsSidechain || r.Type != "assistant" ||
				json.Unmarshal(r.Message.Content, &blocks) != nil {
				continue
			}
			chunk = append(chunk, planOps(blocks)...)
		}
		ops = append(chunk, ops...)
		if hasItems(chunk) {
			return ops, nil
		}
		scanned += end - start
		end = start
	}
	return ops, nil
}
