package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/service"
)

// mockAPIKeyRepo is a hand-written mock for domain.APIKeyRepository.
type mockAPIKeyRepo struct {
	getKeyByHashFn   func(ctx context.Context, tokenHash string) (domain.APIKey, error)
	updateLastUsedFn func(ctx context.Context, ids []uuid.UUID) error
}

func (m *mockAPIKeyRepo) InsertAPIKey(_ context.Context, _ domain.APIKey) error {
	return nil
}

func (m *mockAPIKeyRepo) GetKeyByTokenHash(ctx context.Context, tokenHash string) (domain.APIKey, error) {
	if m.getKeyByHashFn != nil {
		return m.getKeyByHashFn(ctx, tokenHash)
	}
	return domain.APIKey{}, errors.New("not found")
}

func (m *mockAPIKeyRepo) ListKeys(_ context.Context, _ domain.Namespace, _ domain.ListParams) ([]domain.APIKey, error) {
	return nil, nil
}

func (m *mockAPIKeyRepo) DeleteKey(_ context.Context, _ uuid.UUID, _ domain.Namespace) error {
	return nil
}

func (m *mockAPIKeyRepo) UpdateLastUsedAt(ctx context.Context, ids []uuid.UUID) error {
	if m.updateLastUsedFn != nil {
		return m.updateLastUsedFn(ctx, ids)
	}
	return nil
}

func TestMultiKeyAuth_ValidDBKey(t *testing.T) {
	expectedHash := service.HashToken("test-token")

	repo := &mockAPIKeyRepo{
		getKeyByHashFn: func(_ context.Context, tokenHash string) (domain.APIKey, error) {
			if tokenHash == expectedHash {
				return domain.APIKey{
					ID:        uuid.New(),
					Namespace: domain.Namespace("tenant-1"),
					TokenHash: expectedHash,
					Enabled:   true,
				}, nil
			}
			return domain.APIKey{}, errors.New("not found")
		},
	}

	var gotNS domain.Namespace
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNS = domain.NamespaceFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := MultiKeyAuthMiddleware(repo, "", next)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if gotNS != domain.Namespace("tenant-1") {
		t.Errorf("expected namespace %q, got %q", "tenant-1", gotNS)
	}
}

func TestMultiKeyAuth_DisabledKey_FallbackMatch(t *testing.T) {
	expectedHash := service.HashToken("test-token")

	repo := &mockAPIKeyRepo{
		getKeyByHashFn: func(_ context.Context, tokenHash string) (domain.APIKey, error) {
			if tokenHash == expectedHash {
				return domain.APIKey{
					ID:        uuid.New(),
					Namespace: domain.Namespace("tenant-1"),
					TokenHash: expectedHash,
					Enabled:   false,
				}, nil
			}
			return domain.APIKey{}, errors.New("not found")
		},
	}

	var gotNS domain.Namespace
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNS = domain.NamespaceFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := MultiKeyAuthMiddleware(repo, "test-token", next)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if gotNS != domain.Namespace("default") {
		t.Errorf("expected namespace %q, got %q", "default", gotNS)
	}
}

func TestMultiKeyAuth_NoDBMatch_FallbackMatch(t *testing.T) {
	repo := &mockAPIKeyRepo{
		getKeyByHashFn: func(_ context.Context, _ string) (domain.APIKey, error) {
			return domain.APIKey{}, errors.New("not found")
		},
	}

	var gotNS domain.Namespace
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNS = domain.NamespaceFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := MultiKeyAuthMiddleware(repo, "test-token", next)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if gotNS != domain.Namespace("default") {
		t.Errorf("expected namespace %q, got %q", "default", gotNS)
	}
}

func TestMultiKeyAuth_NoMatch(t *testing.T) {
	repo := &mockAPIKeyRepo{
		getKeyByHashFn: func(_ context.Context, _ string) (domain.APIKey, error) {
			return domain.APIKey{}, errors.New("not found")
		},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := MultiKeyAuthMiddleware(repo, "other", next)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

func TestMultiKeyAuth_NoBearerPrefix(t *testing.T) {
	repo := &mockAPIKeyRepo{}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := MultiKeyAuthMiddleware(repo, "", next)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("Authorization", "Basic test-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

func TestMultiKeyAuth_NoAuthHeader(t *testing.T) {
	repo := &mockAPIKeyRepo{}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := MultiKeyAuthMiddleware(repo, "", next)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

func TestMultiKeyAuth_HealthBypass(t *testing.T) {
	repo := &mockAPIKeyRepo{}

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := MultiKeyAuthMiddleware(repo, "", next)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if !called {
		t.Error("expected next handler to be called for /health")
	}
}

func TestMultiKeyAuth_MetricsBypass(t *testing.T) {
	repo := &mockAPIKeyRepo{}

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := MultiKeyAuthMiddleware(repo, "", next)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if !called {
		t.Error("expected next handler to be called for /metrics")
	}
}

func TestMultiKeyAuth_MCPBypass(t *testing.T) {
	repo := &mockAPIKeyRepo{}

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := MultiKeyAuthMiddleware(repo, "", next)

	req := httptest.NewRequest(http.MethodGet, "/mcp/sse", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if !called {
		t.Error("expected next handler to be called for /mcp/sse")
	}
}
