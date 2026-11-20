package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/khangphantz/aatmt/internal/codeidx"
	"github.com/khangphantz/aatmt/internal/llm"
	"github.com/khangphantz/aatmt/internal/store"
)

func sleep(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// Run executes pending plan steps one by one until review, pause or error.
func (e *Engine) Run(id string) error {
	t, err := e.Store.Task(id)
	if err != nil {
		return err
	}
	if len(t.Steps) == 0 {
		return errors.New("no plan yet: analyse the task first")
	}
	return e.start(id, "run", t.ProjectID, func(ctx context.Context) error {
		e.setStage(id, store.StageRunning, "working")
		for {
			if err := context.Cause(ctx); err != nil {
				return err
			}
			t, err := e.Store.Task(id)
			if err != nil {
				return err
			}
			idx := -1
			for i, s := range t.Steps {
				if s.Status == store.StepPending || s.Status == store.StepRunning {
					idx = i
					break
				}
			}
			if idx < 0 {
				done, total := t.Progress()
				e.setStage(id, store.StageReview, fmt.Sprintf("all steps finished (%d/%d) — review the changes", done, total))
				return nil
			}
			paused, err := e.runStep(ctx, t, idx)
			if err != nil || paused {
				return err
			}
		}
	})
}

// actionLog is a local (free) record of what happened in a step, used to
// restart the step session compactly instead of resending a huge history.
type actionLog []string

func (l *actionLog) add(call llm.ToolCall, out toolOutcome, max int) {
	args := string(call.Args)
	var m map[string]any
	if json.Unmarshal(call.Args, &m) == nil {
		delete(m, "content") // never keep full file bodies in the log
		if v, ok := m["new_text"].(string); ok {
			m["new_text"] = clip(v, max/4)
		}
		if v, ok := m["old_text"].(string); ok {
			m["old_text"] = clip(v, max/4)
		}
		b, _ := json.Marshal(m)
		args = string(b)
	}
	status := "ok"
	if out.isError {
		status = "error"
	}
	*l = append(*l, fmt.Sprintf("%s(%s) -> %s: %s", call.Name, clip(args, max/2), status,
		clip(strings.ReplaceAll(out.content, "\n", " ⏎ "), max/2)))
}

func (e *Engine) runStep(ctx context.Context, t *store.Task, idx int) (paused bool, err error) {
	step := t.Steps[idx]
	st := e.Store.Config().Settings
	now := time.Now().UTC()
	_, _ = e.Store.UpdateTask(t.ID, func(t *store.Task) error {
		t.Steps[idx].Status = store.StepRunning
		if t.Steps[idx].StartedAt == nil {
			t.Steps[idx].StartedAt = &now
		}
		t.Status = fmt.Sprintf("step %d/%d: %s", idx+1, len(t.Steps), step.Title)
		return nil
	})
	e.emit(t.ID, "step", step.ID, fmt.Sprintf("Step %d started: %s", idx+1, step.Title), step.Detail, nil)

	ws := e.Store.Workspace(t)
	var proj *store.Project
	if t.ProjectID != "" {
		proj, _ = e.Store.Project(t.ProjectID)
	}
	tree := ""
	if ws != "" {
		files, _ := codeidx.Scan(ws, st.Limits.CodeScanMaxFiles)
		tree = codeidx.Tree(files, st.Limits.StepTreeLines)
	}
	notes := takeNotes(e, t.ID)
	base := e.stepPrompt(t, proj, idx, notes, tree)
	system := e.workerSystem(st, ws != "")
	cfg := e.Store.Config()
	tools := toolDefs(toolSet{workspace: ws != "", settings: st, project: proj, skills: len(cfg.Skills) > 0,
		mcp: e.MCP.ToolsFor(ctx, store.RoleWorker)})
	tc := &toolCtx{e: e, ctx: ctx, taskID: t.ID, stepID: step.ID, root: ws, settings: st, project: proj}
	if st.UntrustedBoundary {
		tc.nonce = store.ShortID()
	}
	rotate := 0
	if t.Rotate {
		rotate = idx
	}
	opts := callOpts{taskID: t.ID, projectID: t.ProjectID, role: store.RoleWorker, step: step.ID, override: e.override(t, store.RoleWorker), rotate: rotate}

	msgs := []llm.Message{{Role: "user", Text: base}}
	var log actionLog
	noTool := 0
	for turn := 1; turn <= st.MaxTurnsPerStep; turn++ {
		if err := context.Cause(ctx); err != nil {
			return false, err
		}
		req := llm.Request{System: system, Messages: msgs, Tools: tools}

		// Compaction: when the step session grows past the threshold, start a
		// fresh session from the brief + a locally built action log. The old
		// history is dropped as a whole (never edited in place), so provider
		// caches / thinking signatures of the new session stay valid.
		if prof := e.chain(store.RoleWorker, opts.override, rotate); len(prof) > 0 {
			cw := prof[0].ContextWindow
			if cw <= 0 {
				cw = st.Limits.DefaultContextWindow
			}
			if est := llm.EstimateRequest(prof[0], req); est > cw*st.CompactAtPct/100 && len(msgs) > 2 {
				fresh, _ := e.Store.Task(t.ID)
				msgs = []llm.Message{{Role: "user", Text: e.stepPrompt(fresh, proj, idx, notes, tree) +
					"\n\n## Progress so far in THIS step (session restarted to save tokens)\n- " +
					strings.Join(log, "\n- ") + "\n\nContinue from here; re-read only what you still need."}}
				req.Messages = msgs
				e.emit(t.ID, "info", step.ID, "context compacted", fmt.Sprintf("≈%d tokens → ≈%d", est, llm.EstimateRequest(prof[0], req)), nil)
			}
		}

		resp, err := e.call(ctx, opts, req)
		if err != nil {
			if errors.Is(err, errBudget) {
				e.pause(t.ID, err.Error())
				return true, nil
			}
			return false, err
		}
		cur, _ := e.Store.UpdateTask(t.ID, func(t *store.Task) error {
			t.Steps[idx].Turns++
			t.Steps[idx].Tokens += resp.Usage.Total()
			t.Steps[idx].Profile = resp.Profile.Name
			t.Turns++
			return nil
		})
		if b := st.TaskTurnBudget; b > 0 && cur != nil && cur.Turns >= b {
			e.pause(t.ID, fmt.Sprintf("task turn budget reached (%d turns) — raise it in Settings and resume", b))
			return true, nil
		}
		msgs = append(msgs, resp.Assistant())

		if len(resp.ToolCalls) == 0 {
			noTool++
			if noTool >= 2 && strings.TrimSpace(resp.Text) != "" {
				return false, e.finishStep(t.ID, idx, resp.Text)
			}
			msgs = append(msgs, llm.Message{Role: "user", Text: "Continue with tool calls. When the step is complete, call step_done with a summary."})
			continue
		}
		noTool = 0

		var results []llm.ToolResult
		var finished bool
		var summary, question string
		for _, call := range resp.ToolCalls {
			out := tc.exec(call)
			log.add(call, out, st.Limits.SummaryChars)
			if call.Name != "run_command" { // run_command emits its own event
				e.emit(t.ID, "tool", step.ID, call.Name+" "+toolLabel(call, st.Limits.SummaryChars), excerpt(out.content, st.Limits.EventDetailChars), map[string]any{"error": out.isError})
			} else {
				e.emit(t.ID, "tool", step.ID, "output", excerpt(out.content, st.Limits.EventDetailChars), map[string]any{"error": out.isError})
			}
			results = append(results, llm.ToolResult{CallID: call.ID, Name: call.Name, Content: out.content, IsError: out.isError})
			if out.done {
				finished, summary = true, out.summary
			}
			if out.question != "" {
				question = out.question
			}
		}
		if question != "" {
			_, _ = e.Store.UpdateTask(t.ID, func(t *store.Task) error {
				t.Steps[idx].Status = store.StepPending
				t.Stage, t.Status = store.StagePaused, "question: "+question
				return nil
			})
			e.emit(t.ID, "question", step.ID, "Agent asks", question, nil)
			return true, nil
		}
		if finished {
			return false, e.finishStep(t.ID, idx, summary)
		}
		next := llm.Message{Role: "user", ToolResults: results}
		// operator notes typed while the step runs are appended after the tool
		// results (append-only: earlier turns are never rewritten)
		if fresh := takeNotes(e, t.ID); len(fresh) > 0 {
			var b strings.Builder
			b.WriteString("Operator note (highest priority):\n")
			for _, n := range fresh {
				b.WriteString("- " + n.Text + "\n")
			}
			next.Text = b.String()
			notes = append(notes, fresh...)
		}
		if turn == st.MaxTurnsPerStep-2 {
			next.Text += "\nYou are close to the turn limit for this step: finish up and call step_done."
		}
		msgs = append(msgs, next)
	}
	_, _ = e.Store.UpdateTask(t.ID, func(t *store.Task) error {
		t.Steps[idx].Status = store.StepPending
		t.Stage = store.StagePaused
		t.Status = fmt.Sprintf("step %d hit the turn limit (%d) — add a note and resume, or edit the plan", idx+1, st.MaxTurnsPerStep)
		return nil
	})
	e.emit(t.ID, "stage", step.ID, "paused", "turn limit reached", nil)
	return true, nil
}

func (e *Engine) finishStep(id string, idx int, summary string) error {
	now := time.Now().UTC()
	var title string
	_, err := e.Store.UpdateTask(id, func(t *store.Task) error {
		t.Steps[idx].Status = store.StepDone
		t.Steps[idx].Summary = strings.TrimSpace(summary)
		t.Steps[idx].FinishedAt = &now
		title = t.Steps[idx].Title
		return nil
	})
	e.emit(id, "step", "", fmt.Sprintf("Step %d done: %s", idx+1, title), summary, nil)
	return err
}

func takeNotes(e *Engine, id string) []store.Note {
	var out []store.Note
	_, _ = e.Store.UpdateTask(id, func(t *store.Task) error {
		for i := range t.Notes {
			if !t.Notes[i].Consumed {
				t.Notes[i].Consumed = true
				out = append(out, t.Notes[i])
			}
		}
		return nil
	})
	return out
}

func toolLabel(c llm.ToolCall, max int) string {
	var m map[string]any
	_ = json.Unmarshal(c.Args, &m)
	for _, k := range []string{"path", "query", "command", "note", "question"} {
		if v, ok := m[k].(string); ok && v != "" {
			return clip(v, max)
		}
	}
	return ""
}
