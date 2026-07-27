package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
)

func TestAuthMiddleware_ValidKey(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := AuthMiddleware("test-key", inner)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAuthMiddleware_MissingKey(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := AuthMiddleware("test-key", inner)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_WrongKey(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := AuthMiddleware("test-key", inner)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_HealthBypass(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := AuthMiddleware("test-key", inner)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (health bypass), got %d", w.Code)
	}
}

func TestAuthMiddleware_MetricsBypass(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := AuthMiddleware("test-key", inner)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for /metrics without auth, got %d", w.Code)
	}
}

func TestAuthMiddleware_Disabled(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := AuthMiddleware("", inner)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (auth disabled), got %d", w.Code)
	}
}

func TestAuthMiddleware_ConstantTimeComparison(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := AuthMiddleware("abcdef123456", inner)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("Authorization", "Bearer abcdef")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for partial key, got %d", w.Code)
	}
}

func TestRateLimitMiddleware_AllowsUnderLimit(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RateLimitMiddleware(10, inner) // 10 req/sec

	req := httptest.NewRequest(http.MethodPost, "/jobs", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestRateLimitMiddleware_BlocksOverLimit(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RateLimitMiddleware(2, inner) // 2 req/sec

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/jobs", nil)
		req.RemoteAddr = "1.2.3.4:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}

	// One more request should be rate limited
	req := httptest.NewRequest(http.MethodPost, "/jobs", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", w.Code)
	}
}

func TestRateLimitMiddleware_HealthBypass(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RateLimitMiddleware(1, inner)

	// Health should never be rate limited
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.RemoteAddr = "1.2.3.4:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected 200 for /health, got %d", i, w.Code)
		}
	}
}

func TestRateLimitMiddleware_PerIPIsolation(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RateLimitMiddleware(2, inner)

	// Exhaust limit for IP 1
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/jobs", nil)
		req.RemoteAddr = "1.1.1.1:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}

	// IP 2 should still work
	req := httptest.NewRequest(http.MethodPost, "/jobs", nil)
	req.RemoteAddr = "2.2.2.2:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for different IP, got %d", w.Code)
	}
}

func TestIPRateLimiterEvictsStaleClients(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	limiter := newIPRateLimiterWithClock(2, func() time.Time { return now })
	if !limiter.allow("stale-1") || !limiter.allow("stale-2") {
		t.Fatal("initial clients were rejected")
	}

	now = now.Add(ipRateLimiterClientTTL + ipRateLimiterCleanupInterval)

	if !limiter.allow("fresh") {
		t.Fatal("fresh client was rejected after stale clients should have been evicted")
	}
	if _, exists := limiter.clients["stale-1"]; exists {
		t.Fatal("stale client was not evicted")
	}
	if _, exists := limiter.clients["stale-2"]; exists {
		t.Fatal("stale client was not evicted")
	}
	if _, exists := limiter.clients["fresh"]; !exists {
		t.Fatal("fresh client was not recorded")
	}
}

func TestIPRateLimiterEnforcesHardClientCap(t *testing.T) {
	limiter := newIPRateLimiter(1)
	const (
		workers          = 100
		clientsPerWorker = 110
	)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for client := 0; client < clientsPerWorker; client++ {
				limiter.allow(fmt.Sprintf("client-%d-%d", worker, client))
			}
		}()
	}
	wg.Wait()

	if got := len(limiter.clients); got != ipRateLimiterMaxClients {
		t.Fatalf("tracked clients = %d, want exactly %d", got, ipRateLimiterMaxClients)
	}
}

func TestNamespaceRateLimit_AllowsUnderLimit(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := NamespaceRateLimitMiddleware(10, inner)

	ctx := domain.NamespaceToContext(context.Background(), domain.Namespace("ns1"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestNamespaceRateLimit_BlocksOverLimit(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := NamespaceRateLimitMiddleware(2, inner)

	ctx := domain.NamespaceToContext(context.Background(), domain.Namespace("ns1"))

	// Exhaust the limit
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/jobs", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}

	// Next request should be blocked
	req := httptest.NewRequest(http.MethodPost, "/jobs", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", w.Code)
	}
}

func TestNamespaceRateLimit_CrossNamespaceIsolation(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := NamespaceRateLimitMiddleware(2, inner)

	ctxA := domain.NamespaceToContext(context.Background(), domain.Namespace("ns-a"))

	// Exhaust limit for namespace A
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/jobs", nil).WithContext(ctxA)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}

	// Namespace B should still work
	ctxB := domain.NamespaceToContext(context.Background(), domain.Namespace("ns-b"))
	req := httptest.NewRequest(http.MethodPost, "/jobs", nil).WithContext(ctxB)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for different namespace, got %d", w.Code)
	}
}

func TestNamespaceRateLimit_NoNamespacePassThrough(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := NamespaceRateLimitMiddleware(1, inner)

	// Request without namespace context — should pass through
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected 200 for no-namespace pass-through, got %d", i, w.Code)
		}
	}
}

func TestNamespaceRateLimit_HealthBypass(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := NamespaceRateLimitMiddleware(1, inner)

	ctx := domain.NamespaceToContext(context.Background(), domain.Namespace("ns1"))

	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/health", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected 200 for /health bypass, got %d", i, w.Code)
		}
	}
}
