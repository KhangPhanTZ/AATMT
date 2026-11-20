package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/khangphantz/aatmt/internal/codeidx"
	"github.com/khangphantz/aatmt/internal/llm"
	"github.com/khangphantz/aatmt/internal/mcp"
	"github.com/khangphantz/aatmt/internal/store"
)

func obj(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

type toolSet struct {
	workspace bool
	readOnly  bool // chat: no writes, no commands, no task-state tools
	settings  store.Settings
	project   *store.Project
	skills    bool
	mcp       []mcp.Bound
}

func toolDefs(o toolSet) []llm.Tool {
	var tools []llm.Tool
	if o.workspace {
		tools = append(tools,
			llm.Tool{Name: "list_dir", Description: "List files under a workspace directory (skips vendor/build dirs).",
				Schema: obj(map[string]any{"path": str("directory relative to workspace root, default '.'"), "depth": num("max depth, default 2")})},
			llm.Tool{Name: "read_file", Description: "Read a text file with line numbers. Use start_line/end_line for large files.",
				Schema: obj(map[string]any{"path": str("file path"), "start_line": num("1-based, default 1"), "end_line": num("inclusive, default start+399")}, "path")},
			llm.Tool{Name: "search", Description: "Search file contents. Returns path:line: text. Literal by default.",
				Schema: obj(map[string]any{"query": str("text or regex"), "regex": map[string]any{"type": "boolean"}, "path": str("optional subdirectory"), "max_results": num("default 50")}, "query")},
		)
		if !o.readOnly {
			tools = append(tools,
				llm.Tool{Name: "write_file", Description: "Create or overwrite a file with the full content.",
					Schema: obj(map[string]any{"path": str("file path"), "content": str("complete file content")}, "path", "content")},
				llm.Tool{Name: "edit_file", Description: "Replace an exact text fragment in a file. old_text must match exactly once unless replace_all is true.",
					Schema: obj(map[string]any{"path": str("file path"), "old_text": str("exact existing text, including indentation"), "new_text": str("replacement"), "replace_all": map[string]any{"type": "boolean"}}, "path", "old_text", "new_text")},
				llm.Tool{Name: "delete_file", Description: "Delete a file.",
					Schema: obj(map[string]any{"path": str("file path")}, "path")},
			)
			if o.settings.CommandPolicy != "off" {
				tools = append(tools, llm.Tool{Name: "run_command", Description: "Run a shell command in the workspace root (build, test, lint, git). Output is truncated.",
					Schema: obj(map[string]any{"command": str("shell command")}, "command")})
			}
			if o.project != nil && len(o.project.Commands) > 0 {
				names := []any{}
				for _, c := range o.project.Commands {
					names = append(names, c.Name)
				}
				tools = append(tools, llm.Tool{Name: "project_command", Description: "Run one of the project's predefined commands (build/test/lint…). No approval needed.",
					Schema: obj(map[string]any{"name": map[string]any{"type": "string", "enum": names}}, "name")})
			}
		}
	}
	if o.skills {
		tools = append(tools, llm.Tool{Name: "load_skill", Description: "Load the full content of a skill (playbook) by name.",
			Schema: obj(map[string]any{"name": str("skill name")}, "name")})
	}
	if o.settings.WebSearch != "" {
		tools = append(tools, llm.Tool{Name: "web_search", Description: "Search the web (docs, error messages, library APIs). Returns titles, URLs and snippets.",
			Schema: obj(map[string]any{"query": str("search query")}, "query")})
	}
	if o.settings.WebFetch {
		tools = append(tools, llm.Tool{Name: "web_fetch", Description: "Fetch a public http(s) URL and return its text content (truncated).",
			Schema: obj(map[string]any{"url": str("absolute URL")}, "url")})
	}
	for _, b := range o.mcp {
		if o.readOnly && b.Approval {
			continue // approval-gated MCP tools are only offered to the worker
		}
		schema := b.Tool.InputSchema
		if schema == nil || schema["type"] != "object" {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		desc := strings.TrimSpace(b.Tool.Description)
		if len(desc) > 600 {
			desc = desc[:600] + "…"
		}
		tools = append(tools, llm.Tool{Name: b.Name, Description: "[MCP " + b.Server.Name + "] " + desc, Schema: schema})
	}
	if !o.readOnly {
		tools = append(tools,
			llm.Tool{Name: "save_memory", Description: "Save a one-line durable note. scope=task (default) for this task, scope=project for knowledge every future task on this codebase should know.",
				Schema: obj(map[string]any{"note": str("the note"), "scope": map[string]any{"type": "string", "enum": []any{"task", "project"}}}, "note")},
			llm.Tool{Name: "ask_user", Description: "Pause the task and ask the human one precise question.",
				Schema: obj(map[string]any{"question": str("the question")}, "question")},
			llm.Tool{Name: "step_done", Description: "Mark the current step complete.",
				Schema: obj(map[string]any{"summary": str("what was done and how it was verified")}, "summary")},
		)
	}
	return tools
}

// toolCtx carries what a tool execution needs.
type toolCtx struct {
	e        *Engine
	ctx      context.Context
	taskID   string
	stepID   string
	root     string
	settings store.Settings
	project  *store.Project
	readOnly bool
	nonce    string // untrusted-data boundary id ("" = off)
}

type toolOutcome struct {
	content  string
	isError  bool
	done     bool
	summary  string
	question string
}

var readOnlyTools = map[string]bool{"list_dir": true, "read_file": true, "search": true, "load_skill": true, "web_search": true, "web_fetch": true}

// untrusted tools return content that came from files, commands or the web.
var untrustedTools = map[string]bool{"read_file": true, "search": true, "run_command": true, "project_command": true, "web_search": true, "web_fetch": true}

func (tc *toolCtx) exec(call llm.ToolCall) toolOutcome {
	var a map[string]any
	_ = json.Unmarshal(call.Args, &a)
	s := func(k string) string { v, _ := a[k].(string); return v }
	n := func(k string, def int) int {
		if v, ok := a[k].(float64); ok && v > 0 {
			return int(v)
		}
		return def
	}
	b := func(k string) bool { v, _ := a[k].(bool); return v }
	isMCP := strings.HasPrefix(call.Name, "mcp__")
	if tc.readOnly && !readOnlyTools[call.Name] && !isMCP {
		return toolOutcome{content: "tool not available in chat (read-only)", isError: true}
	}

	var (
		out string
		err error
	)
	switch call.Name {
	case "step_done":
		return toolOutcome{content: "ok", done: true, summary: s("summary")}
	case "ask_user":
		return toolOutcome{content: "question sent to the user; task paused", question: s("question")}
	case "save_memory":
		out, err = tc.saveMemory(strings.TrimSpace(s("note")), s("scope"))
	case "load_skill":
		out, err = tc.loadSkill(s("name"))
	case "list_dir":
		out, err = tc.listDir(s("path"), n("depth", 2))
	case "read_file":
		out, err = tc.readFile(s("path"), n("start_line", 1), n("end_line", 0))
	case "search":
		out, err = tc.search(s("query"), b("regex"), s("path"), n("max_results", 50))
	case "write_file":
		out, err = tc.writeFile(s("path"), s("content"))
	case "edit_file":
		out, err = tc.editFile(s("path"), s("old_text"), s("new_text"), b("replace_all"))
	case "delete_file":
		out, err = tc.deleteFile(s("path"))
	case "run_command":
		out, err = tc.runCommand(s("command"), false)
	case "project_command":
		out, err = tc.projectCommand(s("name"))
	case "web_search":
		out, err = webSearch(tc.ctx, tc.settings, s("query"))
	case "web_fetch":
		out, err = webFetch(tc.ctx, s("url"))
	default:
		if isMCP {
			var isErr bool
			out, isErr, err = tc.mcpCall(call)
			if err == nil && isErr {
				err = errors.New(out)
			}
		} else {
			err = fmt.Errorf("unknown tool %q", call.Name)
		}
	}
	if err != nil {
		return toolOutcome{content: clip(err.Error(), tc.settings.ToolOutputMaxChars), isError: true}
	}
	out = clip(out, tc.settings.ToolOutputMaxChars)
	if tc.nonce != "" && (untrustedTools[call.Name] || isMCP) {
		out = fmt.Sprintf("<<untrusted-data id=%s>>\n%s\n<<end untrusted-data id=%s>>", tc.nonce, strings.ReplaceAll(out, "<<end untrusted-data", "<<end_untrusted-data"), tc.nonce)
	}
	return toolOutcome{content: out}
}

func (tc *toolCtx) saveMemory(note, scope string) (string, error) {
	if note == "" {
		return "", errors.New("empty note")
	}
	note = clip(note, 500)
	if scope == "project" && tc.project != nil {
		_, err := tc.e.Store.UpdateProject(tc.project.ID, func(p *store.Project) error {
			for _, k := range p.Knowledge {
				if strings.EqualFold(k.Text, note) {
					return nil
				}
			}
			p.Knowledge = append(p.Knowledge, store.Knowledge{ID: store.ShortID(), Text: note, TaskID: tc.taskID, At: time.Now().UTC()})
			return nil
		})
		if err == nil {
			tc.e.emit(tc.taskID, "info", tc.stepID, "project knowledge saved", note, nil)
		}
		return "saved to project knowledge", err
	}
	_, err := tc.e.Store.UpdateTask(tc.taskID, func(t *store.Task) error {
		if t.Brief == nil {
			t.Brief = &store.Brief{}
		}
		t.Brief.Memory = append(t.Brief.Memory, note)
		return nil
	})
	return "saved", err
}

// mcpCall runs an MCP tool, asking the user first when the server requires it.
func (tc *toolCtx) mcpCall(call llm.ToolCall) (string, bool, error) {
	srv, tool, ok := tc.e.MCP.Lookup(call.Name)
	if !ok {
		return "", false, fmt.Errorf("unknown MCP tool %q", call.Name)
	}
	label := fmt.Sprintf("mcp %s.%s %s", srv.Name, tool, excerpt(string(call.Args), 300))
	if srv.Approval {
		if tc.readOnly || tc.taskID == "" {
			return "", false, errors.New("this MCP server requires approval; it can only be used by the worker inside a task")
		}
		title := ""
		if t, err := tc.e.Store.Task(tc.taskID); err == nil {
			title = t.Title
		}
		okd, err := tc.e.waitApproval(tc.ctx, tc.taskID, tc.stepID, label)
		if err != nil {
			return "", false, err
		}
		rec := store.ApprovalRecord{TaskID: tc.taskID, TaskTitle: title, Command: label, Decision: "allowed", DecidedBy: "human"}
		if !okd {
			rec.Decision = "denied"
			tc.e.Store.AddApproval(rec)
			return "", false, errors.New("the user DENIED this MCP call; continue without it")
		}
		tc.e.Store.AddApproval(rec)
	}
	return tc.e.MCP.Call(tc.ctx, call.Name, call.Args)
}

func (tc *toolCtx) loadSkill(name string) (string, error) {
	for _, sk := range tc.e.Store.Config().Skills {
		if sk.Enabled && strings.EqualFold(sk.Name, strings.TrimSpace(name)) {
			return "# " + sk.Name + "\n" + sk.Content, nil
		}
	}
	return "", fmt.Errorf("no skill named %q", name)
}

// resolve confines a model-supplied path to the workspace (no .., no symlink escape).
func (tc *toolCtx) resolve(p string) (string, error) {
	if tc.root == "" {
		return "", errors.New("this task has no workspace")
	}
	p = strings.TrimSpace(p)
	if p == "" {
		p = "."
	}
	if filepath.IsAbs(p) {
		return "", errors.New("use a path relative to the workspace root")
	}
	full := filepath.Join(tc.root, filepath.FromSlash(p))
	rel, err := filepath.Rel(tc.root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the workspace")
	}
	// resolve symlinks on the deepest existing ancestor
	probe := full
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	realRoot, _ := filepath.EvalSymlinks(tc.root)
	if real, err := filepath.EvalSymlinks(probe); err == nil && realRoot != "" {
		r, err := filepath.Rel(realRoot, real)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", errors.New("path escapes the workspace via a symlink")
		}
	}
	if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
		return full, errNoGit
	}
	return full, nil
}

