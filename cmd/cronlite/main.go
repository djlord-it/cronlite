package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/djlord-it/cronlite/internal/analytics"
	"github.com/djlord-it/cronlite/internal/api"
	"github.com/djlord-it/cronlite/internal/circuitbreaker"
	"github.com/djlord-it/cronlite/internal/config"
	"github.com/djlord-it/cronlite/internal/cron"
	"github.com/djlord-it/cronlite/internal/dispatcher"
	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/leaderelection"
	mcpsrv "github.com/djlord-it/cronlite/internal/mcp"
	"github.com/djlord-it/cronlite/internal/metrics"
	"github.com/djlord-it/cronlite/internal/migrate"
	"github.com/djlord-it/cronlite/internal/reconciler"
	"github.com/djlord-it/cronlite/internal/scheduler"
	"github.com/djlord-it/cronlite/internal/service"
	"github.com/djlord-it/cronlite/internal/store/postgres"
	"github.com/djlord-it/cronlite/internal/tailnet"
	"github.com/djlord-it/cronlite/internal/transport/channel"
	"github.com/djlord-it/cronlite/internal/webadmin"
	"github.com/djlord-it/cronlite/internal/webhookurl"
	"github.com/djlord-it/cronlite/schema"

	_ "github.com/lib/pq"
)

type cronParserAdapter struct {
	parser *cron.Parser
}

func (a *cronParserAdapter) Parse(expression string, timezone string) (scheduler.CronSchedule, error) {
	sched, err := a.parser.Parse(expression, timezone)
	if err != nil {
		return nil, err
	}
	return sched, nil
}

// Build-time variables set via -ldflags
var (
	version = "dev"
	commit  = "unknown"
)

const (
	exitSuccess       = 0
	exitRuntimeError  = 1
	exitInvalidConfig = 2

	httpReadHeaderTimeout = 5 * time.Second
	httpReadTimeout       = 30 * time.Second
	httpIdleTimeout       = 60 * time.Second

	// tailnetUpTimeout bounds how long serve waits for the embedded Tailscale
	// node to connect before giving up.
	tailnetUpTimeout = 60 * time.Second
	// tailnetCertTimeout bounds the background HTTPS certificate fetch.
	tailnetCertTimeout = 2 * time.Minute
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(exitRuntimeError)
	}

	cmd := os.Args[1]

	switch cmd {
	case "serve":
		os.Exit(runServe())
	case "validate":
		os.Exit(runValidate())
	case "config":
		os.Exit(runConfig())
	case "version":
		os.Exit(runVersion())
	case "create-key":
		os.Exit(runCreateKey())
	case "migrate":
		os.Exit(runMigrate(os.Args[2:]))
	case "--help", "-h", "help":
		printUsage()
		os.Exit(exitSuccess)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(exitRuntimeError)
	}
}

