package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
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
	"github.com/djlord-it/cronlite/internal/reconciler"
	"github.com/djlord-it/cronlite/internal/scheduler"
	"github.com/djlord-it/cronlite/internal/service"
	"github.com/djlord-it/cronlite/internal/store/postgres"
	"github.com/djlord-it/cronlite/internal/transport/channel"

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
	return &cronScheduleAdapter{sched: sched}, nil
}

type cronScheduleAdapter struct {
	sched cron.Schedule
}

func (a *cronScheduleAdapter) Next(after time.Time) time.Time {
	return a.sched.Next(after)
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

Environment Variables:
  CRONLITE_ENV              Runtime environment; "production" enables strict validation
  DATABASE_URL              PostgreSQL connection string (required)
  REDIS_ADDR                Redis address for analytics (optional)
  HTTP_ADDR                 HTTP server address (default: ":8080")
  API_KEY                   API key for Bearer authentication (optional, recommended)
  TICK_INTERVAL             Scheduler tick interval (default: "30s")

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
  DISPATCHER_WORKERS        Concurrent dispatch workers (default: "1", db mode only)

  LEADER_LOCK_KEY           Advisory lock key for leader election (default: "728379", db mode only)
  LEADER_RETRY_INTERVAL     Follower lock acquisition retry interval (default: "5s", db mode only)
  LEADER_HEARTBEAT_INTERVAL Leader connection health check interval (default: "2s", db mode only)

  METRICS_ENABLED           Enable Prometheus metrics (default: "false")
  METRICS_PATH              Metrics endpoint path (default: "/metrics")
  METRICS_PUBLIC            Allow unauthenticated metrics access (default: "false")

  RECONCILE_ENABLED         Enable orphan execution reconciler (default: "false")
  RECONCILE_INTERVAL        How often to scan for orphans (default: "5m")
  RECONCILE_THRESHOLD       Age before emitted execution is orphaned (default: "15m")
  RECONCILE_REQUEUE_THRESHOLD Age before in_progress execution is requeued (default: "2m", db mode)
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
	recon       *reconciler.Reconciler
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

func (lr *leaderRuntime) start(leaderCtx context.Context) {
	lr.mu.Lock()
	defer lr.mu.Unlock()

	if lr.running {
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
				Interval:         lr.cfg.ReconcileInterval,
				Threshold:        lr.cfg.ReconcileThreshold,
				RequeueThreshold: lr.cfg.ReconcileRequeueThreshold,
				BatchSize:        lr.cfg.ReconcileBatchSize,
			},
			lr.store,
			lr.emitter,
		)
		if lr.metricsSink != nil {
			recon = recon.WithMetrics(lr.metricsSink)
		}
		lr.recon = recon

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

	if !lr.running {
		lr.mu.Unlock()
		return
	}

	if lr.cancelScheduler != nil {
		lr.cancelScheduler()
	}
	if lr.cancelReconciler != nil {
		lr.cancelReconciler()
	}

	lr.running = false
	lr.mu.Unlock()

	// Wait outside the lock so Run goroutines can return without deadlock.
	lr.wg.Wait()
	log.Println("cronlite: leader duties stopped (scheduler + reconciler)")
}

