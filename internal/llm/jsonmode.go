package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/khangphantz/aatmt/internal/store"
)

// JSON tool mode: for enterprise gateways that strip or do not support native
// tool calling. Tools are described in the system prompt and the model answers
// with a single JSON object {"tool": "...", "args": {...}}.

func jsonModeSystem(req Request) string {
	var b strings.Builder
	b.WriteString(req.System)
	b.WriteString("\n\n## Tool protocol\nYou can call exactly ONE tool per reply. To call a tool, reply with ONLY a JSON object, no prose, no code fence:\n")
	b.WriteString(`{"tool": "<tool name>", "args": { ... }}`)
	b.WriteString("\nThe tool result will be sent back to you in the next message.\n\n### Tools\n")
	for _, t := range req.Tools {
		sch, _ := json.Marshal(t.Schema)
		fmt.Fprintf(&b, "- %s: %s\n  args schema: %s\n", t.Name, t.Description, sch)
	}
	return b.String()
}

func jsonModeMessages(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		n := Message{Role: m.Role, Text: m.Text, Images: m.Images}
		if m.Role == "assistant" {
			for _, c := range m.ToolCalls {
				js, _ := json.Marshal(map[string]any{"tool": c.Name, "args": c.Args})
				if n.Text != "" {
					n.Text += "\n"
				}
				n.Text += string(js)
			}
		} else if len(m.ToolResults) > 0 {
			var b strings.Builder
			for _, r := range m.ToolResults {
				status := "ok"
				if r.IsError {
					status = "error"
				}
				fmt.Fprintf(&b, "<tool_result name=%q status=%q>\n%s\n</tool_result>\n", r.Name, status, r.Content)
			}
			if m.Text != "" {
				b.WriteString("\n" + m.Text)
			}
			n.Text = b.String()
		}
		out = append(out, n)
	}
	return out
}

func callJSONMode(ctx context.Context, p *store.Profile, req Request) (*Response, error) {
	jr := Request{System: jsonModeSystem(req), Messages: jsonModeMessages(req.Messages), MaxTokens: req.MaxTokens}
	resp, err := callNative(ctx, p, jr)
	if err != nil {
		return nil, err
	}
	resp.Raw = nil // the JSON text is the canonical form; no provider blocks to replay
	if call, ok := ParseJSONAction(resp.Text); ok {
		resp.ToolCalls = []ToolCall{call}
		resp.Text = ""
	}
	return resp, nil
}

// ParseJSONAction extracts {"tool":..., "args":...} from free text (tolerates code fences and prose).
func ParseJSONAction(s string) (ToolCall, bool) {
	for _, obj := range ExtractJSONObjects(s) {
		var a struct {
			Tool      string          `json:"tool"`
			Name      string          `json:"name"`
			Args      json.RawMessage `json:"args"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal([]byte(obj), &a) != nil {
			continue
		}
		name := a.Tool
		if name == "" {
			name = a.Name
		}
		if name == "" {
			continue
		}
		args := a.Args
		if len(args) == 0 {
			args = a.Arguments
		}
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage(`{}`)
		}
		// some models double-encode args as a string
		var str string
		if json.Unmarshal(args, &str) == nil && json.Valid([]byte(str)) {
			args = json.RawMessage(str)
		}
		return ToolCall{ID: "call_" + store.ShortID(), Name: name, Args: args}, true
	}
	return ToolCall{}, false
}

// ExtractJSONObjects returns every top-level balanced {...} substring, in order.
func ExtractJSONObjects(s string) []string {
	var out []string
	depth, start := 0, -1
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			if depth > 0 {
				inStr = true
			}
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					out = append(out, s[start:i+1])
					start = -1
				}
			}
		}
	}
	return out
}
