package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/khangphantz/aatmt/internal/store"
)

const anthropicVersion = "2023-06-01"

// ErrRefusal is returned when the model declined the request (stop_reason "refusal").
var ErrRefusal = errors.New("model refused the request")

func anthropicURL(p *store.Profile) string {
	b := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if b == "" {
		b = "https://api.anthropic.com"
	}
	if strings.Contains(b, "/messages") {
		return b
	}
	if strings.HasSuffix(b, "/v1") {
		return b + "/messages"
	}
	return b + "/v1/messages"
}

func anthropicBase(p *store.Profile) string {
	b := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if b == "" {
		return "https://api.anthropic.com"
	}
	if i := strings.Index(b, "/v1"); i >= 0 {
		b = b[:i]
	}
	return b
}

var ephemeral = map[string]any{"type": "ephemeral"}

func toAnthropicMessages(p *store.Profile, req Request) []map[string]any {
	out := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			if len(m.Raw) > 0 && m.RawProfile == p.ID {
				// Same profile: replay the provider's own blocks byte-for-byte
				// (keeps thinking signatures valid and the cache prefix stable).
				out = append(out, map[string]any{"role": "assistant", "content": m.Raw})
				continue
			}
			blocks := []map[string]any{}
			if m.Text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Text})
			}
			for _, c := range m.ToolCalls {
				var input any = map[string]any{}
				if len(c.Args) > 0 {
					_ = json.Unmarshal(c.Args, &input)
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": input})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, map[string]any{"type": "text", "text": "(no output)"})
			}
			out = append(out, map[string]any{"role": "assistant", "content": blocks})
			continue
		}
		blocks := []map[string]any{}
		for _, r := range m.ToolResults {
			c := r.Content
			if c == "" {
				c = "(empty)"
			}
			blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": r.CallID, "content": c, "is_error": r.IsError})
		}
		for _, im := range m.Images {
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{
				"type": "base64", "media_type": im.Mime, "data": base64.StdEncoding.EncodeToString(im.Data),
			}})
		}
		if m.Text != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": m.Text})
		}
		if len(blocks) == 0 {
			blocks = append(blocks, map[string]any{"type": "text", "text": "(continue)"})
		}
		out = append(out, map[string]any{"role": "user", "content": blocks})
	}
	// Cache breakpoint on the newest user turn: the next request in the same
	// step re-reads the whole prefix from cache instead of paying full price.
	if p.PromptCache && len(out) > 0 {
		last := out[len(out)-1]
		if blocks, ok := last["content"].([]map[string]any); ok && len(blocks) > 0 {
			blocks[len(blocks)-1]["cache_control"] = ephemeral
		}
	}
	return out
}

func callAnthropic(ctx context.Context, p *store.Profile, req Request) (*Response, error) {
	body := map[string]any{
		"model":      p.Model,
		"max_tokens": maxTokens(p, req),
		"messages":   toAnthropicMessages(p, req),
	}
	if req.System != "" {
		if p.PromptCache {
			body["system"] = []map[string]any{{"type": "text", "text": req.System, "cache_control": ephemeral}}
		} else {
			body["system"] = req.System
		}
	}
	if p.Temperature != nil {
		body["temperature"] = *p.Temperature
	}
	if p.Effort != "" {
		body["output_config"] = map[string]any{"effort": p.Effort}
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Schema})
		}
		if p.PromptCache {
			tools[len(tools)-1]["cache_control"] = ephemeral
		}
		body["tools"] = tools
	}
	raw, err := postJSON(ctx, p, anthropicURL(p), body, "x-api-key", "", map[string]string{"anthropic-version": anthropicVersion})
	if err != nil {
		return nil, err
	}
	var out struct {
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stop_reason"`
		Usage      struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, errors.New(out.Error.Message)
	}
	if out.StopReason == "refusal" {
		return nil, ErrRefusal
	}
	var blocks []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(out.Content, &blocks)
	r := &Response{StopReason: out.StopReason, Raw: out.Content}
	var text strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			args := b.Input
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			r.ToolCalls = append(r.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Args: args})
		}
	}
	r.Text = text.String()
	r.Usage = store.Usage{
		Input:      out.Usage.InputTokens,
		Output:     out.Usage.OutputTokens,
		CacheRead:  out.Usage.CacheReadInputTokens,
		CacheWrite: out.Usage.CacheCreationInputTokens,
	}
	return r, nil
}
