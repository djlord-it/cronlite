package mcp

import (
	"net/http"

	"github.com/mark3labs/mcp-go/server"

	"github.com/djlord-it/cronlite/internal/api"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/service"
)

// NewServer creates a new MCP server with all Phase 1 tools registered.
// The tools delegate to the service layer, which requires a namespace in
// the context (injected by the auth middleware / HTTPContextFunc).
func NewServer(svc *service.JobService) *server.MCPServer {
	s := server.NewMCPServer(
		"CronLite",
		"1.0.0",
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)
	RegisterTools(s, svc)
	return s
}

// MountHTTP creates a StreamableHTTPServer that handles /mcp requests and
// wraps it with auth middleware to resolve Bearer tokens to namespaces.
// It also applies body-size and per-namespace rate limiting.
// The returned http.Handler should be mounted at "/mcp" on the main mux.
func MountHTTP(
	mcpServer *server.MCPServer,
	keyRepo domain.APIKeyRepository,
	fallbackKey string,
	namespaceRateLimit int,
) http.Handler {
	httpServer := server.NewStreamableHTTPServer(mcpServer,
		server.WithEndpointPath("/mcp"),
		server.WithHTTPContextFunc(httpContextFunc(keyRepo, fallbackKey)),
	)

	limited := api.BodySizeLimitMiddleware(httpServer)
	limited = api.NamespaceRateLimitMiddleware(namespaceRateLimit, limited)

	// Wrap with auth middleware for the initial HTTP connection.
	// The HTTPContextFunc injects the namespace into the MCP context,
	// while the middleware rejects unauthenticated requests entirely.
	return AuthMiddleware(keyRepo, fallbackKey, limited)
}
