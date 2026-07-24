package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/djlord-it/cronlite/internal/config"
)

func TestRegisterAdminRoutesIsOptIn(t *testing.T) {
	admin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	t.Run("disabled", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.Handle("/", http.NotFoundHandler())
		registerAdminRoutes(mux, config.Config{AdminEnabled: false, IPRateLimit: 10}, admin)

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 when disabled, got %d", rec.Code)
		}
	})

	t.Run("enabled", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.Handle("/", http.NotFoundHandler())
		registerAdminRoutes(mux, config.Config{AdminEnabled: true, IPRateLimit: 10}, admin)

		for _, path := range []string{"/admin", "/admin/login"} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusTeapot {
				t.Fatalf("%s: expected admin handler, got %d", path, rec.Code)
			}
		}
	})
}
