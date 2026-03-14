package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// tokenBucket implements a simple token bucket per key.
type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
	capacity   float64
	rate       float64 // tokens per second
	mu         sync.Mutex
}

func (tb *tokenBucket) allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.tokens = min(tb.capacity, tb.tokens+elapsed*tb.rate)
	tb.lastRefill = now

	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

// RateLimiter provides per-IP token bucket rate limiting.
type RateLimiter struct {
	buckets sync.Map // map[string]*tokenBucket
	rate    float64  // tokens per second
	burst   int      // max tokens (capacity)
}

// NewRateLimiter creates a rate limiter. rate is requests per second, burst is max concurrent burst.
func NewRateLimiter(rate float64, burst int) *RateLimiter {
	if rate <= 0 {
		rate = 10
	}
	if burst <= 0 {
		burst = 20
	}
	return &RateLimiter{
		rate:  rate,
		burst: burst,
	}
}

func (rl *RateLimiter) getBucket(key string) *tokenBucket {
	if v, ok := rl.buckets.Load(key); ok {
		return v.(*tokenBucket)
	}
	tb := &tokenBucket{
		tokens:     float64(rl.burst),
		lastRefill: time.Now(),
		capacity:   float64(rl.burst),
		rate:       rl.rate,
	}
	if v, loaded := rl.buckets.LoadOrStore(key, tb); loaded {
		return v.(*tokenBucket)
	}
	return tb
}

// Middleware returns a middleware that rate limits by client IP.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			// Use first IP in chain (client)
			if first := strings.TrimSpace(strings.Split(forwarded, ",")[0]); first != "" {
				ip = first
			}
		}

		tb := rl.getBucket(ip)
		if !tb.allow() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}

		next.ServeHTTP(w, r)
	})
}
