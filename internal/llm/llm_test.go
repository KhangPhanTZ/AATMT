package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/khangphantz/aatmt/internal/store"
)

func TestParseJSONAction(t *testing.T) {
	cases := []string{
		`{"tool":"read_file","args":{"path":"a.go"}}`,
		"Sure.\n```json\n{\"tool\": \"read_file\", \"args\": {\"path\": \"a.go\"}}\n```",
		`{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}`,
	}
	for _, c := range cases {
		call, ok := ParseJSONAction(c)
		if !ok || call.Name != "read_file" {
			t.Fatalf("parse %q: ok=%v name=%q", c, ok, call.Name)
		}
		var a map[string]string
		if err := json.Unmarshal(call.Args, &a); err != nil || a["path"] != "a.go" {
			t.Fatalf("args %q: %s", c, call.Args)
		}
	}
	if _, ok := ParseJSONAction("no json here"); ok {
		t.Fatal("expected no action")
	}
}

func TestExtractJSONObjectsWithBracesInStrings(t *testing.T) {
	got := ExtractJSONObjects(`x {"a":"}{"} y {"b":1}`)
	if len(got) != 2 || got[0] != `{"a":"}{"}` {
		t.Fatalf("got %q", got)
	}
}

func TestOpenAIAdapterToolsAndUsage(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("path=%s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		io.WriteString(w, `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"search","arguments":"{\"query\":\"x\"}"}}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":40}}}`)
	}))
	defer srv.Close()
	p := &store.Profile{ID: "p", Format: "openai", BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", Enabled: true}
	resp, err := Call(context.Background(), p, Request{
		System: "sys",
		Messages: []Message{
			{Role: "user", Text: "hi", Images: []Image{{Mime: "image/png", Data: []byte{1, 2}}}},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "c0", Name: "list_dir", Args: json.RawMessage(`{}`)}}},
			{Role: "user", ToolResults: []ToolResult{{CallID: "c0", Name: "list_dir", Content: "a.go"}}, Text: "note"},
		},
		Tools: []Tool{{Name: "search", Description: "d", Schema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "search" {
		t.Fatalf("tool calls %+v", resp.ToolCalls)
	}
	if resp.Usage.Input != 60 || resp.Usage.CacheRead != 40 || resp.Usage.Output != 7 {
		t.Fatalf("usage %+v", resp.Usage)
	}
	msgs := body["messages"].([]any)
	// system, user(with image), assistant(tool_calls), tool, user(note)
	if len(msgs) != 5 || msgs[3].(map[string]any)["role"] != "tool" || msgs[4].(map[string]any)["content"] != "note" {
		t.Fatalf("messages %v", msgs)
	}
	if _, ok := body["temperature"]; ok {
		t.Fatal("temperature must be omitted unless configured")
	}
}

func TestAnthropicAdapterReplaysRawBlocksForSameProfile(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("path=%s headers=%v", r.URL.Path, r.Header)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		io.WriteString(w, `{"content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"text","text":"ok"},
			{"type":"tool_use","id":"tu1","name":"step_done","input":{"summary":"s"}}],"stop_reason":"tool_use",
			"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":90,"cache_creation_input_tokens":3}}`)
	}))
	defer srv.Close()
	p := &store.Profile{ID: "a", Format: "anthropic", BaseURL: srv.URL, APIKey: "k", Model: "claude-x", PromptCache: true, Enabled: true}
	raw := json.RawMessage(`[{"type":"thinking","thinking":"","signature":"keep-me"},{"type":"tool_use","id":"t0","name":"x","input":{}}]`)
	resp, err := Call(context.Background(), p, Request{
		System: "sys",
		Messages: []Message{
			{Role: "user", Text: "go"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "t0", Name: "x"}}, Raw: raw, RawProfile: "a"},
			{Role: "user", ToolResults: []ToolResult{{CallID: "t0", Content: "r"}}},
		},
		Tools: []Tool{{Name: "step_done", Schema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "ok" || len(resp.ToolCalls) != 1 || resp.Usage.CacheRead != 90 || resp.Usage.CacheWrite != 3 {
		t.Fatalf("resp %+v", resp)
	}
	msgs := body["messages"].([]any)
	asst := msgs[1].(map[string]any)["content"].([]any)
	if asst[0].(map[string]any)["signature"] != "keep-me" {
		t.Fatalf("raw blocks not replayed verbatim: %v", asst)
	}
	if _, ok := body["system"].([]any); !ok {
		t.Fatal("system should carry cache_control blocks when prompt cache is on")
	}
	last := msgs[2].(map[string]any)["content"].([]any)
	if last[len(last)-1].(map[string]any)["cache_control"] == nil {
		t.Fatal("expected a cache breakpoint on the newest user turn")
	}
	// a different profile must get the normalized form, not the raw blocks
	p2 := *p
	p2.ID = "other"
	_, _ = Call(context.Background(), &p2, Request{Messages: []Message{
		{Role: "user", Text: "go"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t0", Name: "x"}}, Raw: raw, RawProfile: "a"},
		{Role: "user", ToolResults: []ToolResult{{CallID: "t0", Content: "r"}}},
	}})
	asst = body["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if asst[0].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("expected normalized blocks for another profile, got %v", asst)
	}
}

func TestPoolFailsOverAndTripsBreaker(t *testing.T) {
	var badHits, goodHits int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&badHits, 1)
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"invalid key"}}`)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&goodHits, 1)
		io.WriteString(w, `{"choices":[{"message":{"content":"pong"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer good.Close()
	chain := []*store.Profile{
		{ID: "bad", Name: "bad", Format: "openai", BaseURL: bad.URL, Model: "m", Enabled: true},
		{ID: "good", Name: "good", Format: "openai", BaseURL: good.URL, Model: "m", Enabled: true},
	}
	pool := NewPool()
	for i := 0; i < 2; i++ {
		resp, err := pool.Do(context.Background(), chain, Request{Messages: []Message{{Role: "user", Text: "ping"}}}, false, nil)
		if err != nil || resp.Text != "pong" || resp.Profile.ID != "good" {
			t.Fatalf("resp=%+v err=%v", resp, err)
		}
	}
	if badHits != 1 {
		t.Fatalf("bad profile should be skipped once its breaker is open, hits=%d", badHits)
	}
	// vision requests skip non-vision profiles
	if _, err := pool.Do(context.Background(), chain, Request{}, true, nil); err == nil || !strings.Contains(err.Error(), "vision") {
		t.Fatalf("expected vision error, got %v", err)
	}
}

func TestJSONModeRoundTrip(t *testing.T) {
	var sys string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
			Tools any `json:"tools"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		if body.Tools != nil {
			t.Error("json mode must not send native tools")
		}
		sys, _ = body.Messages[0].Content.(string)
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"tool\":\"step_done\",\"args\":{\"summary\":\"done\"}}"}}]}`)
	}))
	defer srv.Close()
	p := &store.Profile{ID: "j", Format: "openai", BaseURL: srv.URL, Model: "m", ToolMode: "json", Enabled: true}
	resp, err := Call(context.Background(), p, Request{System: "base", Messages: []Message{{Role: "user", Text: "x"}},
		Tools: []Tool{{Name: "step_done", Description: "finish", Schema: map[string]any{"type": "object"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "step_done" || !strings.Contains(sys, "Tool protocol") {
		t.Fatalf("resp=%+v sys=%q", resp, sys)
	}
}
