package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/khangphantz/aatmt/internal/codeidx"
	"github.com/khangphantz/aatmt/internal/llm"
	"github.com/khangphantz/aatmt/internal/store"
)

// Chat answers a side question about a task (kind "task") or a codebase
// (kind "project") with read-only tools. It never touches the task's run and
// never edits files; the UI can forward an answer to the worker as a note.
func (e *Engine) Chat(kind, id, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("empty message")
	}
	key := kind + ":" + id
	e.mu.Lock()
	if e.chats[key] {
		e.mu.Unlock()
		return errors.New("the assistant is still answering the previous message")
	}
	e.chats[key] = true
	e.mu.Unlock()

	user := store.ChatMsg{ID: store.ShortID(), Role: "user", Text: text, At: time.Now().UTC()}
	var history []store.ChatMsg
	var err error
	switch kind {
	case "task":
		var t *store.Task
		t, err = e.Store.UpdateTask(id, func(t *store.Task) error { t.Chat = append(t.Chat, user); return nil })
		if t != nil {
			history = t.Chat
		}
	case "project":
		var p *store.Project
		p, err = e.Store.UpdateProject(id, func(p *store.Project) error { p.Chat = append(p.Chat, user); return nil })
		if p != nil {
			history = p.Chat
		}
	default:
		err = errors.New("unknown chat target")
	}
	if err != nil {
		e.mu.Lock()
		delete(e.chats, key)
		e.mu.Unlock()
		return err
	}
	if kind == "task" {
		e.emit(id, "chat", "", "you asked", text, nil)
	}
	go func() {
		defer func() {
			e.mu.Lock()
			delete(e.chats, key)
			e.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		reply := e.answer(ctx, kind, id, history)
		switch kind {
		case "task":
			_, _ = e.Store.UpdateTask(id, func(t *store.Task) error { t.Chat = append(t.Chat, reply); return nil })
			e.emit(id, "chat", "", "assistant answered", excerpt(reply.Text, 600), nil)
		case "project":
			_, _ = e.Store.UpdateProject(id, func(p *store.Project) error { p.Chat = append(p.Chat, reply); return nil })
		}
	}()
	return nil
}

// ChatBusy reports whether an answer is being generated for the target.
func (e *Engine) ChatBusy(kind, id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.chats[kind+":"+id]
}

func (e *Engine) answer(ctx context.Context, kind, id string, history []store.ChatMsg) store.ChatMsg {
	cfg := e.Store.Config()
	st := cfg.Settings
	var (
		system    strings.Builder
		ws        string
		proj      *store.Project
		taskID    string
		projectID string
	)
	system.WriteString(e.prompt(store.RoleChat, BuiltinPrompts[store.RoleChat]))
	fmt.Fprintf(&system, "\n- Answer in the language of the user's question (default %s). Use Markdown sparingly.\n", st.Language)
	if st.UntrustedBoundary {
		system.WriteString(boundaryNote + "\n")
	}
	switch kind {
	case "task":
		t, err := e.Store.Task(id)
		if err != nil {
			return chatErr(err)
		}
		taskID, projectID, ws = t.ID, t.ProjectID, e.Store.Workspace(t)
		if t.ProjectID != "" {
			proj, _ = e.Store.Project(t.ProjectID)
		}
		system.WriteString(e.taskContext(t))
		writeProject(&system, proj)
	case "project":
		p, err := e.Store.Project(id)
		if err != nil {
			return chatErr(err)
		}
		proj, projectID, ws = p, p.ID, p.Workspace
		writeProject(&system, p)
		var rows []string
		for _, t := range e.Store.Tasks() {
			if t.ProjectID == p.ID {
				rows = append(rows, fmt.Sprintf("- [%s] %s (%s)", t.Stage, t.Title, t.ID))
			}
		}
		if len(rows) > 0 {
			if len(rows) > 30 {
				rows = rows[:30]
			}
			system.WriteString("\n## Tasks on this project\n" + strings.Join(rows, "\n") + "\n")
		}
	}
	if ws != "" {
		files, _ := codeidx.Scan(ws, 4000)
		system.WriteString("\n## Workspace tree\n" + codeidx.Tree(files, 60))
	}

	// replay the recent conversation (strictly alternating, starting with user)
	var msgs []llm.Message
	if len(history) > 12 {
		history = history[len(history)-12:]
	}
	for _, m := range history {
		role := m.Role
		if role == "error" {
			continue
		}
		if len(msgs) == 0 && role != "user" {
			continue
		}
		if len(msgs) > 0 && msgs[len(msgs)-1].Role == role {
			msgs[len(msgs)-1].Text += "\n\n" + m.Text
			continue
		}
		msgs = append(msgs, llm.Message{Role: role, Text: m.Text})
	}
	tools := toolDefs(toolSet{workspace: ws != "", readOnly: true, settings: st, project: proj, skills: len(cfg.Skills) > 0,
		mcp: e.MCP.ToolsFor(ctx, store.RoleChat)})
	tc := &toolCtx{e: e, ctx: ctx, taskID: taskID, root: ws, settings: st, project: proj, readOnly: true}
	if st.UntrustedBoundary {
		tc.nonce = store.ShortID()
	}
	opts := callOpts{taskID: taskID, projectID: projectID, role: store.RoleChat, step: "chat"}
	total := 0
	for turn := 0; turn < 8; turn++ {
		req := llm.Request{System: system.String(), Messages: msgs, Tools: tools, MaxTokens: 4096}
		if turn == 7 {
			req.Tools = nil // force a final answer
		}
		resp, err := e.call(ctx, opts, req)
		if err != nil && resp == nil {
			return chatErr(err)
		}
		total += resp.Usage.Total()
		msgs = append(msgs, resp.Assistant())
		if len(resp.ToolCalls) == 0 {
			return store.ChatMsg{ID: store.ShortID(), Role: "assistant", Text: strings.TrimSpace(resp.Text),
				At: time.Now().UTC(), Profile: resp.Profile.Name, Tokens: total}
		}
		var results []llm.ToolResult
		for _, c := range resp.ToolCalls {
			out := tc.exec(c)
			results = append(results, llm.ToolResult{CallID: c.ID, Name: c.Name, Content: out.content, IsError: out.isError})
		}
		msgs = append(msgs, llm.Message{Role: "user", ToolResults: results})
	}
	return chatErr(errors.New("no answer after 8 tool rounds"))
}

