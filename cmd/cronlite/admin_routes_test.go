package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestRegisterAdminRoutesRateLimitResponseHasAdminSecurityHeaders(t *testing.T) {
	admin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux := http.NewServeMux()
	mux.Handle("/", http.NotFoundHandler())
	registerAdminRoutes(mux, config.Config{
		AdminEnabled:      true,
		AdminCookieSecure: true,
		IPRateLimit:       1,
	}, admin)

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	firstReq.RemoteAddr = "192.0.2.10:1234"
	mux.ServeHTTP(first, firstReq)

	limited := httptest.NewRecorder()
	limitedReq := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	limitedReq.RemoteAddr = "192.0.2.10:1234"
	mux.ServeHTTP(limited, limitedReq)

	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", limited.Code, limited.Body.String())
	}
	for name, want := range map[string]string{
		"Content-Security-Policy":   "frame-ancestors 'none'",
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "no-referrer",
		"X-Frame-Options":           "DENY",
		"Strict-Transport-Security": "max-age=31536000",
		"Cache-Control":             "no-store",
	} {
		got := limited.Header().Get(name)
		if name == "Content-Security-Policy" {
			if !strings.Contains(got, want) {
				t.Errorf("%s = %q, want it to contain %q", name, got, want)
			}
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}
