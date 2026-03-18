package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// AuthMiddleware returns an http.Handler that requires a valid API key
// in the Authorization header (Bearer token) for all endpoints except /health.
// If apiKey is empty, authentication is disabled (pass-through).
func AuthMiddleware(apiKey string, next http.Handler) http.Handler {
	if apiKey == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow health checks without auth (used by load balancers / k8s probes)
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if auth == token || subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}
