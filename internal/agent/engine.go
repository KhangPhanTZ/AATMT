// Package agent runs the task pipeline: capture -> vision transcription ->
// analysis (brief + plan) -> step-by-step execution with tools -> review.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/khangphantz/aatmt/internal/llm"
	"github.com/khangphantz/aatmt/internal/store"
)

type Engine struct {
	Store *store.Store
	Pool  *llm.Pool

	mu        sync.Mutex
	jobs      map[string]*job
	approvals map[string]chan decision
	locks     map[string]chan struct{} // per-project run lock
	chats     map[string]bool          // chat target -> busy
	sem       chan struct{}
	semSize   int
}

type job struct {
	kind   string // "analyze" | "run"
	cancel context.CancelCauseFunc
	done   chan struct{}
}

type decision struct {
	id      string
	approve bool
	always  bool
}

var (
	ErrBusy   = errors.New("task already has a job running")
	errPaused = errors.New("paused by user")
)

func New(st *store.Store, pool *llm.Pool) *Engine {
	return &Engine{Store: st, Pool: pool, jobs: map[string]*job{}, approvals: map[string]chan decision{},
		locks: map[string]chan struct{}{}, chats: map[string]bool{}}
}

// acquire limits how many tasks talk to providers at once (enterprise quotas).
// A settings change swaps in a new semaphore; holders release the one they took.
func (e *Engine) acquire(ctx context.Context) (chan struct{}, error) {
	n := e.Store.Config().Settings.MaxConcurrentTasks
	e.mu.Lock()
	if e.sem == nil || e.semSize != n {
		e.sem, e.semSize = make(chan struct{}, n), n
	}
	sem := e.sem
	e.mu.Unlock()
	select {
	case sem <- struct{}{}:
		return sem, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

func (e *Engine) Running(id string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if j := e.jobs[id]; j != nil {
		return j.kind
	}
	return ""
}

func (e *Engine) start(id, kind, projectID string, fn func(ctx context.Context) error) error {
	e.mu.Lock()
	if e.jobs[id] != nil {
		e.mu.Unlock()
		return ErrBusy
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	j := &job{kind: kind, cancel: cancel, done: make(chan struct{})}
	e.jobs[id] = j
	e.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				e.fail(id, fmt.Errorf("internal error: %v", r))
			}
			e.mu.Lock()
			delete(e.jobs, id)
			e.mu.Unlock()
			cancel(nil)
			close(j.done)
		}()
		// runs on one project share a workspace: one at a time (analysis is read-only)
		if kind == "run" && projectID != "" {
			e.setStatus(id, "queued (another task is working on this project)")
			unlock, err := e.projectLock(ctx, projectID)
			if err != nil {
				e.pause(id, "cancelled while queued")
				return
			}
			defer unlock()
		}
		e.setStatus(id, "queued (waiting for a free slot)")
		sem, err := e.acquire(ctx)
		if err != nil {
			e.pause(id, "cancelled before start")
			return
		}
		defer func() { <-sem }()
		err = fn(ctx)
		switch {
		case err == nil:
		case errors.Is(context.Cause(ctx), errPaused):
			e.pause(id, "paused by user")
		default:
			e.fail(id, err)
		}
	}()
	return nil
}

// Pause cancels the task's running job; it can be resumed later.
func (e *Engine) Pause(id string) {
	e.mu.Lock()
	j := e.jobs[id]
	e.mu.Unlock()
	if j == nil {
		return
	}
	j.cancel(errPaused)
	select {
	case <-j.done:
	case <-time.After(10 * time.Second):
	}
}

// Decide answers a pending command approval.
func (e *Engine) Decide(id, approvalID string, approve, always bool) error {
	e.mu.Lock()
	ch := e.approvals[id]
	e.mu.Unlock()
	if ch == nil {
		return errors.New("no pending approval")
	}
	select {
	case ch <- decision{id: approvalID, approve: approve, always: always}:
		return nil
	default:
		return errors.New("approval already answered")
	}
}

// ---------- shared helpers ----------

