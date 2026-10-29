package postgres

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/djlord-it/cronlite/internal/domain"
)

func TestGetOrCreateTailnetKey_Integration(t *testing.T) {
	dsn := os.Getenv("CRONLITE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set CRONLITE_TEST_DATABASE_URL to run real Postgres integration tests")
	}
	ctx := context.Background()
	db, cleanup := openIsolatedPostgresSchema(t, dsn)
	defer cleanup()
	store := New(db, 5*time.Second)

	newKey := func(ns, login string) domain.APIKey {
		return domain.APIKey{
			ID: uuid.New(), Namespace: domain.Namespace(ns), TokenHash: uuid.NewString(),
			Label: "tailscale: " + login, Enabled: true, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
			TailnetLogin: login,
		}
	}

	first := newKey("billing", "alice@example.com")
	got, err := store.GetOrCreateTailnetKey(ctx, first)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.ID != first.ID || got.TailnetLogin != "alice@example.com" || got.Namespace != "billing" {
		t.Fatalf("create returned %+v, want the inserted key", got)
	}

	again, err := store.GetOrCreateTailnetKey(ctx, newKey("billing", "alice@example.com"))
	if err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("reuse returned key %s, want %s", again.ID, first.ID)
	}

	other, err := store.GetOrCreateTailnetKey(ctx, newKey("ops", "alice@example.com"))
	if err != nil {
		t.Fatalf("other namespace: %v", err)
	}
	if other.ID == first.ID {
		t.Fatal("another namespace reused the billing key")
	}

	byHash, err := store.GetKeyByTokenHash(ctx, first.TokenHash)
	if err != nil || byHash.TailnetLogin != "alice@example.com" {
		t.Fatalf("GetKeyByTokenHash = %+v, %v; want the tailnet login", byHash, err)
	}

	// Ordinary keys keep a NULL login and never collide with each other.
	for range 2 {
		plain := newKey("billing", "")
		plain.TailnetLogin = ""
		if err := store.InsertAPIKey(ctx, plain); err != nil {
			t.Fatalf("insert ordinary key: %v", err)
		}
	}
	keys, err := store.ListKeys(ctx, "billing", domain.ListParams{Limit: 10})
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("ListKeys returned %d keys, want 3", len(keys))
	}

	if _, err := store.GetOrCreateTailnetKey(ctx, newKey("billing", "")); err == nil {
		t.Fatal("empty login accepted")
	}

	// Concurrent first sign-ins converge on one key.
	var wg sync.WaitGroup
	ids := make([]uuid.UUID, 8)
	errs := make([]error, len(ids))
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, err := store.GetOrCreateTailnetKey(ctx, newKey("race", "bob@example.com"))
			ids[i], errs[i] = k.ID, err
		}()
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("concurrent %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("concurrent sign-ins created keys %s and %s", ids[0], ids[i])
		}
	}
}