var errNoGit = errors.New("direct access to .git is not allowed; use git commands")

func (tc *toolCtx) relPath(full string) string {
	r, _ := filepath.Rel(tc.root, full)
	return filepath.ToSlash(r)
}

func (tc *toolCtx) listDir(p string, depth int) (string, error) {
	full, err := tc.resolve(p)
	if err != nil {
		return "", err
	}
	files, err := codeidx.Scan(full, 3000)
	if err != nil {
		return "", err
	}
	var kept []codeidx.File
	for _, f := range files {
		if strings.Count(f.Path, "/") < depth {
			kept = append(kept, f)
		}
	}
	tree := codeidx.Tree(kept, 250)
	if tree == "" {
		tree = "(empty)"
	}
	return fmt.Sprintf("%s (%d files total below)\n%s", tc.relPath(full), len(files), tree), nil
}

func (tc *toolCtx) readFile(p string, start, end int) (string, error) {
	full, err := tc.resolve(p)
	if err != nil {
		return "", err
	}
	f, err := os.Open(full)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if end <= 0 || end < start {
		end = start + 399
	}
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	line, total := 0, 0
	for sc.Scan() {
		line++
		total = line
		if line >= start && line <= end {
			fmt.Fprintf(&b, "%d| %s\n", line, sc.Text())
		}
	}
	if start > total && total > 0 {
		return "", fmt.Errorf("file has only %d lines", total)
	}
	if end < total {
		fmt.Fprintf(&b, "…(file has %d lines; showing %d-%d)\n", total, start, end)
	}
	return b.String(), nil
}