func printUsage() {
	fmt.Println(`cronlite - distributed cron job scheduler

Usage:
  cronlite <command>

Commands:
  serve        Start the scheduler and dispatcher
  validate     Validate configuration (no connections made)
  config       Print effective configuration as JSON (secrets masked)
  version      Print version information
  create-key   Create a new API key  (usage: cronlite create-key <namespace> <label>)
  migrate      Apply pending schema migrations (--status to list)

Environment Variables:
  CRONLITE_ENV              Runtime environment; "production" enables strict validation
  DATABASE_URL              PostgreSQL connection string (required)
  REDIS_ADDR                Redis address for analytics (optional)
  HTTP_ADDR                 HTTP server address (default: ":8080")
  API_KEY                   DEPRECATED legacy single key; use 'cronlite create-key' instead
  TICK_INTERVAL             Scheduler tick interval (default: "30s")
  MIGRATE_ON_START          Apply pending schema migrations on 'serve' start (default: "false")
  MIGRATE_LOCK_TIMEOUT      Max wait for the migration lock (default: "2m")
  WEBHOOK_ALLOWED_PRIVATE_SUFFIXES  Comma-separated hostnames (".suffix" or exact) allowed to resolve
                            to private ranges (RFC 1918 / fc00::/7); loopback and link-local stay blocked
  WEBHOOK_ALLOW_TAILNET     Deliver webhooks to hosts under this node's tailnet (*.<tailnet>.ts.net) over an
                            embedded Tailscale node (default: "false"; needs a -tags tailscale build)
  TS_AUTHKEY                Tailscale auth key for the embedded node (optional once TS_STATE_DIR is logged in)
  TS_HOSTNAME               Node name on the tailnet (default: "cronlite")
  TS_STATE_DIR              Directory for the node's state (required with WEBHOOK_ALLOW_TAILNET or TS_LISTEN)
  TS_LISTEN                 Also serve the API, admin UI and MCP at https://<TS_HOSTNAME>.<tailnet>.ts.net
                            (default: "false"; needs a -tags tailscale build and tailnet HTTPS certificates)
  TS_DISABLE_PUBLIC_LISTENER Skip the HTTP_ADDR listener and serve on the tailnet only (default: "false")

  DB_OP_TIMEOUT             Database operation timeout (default: "5s")
  DB_MAX_OPEN_CONNS         Max open database connections (default: "25")
  DB_MAX_IDLE_CONNS         Max idle database connections (default: "5")
  DB_CONN_MAX_LIFETIME      Max connection lifetime (default: "30m")
  DB_CONN_MAX_IDLE_TIME     Max connection idle time (default: "5m")

  HTTP_SHUTDOWN_TIMEOUT     Graceful HTTP shutdown timeout (default: "10s")
  DISPATCHER_DRAIN_TIMEOUT  Dispatcher event drain timeout (default: "30s")

  EVENTBUS_BUFFER_SIZE      Event bus channel buffer capacity (default: "100")

  CIRCUIT_BREAKER_THRESHOLD Circuit breaker failure threshold (default: "5", 0=disabled)
  CIRCUIT_BREAKER_COOLDOWN  Circuit breaker cooldown duration (default: "2m")

  DISPATCH_MODE             Dispatch mode: "channel" or "db" (default: "channel")
  DB_POLL_INTERVAL          DB poll sleep interval (default: "500ms", db mode only)
  DISPATCHER_WORKERS        DB poll workers; each allows 4 concurrent deliveries (default: "1", db mode only)

  LEADER_LOCK_KEY           Advisory lock key for leader election (default: "728379", db mode only)
  LEADER_RETRY_INTERVAL     Follower lock acquisition retry interval (default: "5s", db mode only)
  LEADER_HEARTBEAT_INTERVAL Leader connection health check interval (default: "2s", db mode only)

  METRICS_ENABLED           Enable Prometheus metrics (default: "false")
  METRICS_PATH              Metrics endpoint path (default: "/metrics")
  METRICS_PUBLIC            Allow unauthenticated metrics access (default: "false")

  ADMIN_ENABLED             Enable server-rendered admin UI at /admin (default: "false")
  ADMIN_BOOTSTRAP_TOKEN     Secret required by /admin/setup when no API key exists
  ADMIN_SESSION_TTL         Idle admin session timeout (default: "30m")
  ADMIN_SESSION_ABSOLUTE_TTL Maximum admin session lifetime (default: "12h")
  ADMIN_COOKIE_SECURE       Force Secure admin cookies (default: true in production)

  RECONCILE_ENABLED         Enable orphan execution reconciler (default: "false")
  RECONCILE_INTERVAL        How often to scan for orphans (default: "5m")
  RECONCILE_THRESHOLD       Age before emitted execution is orphaned (default: "19m")
  RECONCILE_REQUEUE_THRESHOLD Renewable execution lease duration (default: "2m", both modes)
  RECONCILE_BATCH_SIZE      Max orphans per cycle (default: "100")`)
}

// leaderRuntime manages the lifecycle of leader-only components (scheduler, reconciler).
// All methods are safe for concurrent use. stop() is idempotent.
type leaderRuntime struct {
	mu      sync.Mutex
	running bool

	cancelScheduler  context.CancelFunc
	cancelReconciler context.CancelFunc
	wg               sync.WaitGroup

	sched       *scheduler.Scheduler
	metricsSink *metrics.PrometheusSink
	cfg         config.Config
	store       *postgres.Store
	emitter     interface {
		Emit(ctx context.Context, event domain.TriggerEvent) error
	}
}

func newMetricsHandler(
	appCtx context.Context,
	keyRepo domain.APIKeyRepository,
	fallbackKey string,
	public bool,
) http.Handler {
	metricsHandler := http.Handler(promhttp.Handler())
	if public {
		return metricsHandler
	}
	return api.MultiKeyAuthMiddleware(appCtx, keyRepo, fallbackKey, metricsHandler)
}

func registerAdminRoutes(mux *http.ServeMux, cfg config.Config, handler http.Handler) {
	if !cfg.AdminEnabled {
		return
	}
	rateLimited := api.RateLimitMiddleware(cfg.IPRateLimit, handler)
	protected := webadmin.SecurityHeaders(rateLimited, cfg.AdminCookieSecure)
	mux.Handle("/admin", protected)
	mux.Handle("/admin/", protected)
}

func newWebAdminConfig(
	cfg config.Config,
	jobService *service.JobService,
	store *postgres.Store,
) webadmin.Config {
	return webadmin.Config{
		RuntimeSettings: []webadmin.RuntimeSetting{
			{Name: "Environment", Value: cfg.Environment},
			{Name: "Dispatcher", Value: cfg.DispatchMode},
			{Name: "Workers", Value: fmt.Sprint(cfg.DispatcherWorkers)},
			{Name: "Tick interval", Value: cfg.TickInterval.String()},
			{Name: "Reconciliation", Value: fmt.Sprint(cfg.ReconcileEnabled)},
			{Name: "Metrics", Value: fmt.Sprint(cfg.MetricsEnabled)},
		},
		Service:            jobService,
		Sessions:           store,
		Keys:               store,
		BootstrapToken:     cfg.AdminBootstrapToken,
		SessionTTL:         cfg.AdminSessionTTL,
		SessionAbsoluteTTL: cfg.AdminSessionAbsoluteTTL,
		CookieSecure:       cfg.AdminCookieSecure,
		Logger:             log.Default(),
	}
}