func (e *Engine) emit(id, typ, step, title, detail string, data map[string]any) {
	e.Store.Emit(store.Event{TaskID: id, Type: typ, Step: step, Title: title, Detail: detail, Data: data})
	switch typ {
	case "stage", "error", "approval", "question", "step":
		log.Printf("task %s %s: %s %s", id, typ, title, excerpt(strings.ReplaceAll(detail, "\n", " "), 160))
	}
}

func (e *Engine) setStatus(id, s string) {
	_, _ = e.Store.UpdateTask(id, func(t *store.Task) error { t.Status = s; return nil })
}

func (e *Engine) setStage(id, stage, status string) {
	changed := false
	defer func() {
		if changed {
			e.emit(id, "stage", "", stage, status, nil) // after the store lock is released
		}
	}()
	_, _ = e.Store.UpdateTask(id, func(t *store.Task) error {
		changed = t.Stage != stage
		t.Stage, t.Status = stage, status
		if stage != store.StagePaused {
			t.Error = ""
		}
		return nil
	})
}

func (e *Engine) pause(id, reason string) {
	_, _ = e.Store.UpdateTask(id, func(t *store.Task) error {
		for i := range t.Steps {
			if t.Steps[i].Status == store.StepRunning {
				t.Steps[i].Status = store.StepPending
			}
		}
		if t.Stage == store.StageAnalyzing {
			t.Stage = store.StageInbox
		} else if t.Stage == store.StageRunning {
			t.Stage = store.StagePaused
		}
		t.Status = reason
		t.Pending = nil
		return nil
	})
	e.emit(id, "stage", "", "paused", reason, nil)
}

func (e *Engine) fail(id string, err error) {
	_, _ = e.Store.UpdateTask(id, func(t *store.Task) error {
		for i := range t.Steps {
			if t.Steps[i].Status == store.StepRunning {
				t.Steps[i].Status = store.StepPending
			}
		}
		if t.Stage == store.StageAnalyzing {
			t.Stage = store.StageInbox
		} else if t.Stage == store.StageRunning {
			t.Stage = store.StagePaused
		}
		t.Error = err.Error()
		t.Status = "error"
		t.Pending = nil
		return nil
	})
	e.emit(id, "error", "", "error", err.Error(), nil)
}

// chain resolves a role to its ordered profile chain; a per-task override goes
// first. rotate > 0 rotates the chain (multi-LLM worker rotation: step i
// starts on profile i % n and fails over through the rest).
func (e *Engine) chain(role, override string, rotate int) []*store.Profile {
	cfg := e.Store.Config()
	ids := cfg.Roles.Get(role)
	byID := map[string]*store.Profile{}
	for _, p := range cfg.Profiles {
		byID[p.ID] = p
	}
	var out []*store.Profile
	seen := map[string]bool{}
	add := func(id string) {
		if p := byID[id]; p != nil && !seen[id] {
			seen[id] = true
			out = append(out, p)
		}
	}
	if override != "" {
		add(override)
	}
	for _, id := range ids {
		add(id)
	}
	if len(ids) == 0 {
		for _, p := range cfg.Profiles {
			add(p.ID)
		}
	}
	if rotate > 0 && len(out) > 1 {
		var enabled []*store.Profile
		for _, p := range out {
			if p.Enabled {
				enabled = append(enabled, p)
			}
		}
		if n := len(enabled); n > 1 {
			k := rotate % n
			out = append(append([]*store.Profile{}, enabled[k:]...), enabled[:k]...)
		}
	}
	return out
}

type callOpts struct {
	taskID    string
	projectID string
	role      string
	step      string
	override  string
	rotate    int
	vision    bool
}

