// Package store holds AATMT's data model and its JSON-file persistence.
//
// Everything lives under one data directory:
//
//	config.json                    providers, agents, skills, rules, webhooks, templates, settings
//	approvals.jsonl                every command decision (human / rule / policy)
//	projects/<id>/project.json     a codebase shared by many tasks + its knowledge base and chat
//	projects/<id>/workspace/       cloned / extracted source (git & zip projects)
//	tasks/<id>/task.json           one task (captures, brief, plan, changes, chat, usage)
//	tasks/<id>/events.jsonl        append-only activity log
//	tasks/<id>/llm.jsonl           recorded LLM calls (truncated request/response)
//	tasks/<id>/images/             captured screenshots
//	tasks/<id>/backups/            originals of files the agent modified
package store

import "time"

// Task stages, in workflow order.
const (
	StageInbox     = "inbox"     // captured, not analysed yet
	StageAnalyzing = "analyzing" // vision + analyzer running
	StagePlanned   = "planned"   // brief + plan ready, waiting for approval
	StageRunning   = "running"   // worker executing steps
	StagePaused    = "paused"    // paused by user, question, error or budget
	StageReview    = "review"    // all steps finished, waiting for human review
	StageDone      = "done"
)

var Stages = []string{StageInbox, StageAnalyzing, StagePlanned, StageRunning, StagePaused, StageReview, StageDone}

// Step statuses.
const (
	StepPending = "pending"
	StepRunning = "running"
	StepDone    = "done"
	StepFailed  = "failed"
	StepSkipped = "skipped"
)

// Agent roles. Each role has its own model chain and an optional prompt override.
const (
	RoleVision   = "vision"
	RoleAnalyzer = "analyzer"
	RoleWorker   = "worker"
	RoleChat     = "chat"
)

var AgentRoles = []string{RoleVision, RoleAnalyzer, RoleWorker, RoleChat}

// Profile is one configured AI provider endpoint + model.
type Profile struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Format  string `json:"format"`   // "openai" (any OpenAI-compatible) | "anthropic"
	BaseURL string `json:"base_url"` // base URL, or the full .../chat/completions | .../messages URL
	APIKey  string `json:"api_key"`  // literal key, or "env:VAR_NAME"
	Model   string `json:"model"`

	MaxTokens      int      `json:"max_tokens"`       // per-reply output cap (0 = 8192)
	MaxTokensField string   `json:"max_tokens_field"` // openai only: "max_tokens" (default) | "max_completion_tokens"
	ContextWindow  int      `json:"context_window"`   // tokens; drives compaction (0 = 128000)
	Temperature    *float64 `json:"temperature,omitempty"`
	Effort         string   `json:"effort,omitempty"` // anthropic output_config.effort / openai reasoning_effort ("" = omit)

	Vision      bool              `json:"vision"`       // accepts image input
	ToolMode    string            `json:"tool_mode"`    // "native" | "json" (prompted JSON actions for gateways without tool calling)
	PromptCache bool              `json:"prompt_cache"` // anthropic: mark system/tools with cache_control
	Headers     map[string]string `json:"headers,omitempty"`
	AuthHeader  string            `json:"auth_header,omitempty"` // override, e.g. "api-key" for Azure
	AuthPrefix  string            `json:"auth_prefix,omitempty"` // override, "none" = raw key

	RatePerMinute int  `json:"rate_per_minute"` // 0 = unlimited
	TimeoutSec    int  `json:"timeout_sec"`     // 0 = 300
	Enabled       bool `json:"enabled"`

	// Optional price per 1M tokens, for cost display only.
	PriceIn  float64 `json:"price_in,omitempty"`
	PriceOut float64 `json:"price_out,omitempty"`
}

// Roles maps each agent role to an ordered failover chain of profile ids.
// An empty chain means "every enabled profile, in list order".
type Roles struct {
	Vision   []string `json:"vision"`
	Analyzer []string `json:"analyzer"`
	Worker   []string `json:"worker"`
	Chat     []string `json:"chat"` // empty = analyzer chain
}

func (r Roles) Get(role string) []string {
	switch role {
	case RoleVision:
		return r.Vision
	case RoleAnalyzer:
		return r.Analyzer
	case RoleChat:
		if len(r.Chat) == 0 {
			return r.Analyzer
		}
		return r.Chat
	default:
		return r.Worker
	}
}

