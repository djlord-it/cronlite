// Package testutil provides shared test helpers for cronlite.
package testutil

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// FakeClock provides deterministic time for testing.
type FakeClock struct {
	mu      sync.Mutex
	current time.Time
}

// NewFakeClock creates a FakeClock set to the given time.
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{current: t}
}

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(d)
}

// TestContext returns a context with a 5-second timeout.
// The context is cancelled when the test completes.
func TestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// MustParseUUID parses a UUID string and panics on error.
// Only for use in tests.
func MustParseUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		panic("testutil.MustParseUUID: " + err.Error())
	}
	return id
}
