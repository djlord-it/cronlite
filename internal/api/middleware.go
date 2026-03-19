package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"
	"time"
)

// AuthMiddleware returns an http.Handler that requires a valid API key
// in the Authorization header (Bearer token) for all endpoints except /health.
// If apiKey is empty, authentication is disabled (pass-through).
func AuthMiddleware(apiKey string, next http.Handler) http.Handler {
	if apiKey == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow health checks without auth (used by load balancers / k8s probes)
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if auth == token || subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RateLimitMiddleware limits requests per IP using a simple token bucket.
// ratePerSecond is the sustained request rate; burst allows short spikes.
// Health checks (/health) are exempt from rate limiting.
func RateLimitMiddleware(ratePerSecond int, next http.Handler) http.Handler {
	limiter := newIPRateLimiter(ratePerSecond)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		ip := extractIP(r)
		if !limiter.allow(ip) {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractIP gets the client IP from RemoteAddr, stripping the port.
func extractIP(r *http.Request) string {
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return ip
}

// ipRateLimiter tracks request counts per IP using a simple token bucket.
type ipRateLimiter struct {
	mu      sync.Mutex
	rate    int
	clients map[string]*clientBucket
}

type clientBucket struct {
	tokens   int
	lastSeen time.Time
}

func newIPRateLimiter(ratePerSecond int) *ipRateLimiter {
	return &ipRateLimiter{
		rate:    ratePerSecond,
		clients: make(map[string]*clientBucket),
	}
}

func (l *ipRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, exists := l.clients[ip]
	if !exists {
		l.clients[ip] = &clientBucket{tokens: l.rate - 1, lastSeen: now}
		return true
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.tokens += int(elapsed * float64(l.rate))
	if b.tokens > l.rate {
		b.tokens = l.rate
	}
	b.lastSeen = now

	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}