type Settings struct {
	Language           string   `json:"language"`              // language for briefs/summaries written by the AI
	MaxConcurrentTasks int      `json:"max_concurrent_tasks"`  // tasks talking to providers at once
	MaxTurnsPerStep    int      `json:"max_turns_per_step"`    // tool-loop turns per plan step
	TaskTokenBudget    int      `json:"task_token_budget"`     // 0 = unlimited; task pauses when exceeded
	TaskTurnBudget     int      `json:"task_turn_budget"`      // 0 = unlimited; total worker turns per task
	CodeContextTokens  int      `json:"code_context_tokens"`   // budget for ranked code snippets in analysis
	CompactAtPct       int      `json:"compact_at_pct"`        // restart a step session from a summary at this % of context
	ToolOutputMaxChars int      `json:"tool_output_max_chars"` // cap per tool result
	CommandPolicy      string   `json:"command_policy"`        // fallback when no rule matches: "off" | "ask" | "allowlist" | "auto"
	CommandAllowlist   []string `json:"command_allowlist"`     // command prefixes that run without asking
	CommandTimeoutSec  int      `json:"command_timeout_sec"`
	AutoRun            bool     `json:"auto_run"`             // start working right after analysis
	AutoAnalyze        bool     `json:"auto_analyze"`         // analyse right after capture
	AutoPromoteMemory  bool     `json:"auto_promote_memory"`  // copy task memory into project knowledge when done
	UntrustedBoundary  bool     `json:"untrusted_boundary"`   // frame tool output as data, not instructions
	RecordLLM          bool     `json:"record_llm"`           // keep llm.jsonl per task
	RetryAttempts      int      `json:"retry_attempts"`       // per profile before failing over (transient errors)
	BreakerCooldownSec int      `json:"breaker_cooldown_sec"` // 0 = 1/5/30 min ladder
	WebSearch          string   `json:"web_search"`           // "" (off) | "tavily" | "brave"
	WebSearchKey       string   `json:"web_search_key"`       // literal or env:NAME
	WebFetch           bool     `json:"web_fetch"`            // allow the agent to fetch public URLs
	PublicURL          string   `json:"public_url"`           // used in notification links
}

func DefaultSettings() Settings {
	return Settings{
		Language:           "English",
		MaxConcurrentTasks: 2,
		MaxTurnsPerStep:    30,
		CodeContextTokens:  12000,
		CompactAtPct:       60,
		ToolOutputMaxChars: 12000,
		CommandPolicy:      "ask",
		CommandAllowlist:   []string{"git status", "git diff", "git log", "go build", "go test", "go vet", "npm test", "npm run build", "ls", "cat"},
		CommandTimeoutSec:  120,
		AutoAnalyze:        true,
		AutoPromoteMemory:  true,
		UntrustedBoundary:  true,
		RecordLLM:          true,
		RetryAttempts:      3,
		PublicURL:          "http://127.0.0.1:8787",
	}
}

// Usage counts tokens. CacheRead/CacheWrite are reported by providers that support prompt caching.
type Usage struct {
	Input      int     `json:"input"`
	Output     int     `json:"output"`
	CacheRead  int     `json:"cache_read"`
	CacheWrite int     `json:"cache_write"`
	Calls      int     `json:"calls"`
	Cost       float64 `json:"cost"`
}

func (u *Usage) Add(o Usage) {
	u.Input += o.Input
	u.Output += o.Output
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
	u.Calls += o.Calls
	u.Cost += o.Cost
}

func (u Usage) Total() int { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

// Skill is a reusable playbook (conventions, procedures, checklists). Its
// name + description are always visible to the agents; the content is loaded
// only when relevant (always-on, attached to the project, keyword match or
// the load_skill tool), so a big skill library costs almost no tokens.
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Content     string   `json:"content"`
	Keywords    []string `json:"keywords"`
	Always      bool     `json:"always"`
	Enabled     bool     `json:"enabled"`
}

// Rule decides a shell command before the fallback policy is consulted.
type Rule struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`    // "prefix" | "argv" | "regex" | "contains"
	Pattern string `json:"pattern"` // argv: space-separated tokens
	Action  string `json:"action"`  // "allow" | "ask" | "deny"
	Note    string `json:"note"`
	Enabled bool   `json:"enabled"`
	Hits    int    `json:"hits"`
}

// Webhook posts a short message on selected events.
type Webhook struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Format  string   `json:"format"` // "slack" | "discord" | "teams" | "generic"
	Events  []string `json:"events"` // approval | question | review | done | error | paused
	Enabled bool     `json:"enabled"`
}

// MCPServer is an external Model Context Protocol server whose tools are
// exposed to the agents (namespaced mcp__<server>__<tool>).
type MCPServer struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`      // short slug, used in tool names
	Transport  string            `json:"transport"` // "http" (Streamable HTTP) | "sse" (legacy SSE) | "stdio"
	URL        string            `json:"url,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"` // values may be env:NAME
	Command    string            `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"` // values may be env:NAME
	Roles      []string          `json:"roles"`         // agents that get the tools: worker | chat
	Tools      []string          `json:"tools"`         // allowlist of tool names (empty = all)
	Approval   bool              `json:"approval"`      // ask the user before every call
	TimeoutSec int               `json:"timeout_sec"`
	Enabled    bool              `json:"enabled"`
}

