package api

import (
	"context"
	"sync"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
)

const (
	apiKeyCacheTTL        = 5 * time.Second
	apiKeyCacheMaxEntries = 1024
)

type cachedAPIKey struct {
	key     domain.APIKey
	expires time.Time
}

type apiKeyLookup struct {
	done chan struct{}
	key  domain.APIKey
	err  error
}

// apiKeyCache bounds authentication queries during polling. Enabled keys may
// remain usable for at most apiKeyCacheTTL after they are disabled or deleted.
type apiKeyCache struct {
	mu       sync.Mutex
	keys     map[string]cachedAPIKey
	inflight map[string]*apiKeyLookup
	repo     domain.APIKeyRepository
	now      func() time.Time
}

func newAPIKeyCache(repo domain.APIKeyRepository) *apiKeyCache {
	return &apiKeyCache{
		keys:     make(map[string]cachedAPIKey),
		inflight: make(map[string]*apiKeyLookup),
		repo:     repo,
		now:      time.Now,
	}
}

func (c *apiKeyCache) get(ctx context.Context, hash string) (domain.APIKey, error) {
	c.mu.Lock()
	if entry, ok := c.keys[hash]; ok {
		if c.now().Before(entry.expires) {
			c.mu.Unlock()
			return entry.key, nil
		}
		delete(c.keys, hash)
	}
	if lookup, ok := c.inflight[hash]; ok {
		c.mu.Unlock()
		select {
		case <-lookup.done:
			return lookup.key, lookup.err
		case <-ctx.Done():
			return domain.APIKey{}, ctx.Err()
		}
	}
	lookup := &apiKeyLookup{done: make(chan struct{})}
	c.inflight[hash] = lookup
	c.mu.Unlock()

	key, err := c.repo.GetKeyByTokenHash(ctx, hash)
	c.mu.Lock()
	if err == nil && key.Enabled {
		if len(c.keys) >= apiKeyCacheMaxEntries {
			for oldest := range c.keys {
				delete(c.keys, oldest)
				break
			}
		}
		c.keys[hash] = cachedAPIKey{key: key, expires: c.now().Add(apiKeyCacheTTL)}
	}
	lookup.key, lookup.err = key, err
	delete(c.inflight, hash)
	close(lookup.done)
	c.mu.Unlock()
	return key, err
}