// call runs one LLM request through the pool, books usage on the task,
// project and profile, and records the call.
func (e *Engine) call(ctx context.Context, o callOpts, req llm.Request) (*llm.Response, error) {
	chain := e.chain(o.role, o.override, o.rotate)
	st := e.Store.Config().Settings
	resp, err := e.Pool.Do(ctx, chain, req, o.vision, func(a llm.Attempt) {
		if o.taskID != "" {
			e.emit(o.taskID, "error", o.step, "provider error: "+a.Profile.Name, a.Err.Error(), map[string]any{"profile": a.Profile.Name})
			if st.RecordLLM {
				e.record(o, req, nil, a.Profile, a.Err)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	u := resp.Usage
	e.Store.AddProfileUsage(resp.Profile.ID, u)
	e.Store.AddProjectUsage(o.projectID, u)
	if o.taskID == "" {
		return resp, nil
	}
	t, _ := e.Store.UpdateTask(o.taskID, func(t *store.Task) error { t.Usage.Add(u); return nil })
	if st.RecordLLM {
		e.record(o, req, resp, resp.Profile, nil)
	}
	e.emit(o.taskID, "llm", o.step, o.role+" · "+resp.Profile.Name, excerpt(resp.Text, 600), map[string]any{
		"profile": resp.Profile.Name, "model": resp.Profile.Model, "input": u.Input, "output": u.Output,
		"cache_read": u.CacheRead, "cache_write": u.CacheWrite, "ms": resp.Latency.Milliseconds(),
		"tools": len(resp.ToolCalls), "stop": resp.StopReason,
	})
	if b := st.TaskTokenBudget; b > 0 && t != nil {
		if spent := t.Usage.Input + t.Usage.Output + t.Usage.CacheWrite; spent >= b {
			return resp, fmt.Errorf("%w (%d / %d tokens)", errBudget, spent, b)
		}
	}
	return resp, nil
}

func (e *Engine) record(o callOpts, req llm.Request, resp *llm.Response, p *store.Profile, callErr error) {
	r := store.LLMRecord{TaskID: o.taskID, Role: o.role, Step: o.step, Messages: len(req.Messages),
		Tools: len(req.Tools), SystemLen: len(req.System)}
	if p != nil {
		r.Profile, r.Model = p.Name, p.Model
	}
	if n := len(req.Messages); n > 0 {
		m := req.Messages[n-1]
		var b strings.Builder
		b.WriteString(m.Text)
		for _, tr := range m.ToolResults {
			fmt.Fprintf(&b, "\n[tool_result %s] %s", tr.Name, excerpt(tr.Content, 1500))
		}
		if len(m.Images) > 0 {
			fmt.Fprintf(&b, "\n[%d image(s)]", len(m.Images))
		}
		r.Request = excerpt(b.String(), 6000)
	}
	if resp != nil {
		r.Usage, r.Ms = resp.Usage, resp.Latency.Milliseconds()
		var b strings.Builder
		b.WriteString(resp.Text)
		for _, c := range resp.ToolCalls {
			fmt.Fprintf(&b, "\n→ %s(%s)", c.Name, excerpt(string(c.Args), 1500))
		}
		r.Response = excerpt(b.String(), 8000)
	}
	if callErr != nil {
		r.Error = callErr.Error()
	}
	e.Store.AddLLMRecord(r)
}

// projectLock serialises runs of tasks that share one workspace.
func (e *Engine) projectLock(ctx context.Context, projectID string) (func(), error) {
	if projectID == "" {
		return func() {}, nil
	}
	e.mu.Lock()
	ch := e.locks[projectID]
	if ch == nil {
		ch = make(chan struct{}, 1)
		e.locks[projectID] = ch
	}
	e.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

// override picks the task's model override, else the project's default.
func (e *Engine) override(t *store.Task, role string) string {
	pick := func(o store.Overrides) string {
		if role == store.RoleAnalyzer {
			return o.Analyzer
		}
		return o.Worker
	}
	if v := pick(t.Overrides); v != "" {
		return v
	}
	if t.ProjectID != "" {
		if p, err := e.Store.Project(t.ProjectID); err == nil {
			return pick(p.Overrides)
		}
	}
	return ""
}

// prompt returns the user's instruction override for a role, or the built-in one.
func (e *Engine) prompt(role, builtin string) string {
	if p := strings.TrimSpace(e.Store.Config().Prompts[role]); p != "" {
		return p
	}
	return builtin
}

var errBudget = errors.New("task token budget exceeded — raise it in Settings and resume")

func excerpt(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