// Template pre-fills a new task.
type Template struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Title     string   `json:"title"`
	Message   string   `json:"message"`
	Priority  string   `json:"priority"`
	Tags      []string `json:"tags"`
	ProjectID string   `json:"project_id"`
	Steps     []string `json:"steps"` // optional fixed plan (skips analysis planning when non-empty)
}

type Config struct {
	Profiles  []*Profile        `json:"profiles"`
	Roles     Roles             `json:"roles"`
	Prompts   map[string]string `json:"prompts"` // role -> instruction override ("" = built-in)
	Skills    []*Skill          `json:"skills"`
	Rules     []*Rule           `json:"rules"`
	Webhooks  []*Webhook        `json:"webhooks"`
	Templates []*Template       `json:"templates"`
	MCP       []*MCPServer      `json:"mcp_servers"`
	Settings  Settings          `json:"settings"`
	Usage     map[string]Usage  `json:"usage"` // per profile id
	Daily     map[string]Usage  `json:"daily"` // per UTC date (YYYY-MM-DD), last 90 days
}

// Message is a captured message from a manager / client / chat.
type Message struct {
	ID   string    `json:"id"`
	From string    `json:"from"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Image is a captured screenshot. Transcript is produced ONCE by the vision role
// (or typed by the user) and reused by every later prompt, so non-vision / cheaper
// models can continue the task without paying for image tokens again.
type Image struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	File          string `json:"file"` // file name inside tasks/<id>/images
	Mime          string `json:"mime"`
	Size          int64  `json:"size"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Transcript    string `json:"transcript"`
	TranscribedBy string `json:"transcribed_by"`
}

type Source struct {
	Kind string `json:"kind"` // "none" | "path" | "git" | "upload"
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
	Ref  string `json:"ref,omitempty"`
}

// Knowledge is a durable fact about a project, shared by all its tasks.
type Knowledge struct {
	ID     string    `json:"id"`
	Text   string    `json:"text"`
	TaskID string    `json:"task_id,omitempty"` // task that discovered it
	At     time.Time `json:"at"`
}

// Command is a named project command exposed to the worker as a tool
// (e.g. test -> "npm test"). User-defined, so it runs without approval.
type Command struct {
	Name        string `json:"name"`
	Command     string `json:"command"`
	Description string `json:"description"`
}

// ChatMsg is one turn of a side conversation with the agent (task or project).
type ChatMsg struct {
	ID      string    `json:"id"`
	Role    string    `json:"role"` // user | assistant | error
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
	Profile string    `json:"profile,omitempty"`
	Tokens  int       `json:"tokens,omitempty"`
}

