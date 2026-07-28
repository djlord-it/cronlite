package main

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

func runDuplicateRace(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	if !env.Capabilities.DockerCompose || env.Controller == nil {
		return skippedScenario("duplicate-race", "Docker Compose process control is unavailable")
	}
	if !env.Capabilities.MultipleInstances {
		return skippedScenario("duplicate-race", "multiple CronLite instances are unavailable")
	}

	started := time.Now()
	plan := BehaviorPlan{Statuses: []int{http.StatusInternalServerError}}
	if env.Config.RequeueThreshold+2*time.Second < env.Config.Timeout {
		plan = BehaviorPlan{
			Statuses: []int{http.StatusNoContent},
			Delay:    env.Config.RequeueThreshold + 2*time.Second,
		}
	}
	job, target, createObservation, err := env.createJob(ctx, "duplicate-race", plan)
	if err != nil {
		return finalizeScenario(
			"duplicate-race",
			started,
			nil,
			[]Observation{createObservation},
			[]error{err},
		)
	}
	record, runErr := runSingleManual(
		ctx,
		env,
		"duplicate-race",
		job.ID,
		target,
		1,
		false,
	)
	result := duplicateRaceVerdict(record)
	result.StartedAt = started.UTC()
	result.FinishedAt = time.Now().UTC()
	result.Observations = []Observation{createObservation}
	if runErr != nil {
		result.Status = ScenarioFailed
		result.Reason = runErr.Error()
	}
	return result
}

func duplicateRaceVerdict(record ExecutionRecord) ScenarioResult {
	result := ScenarioResult{
		Name:       "duplicate-race",
		Status:     ScenarioPassed,
		Executions: []ExecutionRecord{record},
		Findings:   record.Findings,
	}
	for _, finding := range result.Findings {
		if finding.Code == "concurrent_duplicate_delivery" ||
			finding.Code == "duplicate_attempt_id" {
			result.Status = ScenarioFailed
			result.Reason = "receiver observed duplicate HTTP side effects"
			return result
		}
	}
	return result
}

func runCrashRecovery(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	if !env.Capabilities.ProcessControl || env.Controller == nil {
		return skippedScenario("crash-recovery", "Docker Compose process control is unavailable")
	}
	started := time.Now()
	job, target, createObservation, err := env.createJob(ctx, "crash-recovery", BehaviorPlan{
		Statuses: []int{http.StatusNoContent},
		Delay:    10 * time.Second,
	})
	if err != nil {
		return finalizeScenario(
			"crash-recovery",
			started,
			nil,
			[]Observation{createObservation},
			[]error{err},
		)
	}
	execution, triggerObservation, err := env.API.trigger(ctx, job.ID)
	if err != nil {
		return finalizeScenario(
			"crash-recovery",
			started,
			nil,
			[]Observation{createObservation, triggerObservation},
			[]error{err},
		)
	}

	if err := waitForActiveCallback(ctx, env.Receiver, execution.ID); err != nil {
		return finalizeScenario(
			"crash-recovery",
			started,
			nil,
			[]Observation{createObservation, triggerObservation},
			[]error{err},
		)
	}

	stopObservation, stopErr := observeControl(
		"stop_dispatcher",
		func() error { return env.Controller.stopService(ctx, "cronlite_1") },
	)
	startObservation, startErr := observeControl(
		"restart_dispatcher",
		func() error { return env.Controller.startService(ctx, "cronlite_1") },
	)
	bounds, pollErr := env.API.pollTerminal(ctx, execution.ID, env.Config.PollInterval)
	record := ExecutionRecord{
		RunID:          env.RunID,
		Scenario:       "crash-recovery",
		CorrelationID:  execution.ID,
		SampleIndex:    1,
		JobID:          job.ID,
		ExecutionID:    execution.ID,
		TargetURL:      target,
		TriggerRequest: triggerObservation,
		APIExecution:   execution,
		PollBounds:     &bounds,
		Callbacks:      env.Receiver.callbacksFor(execution.ID),
	}
	if env.Diagnostic != nil {
		diagnostic, diagnosticErr := env.Diagnostic.execution(ctx, execution.ID)
		if diagnosticErr == nil {
			record.Diagnostic = &diagnostic
		}
	}
	record = correlate(record)
	errs := []error{}
	errs = appendIfError(errs, stopErr)
	errs = appendIfError(errs, startErr)
	errs = appendIfError(errs, pollErr)
	return finalizeScenario(
		"crash-recovery",
		started,
		[]ExecutionRecord{record},
		[]Observation{createObservation, triggerObservation, stopObservation, startObservation},
		errs,
	)
}

