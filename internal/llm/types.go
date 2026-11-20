// Package llm is a small provider-neutral LLM client: one internal message
// format, adapters for the OpenAI-compatible chat-completions wire format
// (OpenAI, Azure OpenAI, Gemini/OpenRouter/vLLM/LiteLLM/Ollama gateways, most
// enterprise proxies) and the Anthropic Messages format, a JSON "tool protocol"
// fallback for gateways that strip native tool calling, and a failover pool.
package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/khangphantz/aatmt/internal/store"
)

type Image struct {
	Mime string
	Data []byte
}

type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type ToolResult struct {
	CallID  string
	Name    string
	Content string
	IsError bool
}

type Tool struct {
	Name        string
	Description string
	Schema      map[string]any // JSON schema for the arguments object
}

// Message is the provider-neutral conversation unit. A user message may carry
// text, images and tool results; an assistant message text and tool calls.
type Message struct {
	Role        string // "user" | "assistant"
	Text        string
	Images      []Image
	ToolCalls   []ToolCall
	ToolResults []ToolResult

	// Raw is the provider's own assistant content (Anthropic content blocks,
	// including thinking blocks and their signatures). It is replayed verbatim
	// when the same profile continues the conversation, which keeps the prompt
	// cache warm and preserved-thinking signatures valid. A different profile
	// gets the normalized form instead.
	Raw        json.RawMessage
	RawProfile string
}

type Request struct {
	System    string
	Messages  []Message
	Tools     []Tool
	MaxTokens int // 0 = profile default
}

type Response struct {
	Text       string
	ToolCalls  []ToolCall
	Usage      store.Usage
	StopReason string
	Raw        json.RawMessage
	Profile    *store.Profile
	Latency    time.Duration
}

// Assistant converts a response into the message to append to the history.
func (r *Response) Assistant() Message {
	m := Message{Role: "assistant", Text: r.Text, ToolCalls: r.ToolCalls, Raw: r.Raw}
	if r.Profile != nil {
		m.RawProfile = r.Profile.ID
	}
	return m
}

// HTTPError is a non-2xx provider response.
type HTTPError struct {
	Status     int
	Body       string
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	b := strings.TrimSpace(e.Body)
	if len(b) > 400 {
		b = b[:400] + "…"
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, b)
}

// EstimateTokens is a cheap, dependency-free token estimate (~4 chars/token for
// Latin text and code, ~1.5 for CJK). Good enough for budgets and compaction.
func EstimateTokens(s string) int {
	n, wide := 0, 0
	for _, r := range s {
		n++
		if r >= 0x2E80 {
			wide++
		}
	}
	return (n-wide)/4 + wide*2/3 + 1
}

func EstimateRequest(r Request) int {
	t := EstimateTokens(r.System)
	for _, m := range r.Messages {
		t += EstimateTokens(m.Text) + 4
		for _, c := range m.ToolCalls {
			t += EstimateTokens(string(c.Args)) + 8
		}
		for _, tr := range m.ToolResults {
			t += EstimateTokens(tr.Content) + 8
		}
		t += len(m.Images) * 1600
	}
	for _, tl := range r.Tools {
		b, _ := json.Marshal(tl.Schema)
		t += EstimateTokens(tl.Description) + EstimateTokens(string(b))
	}
	return t
}

func cost(p *store.Profile, u store.Usage) float64 {
	if p == nil {
		return 0
	}
	in := float64(u.Input) + float64(u.CacheWrite)*1.25 + float64(u.CacheRead)*0.1
	return (in*p.PriceIn + float64(u.Output)*p.PriceOut) / 1e6
}
