package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/khangphantz/aatmt/internal/store"
)

var httpClient = &http.Client{Transport: &http.Transport{
	Proxy:               http.ProxyFromEnvironment,
	MaxIdleConnsPerHost: 8,
	IdleConnTimeout:     90 * time.Second,
}}

// Call sends one request to one profile.
func Call(ctx context.Context, p *store.Profile, req Request) (*Response, error) {
	if p.ToolMode == "json" && len(req.Tools) > 0 {
		return callJSONMode(ctx, p, req)
	}
	return callNative(ctx, p, req)
}

func callNative(ctx context.Context, p *store.Profile, req Request) (*Response, error) {
	start := time.Now()
	var (
		resp *Response
		err  error
	)
	switch p.Format {
	case "anthropic":
		resp, err = callAnthropic(ctx, p, req)
	default:
		resp, err = callOpenAI(ctx, p, req)
	}
	if err != nil {
		return nil, err
	}
	resp.Profile = p
	resp.Latency = time.Since(start)
	record(p.ID, requestChars(req), resp.Usage)
	resp.Usage.Calls = 1
	resp.Usage.Cost = cost(p, resp.Usage)
	return resp, nil
}

func maxTokens(p *store.Profile, req Request) int {
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	if p.MaxTokens > 0 {
		return p.MaxTokens
	}
	mt, _ := defaults()
	return mt
}

func timeout(p *store.Profile) time.Duration {
	if p.TimeoutSec > 0 {
		return time.Duration(p.TimeoutSec) * time.Second
	}
	_, d := defaults()
	return d
}

func postJSON(ctx context.Context, p *store.Profile, url string, body any, defaultAuthHeader, defaultPrefix string, extra map[string]string) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout(p))
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	setAuth(hr, p, defaultAuthHeader, defaultPrefix)
	for k, v := range extra {
		hr.Header.Set(k, v)
	}
	for k, v := range p.Headers {
		hr.Header.Set(k, v)
	}
	return do(hr)
}

func getJSON(ctx context.Context, p *store.Profile, url, defaultAuthHeader, defaultPrefix string, extra map[string]string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout(p))
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	setAuth(hr, p, defaultAuthHeader, defaultPrefix)
	for k, v := range extra {
		hr.Header.Set(k, v)
	}
	for k, v := range p.Headers {
		hr.Header.Set(k, v)
	}
	return do(hr)
}

func setAuth(hr *http.Request, p *store.Profile, defHeader, defPrefix string) {
	key := store.ResolveKey(p.APIKey)
	if key == "" {
		return
	}
	h, prefix := defHeader, defPrefix
	if p.AuthHeader != "" {
		h = p.AuthHeader
		prefix = ""
		if strings.EqualFold(h, "authorization") {
			prefix = "Bearer "
		}
	}
	if p.AuthPrefix != "" {
		prefix = p.AuthPrefix
		if prefix == "none" {
			prefix = ""
		}
	}
	hr.Header.Set(h, prefix+key)
}

func do(hr *http.Request) ([]byte, error) {
	res, err := httpClient.Do(hr)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 != 2 {
		he := &HTTPError{Status: res.StatusCode, Body: string(body)}
		if ra := res.Header.Get("Retry-After"); ra != "" {
			if s, err := strconv.Atoi(ra); err == nil {
				he.RetryAfter = time.Duration(s) * time.Second
			}
		}
		return nil, he
	}
	return body, nil
}

// joinURL appends suffix unless base already ends with a full endpoint path.
func joinURL(base, suffix string, fullMarkers ...string) string {
	for _, m := range fullMarkers {
		if strings.Contains(base, m) {
			return base
		}
	}
	return strings.TrimRight(base, "/") + suffix
}

// ModelInfo is what an endpoint reports about a model. Context and output
// limits are filled only when the provider's /models API returns them.
type ModelInfo struct {
	ID            string `json:"id"`
	ContextWindow int    `json:"context_window,omitempty"`
	MaxOutput     int    `json:"max_output,omitempty"`
	Vision        *bool  `json:"vision,omitempty"`
}

// ListModels returns the models the endpoint advertises (best effort).
func ListModels(ctx context.Context, p *store.Profile) ([]ModelInfo, error) {
	var (
		b   []byte
		err error
	)
	if p.Format == "anthropic" {
		b, err = getJSON(ctx, p, anthropicBase(p)+"/v1/models?limit=1000", "x-api-key", "", map[string]string{"anthropic-version": anthropicVersion})
	} else {
		b, err = getJSON(ctx, p, openaiBase(p)+"/models", "Authorization", "Bearer ", nil)
	}
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			ID             string `json:"id"`
			MaxInputTokens int    `json:"max_input_tokens"` // Anthropic
			MaxTokens      int    `json:"max_tokens"`       // Anthropic
			ContextLength  int    `json:"context_length"`   // OpenRouter / vLLM / LiteLLM
			MaxModelLen    int    `json:"max_model_len"`    // vLLM
			TopProvider    struct {
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"` // OpenRouter
			Capabilities struct {
				ImageInput struct {
					Supported *bool `json:"supported"`
				} `json:"image_input"`
			} `json:"capabilities"` // Anthropic
			Architecture struct {
				InputModalities []string `json:"input_modalities"`
			} `json:"architecture"` // OpenRouter
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
		} `json:"models"` // Ollama native shape
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	var models []ModelInfo
	for _, m := range out.Data {
		mi := ModelInfo{ID: m.ID, ContextWindow: max(m.MaxInputTokens, m.ContextLength, m.MaxModelLen),
			MaxOutput: max(m.MaxTokens, m.TopProvider.MaxCompletionTokens), Vision: m.Capabilities.ImageInput.Supported}
		if len(m.Architecture.InputModalities) > 0 {
			v := false
			for _, x := range m.Architecture.InputModalities {
				v = v || x == "image"
			}
			mi.Vision = &v
		}
		models = append(models, mi)
	}
	for _, m := range out.Models {
		models = append(models, ModelInfo{ID: m.Name})
	}
	if len(models) == 0 {
		return nil, errors.New("endpoint returned no models")
	}
	return models, nil
}

// Test sends a tiny request to verify credentials, model and (optionally) tool support.
func Test(ctx context.Context, p *store.Profile) (*Response, error) {
	req := Request{
		System:   "You are a connectivity check.",
		Messages: []Message{{Role: "user", Text: "Reply with the single word: pong"}},
	}
	return Call(ctx, p, req)
}