func logAdminStartup(cfg config.Config) {
	log.Printf(
		"cronlite: admin UI mounted at /admin (idle_session_ttl=%s, absolute_session_ttl=%s, secure_cookie=%t)",
		cfg.AdminSessionTTL,
		cfg.AdminSessionAbsoluteTTL,
		cfg.AdminCookieSecure,
	)
}

func (lr *leaderRuntime) start(leaderCtx context.Context) {
	lr.mu.Lock()
	defer lr.mu.Unlock()

	if lr.running || leaderCtx.Err() != nil {
		return
	}

	schedCtx, cancelSched := context.WithCancel(leaderCtx)
	lr.cancelScheduler = cancelSched

	lr.wg.Add(1)
	go func() {
		defer lr.wg.Done()
		if err := lr.sched.Run(schedCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("cronlite: leader scheduler stopped with error: %v", err)
		}
	}()

	if lr.cfg.ReconcileEnabled {
		reconCtx, cancelRecon := context.WithCancel(leaderCtx)
		lr.cancelReconciler = cancelRecon

		recon := reconciler.New(
			reconciler.Config{
				Interval:  lr.cfg.ReconcileInterval,
				Threshold: lr.cfg.ReconcileThreshold,
				BatchSize: lr.cfg.ReconcileBatchSize,
			},
			lr.store,
			lr.emitter,
		)
		if lr.metricsSink != nil {
			recon = recon.WithMetrics(lr.metricsSink)
		}

		lr.wg.Add(1)
		go func() {
			defer lr.wg.Done()
			recon.Run(reconCtx)
		}()
		log.Printf("cronlite: reconciler started (interval=%s, threshold=%s, requeue_threshold=%s, batch=%d)",
			lr.cfg.ReconcileInterval, lr.cfg.ReconcileThreshold, lr.cfg.ReconcileRequeueThreshold, lr.cfg.ReconcileBatchSize)
	}

	lr.running = true
	log.Println("cronlite: leader duties started (scheduler + reconciler)")
}

func (lr *leaderRuntime) stop() {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if !lr.running {
		return
	}
	if lr.cancelScheduler != nil {
		lr.cancelScheduler()
	}
	if lr.cancelReconciler != nil {
		lr.cancelReconciler()
	}
	// Keep generations serialized until all old duties have stopped.
	lr.wg.Wait()
	lr.running = false
	log.Println("cronlite: leader duties stopped (scheduler + reconciler)")
}