func (tc *toolCtx) search(q string, isRe bool, sub string, maxRes int) (string, error) {
	if q == "" {
		return "", errors.New("empty query")
	}
	base, err := tc.resolve(sub)
	if err != nil {
		return "", err
	}
	var re *regexp.Regexp
	if isRe {
		if re, err = regexp.Compile(q); err != nil {
			return "", err
		}
	}
	lq := strings.ToLower(q)
	files, _ := codeidx.Scan(base, 8000)
	var b strings.Builder
	count := 0
	for _, f := range files {
		if f.Size > 512*1024 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(f.Path)))
		if err != nil || bytes.IndexByte(data[:min(len(data), 512)], 0) >= 0 {
			continue
		}
		for i, ln := range strings.Split(string(data), "\n") {
			hit := false
			if re != nil {
				hit = re.MatchString(ln)
			} else {
				hit = strings.Contains(strings.ToLower(ln), lq)
			}
			if hit {
				rel := tc.relPath(filepath.Join(base, filepath.FromSlash(f.Path)))
				fmt.Fprintf(&b, "%s:%d: %s\n", rel, i+1, clip(strings.TrimSpace(ln), 200))
				count++
				if count >= maxRes {
					fmt.Fprintf(&b, "…(stopped at %d results)\n", maxRes)
					return b.String(), nil
				}
			}
		}
	}
	if count == 0 {
		return "no matches", nil
	}
	return b.String(), nil
}

