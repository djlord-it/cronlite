package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
)

const (
	exitSuccess       = 0
	exitRuntime       = 1
	exitConfiguration = 2
	exitCorrectness   = 3
)

type diagnosticHandle interface {
	diagnosticReader
	resources(context.Context) (DatabaseResources, error)
	Close() error
}

type runtimeDependencies struct {
	Stdout             io.Writer
	Stderr             io.Writer
	StartReceiver      func(string, string, ReceiverLimits) (*receiverServer, error)
	NewAPI             func(string, string, time.Duration) scenarioAPI
	OpenDiagnostic     func(string) (diagnosticHandle, error)
	NewMetrics         func(string, string, time.Duration) metricsReader
	NewController      func(Config) *composeController
	RunScenarios       func(context.Context, *scenarioEnvironment) []ScenarioResult
	CaptureEnvironment func(context.Context, Config, *composeController) EnvironmentInfo
	WriteOutputs       func(string, RunResult) (OutputPaths, error)
}

func defaultRuntimeDependencies() runtimeDependencies {
	return runtimeDependencies{
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
		StartReceiver: startReceiver,
		NewAPI: func(baseURL, apiKey string, timeout time.Duration) scenarioAPI {
			return newAPIClient(baseURL, apiKey, timeout)
		},
		OpenDiagnostic: func(databaseURL string) (diagnosticHandle, error) {
			return openDiagnosticCollector(databaseURL)
		},
		NewMetrics: func(metricsURL, apiKey string, timeout time.Duration) metricsReader {
			return newPrometheusCollector(metricsURL, apiKey, timeout)
		},
		NewController:      newComposeController,
		RunScenarios:       runSelectedScenarios,
		CaptureEnvironment: captureEnvironment,
		WriteOutputs:       writeOutputs,
	}
}

func main() {
	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], defaultRuntimeDependencies()))
}

func run(ctx context.Context, args []string, deps runtimeDependencies) int {
	cfg, command, err := parseConfig(args, deps.Stderr)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "benchmark configuration: %v\n", err)
		return exitConfiguration
	}
	runID := uuid.NewString()
	if cfg.StartCompose && cfg.ComposeProject == "" {
		cfg.ComposeProject = benchmarkComposePrefix + strings.ReplaceAll(runID, "-", "")[:12]
	}

	receiver, err := deps.StartReceiver(
		cfg.ReceiverAddr,
		cfg.WebhookSecret,
		defaultReceiverLimits(),
	)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "start webhook receiver: %v\n", err)
		return exitRuntime
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = receiver.Close(closeCtx)
	}()

	var controller *composeController
	if cfg.StartCompose || cfg.ComposeProject != "" {
		controller = deps.NewController(cfg)
	}
	if cfg.StartCompose {
		if err := controller.up(ctx); err != nil {
			fmt.Fprintf(deps.Stderr, "start benchmark environment: %v\n", err)
			return exitRuntime
		}
		if cfg.CleanupEnvironment {
			defer func() {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				_ = controller.down(cleanupCtx, true)
			}()
		}
	}

	api := deps.NewAPI(cfg.BaseURL, cfg.APIKey, cfg.Timeout)
	if cfg.StartCompose {
		readinessCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		readinessErr := waitForAPIReadiness(readinessCtx, api, 250*time.Millisecond)
		cancel()
		if readinessErr != nil {
			fmt.Fprintf(deps.Stderr, "wait for CronLite readiness: %v\n", readinessErr)
			return exitRuntime
		}
	}
	var diagnostic diagnosticHandle
	if cfg.Diagnostic {
		diagnostic, err = deps.OpenDiagnostic(cfg.DatabaseURL)
		if err != nil {
			fmt.Fprintf(deps.Stderr, "open diagnostic database: %v\n", err)
			return exitRuntime
		}
		defer diagnostic.Close()
	}
	var metrics metricsReader
	if cfg.MetricsURL != "" && deps.NewMetrics != nil {
		metrics = deps.NewMetrics(cfg.MetricsURL, cfg.APIKey, cfg.Timeout)
	}

	env := &scenarioEnvironment{
		Config:       cfg,
		RunID:        runID,
		API:          api,
		Receiver:     receiver.store,
		Diagnostic:   diagnostic,
		Metrics:      metrics,
		Capabilities: detectCapabilities(controller),
		Controller:   controller,
	}
	result := RunResult{
		SchemaVersion: resultSchemaVersion,
		RunID:         runID,
		RandomSeed:    cfg.RandomSeed,
		Command:       command,
		StartedAt:     time.Now().UTC(),
		Config:        cfg.Redacted(),
		Environment:   deps.CaptureEnvironment(ctx, cfg, controller),
		Limitations:   benchmarkLimitations(),
	}

	if metrics != nil {
		before, _, snapshotErr := metrics.snapshot(ctx)
		if snapshotErr == nil {
			result.MetricsBefore = before
		}
	}
	if diagnostic != nil {
		if resources, resourceErr := diagnostic.resources(ctx); resourceErr == nil {
			result.Resources = append(result.Resources, resources)
		}
	}

	result.Scenarios = deps.RunScenarios(ctx, env)
	cleanupObservations := env.cleanup(ctx)
	if len(cleanupObservations) > 0 {
		now := time.Now().UTC()
		result.Scenarios = append(result.Scenarios, ScenarioResult{
			Name:         "cleanup",
			Status:       ScenarioPassed,
			StartedAt:    now,
			FinishedAt:   time.Now().UTC(),
			Observations: cleanupObservations,
		})
	}

	if metrics != nil {
		after, _, snapshotErr := metrics.snapshot(ctx)
		if snapshotErr == nil {
			result.MetricsAfter = after
			result.MetricsDelta = metricDelta(result.MetricsBefore, after)
		}
	}
	if diagnostic != nil {
		if resources, resourceErr := diagnostic.resources(ctx); resourceErr == nil {
			result.Resources = append(result.Resources, resources)
		}
	}

	result.FinishedAt = time.Now().UTC()
	result.Findings = collectRunFindings(result.Scenarios)
	paths, err := deps.WriteOutputs(cfg.OutputDir, result)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "write benchmark outputs: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(deps.Stdout, "JSON: %s\nCSV: %s\nReport: %s\n", paths.JSON, paths.CSV, paths.Markdown)

	if cfg.FailOnCorrectness && hasCriticalFinding(result.Findings) {
		return exitCorrectness
	}
	for _, scenario := range result.Scenarios {
		if scenario.Status == ScenarioFailed && len(scenario.Findings) == 0 {
			return exitRuntime
		}
	}
	return exitSuccess
}

