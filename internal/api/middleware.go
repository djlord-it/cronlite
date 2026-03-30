package api

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/djlord-it/easy-cron/internal/domain"
	"github.com/djlord-it/easy-cron/internal/service"
)

// AuthMiddleware returns an http.Handler that requires a valid API key
// in the Authorization header (Bearer token) for all endpoints except /health.
// If apiKey is empty, authentication is disabled (pass-through).
//
// Deprecated: Use MultiKeyAuthMiddleware for new code. This remains for
// backward compatibility during transition.
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

// MultiKeyAuthMiddleware performs multi-key authentication with SHA-256 lookup.
// It also supports the legacy single API_KEY env var as a fallback.
//
// - Exempt paths: /health, /mcp (SSE transport, future use)
// - If a matching key is found via the repository, the key's namespace is
//   injected into the request context.
// - If the legacy fallbackKey matches, namespace "default" is used.
// - If neither matches, 401 is returned.
// - last_used_at is tracked with in-process debounce (dirty map + background flush).
func MultiKeyAuthMiddleware(
	keyRepo domain.APIKeyRepository,
	fallbackKey string,
	next http.Handler,
) http.Handler {
	tracker := newLastUsedTracker(keyRepo)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Exempt paths
		if r.URL.Path == "/health" || strings.HasPrefix(r.URL.Path, "/mcp") {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")

		// Try multi-key lookup via SHA-256 hash.
		tokenHash := service.HashToken(token)
		key, err := keyRepo.GetKeyByTokenHash(r.Context(), tokenHash)
		if err == nil && key.Enabled {
			// Found a valid key.
			tracker.markUsed(key.ID)
			ctx := domain.NamespaceToContext(r.Context(), key.Namespace)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Fallback: legacy single API_KEY support.
		if fallbackKey != "" && subtle.ConstantTimeCompare([]byte(token), []byte(fallbackKey)) == 1 {
			ctx := domain.NamespaceToContext(r.Context(), domain.Namespace("default"))
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		writeError(w, http.StatusUnauthorized, "unauthorized")
	})
}

// lastUsedTracker debounces last_used_at updates for API keys.
// Dirty keys are flushed to the repository every 60 seconds.
type lastUsedTracker struct {
	mu      sync.Mutex
	dirty   map[uuid.UUID]struct{}
	repo    domain.APIKeyRepository
	stopCh  chan struct{}
	stopped bool
}

func newLastUsedTracker(repo domain.APIKeyRepository) *lastUsedTracker {
	t := &lastUsedTracker{
		dirty:  make(map[uuid.UUID]struct{}),
		repo:   repo,
		stopCh: make(chan struct{}),
	}
	go t.flushLoop()
	return t
}

func (t *lastUsedTracker) markUsed(id uuid.UUID) {
	t.mu.Lock()
	t.dirty[id] = struct{}{}
	t.mu.Unlock()
}

func (t *lastUsedTracker) flushLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.flush()
		case <-t.stopCh:
			t.flush()
			return
		}
	}
}

func (t *lastUsedTracker) flush() {
	t.mu.Lock()
	if len(t.dirty) == 0 {
		t.mu.Unlock()
		return
	}
	ids := make([]uuid.UUID, 0, len(t.dirty))
	for id := range t.dirty {
		ids = append(ids, id)
	}
	t.dirty = make(map[uuid.UUID]struct{})
	t.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := t.repo.UpdateLastUsedAt(ctx, ids); err != nil {
		log.Printf("api: failed to flush last_used_at for %d keys: %v", len(ids), err)
	}
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