func runServe() int {
	for _, arg := range os.Args[2:] {
		if arg == "--help" || arg == "-h" {
			printUsage()
			return exitSuccess
		}
	}

	cfg := config.Load()

	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		return exitInvalidConfig
	}

	logConfigWarnings(&cfg)

	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open database: %v\n", err)
		return exitRuntimeError
	}
	defer db.Close()

	db.SetMaxOpenConns(cfg.DBMaxOpenConns)
	db.SetMaxIdleConns(cfg.DBMaxIdleConns)
	db.SetConnMaxLifetime(cfg.DBConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.DBConnMaxIdleTime)

	log.Printf("cronlite: db pool configured (max_open=%d, max_idle=%d, max_lifetime=%s, max_idle_time=%s)",
		cfg.DBMaxOpenConns, cfg.DBMaxIdleConns, cfg.DBConnMaxLifetime, cfg.DBConnMaxIdleTime)

	if err := db.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to database: %v\n", err)
		return exitRuntimeError
	}

	if cfg.MigrateOnStart {
		res, err := applyMigrations(context.Background(), db, cfg.MigrateLockTimeout, log.Printf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
			return exitRuntimeError
		}
		log.Printf("cronlite: migrations complete (applied=%d, baselined=%d)", len(res.Applied), len(res.Baselined))
	}

	if err := probeExecutionOwnership(db); err != nil {
		fmt.Fprintf(os.Stderr, "execution ownership migration probe failed: %v\n", err)
		return exitInvalidConfig
	}

	store := postgres.New(db, cfg.DBOpTimeout).WithExecutionLease(cfg.ReconcileRequeueThreshold)
	cronParser := &cronParserAdapter{parser: cron.NewParser()}
	webhookAllowlist, err := webhookurl.ParseAllowlist(cfg.WebhookAllowedPrivateSuffixes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid WEBHOOK_ALLOWED_PRIVATE_SUFFIXES: %v\n", err)
		return exitInvalidConfig
	}
	senderOpts := []dispatcher.SenderOption{dispatcher.WithAllowlist(webhookAllowlist)}
	var tailnetNode *tailnet.Node
	var tailnetPolicy *webhookurl.TailnetPolicy
	if cfg.TailnetEnabled() {
		log.Printf("tailnet: starting node %q (state=%s)", cfg.TSHostname, cfg.TSStateDir)
		upCtx, cancelUp := context.WithTimeout(context.Background(), tailnetUpTimeout)
		tailnetNode, err = tailnet.Start(upCtx, tailnet.Config{
			AuthKey:  cfg.TSAuthKey,
			Hostname: cfg.TSHostname,
			StateDir: cfg.TSStateDir,
			Logf: func(format string, args ...any) {
				log.Printf("tailnet: "+format, args...)
			},
		})
		cancelUp()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start tailnet node: %v\n", err)
			return exitRuntimeError
		}
		defer func() {
			if tailnetNode != nil { // early return before ordered shutdown
				closeTailnet(tailnetNode)
			}
		}()
		log.Printf("tailnet: node up on %s (addresses %v)", tailnetNode.Suffix(), tailnetNode.SelfAddrs())
		if cfg.WebhookAllowTailnet {
			tailnetPolicy, err = webhookurl.ParseTailnetPolicy(cfg.WebhookTailnetHosts)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid WEBHOOK_TAILNET_HOSTS: %v\n", err)
				return exitInvalidConfig
			}
			senderOpts = append(senderOpts, dispatcher.WithTailnet(tailnetNode), dispatcher.WithTailnetPolicy(tailnetPolicy))
			log.Printf("tailnet: webhook delivery enabled for *.%s (namespaces: %s)",
				tailnetNode.Suffix(), strings.Join(tailnetPolicy.Namespaces(), ", "))
		}
	}
	webhookSender := dispatcher.NewHTTPWebhookSender(senderOpts...)

	var metricsSink *metrics.PrometheusSink

	if cfg.MetricsEnabled {
		metricsSink = metrics.NewPrometheusSink(prometheus.DefaultRegisterer)
		if tailnetNode != nil {
			node := tailnetNode
			metricsSink.RegisterTailnetUp(prometheus.DefaultRegisterer, func() bool {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				return node.Running(ctx)
			})
		}
		log.Printf("cronlite: metrics enabled (path=%s)", cfg.MetricsPath)
	} else {
		log.Println("cronlite: METRICS_ENABLED not set; metrics disabled")
	}

	// Dispatch mode: "channel" uses an in-memory EventBus, "db" uses NopEmitter
	// because workers poll Postgres directly.
	var emitter interface {
		Emit(ctx context.Context, event domain.TriggerEvent) error
	}
	var dispatchCh <-chan domain.TriggerEvent

	if cfg.DispatchMode == "db" {
		emitter = channel.NopEmitter{}
		log.Printf("cronlite: dispatch mode=db (workers=%d, poll_interval=%s)",
			cfg.DispatcherWorkers, cfg.DBPollInterval)
	} else {
		var busOpts []channel.Option
		if metricsSink != nil {
			busOpts = append(busOpts, channel.WithMetrics(metricsSink))
		}
		bus := channel.NewEventBus(cfg.EventBusBufferSize, busOpts...)
		emitter = bus
		dispatchCh = bus.Channel()
		log.Printf("cronlite: dispatch mode=channel (buffer=%d)", cfg.EventBusBufferSize)
	}

	sched := scheduler.New(
		scheduler.Config{
			TickInterval:    cfg.TickInterval,
			MaxFiresPerTick: cfg.MaxFiresPerTick,
		},
		store,
		cronParser,
		emitter,
	)
	if metricsSink != nil {
		sched = sched.WithMetrics(metricsSink)
	}

	disp := dispatcher.New(store, webhookSender).
		WithDrainTimeout(cfg.DispatcherDrainTimeout)
	if metricsSink != nil {
		disp = disp.WithMetrics(metricsSink)
	}

	if cfg.RedisAddr != "" {
		redisClient := redis.NewClient(&redis.Options{
			Addr: cfg.RedisAddr,
		})
		defer redisClient.Close()
		sink := analytics.NewRedisSink(redisClient)
		disp = disp.WithAnalytics(sink)
		log.Printf("cronlite: analytics enabled (redis=%s)", cfg.RedisAddr)
	} else {
		log.Println("cronlite: REDIS_ADDR not set; analytics disabled")
	}

	if cfg.CircuitBreakerThreshold > 0 {
		cb := circuitbreaker.New(cfg.CircuitBreakerThreshold, cfg.CircuitBreakerCooldown)
		disp = disp.WithCircuitBreaker(cb)
		log.Printf("cronlite: circuit breaker enabled (threshold=%d, cooldown=%s)",
			cfg.CircuitBreakerThreshold, cfg.CircuitBreakerCooldown)
	} else {
		log.Println("cronlite: circuit breaker disabled (threshold=0)")
	}

	// ── Service layer ─────────────────────────────────────────────────────────
	jobService := service.NewJobService(store, store, store, store, store, store, cronParser.parser)

	if tailnetPolicy != nil {
		jobService = jobService.WithTailnetPolicy(tailnetPolicy, tailnetNode.Suffix())
	}
	if cfg.DispatchMode == "channel" {
		jobService = jobService.WithEmitter(emitter)
	}

	// ── REST transport (oapi-codegen strict handler) ──────────────────────────
	serverImpl := api.NewServerImpl(jobService).WithHealthChecker(db)
	if tailnetNode != nil {
		serverImpl = serverImpl.WithTailnetChecker(tailnetNode)
	}
	strictHandler := api.NewStrictHandler(serverImpl, nil)
	apiRouter := api.Handler(strictHandler)

	// App-level context cancelled on shutdown — used for background goroutines
	// like the auth middleware's lastUsedTracker.
	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()

	// Middleware chain (outermost executes first):
	//   IP rate limit → Auth → Namespace rate limit → Body size limit → Router
	rootHandler := apiRouter
	rootHandler = api.BodySizeLimitMiddleware(rootHandler)                              // body size limit, innermost
	rootHandler = api.NamespaceRateLimitMiddleware(cfg.NamespaceRateLimit, rootHandler) // per-namespace, after auth
	rootHandler = api.MultiKeyAuthMiddleware(appCtx, store, cfg.APIKey, rootHandler)    // sets namespace in ctx
	rootHandler = api.RateLimitMiddleware(cfg.IPRateLimit, rootHandler)                 // per-IP, before auth
	rootHandler = api.CORSMiddleware(cfg.CORSOrigins, rootHandler)                      // CORS, outermost
	logAuthStatus(appCtx, cfg.APIKey, store)
	log.Printf("cronlite: rate limits: %d req/s per IP, %d req/s per namespace", cfg.IPRateLimit, cfg.NamespaceRateLimit)

	// ── MCP transport (embedded SSE for AI agents) ──────────────────────────
	mcpServer := mcpsrv.NewServer(jobService)
	mcpTransport := mcpsrv.MountHTTP(mcpServer, store, cfg.APIKey, cfg.NamespaceRateLimit)
	mcpShutdown := mcpTransport.(interface{ Shutdown(context.Context) error }).Shutdown
	defer func() {
		if err := mcpShutdown(context.Background()); err != nil {
			log.Printf("mcp: cleanup: %v", err)
		}
	}()
	mcpHandler := mcpTransport
	mcpHandler = api.RateLimitMiddleware(cfg.IPRateLimit, mcpHandler)
	log.Printf("mcp: SSE transport mounted at /mcp")

	httpMux := http.NewServeMux()
	if cfg.MetricsEnabled {
		httpMux.Handle(cfg.MetricsPath, newMetricsHandler(appCtx, store, cfg.APIKey, cfg.MetricsPublic))
	}
	if cfg.AdminEnabled {
		adminHandler, err := webadmin.NewHandler(newWebAdminConfig(cfg, jobService, store))
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to initialize admin UI: %v\n", err)
			return exitRuntimeError
		}
		registerAdminRoutes(httpMux, cfg, adminHandler)
		logAdminStartup(cfg)
	}
	httpMux.Handle("/mcp", mcpHandler)
	httpMux.Handle("/", rootHandler)

	// HTTP server runs on all instances regardless of dispatch mode.
	httpServer := newHTTPServer(cfg.HTTPAddr, httpMux)
	httpCtx, cancelHTTP := context.WithCancel(context.Background())
	defer cancelHTTP()
	httpServer.BaseContext = func(net.Listener) context.Context { return httpCtx }
	// The public HTTP_ADDR listener (unless TS_DISABLE_PUBLIC_LISTENER) and,
	// with TS_LISTEN, the tailnet HTTPS listener both serve httpMux through
	// httpServer, so one Shutdown drains them together. Plain HTTP on the
	// tailnet only redirects to HTTPS.
	type namedListener struct {
		name string
		ln   net.Listener
	}
	var listeners []namedListener
	closeListeners := func() {
		for _, l := range listeners {
			_ = l.ln.Close()
		}
	}
	if cfg.TSDisablePublicListener {
		log.Println("cronlite: TS_DISABLE_PUBLIC_LISTENER=true; HTTP_ADDR listener disabled, serving on the tailnet only")
	} else {
		listener, err := net.Listen("tcp", cfg.HTTPAddr)
		if err != nil {
			log.Printf("cronlite: http listen error: %v", err)
			return exitRuntimeError
		}
		listeners = append(listeners, namedListener{name: cfg.HTTPAddr, ln: listener})
	}
	var redirectServer *http.Server
	var redirectListener net.Listener
	if cfg.TSListen {
		tlsListener, err := tailnetNode.ListenTLS()
		if err != nil {
			closeListeners()
			log.Printf("tailnet: https listen error: %v", err)
			return exitRuntimeError
		}
		domain := tailnetNode.CertDomain()
		listeners = append(listeners, namedListener{name: "https://" + domain, ln: tlsListener})
		// Fetch the certificate now rather than on the first handshake, which
		// would otherwise wait for issuance and usually time out.
		go func(node *tailnet.Node) {
			ctx, cancel := context.WithTimeout(appCtx, tailnetCertTimeout)
			defer cancel()
			start := time.Now()
			if err := node.WarmCert(ctx); err != nil {
				log.Printf("tailnet: https certificate not ready: %v (the next https request retries)", err)
				return
			}
			log.Printf("tailnet: https certificate ready for %s (%s)", domain, time.Since(start).Round(time.Millisecond))
		}(tailnetNode)
		redirectListener, err = tailnetNode.ListenHTTP()
		if err != nil {
			closeListeners()
			log.Printf("tailnet: http listen error: %v", err)
			return exitRuntimeError
		}
		redirectServer = newHTTPServer(":80", tailnet.RedirectHandler(domain))
	}
	httpErrors := make(chan error, len(listeners)+1)

	listenerNames := make([]string, 0, len(listeners))
	for _, l := range listeners {
		listenerNames = append(listenerNames, l.name)
		go func() {
			log.Printf("cronlite: http server listening on %s", l.name)
			if err := httpServer.Serve(l.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				httpErrors <- fmt.Errorf("%s: %w", l.name, err)
			}
		}()
	}
	if redirectServer != nil {
		go func() {
			if err := redirectServer.Serve(redirectListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				httpErrors <- fmt.Errorf("tailnet http redirect: %w", err)
			}
		}()
	}

	// Dispatcher runs on all instances regardless of dispatch mode.
	dispatcherCtx, cancelDispatcher := context.WithCancel(context.Background())
	var dispatcherWg sync.WaitGroup

	dispatcherWg.Add(1)
	go func() {
		defer dispatcherWg.Done()
		if cfg.DispatchMode == "db" {
			disp.RunDBPoll(dispatcherCtx, cfg.DBPollInterval, cfg.DispatcherWorkers)
		} else {
			disp.Run(dispatcherCtx, dispatchCh)
		}
	}()

	// shutdown stops scheduler+reconciler (or leader election, which stops them indirectly).
	var shutdown func()

	if cfg.DispatchMode == "db" {
		// Scheduler and reconciler run only on the leader instance.
		leaderCtx, cancelLeader := context.WithCancel(context.Background())

		lr := &leaderRuntime{
			sched:       sched,
			metricsSink: metricsSink,
			cfg:         cfg,
			store:       store,
			emitter:     emitter,
		}

		elector := leaderelection.New(
			db, cfg.LeaderLockKey,
			cfg.LeaderRetryInterval, cfg.LeaderHeartbeatInterval,
			func(electedCtx context.Context) { lr.start(electedCtx) },
			func() { lr.stop() },
		)
		if metricsSink != nil {
			elector = elector.WithMetrics(metricsSink)
		}

		var electorWg sync.WaitGroup
		electorWg.Add(1)
		go func() {
			defer electorWg.Done()
			elector.Run(leaderCtx)
		}()

		shutdown = func() {
			log.Println("cronlite: stopping leader election...")
			cancelLeader()
			electorWg.Wait()
			log.Println("cronlite: leader election stopped")
		}

		log.Printf("cronlite: leader election enabled (lock_key=%d, retry=%s, heartbeat=%s)",
			cfg.LeaderLockKey, cfg.LeaderRetryInterval, cfg.LeaderHeartbeatInterval)
		log.Printf("cronlite: IMPORTANT: if multiple CronLite clusters share this Postgres instance, each must use a distinct LEADER_LOCK_KEY (current: %d) or they will silently compete for the same lock", cfg.LeaderLockKey)
	} else {
		// Channel mode: no leader election needed.
		schedulerCtx, cancelScheduler := context.WithCancel(context.Background())
		var schedulerWg sync.WaitGroup

		schedulerWg.Add(1)
		go func() {
			defer schedulerWg.Done()
			if err := sched.Run(schedulerCtx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("cronlite: scheduler stopped with error: %v", err)
			}
		}()

		var reconcilerWg sync.WaitGroup
		var cancelReconciler context.CancelFunc

		if cfg.ReconcileEnabled {
			var reconcilerCtx context.Context
			reconcilerCtx, cancelReconciler = context.WithCancel(context.Background())
			recon := reconciler.New(
				reconciler.Config{
					Interval:  cfg.ReconcileInterval,
					Threshold: cfg.ReconcileThreshold,
					BatchSize: cfg.ReconcileBatchSize,
				},
				store,
				emitter,
			)
			if metricsSink != nil {
				recon = recon.WithMetrics(metricsSink)
			}
			reconcilerWg.Add(1)
			go func() {
				defer reconcilerWg.Done()
				recon.Run(reconcilerCtx)
			}()
			log.Printf("cronlite: reconciler enabled (interval=%s, threshold=%s, requeue_threshold=%s, batch=%d)",
				cfg.ReconcileInterval, cfg.ReconcileThreshold, cfg.ReconcileRequeueThreshold, cfg.ReconcileBatchSize)
		} else {
			log.Println("cronlite: RECONCILE_ENABLED not set; reconciler disabled")
		}

		shutdown = func() {
			log.Println("cronlite: stopping scheduler...")
			cancelScheduler()
			schedulerWg.Wait()
			log.Println("cronlite: scheduler stopped")

			if cancelReconciler != nil {
				log.Println("cronlite: stopping reconciler...")
				cancelReconciler()
				reconcilerWg.Wait()
				log.Println("cronlite: reconciler stopped")
			}
		}
	}

	log.Printf("cronlite: started (tick=%s, http=%s, dispatch_mode=%s)", cfg.TickInterval, strings.Join(listenerNames, ","), cfg.DispatchMode)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	exitCode := exitSuccess
	select {
	case received := <-sig:
		log.Printf("cronlite: received signal %v, shutting down", received)
	case err := <-httpErrors:
		log.Printf("cronlite: http server error: %v, shutting down", err)
		exitCode = exitRuntimeError
	}

	// HTTP handlers can emit manual triggers too. Finish ingress before stopping
	// producers and draining the dispatcher; close MCP streams so they cannot
	// hold HTTP shutdown open indefinitely.
	log.Println("cronlite: stopping http server...")
	httpShutdownCtx, httpShutdownCancel := context.WithTimeout(context.Background(), cfg.HTTPShutdownTimeout)
	if err := mcpShutdown(httpShutdownCtx); err != nil {
		log.Printf("mcp: shutdown error: %v", err)
	}
	if redirectServer != nil {
		if err := redirectServer.Shutdown(httpShutdownCtx); err != nil {
			_ = redirectServer.Close()
		}
	}
	if err := httpServer.Shutdown(httpShutdownCtx); err != nil {
		log.Printf("cronlite: http server shutdown error: %v", err)
		cancelHTTP()
		_ = httpServer.Close()
	}
	httpShutdownCancel()
	log.Println("cronlite: http server stopped")
	shutdown()

	log.Println("cronlite: stopping dispatcher (draining events)...")
	cancelDispatcher()
	dispatcherWg.Wait()
	log.Println("cronlite: dispatcher stopped")

	// The tailnet node outlives the dispatcher so in-flight tailnet deliveries
	// can finish draining.
	if tailnetNode != nil {
		closeTailnet(tailnetNode)
		tailnetNode = nil
	}

	log.Println("cronlite: stopped")
	return exitCode
}

