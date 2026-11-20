package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/khangphantz/aatmt/internal/store"
)

func openaiBase(p *store.Profile) string {
	b := strings.TrimSpace(p.BaseURL)
	if b == "" {
		b = "https://api.openai.com/v1"
	}
	if i := strings.Index(b, "/chat/completions"); i >= 0 {
		b = b[:i]
	}
	return strings.TrimRight(b, "/")
}

type oaMsg struct {
	Role       string       `json:"role"`
	Content    any          `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func toOpenAIMessages(req Request) []oaMsg {
	msgs := []oaMsg{}
	if req.System != "" {
		msgs = append(msgs, oaMsg{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			am := oaMsg{Role: "assistant"}
			if m.Text != "" {
				am.Content = m.Text
			}
			for _, c := range m.ToolCalls {
				var tc oaToolCall
				tc.ID, tc.Type = c.ID, "function"
				tc.Function.Name = c.Name
				args := string(c.Args)
				if args == "" {
					args = "{}"
				}
				tc.Function.Arguments = args
				am.ToolCalls = append(am.ToolCalls, tc)
			}
			if am.Content == nil && len(am.ToolCalls) == 0 {
				am.Content = ""
			}
			msgs = append(msgs, am)
			continue
		}
		for _, r := range m.ToolResults {
			c := r.Content
			if r.IsError {
				c = "ERROR: " + c
			}
			msgs = append(msgs, oaMsg{Role: "tool", ToolCallID: r.CallID, Content: c})
		}
		if m.Text == "" && len(m.Images) == 0 {
			continue
		}
		if len(m.Images) == 0 {
			msgs = append(msgs, oaMsg{Role: "user", Content: m.Text})
			continue
		}
		parts := []map[string]any{}
		if m.Text != "" {
			parts = append(parts, map[string]any{"type": "text", "text": m.Text})
		}
		for _, im := range m.Images {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{
				"url": "data:" + im.Mime + ";base64," + base64.StdEncoding.EncodeToString(im.Data),
			}})
		}
		msgs = append(msgs, oaMsg{Role: "user", Content: parts})
	}
	return msgs
}

func callOpenAI(ctx context.Context, p *store.Profile, req Request) (*Response, error) {
	body := map[string]any{
		"model":    p.Model,
		"messages": toOpenAIMessages(req),
	}
	field := p.MaxTokensField
	if field == "" {
		field = "max_tokens"
	}
	body[field] = maxTokens(p, req)
	if p.Temperature != nil {
		body["temperature"] = *p.Temperature
	}
	if p.Effort != "" {
		body["reasoning_effort"] = p.Effort
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Schema,
			}})
		}
		body["tools"] = tools
	}
	url := joinURL(strings.TrimSpace(p.BaseURL), "/chat/completions", "/chat/completions")
	if strings.TrimSpace(p.BaseURL) == "" {
		url = "https://api.openai.com/v1/chat/completions"
	}
	raw, err := postJSON(ctx, p, url, body, "Authorization", "Bearer ", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   json.RawMessage `json:"content"`
				ToolCalls []oaToolCall    `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
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
	if len(out.Choices) == 0 {
		return nil, errors.New("empty response (no choices)")
	}
	ch := out.Choices[0]
	r := &Response{StopReason: ch.FinishReason, Text: contentText(ch.Message.Content)}
	for _, tc := range ch.Message.ToolCalls {
		args := json.RawMessage(tc.Function.Arguments)
		if !json.Valid(args) {
			args = json.RawMessage(`{}`)
		}
		id := tc.ID
		if id == "" {
			id = "call_" + store.ShortID()
		}
		r.ToolCalls = append(r.ToolCalls, ToolCall{ID: id, Name: tc.Function.Name, Args: args})
	}
	cached := out.Usage.PromptTokensDetails.CachedTokens
	r.Usage = store.Usage{Input: out.Usage.PromptTokens - cached, Output: out.Usage.CompletionTokens, CacheRead: cached}
	return r, nil
}

// contentText accepts content as a string or an array of {type:text,text} parts.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
}