func runLeaderFailover(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	if !env.Capabilities.MultipleInstances || env.Controller == nil {
		return skippedScenario("leader-failover", "multi-instance leader control is unavailable")
	}
	started := time.Now()
	leader, err := env.Controller.leaderService(ctx)
	if err != nil {
		return finalizeScenario("leader-failover", started, nil, nil, []error{err})
	}
	stopObservation, stopErr := observeControl(
		"stop_scheduler_leader",
		func() error { return env.Controller.stopService(ctx, leader) },
	)
	if stopErr != nil {
		return finalizeScenario(
			"leader-failover",
			started,
			nil,
			[]Observation{stopObservation},
			[]error{stopErr},
		)
	}
	defer func() { _ = env.Controller.startService(context.Background(), leader) }()
	newLeader, failoverErr := env.Controller.waitForLeader(ctx, leader)
	startObservation, restartErr := observeControl(
		"restart_previous_leader",
		func() error { return env.Controller.startService(ctx, leader) },
	)
	var errs []error
	errs = appendIfError(errs, failoverErr)
	errs = appendIfError(errs, restartErr)
	result := finalizeScenario(
		"leader-failover",
		started,
		nil,
		[]Observation{stopObservation, startObservation},
		errs,
	)
	if failoverErr == nil {
		result.Reason = fmt.Sprintf("leadership moved from %s to %s", leader, newLeader)
	}
	return result
}

func runDatabaseOutage(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	if !env.Capabilities.PostgreSQL || env.Controller == nil {
		return skippedScenario("database-outage", "controlled PostgreSQL service is unavailable")
	}
	started := time.Now()
	pauseObservation, pauseErr := observeControl(
		"pause_postgres",
		func() error { return env.Controller.pauseService(ctx, "postgres") },
	)
	if pauseErr != nil {
		return finalizeScenario(
			"database-outage",
			started,
			nil,
			[]Observation{pauseObservation},
			[]error{pauseErr},
		)
	}
	defer func() { _ = env.Controller.unpauseService(context.Background(), "postgres") }()
	degradedObservation, _ := env.API.health(ctx, true)
	unpauseObservation, unpauseErr := observeControl(
		"unpause_postgres",
		func() error { return env.Controller.unpauseService(ctx, "postgres") },
	)
	readyObservation, readyErr := waitForReadiness(ctx, env.API, env.Config.PollInterval)
	var errs []error
	errs = appendIfError(errs, unpauseErr)
	errs = appendIfError(errs, readyErr)
	return finalizeScenario(
		"database-outage",
		started,
		nil,
		[]Observation{
			pauseObservation,
			degradedObservation,
			unpauseObservation,
			readyObservation,
		},
		errs,
	)
}

func waitForActiveCallback(
	ctx context.Context,
	store *callbackStore,
	executionID string,
) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if store.activeCount(executionID) > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitForReadiness(
	ctx context.Context,
	api scenarioAPI,
	interval time.Duration,
) (Observation, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		observation, err := api.health(ctx, true)
		if err == nil && observation.StatusCode == http.StatusOK {
			return observation, nil
		}
		select {
		case <-ctx.Done():
			return observation, ctx.Err()
		case <-ticker.C:
		}
	}
}

func observeControl(name string, action func() error) (Observation, error) {
	started := time.Now()
	err := action()
	finished := time.Now()
	observation := Observation{
		Kind:       "process_control",
		Name:       name,
		StartedAt:  started.UTC(),
		FinishedAt: finished.UTC(),
		Duration:   finished.Sub(started),
		Provenance: ProvenanceDirect,
	}
	if err != nil {
		observation.Error = err.Error()
		observation.ErrorClass = "process_control_error"
	}
	return observation, err
}
