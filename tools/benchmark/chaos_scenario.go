package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	chaosProbeTimeout     = 500 * time.Millisecond
	chaosFinalQueryBudget = 30 * time.Second
	// The last recurring fire of a minute-resolution cron needs up to a minute
	// to land after the final fault.
	chaosLastFireMargin = time.Minute
)

// runChaos runs the workload, applies each fault in order, waits for recovery,
// and scores the invariants over everything it recorded.
func runChaos(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	const name = "chaos"
	started := time.Now()
	db, ok := env.Diagnostic.(chaosDiagnosticReader)
	if !ok || env.Diagnostic == nil {
		return skippedScenario(name, "chaos requires diagnostic PostgreSQL access")
	}
	controller := env.ChaosController
	if controller == nil && env.Controller != nil {
		controller = env.Controller
	}
	probe := env.ChaosProbe
	if probe == nil && env.Controller != nil {
		probe = newHTTPInstanceProbe(env.Controller.MetricsURLs, env.Controller.APIURLs, env.Config.APIKey, chaosProbeTimeout)
	}
	if controller == nil || probe == nil {
		return skippedScenario(name, "chaos requires the managed Compose environment")
	}

	cfg := env.Config
	settings := chaosSettingsFor(cfg)
	driver := newFaultDriver(controller, cfg.RandomSeed)
	cleaned := false
	defer func() {
		if !cleaned {
			driver.cleanupAll(context.Background())
		}
	}()

	workload, err := startChaosWorkload(ctx, env)
	if err != nil {
		workload.stop()
		return finalizeScenario(name, started, nil, nil, []error{fmt.Errorf("start chaos workload: %w", err)})
	}

	var activeMu sync.Mutex
	active := ""
	setActive := func(fault string) {
		activeMu.Lock()
		active = fault
		activeMu.Unlock()
	}
	activeFault := func() string {
		activeMu.Lock()
		defer activeMu.Unlock()
		return active
	}
	sampler := newTimelineSampler(probe, db, workload.jobIDs, activeFault, cfg.ChaosSampleInterval, settings)
	samplerCtx, stopSampler := context.WithCancel(ctx)
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		sampler.run(samplerCtx)
	}()

	runStart := time.Now().UTC()
	in := ChaosInputs{Settings: settings, RecoveryTimeout: cfg.ChaosRecoveryTimeout, RunStart: runStart}
	sleepContext(ctx, cfg.ChaosBaseline)
	in.BaselineEnd = time.Now().UTC()

	var errs []error
	for _, spec := range cfg.ChaosFaults {
		if ctx.Err() != nil {
			break
		}
		window, applyErr := driver.apply(ctx, spec)
		if applyErr != nil {
			in.Windows = append(in.Windows, window)
			errs = append(errs, fmt.Errorf("apply %s: %w", spec, applyErr))
			continue
		}
		setActive(spec.String())
		sleepContext(ctx, spec.Duration)
		if clearErr := driver.clear(ctx, &window); clearErr != nil {
			errs = append(errs, fmt.Errorf("clear %s: %w", spec, clearErr))
		}
		setActive("")
		in.Windows = append(in.Windows, window)
		awaitRecovery(ctx, &in, len(in.Windows)-1, sampler, workload, env.Receiver)
		sleepContext(ctx, cfg.ChaosSteady)
	}

	grace := chaosGrace(in, env.Receiver, workload)
	finalWait := grace + chaosLastFireMargin
	if env.chaosGraceOverride != nil {
		grace = *env.chaosGraceOverride
		finalWait = grace
	}
	sleepContext(ctx, finalWait)

	in.Manual = workload.stop()
	stopSampler()
	<-samplerDone
	cleanupObservations := driver.cleanupAll(context.Background())
	cleaned = true
	in.RunEnd = time.Now().UTC()

	queryCtx, cancel := context.WithTimeout(context.Background(), chaosFinalQueryBudget)
	defer cancel()
	jobIDs := workload.jobIDs()
	if in.Executions, err = db.chaosExecutions(queryCtx, jobIDs); err != nil {
		errs = append(errs, err)
	}
	if in.Stuck, err = db.chaosStuck(queryCtx, jobIDs, settings.stuckLeaseBound().Value, settings.ReconcileThreshold+settings.ReconcileInterval); err != nil {
		errs = append(errs, err)
	}
	in.Jobs = workload.recurringJobs()
	in.Callbacks = env.Receiver.snapshot()
	in.Timeline = sampler.snapshot()

	result := finalizeScenario(name, started, nil, cleanupObservations, errs)
	result.Chaos = scoreChaos(in, grace)
	for _, breach := range result.Chaos.Breaches {
		finding := Finding{Severity: breach.Severity, Code: breach.Code, Message: breach.Message, Scenario: name}
		if len(breach.ExecutionIDs) > 0 {
			finding.ExecutionID = breach.ExecutionIDs[0]
		}
		result.Findings = append(result.Findings, finding)
		if breach.Severity == SeverityCritical {
			result.Status = ScenarioFailed
		}
	}
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.Canceled) {
		result.Status = ScenarioFailed
	}
	return result
}

