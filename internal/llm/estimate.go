package llm

import (
	"encoding/json"
	"math"
	"sync"
	"time"

	"github.com/khangphantz/aatmt/internal/store"
)

// ---------------------------------------------------------------- runtime defaults

var (
	defMu        sync.RWMutex
	defMaxTokens int
	defTimeout   time.Duration
)

// SetDefaults sets the values used when a provider leaves max tokens / timeout
// empty. They come from Settings → Limits (no built-in constants).
func SetDefaults(l store.Limits) {
	defMu.Lock()
	defer defMu.Unlock()
	defMaxTokens = l.DefaultMaxTokens
	defTimeout = time.Duration(l.DefaultTimeoutSec) * time.Second
}

func defaults() (int, time.Duration) {
	defMu.RLock()
	defer defMu.RUnlock()
	if defMaxTokens == 0 || defTimeout == 0 { // not configured yet: use the store defaults
		d := store.DefaultLimits()
		return d.DefaultMaxTokens, time.Duration(d.DefaultTimeoutSec) * time.Second
	}
	return defMaxTokens, defTimeout
}

// ---------------------------------------------------------------- token estimates

// Characters per token are measured, not assumed: every successful call
// records request characters against the input tokens the provider billed,
// and estimates for that profile use the measured ratio. Until a profile has
// been measured, the commonly published ~4 chars/token for English text and
// code is used (and labelled as such in the UI).
const fallbackCharsPerToken = 4.0

var (
	ratioMu sync.Mutex
	ratios  = map[string]ratio{}
)

type ratio struct {
	chars, tokens float64
}

// Measured returns the measured characters-per-token of a profile (0 = not measured yet).
func Measured(profileID string) float64 {
	ratioMu.Lock()
	defer ratioMu.Unlock()
	r := ratios[profileID]
	if r.tokens == 0 {
		return 0
	}
	return r.chars / r.tokens
}

func record(profileID string, chars int, u store.Usage) {
	tokens := u.Input + u.CacheRead + u.CacheWrite
	if tokens <= 0 || chars <= 0 {
		return
	}
	ratioMu.Lock()
	r := ratios[profileID]
	r.chars += float64(chars)
	r.tokens += float64(tokens)
	ratios[profileID] = r
	ratioMu.Unlock()
}

func charsPerToken(profileID string) float64 {
	if m := Measured(profileID); m > 0 {
		return m
	}
	return fallbackCharsPerToken
}

// EstimateTokens estimates the tokens of a text for a profile ("" = unmeasured).
func EstimateTokensFor(profileID, s string) int {
	return int(math.Ceil(float64(len([]rune(s))) / charsPerToken(profileID)))
}

// EstimateTokens estimates tokens without a profile.
func EstimateTokens(s string) int { return EstimateTokensFor("", s) }

// ImageTokens follows the providers' published image-token formulas, using the
// real pixel size of the (already downscaled) screenshot.
//   - Anthropic: tokens ≈ width × height / 750
//   - OpenAI-compatible (high detail): fit in 2048², shortest side to 768,
//     then 85 + 170 per 512-px tile
func ImageTokens(format string, w, h int) int {
	if w <= 0 || h <= 0 {
		return 0
	}
	if format == "anthropic" {
		return int(math.Ceil(float64(w*h) / 750))
	}
	fw, fh := float64(w), float64(h)
	if s := math.Min(2048/fw, 2048/fh); s < 1 {
		fw, fh = fw*s, fh*s
	}
	if s := 768 / math.Min(fw, fh); s < 1 {
		fw, fh = fw*s, fh*s
	}
	tiles := math.Ceil(fw/512) * math.Ceil(fh/512)
	return int(85 + 170*tiles)
}

// requestChars is the size of what is actually sent (system, messages
// without image bytes, tool schemas) — the basis of every estimate.
func requestChars(r Request) int {
	n := len([]rune(r.System))
	type lite struct {
		Role        string
		Text        string
		ToolCalls   []ToolCall
		ToolResults []ToolResult
	}
	for _, m := range r.Messages {
		b, _ := json.Marshal(lite{m.Role, m.Text, m.ToolCalls, m.ToolResults})
		n += len([]rune(string(b)))
	}
	for _, t := range r.Tools {
		b, _ := json.Marshal(t)
		n += len([]rune(string(b)))
	}
	return n
}

// EstimateRequest estimates the input tokens of a request for a profile.
func EstimateRequest(p *store.Profile, r Request) int {
	id, format := "", ""
	if p != nil {
		id, format = p.ID, p.Format
	}
	t := int(math.Ceil(float64(requestChars(r)) / charsPerToken(id)))
	for _, m := range r.Messages {
		for _, im := range m.Images {
			t += ImageTokens(format, im.Width, im.Height)
		}
	}
	return t
}

func cost(p *store.Profile, u store.Usage) float64 {
	if p == nil {
		return 0
	}
	cr, cw := p.PriceCacheRead, p.PriceCacheWrite
	if cr == 0 {
		cr = p.PriceIn
	}
	if cw == 0 {
		cw = p.PriceIn
	}
	return (float64(u.Input)*p.PriceIn + float64(u.CacheRead)*cr + float64(u.CacheWrite)*cw + float64(u.Output)*p.PriceOut) / 1e6
}