func parseConfig(args []string, stderr io.Writer) (Config, string, error) {
	cfg := defaultConfig()
	cfg.APIKey = os.Getenv("CRONLITE_API_KEY")
	cfg.DatabaseURL = os.Getenv("CRONLITE_BENCHMARK_DATABASE_URL")
	var scenarioList string
	var concurrencyList string

	flags := flag.NewFlagSet("cronlite-benchmark", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.BaseURL, "base-url", cfg.BaseURL, "CronLite base URL")
	flags.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "CronLite API key (prefer CRONLITE_API_KEY)")
	flags.StringVar(&cfg.ReceiverAddr, "receiver-addr", cfg.ReceiverAddr, "webhook receiver listen address")
	flags.StringVar(
		&cfg.ReceiverPublicURL,
		"receiver-public-url",
		cfg.ReceiverPublicURL,
		"webhook receiver URL reachable by CronLite",
	)
	flags.StringVar(&scenarioList, "scenario", "smoke", "comma-separated scenarios or all")
	flags.IntVar(&cfg.SampleCount, "sample-count", cfg.SampleCount, "measured samples per scenario")
	flags.StringVar(&concurrencyList, "concurrency", "1,5,10,25,50", "comma-separated concurrency levels")
	flags.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "HTTP and execution timeout")
	flags.DurationVar(&cfg.PollInterval, "poll-interval", cfg.PollInterval, "execution polling interval")
	flags.DurationVar(
		&cfg.RequeueThreshold,
		"requeue-threshold",
		cfg.RequeueThreshold,
		"configured stale claim requeue threshold",
	)
	flags.BoolVar(&cfg.Diagnostic, "diagnostic", false, "enable read-only PostgreSQL diagnostics")
	flags.StringVar(&cfg.DatabaseURL, "database-url", cfg.DatabaseURL, "diagnostic PostgreSQL URL")
	flags.StringVar(&cfg.MetricsURL, "metrics-url", cfg.MetricsURL, "Prometheus metrics URL")
	flags.StringVar(&cfg.OutputDir, "output", cfg.OutputDir, "output directory")
	flags.Int64Var(&cfg.RandomSeed, "random-seed", cfg.RandomSeed, "stable random seed")
	flags.StringVar(&cfg.WebhookSecret, "webhook-secret", cfg.WebhookSecret, "benchmark webhook secret")
	flags.StringVar(&cfg.RetryProfile, "retry-profile", cfg.RetryProfile, "fast-test or real-policy")
	flags.BoolVar(&cfg.AllowDisruptive, "allow-disruptive", false, "allow guarded load/recovery scenarios")
	flags.BoolVar(&cfg.AllowNonLocal, "allow-non-local", false, "allow a non-loopback CronLite target")
	flags.BoolVar(&cfg.FailOnCorrectness, "fail-on-correctness", false, "exit 3 on correctness findings")
	flags.BoolVar(&cfg.KeepData, "keep-data", false, "keep benchmark-created jobs")
	flags.BoolVar(&cfg.StartCompose, "start-compose", false, "start the isolated benchmark Compose stack")
	flags.BoolVar(
		&cfg.CleanupEnvironment,
		"cleanup-environment",
		false,
		"remove the harness-owned Compose project and volume after the run",
	)
	flags.StringVar(&cfg.ComposeFile, "compose-file", cfg.ComposeFile, "benchmark Compose file")
	flags.StringVar(&cfg.ComposeProject, "compose-project", "", "harness-owned Compose project name")
	flags.StringVar(&cfg.DispatchMode, "dispatch-mode", cfg.DispatchMode, "channel, db, or unknown")
	if err := flags.Parse(args); err != nil {
		return cfg, "", err
	}
	if flags.NArg() != 0 {
		return cfg, "", fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}

	cfg.Scenarios = splitList(scenarioList)
	if len(cfg.Scenarios) == 1 && cfg.Scenarios[0] == "all" {
		cfg.Scenarios = append([]string(nil), knownScenarios...)
	}
	concurrency, err := parseIntegerList(concurrencyList)
	if err != nil {
		return cfg, "", err
	}
	cfg.Concurrency = concurrency

	defaults := defaultConfig()
	if cfg.StartCompose {
		if cfg.BaseURL == defaults.BaseURL {
			cfg.BaseURL = "http://127.0.0.1:18080"
		}
		if cfg.APIKey == "" {
			cfg.APIKey = "benchmark-local-key"
		}
		if cfg.ReceiverAddr == defaults.ReceiverAddr {
			cfg.ReceiverAddr = "0.0.0.0:19090"
		}
		if cfg.ReceiverPublicURL == defaults.ReceiverPublicURL {
			cfg.ReceiverPublicURL = "http://host.docker.internal:19090"
		}
		if cfg.MetricsURL == defaults.MetricsURL {
			cfg.MetricsURL = "http://127.0.0.1:18080/metrics"
		}
		if cfg.Diagnostic && cfg.DatabaseURL == "" {
			cfg.DatabaseURL = "postgres://cronlite:cronlite@127.0.0.1:15432/cronlite?sslmode=disable"
		}
		if cfg.RequeueThreshold == defaults.RequeueThreshold {
			cfg.RequeueThreshold = 5 * time.Second
		}
		cfg.DispatchMode = "db"
	}
	if err := cfg.Validate(); err != nil {
		return cfg, "", err
	}
	return cfg, redactText("go run ./tools/benchmark " + joinCommandArgs(args)), nil
}

