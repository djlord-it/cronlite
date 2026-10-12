package mcp

import (
	"context"
	"net/http"
	"time"

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
	return mountHTTP(mcpServer, keyRepo, fallbackKey, namespaceRateLimit, 10*time.Minute, 30*time.Second)
}

// httpTransport owns the SDK's idle-session sweeper and request cancellation.
// Keep MountHTTP's handler contract while exposing Shutdown to its owner.
type httpTransport struct {
	http.Handler
	server *server.StreamableHTTPServer
	ctx    context.Context
	cancel context.CancelFunc
}

func (h *httpTransport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.ctx.Err() != nil {
		http.Error(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(h.ctx, cancel)
	defer cancel()
	defer stop()
	defer func() {
		// An initialization admitted before shutdown can finish after the SDK's
		// first cleanup snapshot. Reclaim that late session too.
		if h.ctx.Err() != nil {
			h.server.CloseSessions(context.Background())
		}
	}()
	h.Handler.ServeHTTP(w, r.WithContext(ctx))
}

func (h *httpTransport) Shutdown(ctx context.Context) error {
	h.cancel()
	return h.server.Shutdown(ctx)
}

func mountHTTP(mcpServer *server.MCPServer, keyRepo domain.APIKeyRepository, fallbackKey string, namespaceRateLimit int, ttl, heartbeat time.Duration) *httpTransport {
	httpServer := server.NewStreamableHTTPServer(mcpServer,
		server.WithEndpointPath("/mcp"),
		server.WithHTTPContextFunc(httpContextFunc(keyRepo, fallbackKey)),
		server.WithSessionIdleTTL(ttl),
		server.WithHeartbeatInterval(heartbeat),
	)

	limited := api.BodySizeLimitMiddleware(httpServer)
	limited = api.NamespaceRateLimitMiddleware(namespaceRateLimit, limited)

	// Wrap with auth middleware for the initial HTTP connection.
	// The HTTPContextFunc injects the namespace into the MCP context,
	// while the middleware rejects unauthenticated requests entirely.
	ctx, cancel := context.WithCancel(context.Background())
	return &httpTransport{Handler: AuthMiddleware(keyRepo, fallbackKey, limited), server: httpServer, ctx: ctx, cancel: cancel}
}