// scoreChaos runs every P0 invariant check and builds the scorecard.
func scoreChaos(in ChaosInputs, grace time.Duration) *ChaosResult {
	steady := steadyBound(in)
	lateness, breaches := checkLateness(in)
	breaches = append(checkNoLost(in, grace), breaches...)
	explained, unexplained, concurrent, duplicateBreaches := classifyDuplicates(in)
	breaches = append(breaches, duplicateBreaches...)
	breaches = append(breaches, checkLeadership(in)...)
	recovery, recoveryBreaches := recoveryTimes(in, steady, in.RecoveryTimeout)
	breaches = append(breaches, recoveryBreaches...)
	breaches = append(breaches, checkStuck(in)...)
	breaches = append(breaches, checkSignatures(in)...)
	return newChaosResult(in, breaches, lateness, recovery, DuplicateCounts{explained, unexplained, concurrent})
}

func steadyBound(in ChaosInputs) Bound {
	p99, ok := baselineP99(in, latenessManual)
	if !ok {
		return Bound{}
	}
	return in.Settings.steadyLatenessBound(p99)
}

// awaitRecovery polls the live timeline until the window recovers or the
// recovery timeout passes, then records RecoveredAt.
func awaitRecovery(
	ctx context.Context,
	in *ChaosInputs,
	index int,
	sampler *timelineSampler,
	workload *chaosWorkload,
	receiver *callbackStore,
) {
	window := &in.Windows[index]
	key := faultKey(index, *window)
	deadline := window.ClearedAt.Add(in.RecoveryTimeout)
	poll := min(sampler.interval, time.Second)
	for ctx.Err() == nil && time.Now().Before(deadline.Add(poll)) {
		live := *in
		live.Timeline = sampler.snapshot()
		live.Manual = workload.manualSamples()
		live.Callbacks = receiver.snapshot()
		times, _ := recoveryTimes(live, steadyBound(live), in.RecoveryTimeout)
		if duration, ok := times[key]; ok {
			recoveredAt := window.ClearedAt.Add(duration)
			window.RecoveredAt = &recoveredAt
			return
		}
		sleepContext(ctx, poll)
	}
}

// chaosGrace is the largest lateness bound in the run: fires later than
// RunEnd minus grace are not checked by I1.
func chaosGrace(in ChaosInputs, receiver *callbackStore, workload *chaosWorkload) time.Duration {
	live := in
	live.Manual = workload.manualSamples()
	live.Callbacks = receiver.snapshot()
	grace := steadyBound(live).Value
	for _, window := range in.Windows {
		grace = max(grace, in.Settings.faultLatenessBound(window.Spec).Value)
	}
	return grace
}

func sleepContext(ctx context.Context, duration time.Duration) {
	if duration <= 0 {
		return
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
