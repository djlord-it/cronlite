package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

// mockAPIKeyRepo is a hand-written mock for domain.APIKeyRepository.
type mockAPIKeyRepo struct {
	getKeyByHashFn func(ctx context.Context, tokenHash string) (domain.APIKey, error)
}

func (m *mockAPIKeyRepo) InsertAPIKey(_ context.Context, _ domain.APIKey) error {
	return nil
}

func (m *mockAPIKeyRepo) GetKeyByTokenHash(ctx context.Context, tokenHash string) (domain.APIKey, error) {
	return m.getKeyByHashFn(ctx, tokenHash)
}

func (m *mockAPIKeyRepo) ListKeys(_ context.Context, _ domain.Namespace, _ domain.ListParams) ([]domain.APIKey, error) {
	return nil, nil
}

func (m *mockAPIKeyRepo) DeleteKey(_ context.Context, _ uuid.UUID, _ domain.Namespace) error {
	return nil
}

func (m *mockAPIKeyRepo) UpdateLastUsedAt(_ context.Context, _ []uuid.UUID) error {
	return nil
}

// ---------- AuthMiddleware tests ----------

func TestAuthMiddleware_ValidDBKey(t *testing.T) {
	repo := &mockAPIKeyRepo{
		getKeyByHashFn: func(_ context.Context, _ string) (domain.APIKey, error) {
			return domain.APIKey{Enabled: true, Namespace: "tenant-1"}, nil
		},
	}

	var gotNS domain.Namespace
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNS = domain.NamespaceFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := AuthMiddleware(repo, "", next)

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if gotNS != "tenant-1" {
		t.Fatalf("namespace = %q, want %q", gotNS, "tenant-1")
	}
}

func TestAuthMiddleware_DisabledKey_FallbackMatch(t *testing.T) {
	repo := &mockAPIKeyRepo{
		getKeyByHashFn: func(_ context.Context, _ string) (domain.APIKey, error) {
			return domain.APIKey{Enabled: false, Namespace: "tenant-1"}, nil
		},
	}

	var gotNS domain.Namespace
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNS = domain.NamespaceFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := AuthMiddleware(repo, "test-token", next)

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if gotNS != "default" {
		t.Fatalf("namespace = %q, want %q", gotNS, "default")
	}
}

func TestAuthMiddleware_NoDBMatch_FallbackMatch(t *testing.T) {
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

	handler := AuthMiddleware(repo, "test-token", next)

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if gotNS != "default" {
		t.Fatalf("namespace = %q, want %q", gotNS, "default")
	}
}

func TestAuthMiddleware_NoMatch(t *testing.T) {
	repo := &mockAPIKeyRepo{
		getKeyByHashFn: func(_ context.Context, _ string) (domain.APIKey, error) {
			return domain.APIKey{}, errors.New("not found")
		},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	})

	handler := AuthMiddleware(repo, "wrong", next)

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_NoBearerPrefix(t *testing.T) {
	repo := &mockAPIKeyRepo{
		getKeyByHashFn: func(_ context.Context, _ string) (domain.APIKey, error) {
			return domain.APIKey{}, errors.New("not found")
		},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	})

	handler := AuthMiddleware(repo, "test-token", next)

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	req.Header.Set("Authorization", "Basic test-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_NoAuthHeader(t *testing.T) {
	repo := &mockAPIKeyRepo{
		getKeyByHashFn: func(_ context.Context, _ string) (domain.APIKey, error) {
			return domain.APIKey{}, errors.New("not found")
		},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	})

	handler := AuthMiddleware(repo, "test-token", next)

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

// ---------- httpContextFunc tests ----------

func TestHttpContextFunc_ValidDBKey(t *testing.T) {
	repo := &mockAPIKeyRepo{
		getKeyByHashFn: func(_ context.Context, _ string) (domain.APIKey, error) {
			return domain.APIKey{Enabled: true, Namespace: "tenant-1"}, nil
		},
	}

	fn := httpContextFunc(repo, "")

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-token")

	ctx := fn(context.Background(), req)

	got := domain.NamespaceFromContext(ctx)
	if got != "tenant-1" {
		t.Fatalf("namespace = %q, want %q", got, "tenant-1")
	}
}

func TestHttpContextFunc_FallbackMatch(t *testing.T) {
	repo := &mockAPIKeyRepo{
		getKeyByHashFn: func(_ context.Context, _ string) (domain.APIKey, error) {
			return domain.APIKey{}, errors.New("not found")
		},
	}

	fn := httpContextFunc(repo, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-token")

	ctx := fn(context.Background(), req)

	got := domain.NamespaceFromContext(ctx)
	if got != "default" {
		t.Fatalf("namespace = %q, want %q", got, "default")
	}
}

func TestHttpContextFunc_NoMatch(t *testing.T) {
	repo := &mockAPIKeyRepo{
		getKeyByHashFn: func(_ context.Context, _ string) (domain.APIKey, error) {
			return domain.APIKey{}, errors.New("not found")
		},
	}

	fn := httpContextFunc(repo, "wrong")

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-token")

	baseCtx := context.Background()
	ctx := fn(baseCtx, req)

	got := domain.NamespaceFromContext(ctx)
	if !got.IsZero() {
		t.Fatalf("namespace = %q, want zero value", got)
	}
}

func TestHttpContextFunc_NoBearerPrefix(t *testing.T) {
	repo := &mockAPIKeyRepo{
		getKeyByHashFn: func(_ context.Context, _ string) (domain.APIKey, error) {
			return domain.APIKey{Enabled: true, Namespace: "tenant-1"}, nil
		},
	}

	fn := httpContextFunc(repo, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	req.Header.Set("Authorization", "Basic test-token")

	baseCtx := context.Background()
	ctx := fn(baseCtx, req)

	got := domain.NamespaceFromContext(ctx)
	if !got.IsZero() {
		t.Fatalf("namespace = %q, want zero value", got)
	}
}
