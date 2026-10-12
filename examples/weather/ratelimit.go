package main

import (
	"math"
	"net/http"
	"sync"
	"time"
)

// The default backstop on the priced route. A paid request costs the
// facilitator a verify and a settle, all from this seller's one address, so
// this spends no more than the facilitator's default per-peer limit (10/s,
// burst 20) admits.
const (
	defaultRatePerSecond = 5
	defaultRateBurst     = 10
)

// rateLimit is one token bucket every caller shares: a backstop for the route
// as a whole. Per-client throttling belongs at the proxy in front, the only
// place that knows a client's address.
type rateLimit struct {
	now       func() time.Time
	perSecond float64
	burst     float64

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// newRateLimit returns a full bucket refilled at perSecond up to burst. A
// perSecond that is not a positive finite number, or a burst under one, takes
// the default, so no configuration removes the backstop.
func newRateLimit(perSecond float64, burst int) *rateLimit {
	if !(perSecond > 0) || math.IsInf(perSecond, 1) {
		perSecond = defaultRatePerSecond
	}
	if burst <= 0 {
		burst = defaultRateBurst
	}
	return &rateLimit{
		now:       time.Now,
		perSecond: perSecond,
		burst:     float64(burst),
		tokens:    float64(burst),
		last:      time.Now(),
	}
}

// allow spends a token if a whole one is left.
func (l *rateLimit) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.tokens = min(l.burst, l.tokens+now.Sub(l.last).Seconds()*l.perSecond)
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

// guard answers 429 while the bucket is empty, and hands every other request
// to next.
func (l *rateLimit) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow() {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