// track backs up the original file before the first modification in this task.
func (tc *toolCtx) track(full, op string) error {
	rel := tc.relPath(full)
	_, err := tc.e.Store.UpdateTask(tc.taskID, func(t *store.Task) error {
		for i, c := range t.Changes {
			if c.Path == rel && !c.Reverted {
				if op == "delete" && c.Op == "modify" {
					t.Changes[i].Op = "delete"
				}
				return nil
			}
		}
		ch := store.Change{Path: rel, Op: op, Step: tc.stepID, At: time.Now().UTC()}
		if _, err := os.Stat(full); err == nil {
			if op == "create" {
				ch.Op = "modify"
			}
			ch.Backup = store.ShortID() + ".orig"
			dir := filepath.Join(tc.e.Store.TaskDir(tc.taskID), "backups")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			if err := copyFile(full, filepath.Join(dir, ch.Backup)); err != nil {
				return err
			}
		} else {
			ch.Op = "create"
		}
		t.Changes = append(t.Changes, ch)
		return nil
	})
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func (tc *toolCtx) writeFile(p, content string) (string, error) {
	full, err := tc.resolve(p)
	if err != nil {
		return "", err
	}
	if err := tc.track(full, "create"); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(full); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.WriteFile(full, []byte(content), mode); err != nil {
		return "", err
	}
	tc.e.emit(tc.taskID, "change", tc.stepID, "write "+tc.relPath(full), "", nil)
	return fmt.Sprintf("wrote %s (%d lines)", tc.relPath(full), strings.Count(content, "\n")+1), nil
}

func (tc *toolCtx) editFile(p, oldText, newText string, all bool) (string, error) {
	full, err := tc.resolve(p)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	src := string(data)
	if oldText == "" {
		return "", errors.New("old_text is empty")
	}
	c := strings.Count(src, oldText)
	if c == 0 {
		// tolerate CRLF files
		if strings.Contains(src, "\r\n") {
			o2 := strings.ReplaceAll(oldText, "\n", "\r\n")
			if strings.Count(src, o2) > 0 {
				oldText, newText = o2, strings.ReplaceAll(newText, "\n", "\r\n")
				c = strings.Count(src, oldText)
			}
		}
		if c == 0 {
			return "", errors.New("old_text not found; read the file again and copy the exact text")
		}
	}
	if c > 1 && !all {
		return "", fmt.Errorf("old_text matches %d times; add surrounding context or set replace_all", c)
	}
	if err := tc.track(full, "modify"); err != nil {
		return "", err
	}
	if all {
		src = strings.ReplaceAll(src, oldText, newText)
	} else {
		src = strings.Replace(src, oldText, newText, 1)
	}
	st, _ := os.Stat(full)
	if err := os.WriteFile(full, []byte(src), st.Mode().Perm()); err != nil {
		return "", err
	}
	tc.e.emit(tc.taskID, "change", tc.stepID, "edit "+tc.relPath(full), "", nil)
	return fmt.Sprintf("edited %s (%d replacement(s))", tc.relPath(full), map[bool]int{true: c, false: 1}[all]), nil
}

func (tc *toolCtx) deleteFile(p string) (string, error) {
	full, err := tc.resolve(p)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", errors.New("refusing to delete a directory")
	}
	if err := tc.track(full, "delete"); err != nil {
		return "", err
	}
	if err := os.Remove(full); err != nil {
		return "", err
	}
	tc.e.emit(tc.taskID, "change", tc.stepID, "delete "+tc.relPath(full), "", nil)
	return "deleted " + tc.relPath(full), nil
}

