package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/djlord-it/cronlite/internal/domain"
)

type metricsAPIKeyRepo struct{}

func (metricsAPIKeyRepo) InsertAPIKey(context.Context, domain.APIKey) error {
	return nil
}

func (metricsAPIKeyRepo) GetKeyByTokenHash(context.Context, string) (domain.APIKey, error) {
	return domain.APIKey{}, domain.ErrAPIKeyNotFound
}

func (metricsAPIKeyRepo) ListKeys(context.Context, domain.Namespace, domain.ListParams) ([]domain.APIKey, error) {
	return nil, nil
}

func (metricsAPIKeyRepo) DeleteKey(context.Context, uuid.UUID, domain.Namespace) error {
	return nil
}

func (metricsAPIKeyRepo) UpdateLastUsedAt(context.Context, []uuid.UUID) error {
	return nil
}

func TestNewMetricsHandlerRequiresAuthWithoutLegacyAPIKey(t *testing.T) {
	handler := newMetricsHandler(context.Background(), metricsAPIKeyRepo{}, "", false)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", w.Code)
	}
}

func TestNewMetricsHandlerCanBeExplicitlyPublic(t *testing.T) {
	handler := newMetricsHandler(context.Background(), metricsAPIKeyRepo{}, "", true)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code == http.StatusUnauthorized {
		t.Fatal("expected public metrics handler to bypass auth")
	}
}