func closeTailnet(n *tailnet.Node) {
	if err := n.Close(); err != nil {
		log.Printf("tailnet: shutdown error: %v", err)
		return
	}
	log.Println("tailnet: node stopped")
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		// ReadTimeout bounds header and request-body reads; SSE streams responses.
		ReadTimeout: httpReadTimeout,
		IdleTimeout: httpIdleTimeout,
		// WriteTimeout intentionally remains zero because MCP uses long-lived SSE.
	}
}

func runValidate() int {
	cfg := config.Load()

	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return exitInvalidConfig
	}

	fmt.Println("configuration valid")
	return exitSuccess
}

func runConfig() int {
	cfg := config.Load()

	data, err := cfg.MaskedJSON()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to marshal config: %v\n", err)
		return exitRuntimeError
	}

	fmt.Println(string(data))
	return exitSuccess
}

func runVersion() int {
	fmt.Printf("cronlite version %s (commit: %s)\n", version, commit)
	return exitSuccess
}

func runCreateKey() int {
	args := os.Args[2:]
	if len(args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: cronlite create-key <namespace> <label>\n")
		return exitRuntimeError
	}
	namespace := args[0]
	label := args[1]

	cfg := config.Load()

	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open database: %v\n", err)
		return exitRuntimeError
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to database: %v\n", err)
		return exitRuntimeError
	}

	store := postgres.New(db, cfg.DBOpTimeout)
	svcParser := cron.NewParser()
	svc := service.NewJobService(store, store, store, store, store, store, svcParser)

	ctx := domain.NamespaceToContext(context.Background(), domain.Namespace(namespace))
	result, err := svc.CreateAPIKey(ctx, service.CreateAPIKeyInput{
		Label: label,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create API key: %v\n", err)
		return exitRuntimeError
	}

	fmt.Printf("API Key created:\n")
	fmt.Printf("  ID:        %s\n", result.Key.ID)
	fmt.Printf("  Namespace: %s\n", result.Key.Namespace)
	fmt.Printf("  Label:     %s\n", result.Key.Label)
	fmt.Printf("  Token:     %s\n", result.PlaintextToken)
	fmt.Printf("\nSave this token — it cannot be retrieved again.\n")

	return exitSuccess
}

