package store

import "reflect"

// Limits holds every operational limit, timeout and retention value the app
// uses. Nothing is hard-coded elsewhere: each value is shown and editable in
// Settings → Limits; a zero / missing value falls back to DefaultLimits.
type Limits struct {
	// uploads & images
	MaxImageMB       int `json:"max_image_mb"`       // per screenshot
	MaxUploadMB      int `json:"max_upload_mb"`      // per request (zip sources included)
	MaxUnzipMB       int `json:"max_unzip_mb"`       // extracted size of a zip source
	MaxUnzipFiles    int `json:"max_unzip_files"`    // files in a zip source
	ImageMaxEdge     int `json:"image_max_edge"`     // screenshots are downscaled to this longest edge (px)
	ImageJPEGQuality int `json:"image_jpeg_quality"` // re-encode quality for JPEG screenshots

	// code context
	CodeScanMaxFiles int `json:"code_scan_max_files"` // files indexed per workspace
	CodeMaxFileKB    int `json:"code_max_file_kb"`    // larger files are skipped by ranking/search
	CodeTreeLines    int `json:"code_tree_lines"`     // lines of the file tree in the analysis prompt
	CodeRankFiles    int `json:"code_rank_files"`     // files whose excerpts go into the analysis prompt
	ReadFileLines    int `json:"read_file_lines"`     // default read_file window
	SearchMaxResults int `json:"search_max_results"`  // default search tool result cap
	FileViewMaxKB    int `json:"file_view_max_kb"`    // project file browser

	// prompt budgets
	CaptureMaxChars   int `json:"capture_max_chars"`   // captured request in the analysis prompt
	StepCaptureChars  int `json:"step_capture_chars"`  // captured request in each step prompt
	KnowledgeMaxNotes int `json:"knowledge_max_notes"` // newest project notes injected into prompts
	HistoryTasks      int `json:"history_tasks"`       // recent tasks of the project shown to the planner
	SkillMaxChars     int `json:"skill_max_chars"`     // per injected skill
	SummaryChars      int `json:"summary_chars"`       // step summaries, notes, snippets, tool descriptions in prompts
	ProjectDescChars  int `json:"project_desc_chars"`  // project description in prompts
	StepTreeLines     int `json:"step_tree_lines"`     // file tree in step and chat prompts

	// model call defaults (used when a provider leaves the field empty)
	DefaultMaxTokens     int `json:"default_max_tokens"`
	DefaultContextWindow int `json:"default_context_window"`
	DefaultTimeoutSec    int `json:"default_timeout_sec"`

	// chat & approvals
	ChatMaxTurns       int `json:"chat_max_turns"` // tool rounds per chat answer
	ChatHistory        int `json:"chat_history"`   // previous chat messages replayed
	ChatContextEvents  int `json:"chat_context_events"`
	ChatTimeoutMin     int `json:"chat_timeout_min"`
	ApprovalTimeoutMin int `json:"approval_timeout_min"`

	// provider pool
	BreakerTripAfter int `json:"breaker_trip_after"`  // consecutive transient failures that trip a provider
	RetryBackoffSec  int `json:"retry_backoff_sec"`   // base backoff between retries (grows per attempt)
	RetryAfterCapSec int `json:"retry_after_cap_sec"` // longest Retry-After honoured

	// MCP
	MCPConnectTimeoutSec int `json:"mcp_connect_timeout_sec"`
	MCPCallTimeoutSec    int `json:"mcp_call_timeout_sec"` // when a server sets no timeout
	MCPRetryAfterSec     int `json:"mcp_retry_after_sec"`  // wait before reconnecting a failed server

	// network
	GitTimeoutMin      int `json:"git_timeout_min"`
	WebhookTimeoutSec  int `json:"webhook_timeout_sec"`
	WebFetchTimeoutSec int `json:"web_fetch_timeout_sec"`
	WebFetchMaxKB      int `json:"web_fetch_max_kb"`
	WebSearchResults   int `json:"web_search_results"`

	// records, display & retention
	LLMRecordMaxChars int `json:"llm_record_max_chars"`
	EventDetailChars  int `json:"event_detail_chars"`
	DiffMaxChars      int `json:"diff_max_chars"`  // per file in the Changes tab and reports
	DiffMaxCells      int `json:"diff_max_cells"`  // line-diff matrix size before falling back to a whole-file diff
	ListMaxItems      int `json:"list_max_items"`  // events / records / approvals returned per request
	DashboardItems    int `json:"dashboard_items"` // recent activity rows
	StreamPingSec     int `json:"stream_ping_sec"` // live-update keepalive
	DashboardDays     int `json:"dashboard_days"`
	UsageHistoryDays  int `json:"usage_history_days"`
	LogLines          int `json:"log_lines"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxImageMB: 20, MaxUploadMB: 200, MaxUnzipMB: 500, MaxUnzipFiles: 20000, ImageMaxEdge: 1568, ImageJPEGQuality: 88,
		CodeScanMaxFiles: 8000, CodeMaxFileKB: 256, CodeTreeLines: 220, CodeRankFiles: 12, ReadFileLines: 400, SearchMaxResults: 50, FileViewMaxKB: 1024,
		CaptureMaxChars: 24000, StepCaptureChars: 6000, KnowledgeMaxNotes: 40, HistoryTasks: 8, SkillMaxChars: 3000,
		SummaryChars: 400, ProjectDescChars: 1500, StepTreeLines: 40,
		DefaultMaxTokens: 8192, DefaultContextWindow: 128000, DefaultTimeoutSec: 300,
		ChatMaxTurns: 8, ChatHistory: 12, ChatContextEvents: 30, ChatTimeoutMin: 10, ApprovalTimeoutMin: 30,
		BreakerTripAfter: 2, RetryBackoffSec: 2, RetryAfterCapSec: 30,
		MCPConnectTimeoutSec: 30, MCPCallTimeoutSec: 60, MCPRetryAfterSec: 30,
		GitTimeoutMin: 10, WebhookTimeoutSec: 10, WebFetchTimeoutSec: 25, WebFetchMaxKB: 2048, WebSearchResults: 6,
		LLMRecordMaxChars: 8000, EventDetailChars: 1500, DiffMaxChars: 200000, DiffMaxCells: 9000000, ListMaxItems: 500,
		DashboardItems: 40, StreamPingSec: 25, DashboardDays: 14, UsageHistoryDays: 90, LogLines: 2000,
	}
}

// fillLimits replaces zero / negative values with their defaults.
func fillLimits(l *Limits) {
	d := reflect.ValueOf(DefaultLimits())
	v := reflect.ValueOf(l).Elem()
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Int() <= 0 {
			v.Field(i).SetInt(d.Field(i).Int())
		}
	}
}
