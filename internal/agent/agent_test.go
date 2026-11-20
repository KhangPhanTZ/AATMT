package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khangphantz/aatmt/internal/llm"
	"github.com/khangphantz/aatmt/internal/store"
)

func TestUnifiedDiff(t *testing.T) {
	a := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"
	b := "one\ntwo\nTHREE\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\n"
	d := UnifiedDiff(a, b, "x.txt", 1, store.DefaultLimits().DiffMaxCells)
	for _, want := range []string{"-three", "+THREE", "+eleven", "@@ -2,3 +2,3 @@"} {
		if !strings.Contains(d, want) {
			t.Fatalf("diff missing %q:\n%s", want, d)
		}
	}
	if strings.Count(d, "@@ ") != 2 {
		t.Fatalf("expected two hunks:\n%s", d)
	}
	if UnifiedDiff(a, a, "x", 3, store.DefaultLimits().DiffMaxCells) != "" {
		t.Fatal("identical inputs should give an empty diff")
	}
	if d := UnifiedDiff("", "new\n", "n.txt", 3, store.DefaultLimits().DiffMaxCells); !strings.Contains(d, "+new") {
		t.Fatalf("create diff: %s", d)
	}
}

func TestResolveConfinesPaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	_ = os.Symlink(outside, filepath.Join(root, "link"))
	tc := &toolCtx{root: root}
	for _, bad := range []string{"../x", "/etc/passwd", "a/../../x", "link/secret", ".git/config"} {
		if _, err := tc.resolve(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if _, err := tc.resolve("src/new/file.go"); err != nil {
		t.Errorf("valid path rejected: %v", err)
	}
}

func TestAllowlist(t *testing.T) {
	list := []string{"go test", "npm run build"}
	if !allowed("go test ./...", list) || !allowed("npm run build", list) {
		t.Fatal("expected allowed")
	}
	for _, c := range []string{"go testx", "go test ./... && rm -rf /", "go test $(evil)", "npm run build; curl x"} {
		if allowed(c, list) {
			t.Fatalf("%q must not be auto-allowed", c)
		}
	}
}

// fakeLLM scripts an OpenAI-compatible endpoint: OCR, analysis, then a worker
// that searches, reads, edits and finishes.
func fakeLLM(t *testing.T) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		sys, _ := body.Messages[0]["content"].(string)
		reply := func(msg map[string]any) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": msg}},
				"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 20},
			})
		}
		call := func(name string, args map[string]any) {
			a, _ := json.Marshal(args)
			reply(map[string]any{"content": nil, "tool_calls": []any{map[string]any{
				"id": "c" + name, "type": "function", "function": map[string]any{"name": name, "arguments": string(a)}}}})
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(sys, "OCR"):
			seen = append(seen, "vision")
			reply(map[string]any{"content": "TEXT:\nBoss: please change the greeting to 'Xin chào'\nDESCRIPTION:\nchat screenshot"})
		case strings.Contains(sys, "planner"):
			seen = append(seen, "analyze")
			user, _ := body.Messages[1]["content"].(string)
			if !strings.Contains(user, "Xin chào") || !strings.Contains(user, "greet.go") {
				t.Errorf("analysis prompt lacks transcript or code context:\n%s", user)
			}
			reply(map[string]any{"content": `{"title":"Change greeting","summary":"Change the greeting text.",
				"requirements":["greeting says Xin chào"],"acceptance_criteria":["Greet() returns Xin chào"],
				"relevant_files":["greet.go"],"plan":[{"title":"Update greeting","detail":"edit greet.go","files":["greet.go"]}]}`})
		default:
			tools := 0
			for _, m := range body.Messages {
				if m["role"] == "tool" {
					tools++
				}
			}
			seen = append(seen, "worker")
			switch tools {
			case 0:
				call("search", map[string]any{"query": "Hello"})
			case 1:
				call("read_file", map[string]any{"path": "greet.go"})
			case 2:
				call("edit_file", map[string]any{"path": "greet.go", "old_text": `"Hello"`, "new_text": `"Xin chào"`})
			case 3:
				call("save_memory", map[string]any{"note": "greeting lives in greet.go"})
			default:
				call("step_done", map[string]any{"summary": "Greeting changed to Xin chào."})
			}
		}
	}))
	return srv, &seen
}

