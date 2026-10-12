package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/djlord-it/cronlite/internal/domain"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type observedWaitContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func TestAPIKeyCacheCancelledOwnerDoesNotRejectLiveWaiter(t *testing.T) {
	firstCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, cancelled, publish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	repo := &mockAPIKeyRepo{getKeyByHashFn: func(ctx context.Context, _ string) (domain.APIKey, error) {
		if reads.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			close(cancelled)
			<-publish
			return domain.APIKey{}, ctx.Err()
		}
		return domain.APIKey{Namespace: "tenant", Enabled: true}, nil
	}}
	cache := newAPIKeyCache(repo)
	finished := make(chan struct{})
	go func() { defer close(finished); _, _ = cache.get(firstCtx, "hash") }()
	<-started
	cancel()
	<-cancelled
	waiting := make(chan struct{})
	go func() { <-waiting; close(publish) }()
	ctx := &observedWaitContext{Context: context.Background(), observed: waiting}
	key, err := cache.get(ctx, "hash")
	<-finished
	if err != nil || !key.Enabled || reads.Load() != 2 {
		t.Fatalf("live waiter rejected after another caller cancelled: enabled=%v reads=%d err=%v", key.Enabled, reads.Load(), err)
	}
}

func TestAPIKeyCacheRepositoryDeadlineDoesNotRetryWaiters(t *testing.T) {
	started, publish := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	repo := &mockAPIKeyRepo{getKeyByHashFn: func(context.Context, string) (domain.APIKey, error) {
		if reads.Add(1) == 1 {
			close(started)
		}
		<-publish
		return domain.APIKey{}, context.DeadlineExceeded
	}}
	cache := newAPIKeyCache(repo)
	finished := make(chan struct{})
	go func() { defer close(finished); _, _ = cache.get(context.Background(), "hash") }()
	<-started
	waiting := make(chan struct{})
	go func() { <-waiting; close(publish) }()
	ctx := &observedWaitContext{Context: context.Background(), observed: waiting}
	_, err := cache.get(ctx, "hash")
	<-finished
	if !errors.Is(err, context.DeadlineExceeded) || reads.Load() != 1 {
		t.Fatalf("repository timeout retried: reads=%d err=%v", reads.Load(), err)
	}
}

func TestRESTRejectsTimeoutOutsideDocumentedRange(t *testing.T) {
	for _, seconds := range []string{"-1", "0", "61", "9223372037"} {
		t.Run(seconds, func(t *testing.T) {
			srv := newTestServer(nil, nil, nil, nil, nil, nil)
			router := Handler(NewStrictHandler(srv, nil))
			for _, method := range []string{"POST", "PATCH"} {
				path := "/jobs"
				if method == "PATCH" {
					path += "/12345678-1234-1234-1234-123456789012"
				}
				body := fmt.Sprintf(`{"name":"test","cron_expression":"* * * * *","timezone":"UTC","webhook_url":"https://example.com","webhook_timeout_seconds":%s}`, seconds)
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r = r.WithContext(domain.NamespaceToContext(r.Context(), "test"))
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				if w.Code != 422 {
					t.Fatalf("%s timeout=%s returned %d: %s", method, seconds, w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestAPIKeyCacheSlowLookupDoesNotExtendFreshness(t *testing.T) {
	now := time.Now()
	reads := 0
	repo := &mockAPIKeyRepo{getKeyByHashFn: func(context.Context, string) (domain.APIKey, error) {
		reads++
		if reads == 1 {
			now = now.Add(4 * time.Second)
			return domain.APIKey{Enabled: true}, nil
		}
		return domain.APIKey{Enabled: false}, nil
	}}
	cache := newAPIKeyCache(repo)
	cache.now = func() time.Time { return now }
	if _, err := cache.get(context.Background(), "hash"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	key, err := cache.get(context.Background(), "hash")
	if err != nil || key.Enabled || reads != 2 {
		t.Fatalf("slow lookup extended revoked-key freshness: enabled=%v reads=%d err=%v", key.Enabled, reads, err)
	}
}

func TestIPRateLimiterPreservesFractionalRefill(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter := newIPRateLimiterWithClock(10, func() time.Time { return now })
	for range 10 {
		if !limiter.allow("client") {
			t.Fatal("initial burst refused")
		}
	}
	admitted := 0
	for range 20 {
		now = now.Add(50 * time.Millisecond)
		if limiter.allow("client") {
			admitted++
		}
	}
	if admitted != 10 {
		t.Fatalf("admitted %d requests over one second, want10", admitted)
	}
}

func TestNamespaceRateLimiterPreservesFractionalRefill(t *testing.T) {
	limiter := newNamespaceRateLimiter(10)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	for range 10 {
		limiter.allow("tenant")
	}
	admitted := 0
	for range 20 {
		now = now.Add(50 * time.Millisecond)
		if limiter.allow("tenant") {
			admitted++
		}
	}
	if admitted != 10 {
		t.Fatalf("admitted %d requests over one second, want10", admitted)
	}
}

func TestNamespaceRateLimiterReclaimsInactiveTenants(t *testing.T) {
	limiter := newNamespaceRateLimiter(10)
	for i := 0; i < 20_000; i++ {
		limiter.namespaces[fmt.Sprint(i)] = &clientBucket{lastSeen: time.Now().Add(-11 * time.Minute)}
	}
	if !limiter.allow("active") {
		t.Fatal("active tenant refused")
	}
	if len(limiter.namespaces) != 1 {
		t.Fatalf("retained %d inactive namespace buckets", len(limiter.namespaces)-1)
	}
}