func chatErr(err error) store.ChatMsg {
	return store.ChatMsg{ID: store.ShortID(), Role: "error", Text: err.Error(), At: time.Now().UTC()}
}

// taskContext is a compact snapshot of a task for the chat assistant.
func (e *Engine) taskContext(t *store.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n# Task %s: %s\nStage: %s. Status: %s\n", t.ID, t.Title, t.Stage, t.Status)
	if br := t.Brief; br != nil {
		b.WriteString("## Brief\n" + br.Summary + "\n")
		list(&b, "Requirements", br.Requirements)
		list(&b, "Acceptance criteria", br.Acceptance)
		list(&b, "Open questions", br.Questions)
		list(&b, "Task memory", br.Memory)
	}
	b.WriteString("## Captured request\n" + captureText(t, 4000))
	if len(t.Steps) > 0 {
		b.WriteString("## Plan\n")
		for i, s := range t.Steps {
			fmt.Fprintf(&b, "%d. [%s] %s", i+1, s.Status, s.Title)
			if s.Summary != "" {
				fmt.Fprintf(&b, " — %s", excerpt(strings.ReplaceAll(s.Summary, "\n", " "), 300))
			}
			b.WriteString("\n")
		}
	}
	var changed []string
	for _, c := range t.Changes {
		if !c.Reverted {
			changed = append(changed, c.Op+" "+c.Path)
		}
	}
	list(&b, "Files changed by the agent", changed)
	if evs, _ := e.Store.Events(t.ID, 0, 30); len(evs) > 0 {
		b.WriteString("## Recent activity\n")
		for _, ev := range evs {
			if ev.Type == "llm" || ev.Type == "task" {
				continue
			}
			fmt.Fprintf(&b, "- %s %s: %s %s\n", ev.At.Format("15:04"), ev.Type, ev.Title, excerpt(strings.ReplaceAll(ev.Detail, "\n", " "), 160))
		}
	}
	return b.String()
}

// Complete marks a task done and, when enabled, promotes its memory notes
// into the project's knowledge base so future tasks start smarter.
func (e *Engine) Complete(id string) error {
	if e.Running(id) != "" {
		return errors.New("pause the task first")
	}
	now := time.Now().UTC()
	t, err := e.Store.UpdateTask(id, func(t *store.Task) error {
		t.Stage, t.Status, t.DoneAt, t.Error = store.StageDone, "completed", &now, ""
		return nil
	})
	if err != nil {
		return err
	}
	e.emit(id, "stage", "", store.StageDone, "marked done", nil)
	if e.Store.Config().Settings.AutoPromoteMemory {
		_, _ = e.PromoteMemory(id)
	}
	_ = t
	return nil
}

// PromoteMemory copies task memory notes into project knowledge (deduplicated).
func (e *Engine) PromoteMemory(id string) (int, error) {
	t, err := e.Store.Task(id)
	if err != nil {
		return 0, err
	}
	if t.ProjectID == "" || t.Brief == nil || len(t.Brief.Memory) == 0 {
		return 0, nil
	}
	n := 0
	_, err = e.Store.UpdateProject(t.ProjectID, func(p *store.Project) error {
		have := map[string]bool{}
		for _, k := range p.Knowledge {
			have[strings.ToLower(k.Text)] = true
		}
		for _, m := range t.Brief.Memory {
			if !have[strings.ToLower(m)] {
				p.Knowledge = append(p.Knowledge, store.Knowledge{ID: store.ShortID(), Text: m, TaskID: t.ID, At: time.Now().UTC()})
				have[strings.ToLower(m)] = true
				n++
			}
		}
		return nil
	})
	if err == nil && n > 0 {
		e.emit(id, "info", "", fmt.Sprintf("%d memory note(s) promoted to project knowledge", n), "", nil)
	}
	return n, err
}