func probeExecutionOwnership(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), domain.OwnershipOperationTimeout)
	defer cancel()
	for _, query := range []string{"SELECT claimed_at, claim_generation, lease_until FROM executions LIMIT 0", "SELECT claim_generation FROM delivery_attempts LIMIT 0"} {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("apply migrations 003 and 011_execution_ownership.sql: %w", err)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

// logConfigWarnings emits startup warnings for dangerous or suboptimal
// configuration combinations. It runs after config validation passes and
// before any connections are opened.
// apiKeyChecker is the subset of the store used to report authentication
// status at startup.
type apiKeyChecker interface {
	HasAnyAPIKeys(ctx context.Context) (bool, error)
}

// logAuthStatus reports how API requests will authenticate. Namespace-scoped
// keys are always enforced, so the only actionable cases are the deprecated
// API_KEY fallback and an installation that has no keys yet.
func logAuthStatus(ctx context.Context, legacyKey string, keys apiKeyChecker) {
	if legacyKey != "" {
		log.Println("cronlite: API key authentication enabled (multi-key + DEPRECATED legacy fallback)")
		log.Println("WARNING [P2]: API_KEY env var is set — legacy single-key auth is DEPRECATED. Create namespace-scoped keys via 'cronlite create-key' and remove API_KEY.")
		return
	}

	hasKeys, err := keys.HasAnyAPIKeys(ctx)
	if err != nil {
		log.Printf("cronlite: could not check for API keys: %v", err)
		return
	}
	if !hasKeys {
		log.Println("INFO: no API keys exist yet — all authenticated endpoints will return 401. Create one with 'cronlite create-key <namespace> <label>' or via /admin/setup.")
	}
}

func logConfigWarnings(cfg *config.Config) {
	if cfg.DispatchMode == "channel" && !cfg.ReconcileEnabled {
		log.Printf("WARNING [P0]: DISPATCH_MODE=channel with RECONCILE_ENABLED=false — orphaned executions from buffer overflow will be PERMANENTLY LOST. Set RECONCILE_ENABLED=true for production.")
	}

	if cfg.DispatchMode == "db" && !cfg.ReconcileEnabled {
		log.Printf("WARNING [P0]: DISPATCH_MODE=db with RECONCILE_ENABLED=false — the reconciler is the crash recovery mechanism for in_progress executions. Without it, a dispatcher worker crash leaves executions permanently stuck in in_progress status with no automatic recovery. Set RECONCILE_ENABLED=true.")
	}

	if !cfg.ReconcileEnabled {
		log.Printf("WARNING [P0]: RECONCILE_ENABLED=false — no automatic orphan recovery. Crashed or timed-out executions will remain stuck. Set RECONCILE_ENABLED=true for production.")
	}

	if !cfg.MetricsEnabled {
		log.Printf("WARNING [P1]: METRICS_ENABLED=false — Prometheus metrics unavailable. You will have NO visibility into buffer saturation, orphans, or delivery outcomes.")
	}

	if cfg.DispatchMode == "channel" {
		log.Printf("INFO: DISPATCH_MODE=channel — single-instance only. Use db mode for supported multi-instance scheduling and failover.")
	}

	if cfg.DispatchMode == "db" && cfg.DispatcherWorkers == 1 {
		log.Printf("INFO: DISPATCH_MODE=db with DISPATCHER_WORKERS=1 — consider increasing to 2-4 for production workloads.")
	}

	if cfg.Environment == "benchmark" &&
		cfg.ReconcileRequeueThreshold > 0 &&
		cfg.ReconcileRequeueThreshold < 30*time.Second {
		log.Printf(
			"WARNING [P0]: RECONCILE_REQUEUE_THRESHOLD=%s is a benchmark-only short lease; short leases can expire during delays; ownership is fenced but remote effects may overlap.",
			cfg.ReconcileRequeueThreshold,
		)
	}

	if strings.Contains(cfg.DatabaseURL, "sslmode=disable") {
		log.Printf("WARNING [P1]: DATABASE_URL contains sslmode=disable — database connections are NOT encrypted. Use sslmode=require or sslmode=verify-full for production.")
	}
}

// runMigrate applies pending schema migrations, or with --status lists the
// state of every known migration.
func runMigrate(args []string) int {
	status := false
	for _, a := range args {
		switch a {
		case "--status":
			status = true
		default:
			fmt.Fprintf(os.Stderr, "usage: cronlite migrate [--status]\n")
			return exitInvalidConfig
		}
	}

	cfg := config.Load()
	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		return exitInvalidConfig
	}

	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open database: %v\n", err)
		return exitRuntimeError
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to database: %v\n", err)
		return exitRuntimeError
	}

	ctx := context.Background()

	if status {
		ms, err := migrate.Load(schema.FS)
		if err != nil {
			fmt.Fprintf(os.Stderr, "migration status failed: %v\n", err)
			return exitRuntimeError
		}
		rows, err := migrate.StatusOf(ctx, db, ms)
		if err != nil {
			fmt.Fprintf(os.Stderr, "migration status failed: %v\n", err)
			return exitRuntimeError
		}
		if pre, err := migrate.NeedsBaseline(ctx, db); err != nil {
			fmt.Fprintf(os.Stderr, "migration status failed: %v\n", err)
			return exitRuntimeError
		} else if pre {
			fmt.Println("note: no migration tracking table yet; the existing schema will be baselined on the first run")
		}
		for _, r := range rows {
			fmt.Printf("%03d  %-40s  %s\n", r.Version, r.Name, r.State)
		}
		return exitSuccess
	}

	res, err := applyMigrations(ctx, db, cfg.MigrateLockTimeout, log.Printf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		return exitRuntimeError
	}
	fmt.Printf("applied: %v\nbaselined: %v\n", res.Applied, res.Baselined)
	return exitSuccess
}

// applyMigrations loads the embedded schema migrations and applies any that
// are pending. It is shared by `cronlite migrate` and MIGRATE_ON_START.
func applyMigrations(ctx context.Context, db *sql.DB, lockTimeout time.Duration, logf func(string, ...any)) (migrate.Result, error) {
	ms, err := migrate.Load(schema.FS)
	if err != nil {
		return migrate.Result{}, err
	}
	return migrate.Run(ctx, db, ms, migrate.Options{LockTimeout: lockTimeout, Logf: logf})
}