func splitList(raw string) []string {
	var values []string
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

func parseIntegerList(raw string) ([]int, error) {
	values := splitList(raw)
	result := make([]int, 0, len(values))
	for _, value := range values {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("invalid concurrency %q: %w", value, err)
		}
		result = append(result, parsed)
	}
	return result, nil
}

func joinCommandArgs(args []string) string {
	quoted := make([]string, len(args))
	for index, arg := range args {
		if strings.ContainsAny(arg, " \t\n\"'") {
			quoted[index] = strconv.Quote(arg)
		} else {
			quoted[index] = arg
		}
	}
	return strings.Join(quoted, " ")
}

func collectRunFindings(scenarios []ScenarioResult) []Finding {
	var findings []Finding
	for _, scenario := range scenarios {
		findings = append(findings, scenario.Findings...)
		for _, execution := range scenario.Executions {
			findings = append(findings, execution.Findings...)
		}
	}
	return deduplicateFindings(findings)
}

func hasCriticalFinding(findings []Finding) bool {
	for _, finding := range findings {
		if finding.Severity == SeverityCritical {
			return true
		}
	}
	return false
}

func benchmarkLimitations() []string {
	return []string{
		"CronLite recurring cron resolution is one minute; manual and recurring measurements are separate.",
		"Cross-process wall-clock measurements depend on clock synchronization.",
		"Same-machine Docker results include operating-system, virtualization, and resource-contention noise.",
		"Local results are regression baselines, not universal production capacity guarantees.",
		"High percentiles are unstable at small sample counts.",
		"Production network conditions may differ substantially.",
		"Diagnostic database inspection is internal test visibility, not the customer experience.",
		"A benchmark can reveal duplicates but cannot prove the absence of every race.",
		"Terminal status update time and worker identity are not persisted by current instrumentation.",
		"The public execution API does not expose delivery attempts or claimed_at.",
		"Fast retry injection is unavailable; real-policy retries use production backoff durations.",
		"Unavailable Docker, PostgreSQL, permission, and platform capabilities are reported as skipped.",
	}
}

type healthAPI interface {
	health(context.Context, bool) (Observation, error)
}

func waitForAPIReadiness(
	ctx context.Context,
	api healthAPI,
	interval time.Duration,
) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		observation, err := api.health(ctx, true)
		if err == nil && observation.StatusCode == 200 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