func (tc *toolCtx) projectCommand(name string) (string, error) {
	if tc.project == nil {
		return "", errors.New("no project commands")
	}
	for _, c := range tc.project.Commands {
		if c.Name == name {
			return tc.runCommand(c.Command, true)
		}
	}
	return "", fmt.Errorf("unknown project command %q", name)
}

func (tc *toolCtx) runCommand(cmd string, trusted bool) (string, error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", errors.New("empty command")
	}
	if tc.root == "" {
		return "", errors.New("this task has no workspace")
	}
	cfg := tc.e.Store.Config()
	st := cfg.Settings
	title := ""
	if t, err := tc.e.Store.Task(tc.taskID); err == nil {
		title = t.Title
	}
	rec := store.ApprovalRecord{TaskID: tc.taskID, TaskTitle: title, Command: cmd}
	if trusted {
		rec.Decision, rec.DecidedBy = "allowed", "project"
	} else {
		action, by, rule := decide(cfg, cmd)
		rec.DecidedBy, rec.Rule = by, rule
		switch action {
		case "allow":
			rec.Decision = "allowed"
		case "deny":
			rec.Decision = "denied"
			tc.e.Store.AddApproval(rec)
			tc.e.bumpRule(rule)
			return "", fmt.Errorf("command refused by %s (%s); do without it or ask_user", by, rule)
		default:
			ok, err := tc.e.waitApproval(tc.ctx, tc.taskID, tc.stepID, cmd)
			if err != nil {
				return "", err
			}
			rec.DecidedBy, rec.Rule = "human", ""
			if !ok {
				rec.Decision = "denied"
				tc.e.Store.AddApproval(rec)
				return "", errors.New("the user DENIED this command; continue without it")
			}
			rec.Decision = "allowed"
		}
		tc.e.bumpRule(rule)
	}
	tc.e.Store.AddApproval(rec)
	tc.e.emit(tc.taskID, "tool", tc.stepID, "$ "+cmd, "", map[string]any{"decided_by": rec.DecidedBy})
	ctx, cancel := context.WithTimeout(tc.ctx, time.Duration(st.CommandTimeoutSec)*time.Second)
	defer cancel()
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.CommandContext(ctx, "cmd", "/C", cmd)
	} else {
		c = exec.CommandContext(ctx, "sh", "-c", cmd)
	}
	c.Dir = tc.root
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
	err := c.Run()
	out := buf.String()
	// keep head and tail: errors usually sit at the end
	if max := tc.settings.ToolOutputMaxChars; len(out) > max {
		out = out[:max/3] + "\n…(output truncated)…\n" + out[len(out)-max*2/3:]
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if ctx.Err() != nil {
			return out, fmt.Errorf("timed out after %ds", st.CommandTimeoutSec)
		} else {
			return out, err
		}
	}
	return fmt.Sprintf("exit code %d\n%s", code, out), nil
}

