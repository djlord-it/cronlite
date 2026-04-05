package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/djlord-it/easy-cron/internal/domain"
	"github.com/google/uuid"
)

func TestCreateAPIKey_HappyPath(t *testing.T) {
	var storedKey domain.APIKey
	apiKeyRepo := &mockAPIKeyRepo{
		insertAPIKeyFn: func(_ context.Context, key domain.APIKey) error {
			storedKey = key
			return nil
		},
	}
	svc := newTestServiceFull(nil, nil, nil, nil, apiKeyRepo, nil)

	result, err := svc.CreateAPIKey(ctxWithNS("t1"), CreateAPIKeyInput{
		Label: "test-key",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Plaintext should start with "ec_".
	if !strings.HasPrefix(result.PlaintextToken, "ec_") {
		t.Errorf("expected token to start with 'ec_', got %q", result.PlaintextToken)
	}

	// Token should be 67 chars: "ec_" (3) + 64 hex chars.
	if len(result.PlaintextToken) != 67 {
		t.Errorf("expected token length 67, got %d", len(result.PlaintextToken))
	}

	// The stored hash should match HashToken(plaintext).
	expectedHash := HashToken(result.PlaintextToken)
	if storedKey.TokenHash != expectedHash {
		t.Error("stored hash does not match HashToken(plaintext)")
	}

	// Plaintext should NOT equal the hash.
	if result.PlaintextToken == storedKey.TokenHash {
		t.Error("plaintext token should not equal the stored hash")
	}

	if result.Key.Label != "test-key" {
		t.Errorf("expected label 'test-key', got %q", result.Key.Label)
	}
	if result.Key.Namespace != "t1" {
		t.Errorf("expected namespace 't1', got %q", result.Key.Namespace)
	}
	if !result.Key.Enabled {
		t.Error("expected key to be enabled")
	}
}

func TestCreateAPIKey_NoNamespace(t *testing.T) {
	svc := newTestServiceFull(nil, nil, nil, nil, &mockAPIKeyRepo{}, nil)

	_, err := svc.CreateAPIKey(context.Background(), CreateAPIKeyInput{
		Label: "test-key",
	})

	if !errors.Is(err, domain.ErrNamespaceRequired) {
		t.Errorf("expected ErrNamespaceRequired, got %v", err)
	}
}

func TestListAPIKeys_NoNamespace(t *testing.T) {
	svc := newTestServiceFull(nil, nil, nil, nil, &mockAPIKeyRepo{}, nil)

	_, err := svc.ListAPIKeys(context.Background(), domain.ListParams{})
	if !errors.Is(err, domain.ErrNamespaceRequired) {
		t.Errorf("expected ErrNamespaceRequired, got %v", err)
	}
}

func TestListAPIKeys_HappyPath(t *testing.T) {
	var capturedNS domain.Namespace
	expected := []domain.APIKey{
		{ID: uuid.New(), Namespace: "t1", Label: "key-1"},
		{ID: uuid.New(), Namespace: "t1", Label: "key-2"},
	}
	apiKeyRepo := &mockAPIKeyRepo{
		listKeysFn: func(_ context.Context, ns domain.Namespace, params domain.ListParams) ([]domain.APIKey, error) {
			capturedNS = ns
			return expected, nil
		},
	}
	svc := newTestServiceFull(nil, nil, nil, nil, apiKeyRepo, nil)

	keys, err := svc.ListAPIKeys(ctxWithNS("t1"), domain.ListParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
	if capturedNS != "t1" {
		t.Errorf("expected namespace 't1', got %q", capturedNS)
	}
}

func TestDeleteAPIKey_NoNamespace(t *testing.T) {
	svc := newTestServiceFull(nil, nil, nil, nil, &mockAPIKeyRepo{}, nil)

	err := svc.DeleteAPIKey(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrNamespaceRequired) {
		t.Errorf("expected ErrNamespaceRequired, got %v", err)
	}
}

func TestDeleteAPIKey_HappyPath(t *testing.T) {
	keyID := uuid.New()
	var deletedID uuid.UUID
	var deletedNS domain.Namespace
	apiKeyRepo := &mockAPIKeyRepo{
		deleteKeyFn: func(_ context.Context, id uuid.UUID, ns domain.Namespace) error {
			deletedID = id
			deletedNS = ns
			return nil
		},
	}
	svc := newTestServiceFull(nil, nil, nil, nil, apiKeyRepo, nil)

	err := svc.DeleteAPIKey(ctxWithNS("t1"), keyID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deletedID != keyID {
		t.Errorf("expected deleted ID %s, got %s", keyID, deletedID)
	}
	if deletedNS != "t1" {
		t.Errorf("expected deleted namespace 't1', got %q", deletedNS)
	}
}

func TestCreateAPIKey_InsertError(t *testing.T) {
	insertErr := errors.New("db connection lost")
	apiKeyRepo := &mockAPIKeyRepo{
		insertAPIKeyFn: func(_ context.Context, _ domain.APIKey) error {
			return insertErr
		},
	}
	svc := newTestServiceFull(nil, nil, nil, nil, apiKeyRepo, nil)

	_, err := svc.CreateAPIKey(ctxWithNS("t1"), CreateAPIKeyInput{
		Label: "my-key",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, insertErr) {
		t.Errorf("expected wrapped insertErr, got %v", err)
	}
}

func TestHashToken(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		hash1 := HashToken("ec_abc123")
		hash2 := HashToken("ec_abc123")
		if hash1 != hash2 {
			t.Errorf("expected same hash for same input, got %q and %q", hash1, hash2)
		}
	})

	t.Run("different inputs produce different outputs", func(t *testing.T) {
		hash1 := HashToken("ec_abc123")
		hash2 := HashToken("ec_xyz789")
		if hash1 == hash2 {
			t.Error("expected different hashes for different inputs")
		}
	})

	t.Run("output is hex string of length 64", func(t *testing.T) {
		hash := HashToken("ec_test_token")
		if len(hash) != 64 {
			t.Errorf("expected hash length 64, got %d", len(hash))
		}
		for _, c := range hash {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("expected hex character, got %c", c)
				break
			}
		}
	})
}
