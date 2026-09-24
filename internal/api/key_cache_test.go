package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

func TestAPIKeyCache_ExpiresEnabledKey(t *testing.T) {
	var lookups int
	enabled := true
	repo := &mockAPIKeyRepo{getKeyByHashFn: func(_ context.Context, _ string) (domain.APIKey, error) {
		lookups++
		return domain.APIKey{ID: uuid.New(), Namespace: "tenant", Enabled: enabled}, nil
	}}
	cache := newAPIKeyCache(repo)
	now := time.Now()
	cache.now = func() time.Time { return now }
	first, err := cache.get(context.Background(), "hash")
	if err != nil || !first.Enabled {
		t.Fatalf("first lookup: key=%+v err=%v", first, err)
	}
	enabled = false
	now = now.Add(apiKeyCacheTTL)
	second, err := cache.get(context.Background(), "hash")
	if err != nil || second.Enabled || lookups != 2 {
		t.Fatalf("expired key was reused: key=%+v err=%v lookups=%d", second, err, lookups)
	}
}

func TestAPIKeyCache_ConcurrentPollsShareLookup(t *testing.T) {
	var lookups atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	repo := &mockAPIKeyRepo{getKeyByHashFn: func(_ context.Context, _ string) (domain.APIKey, error) {
		lookups.Add(1)
		close(started)
		<-release
		return domain.APIKey{ID: uuid.New(), Namespace: "tenant", Enabled: true}, nil
	}}
	cache := newAPIKeyCache(repo)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key, err := cache.get(context.Background(), "hash")
			if err != nil || !key.Enabled {
				t.Errorf("lookup: key=%+v err=%v", key, err)
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()
	if got := lookups.Load(); got != 1 {
		t.Fatalf("20 concurrent polls performed %d key lookups, want 1", got)
	}
}
