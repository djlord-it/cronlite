package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

type scenarioAPI interface {
	health(context.Context, bool) (Observation, error)
	createJob(context.Context, CreateJobInput) (APIJob, Observation, error)
	getJob(context.Context, string) (APIJob, Observation, error)
	updateJob(context.Context, string, UpdateJobInput) (APIJob, Observation, error)
	pauseJob(context.Context, string) (Observation, error)
	resumeJob(context.Context, string) (Observation, error)
	deleteJob(context.Context, string) (Observation, error)
	trigger(context.Context, string) (APIExecution, Observation, error)
	getExecution(context.Context, string) (APIExecution, Observation, error)
	listExecutions(context.Context, string) ([]APIExecution, Observation, error)
	pollTerminal(context.Context, string, time.Duration) (PollBounds, error)
}

type diagnosticReader interface {
	execution(context.Context, string) (DiagnosticExecution, error)
	ping(context.Context) (Observation, error)
}

type metricsReader interface {
	snapshot(context.Context) (metricSnapshot, Observation, error)
}

type scenarioEnvironment struct {
	Config       Config
	RunID        string
	API          scenarioAPI
	Receiver     *callbackStore
	Diagnostic   diagnosticReader
	Metrics      metricsReader
	Capabilities Capabilities
	Controller   *composeController
	createdMu    sync.Mutex
	createdJobs  []string
}

type scenarioFunc func(context.Context, *scenarioEnvironment) ScenarioResult

var scenarioRegistryOverride map[string]scenarioFunc

func scenarioRegistry() map[string]scenarioFunc {
	if scenarioRegistryOverride != nil {
		return scenarioRegistryOverride
	}
	return map[string]scenarioFunc{
		"smoke":           runSmoke,
		"baseline":        runBaseline,
		"cold-warm":       runColdWarm,
		"warm-sequential": runWarmSequential,
		"concurrent":      runConcurrent,
		"control-plane":   runControlPlane,
		"recurring":       runRecurring,
		"slow-receiver":   runSlowReceiver,
		"retry":           runRetry,
		"duplicate-race":  runDuplicateRace,
		"crash-recovery":  runCrashRecovery,
		"leader-failover": runLeaderFailover,
		"database-outage": runDatabaseOutage,
		"load":            runLoad,
	}
}

func runSelectedScenarios(ctx context.Context, env *scenarioEnvironment) []ScenarioResult {
	registry := scenarioRegistry()
	results := make([]ScenarioResult, 0, len(env.Config.Scenarios))
	for _, name := range env.Config.Scenarios {
		runner, ok := registry[name]
		if !ok {
			results = append(results, ScenarioResult{
				Name:   name,
				Status: ScenarioSkipped,
				Reason: "scenario is not registered",
			})
			continue
		}
		result := runner(ctx, env)
		if result.Name == "" {
			result.Name = name
		}
		results = append(results, result)
	}
	return results
}

func (e *scenarioEnvironment) trackJob(jobID string) {
	e.createdMu.Lock()
	e.createdJobs = append(e.createdJobs, jobID)
	e.createdMu.Unlock()
}

func (e *scenarioEnvironment) cleanup(ctx context.Context) []Observation {
	if e.Config.KeepData || e.API == nil {
		return nil
	}
	e.createdMu.Lock()
	jobs := append([]string(nil), e.createdJobs...)
	e.createdMu.Unlock()
	var observations []Observation
	for index := len(jobs) - 1; index >= 0; index-- {
		observation, _ := e.API.deleteJob(ctx, jobs[index])
		observations = append(observations, observation)
	}
	return observations
}

func (e *scenarioEnvironment) createJob(
	ctx context.Context,
	scenario string,
	plan BehaviorPlan,
) (APIJob, string, Observation, error) {
	token := e.Receiver.registerPlan(plan)
	target := e.Config.ReceiverPublicURL + "/hook/" + token
	job, observation, err := e.createJobWithTarget(ctx, scenario, target)
	return job, target, observation, err
}

