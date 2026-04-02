package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/djlord-it/easy-cron/internal/domain"
	"github.com/google/uuid"
)

// CreateAPIKeyInput holds the parameters for creating an API key.
type CreateAPIKeyInput struct {
	Label  string
	Scopes []string
}

// CreateAPIKeyResult contains the plaintext token (shown once) and the
// persisted key metadata.
type CreateAPIKeyResult struct {
	PlaintextToken string
	Key            domain.APIKey
}

// CreateAPIKey generates a new API key for the namespace in ctx.
func (s *JobService) CreateAPIKey(ctx context.Context, input CreateAPIKeyInput) (CreateAPIKeyResult, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return CreateAPIKeyResult{}, domain.ErrNamespaceRequired
	}

	// Generate 32 random bytes.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return CreateAPIKeyResult{}, fmt.Errorf("generate token: %w", err)
	}

	plaintext := "ec_" + hex.EncodeToString(raw)
	tokenHash := HashToken(plaintext)

	now := time.Now().UTC()
	key := domain.APIKey{
		ID:        uuid.New(),
		Namespace: ns,
		TokenHash: tokenHash,
		Label:     input.Label,
		Scopes:    append([]string{}, input.Scopes...),
		Enabled:   true,
		CreatedAt: now,
	}

	if err := s.apiKeys.InsertAPIKey(ctx, key); err != nil {
		return CreateAPIKeyResult{}, fmt.Errorf("insert api key: %w", err)
	}

	return CreateAPIKeyResult{
		PlaintextToken: plaintext,
		Key:            key,
	}, nil
}

// ListAPIKeys returns API keys for the namespace in ctx.
func (s *JobService) ListAPIKeys(ctx context.Context, params domain.ListParams) ([]domain.APIKey, error) {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return nil, domain.ErrNamespaceRequired
	}

	params = params.WithDefaults()
	return s.apiKeys.ListKeys(ctx, ns, params)
}

// DeleteAPIKey removes an API key by ID, scoped to the namespace from ctx.
func (s *JobService) DeleteAPIKey(ctx context.Context, id uuid.UUID) error {
	ns := domain.NamespaceFromContext(ctx)
	if ns.IsZero() {
		return domain.ErrNamespaceRequired
	}

	return s.apiKeys.DeleteKey(ctx, id, ns)
}

// HashToken returns the SHA-256 hex digest of a plaintext API token.
// Exported so that auth middleware can use the same hashing logic.
func HashToken(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}
