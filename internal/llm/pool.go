package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/khangphantz/aatmt/internal/store"
)

// Pool walks an ordered chain of profiles with per-profile rate limiting, retry
// with backoff for transient errors and a circuit breaker, so a quota-exhausted
// or down endpoint is skipped by every task until it cools off.
type Pool struct {
	mu       sync.Mutex
	brk      map[string]*Breaker
	next     map[string]time.Time // earliest time the next request may start (rate limit)
	attempts int                  // tries per profile for transient errors
	cooldown time.Duration        // fixed breaker cooldown (0 = 1/5/30 min ladder)
}

// SetPolicy updates retry attempts and breaker cooldown (safe at any time).
func (p *Pool) SetPolicy(attempts int, cooldown time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if attempts <= 0 {
		attempts = 3
	}
	p.attempts, p.cooldown = attempts, cooldown
}

func (p *Pool) tries() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.attempts <= 0 {
		return 3
	}
	return p.attempts
}

type Breaker struct {
	Fails     int       `json:"fails"`
	Trips     int       `json:"trips"`
	OpenUntil time.Time `json:"open_until"`
	LastError string    `json:"last_error"`
}

func NewPool() *Pool {
	return &Pool{brk: map[string]*Breaker{}, next: map[string]time.Time{}}
}

var cooldowns = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

func (p *Pool) State() map[string]Breaker {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]Breaker{}
	for k, v := range p.brk {
		out[k] = *v
	}
	return out
}

func (p *Pool) Reset(id string) {
	p.mu.Lock()
	delete(p.brk, id)
	p.mu.Unlock()
}

func (p *Pool) open(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.brk[id]
	return b != nil && time.Now().Before(b.OpenUntil)
}

func (p *Pool) pass(id string) {
	p.mu.Lock()
	if b := p.brk[id]; b != nil {
		b.Fails = 0
		b.OpenUntil = time.Time{}
	}
	p.mu.Unlock()
}

func (p *Pool) trip(id string, err error, hard bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.brk[id]
	if b == nil {
		b = &Breaker{}
		p.brk[id] = b
	}
	b.Fails++
	b.LastError = err.Error()
	if hard || b.Fails >= 2 {
		d := cooldowns[min(b.Trips, len(cooldowns)-1)]
		if p.cooldown > 0 {
			d = p.cooldown
		}
		b.Trips++
		b.OpenUntil = time.Now().Add(d)
		b.Fails = 0
	}
}

// wait enforces RatePerMinute as a minimum spacing between requests.
func (p *Pool) wait(ctx context.Context, prof *store.Profile) error {
	if prof.RatePerMinute <= 0 {
		return nil
	}
	gap := time.Minute / time.Duration(prof.RatePerMinute)
	p.mu.Lock()
	now := time.Now()
	at := p.next[prof.ID]
	if at.Before(now) {
		at = now
	}
	p.next[prof.ID] = at.Add(gap)
	p.mu.Unlock()
	if d := time.Until(at); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

type errClass int

const (
	errTransient errClass = iota // retry, then fail over
	errHard                      // bad key / no credit: trip now, fail over
	errRequest                   // this request is bad for this model: fail over, no trip
)

func classify(err error) errClass {
	var he *HTTPError
	if errors.As(err, &he) {
		switch {
		case he.Status == 401 || he.Status == 403 || he.Status == 402 || he.Status == 404:
			return errHard
		case he.Status == 429 || he.Status == 408 || he.Status >= 500:
			low := strings.ToLower(he.Body)
			if he.Status == 429 && (strings.Contains(low, "insufficient_quota") || strings.Contains(low, "credit")) {
				return errHard
			}
			return errTransient
		default:
			return errRequest
		}
	}
	if errors.Is(err, ErrRefusal) {
		return errRequest
	}
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
		return errTransient
	}
	return errRequest
}

// Attempt describes one try, for the activity log.
type Attempt struct {
	Profile *store.Profile
	Err     error
}

// Do sends req to the first healthy profile of chain, failing over on errors.
// When needVision is set, profiles without vision support are skipped.
func (p *Pool) Do(ctx context.Context, chain []*store.Profile, req Request, needVision bool, onFail func(Attempt)) (*Response, error) {
	var lastErr error
	tried := 0
	// Two passes: first only closed breakers; if every profile is cooling off,
	// try them anyway rather than failing the task outright.
	for pass := 0; pass < 2 && tried == 0; pass++ {
		for _, prof := range chain {
			if prof == nil || !prof.Enabled || (needVision && !prof.Vision) {
				continue
			}
			if pass == 0 && p.open(prof.ID) {
				continue
			}
			tried++
			tries := p.tries()
			for attempt := 0; attempt < tries; attempt++ {
				if err := p.wait(ctx, prof); err != nil {
					return nil, err
				}
				resp, err := Call(ctx, prof, req)
				if err == nil {
					p.pass(prof.ID)
					return resp, nil
				}
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				lastErr = fmt.Errorf("%s: %w", prof.Name, err)
				if onFail != nil {
					onFail(Attempt{Profile: prof, Err: err})
				}
				cls := classify(err)
				if cls == errHard {
					p.trip(prof.ID, err, true)
					break
				}
				if cls == errRequest {
					break
				}
				// transient: back off, honour Retry-After (capped)
				d := time.Duration(2+attempt*4) * time.Second
				var he *HTTPError
				if errors.As(err, &he) && he.RetryAfter > 0 {
					d = min(he.RetryAfter, 30*time.Second)
				}
				if attempt == tries-1 {
					p.trip(prof.ID, err, false)
					break
				}
				select {
				case <-time.After(d):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
	}
	if tried == 0 {
		if needVision {
			return nil, errors.New("no enabled profile with vision support in this role's chain")
		}
		return nil, errors.New("no enabled AI profile configured for this role")
	}
	return nil, lastErr
}
