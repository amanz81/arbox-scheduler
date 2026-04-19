package main

// Per-token rate limiter for the HTTP API.
//
// Limit: 60 requests / minute, burst 30. Implemented with one
// golang.org/x/time/rate.Limiter per bearer token, kept in a map guarded
// by a mutex. The bucket is keyed by the *raw* bearer token (which is fine
// because tokens are short-lived secrets that already gate access).
//
// On every response (allowed or rejected) we set:
//   X-RateLimit-Limit:     60
//   X-RateLimit-Remaining: <integer tokens left in the bucket>
//   X-RateLimit-Reset:     <unix seconds when the bucket is full again>
// On 429 we additionally set Retry-After: <seconds>.

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	rateLimitPerMinute = 60
	rateLimitBurst     = 30
)

type apiRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	rate     rate.Limit
	burst    int
}

func newAPIRateLimiter() *apiRateLimiter {
	return &apiRateLimiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     rate.Every(time.Second), // ≈ 60/min
		burst:    rateLimitBurst,
	}
}

func (rl *apiRateLimiter) get(token string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	lim, ok := rl.limiters[token]
	if !ok {
		lim = rate.NewLimiter(rl.rate, rl.burst)
		rl.limiters[token] = lim
	}
	return lim
}

// middleware enforces the bucket. Must be wrapped *inside* auth middleware so
// the bearer is already known. If no bearer is set (e.g. unauth'd /healthz),
// the request bypasses rate limiting entirely.
func (rl *apiRateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := tokenFromCtx(r.Context())
		if tok == "" {
			next.ServeHTTP(w, r)
			return
		}
		lim := rl.get(tok)

		// Header values must be set before WriteHeader, so compute them
		// before deciding allow vs deny.
		w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", rateLimitPerMinute))

		if !lim.Allow() {
			res := lim.Reserve()
			d := res.Delay()
			res.Cancel()
			retry := int(d.Seconds())
			if retry < 1 {
				retry = 1
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retry))
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(d).Unix()))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error": "rate limit exceeded",
				"code":  "rate_limited",
			})
			return
		}

		rem := int(lim.Tokens())
		if rem < 0 {
			rem = 0
		}
		if rem > rl.burst {
			rem = rl.burst
		}
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", rem))
		w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(time.Second).Unix()))
		next.ServeHTTP(w, r)
	})
}