func runServe() int {
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

	if cfg.DispatchMode == "db" {
		if err := probeClaimedAtColumn(db); err != nil {
			fmt.Fprintf(os.Stderr, "DISPATCH_MODE=db migration probe failed: %v\n", err)
			return exitInvalidConfig
		}
	}

	store := postgres.New(db, cfg.DBOpTimeout)
	cronParser := &cronParserAdapter{parser: cron.NewParser()}
	webhookSender := dispatcher.NewHTTPWebhookSender()

	var metricsSink *metrics.PrometheusSink

	if cfg.MetricsEnabled {
		metricsSink = metrics.NewPrometheusSink(prometheus.DefaultRegisterer)
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
	svcParser := cron.NewParser()
	jobService := service.NewJobService(store, store, store, store, store, store, svcParser)

	if cfg.DispatchMode == "channel" {
		jobService = jobService.WithEmitter(emitter)
	}

	// ── REST transport (oapi-codegen strict handler) ──────────────────────────
	serverImpl := api.NewServerImpl(jobService).WithHealthChecker(db)
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
	if cfg.APIKey != "" {
		log.Println("cronlite: API key authentication enabled (multi-key + DEPRECATED legacy fallback)")
		log.Println("WARNING [P2]: API_KEY env var is set — legacy single-key auth is DEPRECATED. Create namespace-scoped keys via 'cronlite create-key' and remove API_KEY.")
	} else {
		log.Println("WARNING [P0]: API_KEY not set — API endpoints are unauthenticated. Set API_KEY for production.")
	}
	log.Printf("cronlite: rate limits: %d req/s per IP, %d req/s per namespace", cfg.IPRateLimit, cfg.NamespaceRateLimit)

	// ── MCP transport (embedded SSE for AI agents) ──────────────────────────
	mcpServer := mcpsrv.NewServer(jobService)
	mcpHandler := mcpsrv.MountHTTP(mcpServer, store, cfg.APIKey, cfg.NamespaceRateLimit)
	mcpHandler = api.RateLimitMiddleware(cfg.IPRateLimit, mcpHandler)
	log.Printf("mcp: SSE transport mounted at /mcp")

	httpMux := http.NewServeMux()
	if cfg.MetricsEnabled {
		httpMux.Handle(cfg.MetricsPath, newMetricsHandler(appCtx, store, cfg.APIKey, cfg.MetricsPublic))
	}
	httpMux.Handle("/mcp", mcpHandler)
	httpMux.Handle("/", rootHandler)

	// HTTP server runs on all instances regardless of dispatch mode.
	httpServer := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: httpMux,
	}

	go func() {
		log.Printf("cronlite: http server listening on %s", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("cronlite: http server error: %v", err)
		}
	}()

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
					Interval:         cfg.ReconcileInterval,
					Threshold:        cfg.ReconcileThreshold,
					RequeueThreshold: cfg.ReconcileRequeueThreshold,
					BatchSize:        cfg.ReconcileBatchSize,
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

	log.Printf("cronlite: started (tick=%s, http=%s, dispatch_mode=%s)", cfg.TickInterval, cfg.HTTPAddr, cfg.DispatchMode)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	received := <-sig

	log.Printf("cronlite: received signal %v, shutting down", received)

	// Shutdown order: scheduler/reconciler first, then dispatcher (drains buffered
	// events), then HTTP server. This ensures no new events are emitted while the
	// dispatcher finishes in-flight work.
	shutdown()

	log.Println("cronlite: stopping dispatcher (draining events)...")
	cancelDispatcher()
	dispatcherWg.Wait()
	log.Println("cronlite: dispatcher stopped")

	log.Println("cronlite: stopping http server...")
	httpShutdownCtx, httpShutdownCancel := context.WithTimeout(context.Background(), cfg.HTTPShutdownTimeout)
	defer httpShutdownCancel()
	if err := httpServer.Shutdown(httpShutdownCtx); err != nil {
		log.Printf("cronlite: http server shutdown error: %v", err)
	}
	log.Println("cronlite: http server stopped")

	log.Println("cronlite: stopped")
	return exitSuccess
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

// probeClaimedAtColumn checks that the claimed_at column exists on the
// executions table, which is required for DB dispatch mode (migration 003).
func probeClaimedAtColumn(db *sql.DB) error {
	var col string
	err := db.QueryRow(
		"SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name='executions' AND column_name='claimed_at'",
	).Scan(&col)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("column 'claimed_at' not found on 'executions' table — apply migration 003_add_claimed_at.sql")
	}
	return err
}

// logConfigWarnings emits startup warnings for dangerous or suboptimal
// configuration combinations. It runs after config validation passes and
// before any connections are opened.
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
		log.Printf("INFO: DISPATCH_MODE=channel — single-instance only. DO NOT run multiple instances in this mode (causes duplicate webhooks with no coordination).")
	}

	if cfg.DispatchMode == "db" && cfg.DispatcherWorkers == 1 {
		log.Printf("INFO: DISPATCH_MODE=db with DISPATCHER_WORKERS=1 — consider increasing to 2-4 for production workloads.")
	}

	if cfg.DispatchMode == "db" && cfg.ReconcileEnabled && cfg.ReconcileRequeueThreshold > 5*time.Minute {
		log.Printf("WARNING [P1]: RECONCILE_REQUEUE_THRESHOLD=%s is very conservative. FOR UPDATE SKIP LOCKED prevents premature requeue, so values of 1-2m are safe and improve crash recovery time.", cfg.ReconcileRequeueThreshold)
	}

	if strings.Contains(cfg.DatabaseURL, "sslmode=disable") {
		log.Printf("WARNING [P1]: DATABASE_URL contains sslmode=disable — database connections are NOT encrypted. Use sslmode=require or sslmode=verify-full for production.")
	}
}