// Project is a codebase. Many tasks share one project (one workspace,
// one knowledge base); tasks of the same project run one at a time.
type Project struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Source      Source      `json:"source"`
	Workspace   string      `json:"workspace"`
	Knowledge   []Knowledge `json:"knowledge"`
	Commands    []Command   `json:"commands"`
	SkillIDs    []string    `json:"skill_ids"`
	Overrides   Overrides   `json:"overrides"`
	Chat        []ChatMsg   `json:"chat"`
	Usage       Usage       `json:"usage"`
	Archived    bool        `json:"archived"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// Brief is the compact, model-independent task memory. Every worker prompt is
// built from it instead of replaying chat history, so switching model/provider
// mid-task costs only the brief (~1-2k tokens).
type Brief struct {
	Summary       string   `json:"summary"`
	Requirements  []string `json:"requirements"`
	Acceptance    []string `json:"acceptance_criteria"`
	Questions     []string `json:"open_questions"`
	RelevantFiles []string `json:"relevant_files"`
	Risks         []string `json:"risks"`
	Estimate      string   `json:"estimate"`
	Memory        []string `json:"memory"` // durable notes the worker saved while working
}

type Step struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Detail     string     `json:"detail"`
	Files      []string   `json:"files,omitempty"`
	Status     string     `json:"status"`
	Summary    string     `json:"summary"`
	Turns      int        `json:"turns"`
	Tokens     int        `json:"tokens"`
	Profile    string     `json:"profile,omitempty"` // profile that executed it
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Note is operator feedback that the worker reads on its next turn.
type Note struct {
	ID       string    `json:"id"`
	Text     string    `json:"text"`
	At       time.Time `json:"at"`
	Consumed bool      `json:"consumed"`
}

// Change records one workspace file the agent created/modified/deleted.
type Change struct {
	Path     string    `json:"path"`   // relative to workspace
	Op       string    `json:"op"`     // "create" | "modify" | "delete"
	Backup   string    `json:"backup"` // backup file name ("" for create)
	Step     string    `json:"step"`
	At       time.Time `json:"at"`
	Reverted bool      `json:"reverted"`
}

// Approval is a command waiting for the user's decision.
type Approval struct {
	ID      string    `json:"id"`
	Command string    `json:"command"`
	Step    string    `json:"step"`
	At      time.Time `json:"at"`
}

// ApprovalRecord is the audit trail of every command decision.
type ApprovalRecord struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	TaskTitle string    `json:"task_title"`
	Command   string    `json:"command"`
	Decision  string    `json:"decision"`   // allowed | denied | timeout
	DecidedBy string    `json:"decided_by"` // human | rule | policy | project
	Rule      string    `json:"rule,omitempty"`
	At        time.Time `json:"at"`
}

type Overrides struct {
	Analyzer string `json:"analyzer,omitempty"` // profile id
	Worker   string `json:"worker,omitempty"`
}

type Task struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Stage     string    `json:"stage"`
	Priority  string    `json:"priority"` // low | normal | high | urgent
	Due       string    `json:"due,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	ProjectID string    `json:"project_id,omitempty"`
	Archived  bool      `json:"archived"`
	Pinned    bool      `json:"pinned"`
	Rotate    bool      `json:"rotate"` // multi-LLM worker rotation: step i uses chain[i % n]
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Messages  []Message `json:"messages"`
	Images    []Image   `json:"images"`
	Source    Source    `json:"source"`    // legacy per-task source (new tasks use ProjectID)
	Workspace string    `json:"workspace"` // legacy; resolved from the project when ProjectID is set

	Brief     *Brief     `json:"brief,omitempty"`
	Steps     []Step     `json:"steps"`
	Notes     []Note     `json:"notes"`
	Changes   []Change   `json:"changes"`
	Chat      []ChatMsg  `json:"chat"`
	Pending   *Approval  `json:"pending,omitempty"`
	Overrides Overrides  `json:"overrides"`
	Usage     Usage      `json:"usage"`
	Turns     int        `json:"turns"`  // worker turns across all steps
	Status    string     `json:"status"` // human-readable current activity / pause reason
	Error     string     `json:"error,omitempty"`
	EventSeq  int        `json:"event_seq"`
	LLMSeq    int        `json:"llm_seq"`
	DoneAt    *time.Time `json:"done_at,omitempty"`
}

func (t *Task) Progress() (done, total int) {
	for _, s := range t.Steps {
		if s.Status == StepSkipped {
			continue
		}
		total++
		if s.Status == StepDone {
			done++
		}
	}
	return
}

// Event is one line of a task's activity log.
type Event struct {
	Seq    int            `json:"seq"`
	TaskID string         `json:"task_id"`
	At     time.Time      `json:"at"`
	Type   string         `json:"type"` // stage | llm | tool | step | note | approval | question | change | chat | error | info | task | project
	Step   string         `json:"step,omitempty"`
	Title  string         `json:"title"`
	Detail string         `json:"detail,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

// LLMRecord is one recorded provider call (request/response truncated).
type LLMRecord struct {
	Seq       int       `json:"seq"`
	TaskID    string    `json:"task_id"`
	At        time.Time `json:"at"`
	Role      string    `json:"role"`
	Step      string    `json:"step,omitempty"`
	Profile   string    `json:"profile"`
	Model     string    `json:"model"`
	Ms        int64     `json:"ms"`
	Usage     Usage     `json:"usage"`
	Messages  int       `json:"messages"`
	Tools     int       `json:"tools"`
	SystemLen int       `json:"system_len"`
	Request   string    `json:"request"`  // last user turn, truncated
	Response  string    `json:"response"` // text + tool calls, truncated
	Error     string    `json:"error,omitempty"`
}
