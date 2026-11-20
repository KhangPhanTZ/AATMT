package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khangphantz/aatmt/internal/store"
)

func TestImageTokensFollowProviderFormulas(t *testing.T) {
	// Anthropic: w*h/750
	if got := ImageTokens("anthropic", 1500, 750); got != 1500 {
		t.Fatalf("anthropic: %d", got)
	}
	// OpenAI: 1024x1024 → shortest side 768 → 768x768 → 4 tiles → 85+170*4
	if got := ImageTokens("openai", 1024, 1024); got != 765 {
		t.Fatalf("openai: %d", got)
	}
	if ImageTokens("openai", 0, 10) != 0 {
		t.Fatal("unknown size must not invent tokens")
	}
}

func TestCharsPerTokenIsMeasured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		// report a ratio of exactly 2 chars/token for whatever was sent
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":`+itoa(requestChars(lastReq)/2)+`,"completion_tokens":1}}`)
	}))
	defer srv.Close()
	p := &store.Profile{ID: "measure", Format: "openai", BaseURL: srv.URL, Model: "m", Enabled: true}
	lastReq = Request{System: "system prompt here", Messages: []Message{{Role: "user", Text: "some user text to count"}}}
	if Measured(p.ID) != 0 {
		t.Fatal("must start unmeasured")
	}
	if _, err := Call(context.Background(), p, lastReq); err != nil {
		t.Fatal(err)
	}
	if m := Measured(p.ID); m < 1.9 || m > 2.1 {
		t.Fatalf("measured chars/token = %v, want ≈2", m)
	}
	if got, want := EstimateRequest(p, lastReq), requestChars(lastReq)/2; got < want-1 || got > want+1 {
		t.Fatalf("estimate %d, want ≈%d", got, want)
	}
}

var lastReq Request

func itoa(n int) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestCostUsesConfiguredPricesOnly(t *testing.T) {
	p := &store.Profile{PriceIn: 2, PriceOut: 10}
	u := store.Usage{Input: 1e6, Output: 1e6, CacheRead: 1e6}
	if got := cost(p, u); got != 2+10+2 { // cache read costed at PriceIn when no cache price is set
		t.Fatalf("cost %v", got)
	}
	p.PriceCacheRead = 0.2
	if got := cost(p, u); got != 2+10+0.2 {
		t.Fatalf("cost with cache price %v", got)
	}
}