func (e *scenarioEnvironment) createJobWithTarget(
	ctx context.Context,
	scenario string,
	target string,
) (APIJob, Observation, error) {
	timeoutSeconds := int(e.Config.Timeout / time.Second)
	if timeoutSeconds < 1 {
		timeoutSeconds = 1
	}
	if timeoutSeconds > 60 {
		timeoutSeconds = 60
	}
	job, observation, err := e.API.createJob(ctx, CreateJobInput{
		Name:           "benchmark-" + scenario + "-" + uuid.NewString()[:8],
		CronExpression: "0 0 * * *",
		Timezone:       "UTC",
		WebhookURL:     target,
		WebhookSecret:  e.Config.WebhookSecret,
		TimeoutSeconds: timeoutSeconds,
		Tags: map[string]string{
			"benchmark_run_id": e.RunID,
			"scenario":         scenario,
		},
	})
	if err == nil {
		e.trackJob(job.ID)
	}
	return job, observation, err
}

func runSingleManual(
	ctx context.Context,
	env *scenarioEnvironment,
	scenario string,
	jobID string,
	target string,
	sampleIndex int,
	warmup bool,
) (ExecutionRecord, error) {
	record := ExecutionRecord{
		RunID:         env.RunID,
		Scenario:      scenario,
		CorrelationID: uuid.NewString(),
		SampleIndex:   sampleIndex,
		JobID:         jobID,
		TargetURL:     target,
		Warmup:        warmup,
	}
	execution, observation, err := env.API.trigger(ctx, jobID)
	record.TriggerRequest = observation
	record.APIExecution = execution
	record.ExecutionID = execution.ID
	if err != nil {
		return record, fmt.Errorf("trigger sample %d: %w", sampleIndex, err)
	}

	bounds, err := env.API.pollTerminal(ctx, execution.ID, env.Config.PollInterval)
	record.PollBounds = &bounds
	record.Callbacks = env.Receiver.callbacksFor(execution.ID)
	env.Receiver.markTerminal(execution.ID)
	if env.Diagnostic != nil {
		diagnostic, diagnosticErr := env.Diagnostic.execution(ctx, execution.ID)
		if diagnosticErr == nil {
			record.Diagnostic = &diagnostic
		}
	}
	record = correlate(record)
	if err == nil && len(record.Callbacks) == 0 {
		record.Findings = append(record.Findings, Finding{
			Severity:    SeverityCritical,
			Code:        "missing_callback",
			Message:     "execution reached polling completion without an observed callback",
			Scenario:    scenario,
			ExecutionID: execution.ID,
		})
	}
	if err != nil {
		return record, fmt.Errorf("poll sample %d: %w", sampleIndex, err)
	}
	return record, nil
}

func finalizeScenario(
	name string,
	started time.Time,
	records []ExecutionRecord,
	observations []Observation,
	errs []error,
) ScenarioResult {
	result := ScenarioResult{
		Name:         name,
		Status:       ScenarioPassed,
		StartedAt:    started.UTC(),
		FinishedAt:   time.Now().UTC(),
		Executions:   records,
		Observations: observations,
	}
	for _, record := range records {
		result.Findings = append(result.Findings, record.Findings...)
	}
	if len(errs) > 0 {
		result.Status = ScenarioFailed
		result.Reason = joinErrors(errs)
	}
	for _, finding := range result.Findings {
		if finding.Severity == SeverityCritical {
			result.Status = ScenarioFailed
		}
	}
	return result
}

func joinErrors(errs []error) string {
	var result string
	for _, err := range errs {
		if err == nil {
			continue
		}
		if result != "" {
			result += "; "
		}
		result += err.Error()
	}
	return result
}

func skippedScenario(name, reason string) ScenarioResult {
	now := time.Now().UTC()
	return ScenarioResult{
		Name:       name,
		Status:     ScenarioSkipped,
		Reason:     reason,
		StartedAt:  now,
		FinishedAt: now,
	}
}