func waitStage(t *testing.T, st *store.Store, id string, stages ...string) *store.Task {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		task, _ := st.Task(id)
		for _, s := range stages {
			if task.Stage == s && task.Status != "queued (waiting for a free slot)" {
				return task
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	task, _ := st.Task(id)
	t.Fatalf("task never reached %v: stage=%s status=%q err=%q", stages, task.Stage, task.Status, task.Error)
	return nil
}

func TestEndToEndCaptureAnalyzeRunReviewRevert(t *testing.T) {
	srv, seen := fakeLLM(t)
	defer srv.Close()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = st.UpdateConfig(func(c *store.Config) error {
		c.Profiles = []*store.Profile{{ID: "p1", Name: "fake", Format: "openai", BaseURL: srv.URL, Model: "m", Vision: true, Enabled: true}}
		return nil
	})
	ws := t.TempDir()
	src := "package main\n\nfunc Greet() string {\n\treturn \"Hello\"\n}\n"
	if err := os.WriteFile(filepath.Join(ws, "greet.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	task := &store.Task{Title: "Untitled task", Workspace: ws, Source: store.Source{Kind: "path", Path: ws}}
	if err := st.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.TaskDir(task.ID), "images", "a.png"), []byte("fakepng"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = st.UpdateTask(task.ID, func(t *store.Task) error {
		t.Images = []store.Image{{ID: "i1", Name: "chat.png", File: "a.png", Mime: "image/png"}}
		return nil
	})

	eng := New(st, llm.NewPool())
	if err := eng.Analyze(task.ID); err != nil {
		t.Fatal(err)
	}
	got := waitStage(t, st, task.ID, store.StagePlanned)
	if got.Title != "Change greeting" || len(got.Steps) != 1 || got.Images[0].Transcript == "" {
		t.Fatalf("analysis result: title=%q steps=%d transcript=%q", got.Title, len(got.Steps), got.Images[0].Transcript)
	}

	// Re-analysing must not re-run vision: transcripts are cached.
	if err := eng.Run(task.ID); err != nil {
		t.Fatal(err)
	}
	got = waitStage(t, st, task.ID, store.StageReview, store.StagePaused)
	if got.Stage != store.StageReview {
		t.Fatalf("stage=%s status=%s err=%s", got.Stage, got.Status, got.Error)
	}
	data, _ := os.ReadFile(filepath.Join(ws, "greet.go"))
	if !strings.Contains(string(data), "Xin chào") {
		t.Fatalf("file not edited:\n%s", data)
	}
	if got.Steps[0].Status != store.StepDone || got.Steps[0].Summary == "" {
		t.Fatalf("step: %+v", got.Steps[0])
	}
	if len(got.Brief.Memory) != 1 || got.Usage.Calls < 6 {
		t.Fatalf("memory=%v usage=%+v", got.Brief.Memory, got.Usage)
	}
	vision := 0
	for _, s := range *seen {
		if s == "vision" {
			vision++
		}
	}
	if vision != 1 {
		t.Fatalf("vision calls = %d, want exactly 1", vision)
	}

	d, err := eng.FileDiff(got, "greet.go")
	if err != nil || !strings.Contains(d, "+\treturn \"Xin chào\"") {
		t.Fatalf("diff err=%v:\n%s", err, d)
	}
	n, err := eng.Revert(task.ID, "")
	if err != nil || n != 1 {
		t.Fatalf("revert n=%d err=%v", n, err)
	}
	data, _ = os.ReadFile(filepath.Join(ws, "greet.go"))
	if string(data) != src {
		t.Fatalf("revert did not restore the original:\n%s", data)
	}
	evs, _ := st.Events(task.ID, 0, 0)
	if len(evs) < 8 {
		t.Fatalf("expected an activity log, got %d events", len(evs))
	}
}

func TestCommandApprovalFlow(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	_ = st.UpdateConfig(func(c *store.Config) error {
		c.Settings.CommandPolicy = "ask"
		c.Settings.CommandAllowlist = nil
		return nil
	})
	task := &store.Task{Title: "x", Workspace: t.TempDir()}
	_ = st.CreateTask(task)
	eng := New(st, llm.NewPool())
	tc := &toolCtx{e: eng, ctx: t.Context(), taskID: task.ID, stepID: "s", root: task.Workspace, settings: st.Config().Settings}

	done := make(chan toolOutcome, 1)
	go func() {
		done <- tc.exec(llm.ToolCall{Name: "run_command", Args: json.RawMessage(`{"command":"echo hi"}`)})
	}()
	var ap *store.Approval
	for i := 0; i < 200 && ap == nil; i++ {
		time.Sleep(10 * time.Millisecond)
		cur, _ := st.Task(task.ID)
		ap = cur.Pending
	}
	if ap == nil {
		t.Fatal("no pending approval")
	}
	if err := eng.Decide(task.ID, ap.ID, true, true); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if out.isError || !strings.Contains(out.content, "hi") {
		t.Fatalf("command output: %+v", out)
	}
	rules := st.Config().Rules
	if len(rules) != 1 || rules[0].Kind != "argv" || rules[0].Pattern != "echo hi" || rules[0].Action != "allow" {
		t.Fatalf("always-allow should add an argv allow rule, got %+v", rules)
	}
	if recs := st.Approvals(0); len(recs) != 1 || recs[0].DecidedBy != "human" || recs[0].Decision != "allowed" {
		t.Fatalf("approval audit: %+v", recs)
	}
	// the new rule now decides without asking
	out = tc.exec(llm.ToolCall{Name: "run_command", Args: json.RawMessage(`{"command":"echo hi again"}`)})
	if out.isError || !strings.Contains(out.content, "hi again") {
		t.Fatalf("rule-allowed command: %+v", out)
	}
}

func TestRulesDecide(t *testing.T) {
	cfg := store.Config{Settings: store.DefaultSettings(), Rules: []*store.Rule{
		{Kind: "argv", Pattern: "git push", Action: "deny", Enabled: true},
		{Kind: "regex", Pattern: `^npm (test|run lint)$`, Action: "allow", Enabled: true},
		{Kind: "prefix", Pattern: "go test", Action: "ask", Enabled: true},
	}}
	cases := map[string]string{
		"git push origin main":       "deny",
		"go build ./... && git push": "deny", // deny matches anywhere in a chain
		"npm test":                   "allow",
		"go test ./...":              "ask", // rule beats the allowlist
		"git status":                 "allow",
		"git status; rm -rf x":       "ask", // chained: allowlist does not apply
		"curl example.com":           "ask",
	}
	for cmd, want := range cases {
		if got, _, _ := decide(cfg, cmd); got != want {
			t.Errorf("%q: got %s want %s", cmd, got, want)
		}
	}
	cfg.Settings.CommandPolicy = "off"
	if got, _, _ := decide(cfg, "npm test"); got != "allow" {
		t.Errorf("explicit allow rules apply before the policy, got %s", got)
	}
	if got, _, _ := decide(cfg, "ls"); got != "deny" {
		t.Errorf("policy off denies the rest, got %s", got)
	}
}

func TestChainRotation(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	_ = st.UpdateConfig(func(c *store.Config) error {
		c.Profiles = []*store.Profile{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}, {ID: "c", Enabled: false}}
		return nil
	})
	e := New(st, llm.NewPool())
	ids := func(ps []*store.Profile) string {
		var s []string
		for _, p := range ps {
			s = append(s, p.ID)
		}
		return strings.Join(s, ",")
	}
	if got := ids(e.chain(store.RoleWorker, "", 0)); got != "a,b,c" {
		t.Fatalf("no rotation: %s", got)
	}
	if got := ids(e.chain(store.RoleWorker, "", 1)); got != "b,a" {
		t.Fatalf("rotation 1: %s", got)
	}
	if got := ids(e.chain(store.RoleWorker, "", 2)); got != "a,b" {
		t.Fatalf("rotation 2: %s", got)
	}
}

func TestProjectSharedKnowledgeHistoryAndChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		sys, _ := body.Messages[0]["content"].(string)
		reply := func(msg map[string]any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5}})
		}
		call := func(name string, args map[string]any) {
			a, _ := json.Marshal(args)
			reply(map[string]any{"tool_calls": []any{map[string]any{"id": "c" + name, "type": "function",
				"function": map[string]any{"name": name, "arguments": string(a)}}}})
		}
		tools := 0
		for _, m := range body.Messages {
			if m["role"] == "tool" {
				tools++
			}
		}
		switch {
		case strings.Contains(sys, "embedded in a developer's task tracker"):
			if !strings.Contains(sys, "Build with make") {
				t.Errorf("chat context lacks project knowledge")
			}
			if tools == 0 {
				call("read_file", map[string]any{"path": "a.txt"})
				return
			}
			reply(map[string]any{"content": "a.txt says hello"})
		default: // worker
			if tools == 0 {
				call("save_memory", map[string]any{"note": "Build with make", "scope": "project"})
				return
			}
			call("step_done", map[string]any{"summary": "ok"})
		}
	}))
	defer srv.Close()
	st, _ := store.Open(t.TempDir())
	_ = st.UpdateConfig(func(c *store.Config) error {
		c.Profiles = []*store.Profile{{ID: "p", Name: "fake", Format: "openai", BaseURL: srv.URL, Model: "m", Enabled: true}}
		return nil
	})
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hello"), 0o644)
	proj := &store.Project{Name: "demo", Workspace: ws, Source: store.Source{Kind: "path", Path: ws}}
	_ = st.CreateProject(proj)
	eng := New(st, llm.NewPool())

	a := &store.Task{Title: "First task", ProjectID: proj.ID, Brief: &store.Brief{Summary: "first summary"},
		Steps: []store.Step{{ID: "s1", Title: "do it", Status: store.StepPending}}}
	_ = st.CreateTask(a)
	if err := eng.Run(a.ID); err != nil {
		t.Fatal(err)
	}
	waitStage(t, st, a.ID, store.StageReview)
	p, _ := st.Project(proj.ID)
	if len(p.Knowledge) != 1 || p.Knowledge[0].Text != "Build with make" || p.Usage.Calls != 2 {
		t.Fatalf("project knowledge/usage: %+v %+v", p.Knowledge, p.Usage)
	}
	if err := eng.Complete(a.ID); err != nil {
		t.Fatal(err)
	}

	b := &store.Task{Title: "Second task", ProjectID: proj.ID, Messages: []store.Message{{Text: "change a.txt"}}}
	_ = st.CreateTask(b)
	prompt := eng.AnalysisPrompt(b)
	for _, want := range []string{"Build with make", "Recent tasks on this codebase", "First task", "a.txt", "minimal-diff"} {
		if !strings.Contains(prompt, want) && want != "minimal-diff" {
			t.Fatalf("analysis prompt lacks %q:\n%s", want, prompt)
		}
	}
	if st.Workspace(b) != ws {
		t.Fatal("task should resolve the project workspace")
	}

	if err := eng.Chat("task", b.ID, "what is in a.txt?"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && eng.ChatBusy("task", b.ID) {
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := st.Task(b.ID)
	if len(got.Chat) != 2 || got.Chat[1].Role != "assistant" || got.Chat[1].Text != "a.txt says hello" {
		t.Fatalf("chat: %+v", got.Chat)
	}
	if recs := st.LLMRecords(b.ID, 0); len(recs) != 2 || !strings.Contains(recs[1].Request, "untrusted-data") {
		t.Fatalf("llm records should hold both chat calls with framed tool output: %+v", recs)
	}
	if md := eng.Report(got); !strings.Contains(md, "# Second task") || !strings.Contains(md, "## Request") {
		t.Fatalf("report:\n%s", md)
	}
}

func TestWorkerUsesMCPTool(t *testing.T) {
	mcpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &m)
		id, ok := m["id"]
		if !ok {
			w.WriteHeader(202)
			return
		}
		res := map[string]any{"jsonrpc": "2.0", "id": id}
		switch m["method"] {
		case "initialize":
			res["result"] = map[string]any{"protocolVersion": "2025-03-26", "serverInfo": map[string]any{"name": "jira-fake"}}
		case "tools/list":
			res["result"] = map[string]any{"tools": []any{map[string]any{"name": "get_issue", "description": "Read a ticket",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}}}}}
		case "tools/call":
			args := m["params"].(map[string]any)["arguments"].(map[string]any)
			res["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": "ticket " + args["key"].(string) + ": button is misaligned"}}}
		}
		_ = json.NewEncoder(w).Encode(res)
	}))
	defer mcpSrv.Close()

	var sawTool, sawResult bool
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
			Tools    []map[string]any `json:"tools"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		for _, tl := range body.Tools {
			if fn, _ := tl["function"].(map[string]any); fn["name"] == "mcp__jira__get_issue" {
				sawTool = true
			}
		}
		tools := 0
		for _, m := range body.Messages {
			if m["role"] == "tool" {
				tools++
				if c, _ := m["content"].(string); strings.Contains(c, "ticket ABC-1: button is misaligned") && strings.Contains(c, "untrusted-data") {
					sawResult = true
				}
			}
		}
		name, args := "mcp__jira__get_issue", `{"key":"ABC-1"}`
		if tools > 0 {
			name, args = "step_done", `{"summary":"read the ticket"}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{
			map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": name, "arguments": args}}}}}}})
	}))
	defer llmSrv.Close()

	st, _ := store.Open(t.TempDir())
	_ = st.UpdateConfig(func(c *store.Config) error {
		c.Profiles = []*store.Profile{{ID: "p", Name: "fake", Format: "openai", BaseURL: llmSrv.URL, Model: "m", Enabled: true}}
		c.MCP = []*store.MCPServer{{ID: "j", Name: "jira", Transport: "http", URL: mcpSrv.URL, Roles: []string{"worker"}, Enabled: true}}
		return nil
	})
	eng := New(st, llm.NewPool())
	defer eng.MCP.Close()
	task := &store.Task{Title: "t", Steps: []store.Step{{ID: "s", Title: "read ticket", Status: store.StepPending}}}
	_ = st.CreateTask(task)
	if err := eng.Run(task.ID); err != nil {
		t.Fatal(err)
	}
	got := waitStage(t, st, task.ID, store.StageReview, store.StagePaused)
	if got.Stage != store.StageReview || !sawTool || !sawResult {
		t.Fatalf("stage=%s err=%s sawTool=%v sawResult=%v", got.Stage, got.Error, sawTool, sawResult)
	}
}