// bumpRule counts rule hits (shown on the Rules page).
func (e *Engine) bumpRule(rule string) {
	if rule == "" {
		return
	}
	_ = e.Store.UpdateConfig(func(c *store.Config) error {
		for _, r := range c.Rules {
			if r.Kind+":"+r.Pattern == rule {
				r.Hits++
				break
			}
		}
		return nil
	})
}

// waitApproval blocks until the user approves/denies the command in the UI.
func (e *Engine) waitApproval(ctx context.Context, id, step, cmd string) (bool, error) {
	ap := &store.Approval{ID: store.ShortID(), Command: cmd, Step: step, At: time.Now().UTC()}
	ch := make(chan decision, 1)
	e.mu.Lock()
	e.approvals[id] = ch
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.approvals, id)
		e.mu.Unlock()
		_, _ = e.Store.UpdateTask(id, func(t *store.Task) error { t.Pending = nil; t.Status = "working"; return nil })
	}()
	_, _ = e.Store.UpdateTask(id, func(t *store.Task) error { t.Pending = ap; t.Status = "waiting for command approval"; return nil })
	e.emit(id, "approval", step, "approval needed", cmd, map[string]any{"id": ap.ID})
	timer := time.NewTimer(30 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case d := <-ch:
			if d.id != ap.ID {
				continue
			}
			if d.approve && d.always {
				fields := strings.Fields(cmd)
				prefix := strings.Join(fields[:min(2, len(fields))], " ")
				_ = e.Store.UpdateConfig(func(c *store.Config) error {
					c.Rules = append(c.Rules, &store.Rule{ID: store.ShortID(), Kind: "argv", Pattern: prefix,
						Action: "allow", Note: "added from an approval", Enabled: true})
					return nil
				})
			}
			verdict := "denied"
			if d.approve {
				verdict = "approved"
			}
			e.emit(id, "approval", step, verdict, cmd, nil)
			return d.approve, nil
		case <-timer.C:
			return false, nil
		case <-ctx.Done():
			return false, context.Cause(ctx)
		}
	}
}
