package mcp

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/djlord-it/easy-cron/internal/domain"
	"github.com/djlord-it/easy-cron/internal/service"
)

// AuthMiddleware wraps an http.Handler and extracts the Bearer token from
// the Authorization header, resolves it to a namespace, and injects the
// namespace into the request context. This is the same auth pattern used
// by the REST API MultiKeyAuthMiddleware, adapted for the MCP transport.
//
// If the token matches a key in the repository, the key's namespace is used.
// If the legacy fallbackKey matches, namespace "default" is used.
// If neither matches, a 401 response is returned.
func AuthMiddleware(keyRepo domain.APIKeyRepository, fallbackKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")

		// Try multi-key lookup via SHA-256 hash.
		tokenHash := service.HashToken(token)
		key, err := keyRepo.GetKeyByTokenHash(r.Context(), tokenHash)
		if err == nil && key.Enabled {
			ctx := domain.NamespaceToContext(r.Context(), key.Namespace)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Fallback: legacy single API_KEY support.
		if fallbackKey != "" && subtle.ConstantTimeCompare([]byte(token), []byte(fallbackKey)) == 1 {
			ctx := domain.NamespaceToContext(r.Context(), domain.Namespace("default"))
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// httpContextFunc returns an HTTPContextFunc suitable for use with
// server.WithHTTPContextFunc. It extracts the Bearer token from the HTTP
// request headers, resolves it to a namespace, and injects the namespace
// into the context that the MCP server will use for tool handler calls.
func httpContextFunc(keyRepo domain.APIKeyRepository, fallbackKey string) func(ctx context.Context, r *http.Request) context.Context {
	return func(ctx context.Context, r *http.Request) context.Context {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			return ctx
		}
		token := strings.TrimPrefix(auth, "Bearer ")

		// Try multi-key lookup via SHA-256 hash.
		tokenHash := service.HashToken(token)
		key, err := keyRepo.GetKeyByTokenHash(ctx, tokenHash)
		if err == nil && key.Enabled {
			return domain.NamespaceToContext(ctx, key.Namespace)
		}

		// Fallback: legacy single API_KEY support.
		if fallbackKey != "" && subtle.ConstantTimeCompare([]byte(token), []byte(fallbackKey)) == 1 {
			return domain.NamespaceToContext(ctx, domain.Namespace("default"))
		}

		return ctx
	}
}
