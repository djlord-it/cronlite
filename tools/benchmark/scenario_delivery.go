package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const productionRetryCaseTimeout = 18 * time.Minute

func runSmoke(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	return runSequential(ctx, env, "smoke", 0, env.Config.SampleCount, BehaviorPlan{
		Statuses: []int{http.StatusNoContent},
	})
}

func runWarmSequential(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	return runSequential(ctx, env, "warm-sequential", 1, env.Config.SampleCount, BehaviorPlan{
		Statuses: []int{http.StatusNoContent},
	})
}

func runSequential(
	ctx context.Context,
	env *scenarioEnvironment,
	name string,
	warmups int,
	samples int,
	plan BehaviorPlan,
) ScenarioResult {
	started := time.Now()
	job, target, createObservation, err := env.createJob(ctx, name, plan)
	if err != nil {
		return finalizeScenario(name, started, nil, []Observation{createObservation}, []error{err})
	}
	records := make([]ExecutionRecord, 0, warmups+samples)
	var errs []error
	for index := 0; index < warmups+samples; index++ {
		record, runErr := runSingleManual(
			ctx,
			env,
			name,
			job.ID,
			target,
			index+1,
			index < warmups,
		)
		records = append(records, record)
		if runErr != nil {
			errs = append(errs, runErr)
		}
	}
	return finalizeScenario(name, started, records, []Observation{createObservation}, errs)
}

func runBaseline(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	started := time.Now()
	var observations []Observation
	var errs []error
	for index := 0; index < env.Config.SampleCount; index++ {
		observation, err := env.API.health(ctx, false)
		observations = append(observations, observation)
		if err != nil {
			errs = append(errs, err)
		}
		receiverObservation, err := baselineRequest(ctx, env.Config)
		observations = append(observations, receiverObservation)
		if err != nil {
			errs = append(errs, err)
		}
	}
	if env.Diagnostic != nil {
		observation, err := env.Diagnostic.ping(ctx)
		observations = append(observations, observation)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return finalizeScenario("baseline", started, nil, observations, errs)
}

func baselineRequest(ctx context.Context, cfg Config) (Observation, error) {
	baseURL, err := receiverClientBaseURL(cfg.ReceiverAddr)
	if err != nil {
		return Observation{}, err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		baseURL+"/baseline",
		nil,
	)
	if err != nil {
		return Observation{}, err
	}
	client := &http.Client{Timeout: cfg.Timeout}
	started := time.Now()
	response, err := client.Do(request)
	finished := time.Now()
	observation := Observation{
		Kind:       "baseline",
		Name:       "receiver_http_rtt",
		StartedAt:  started.UTC(),
		FinishedAt: finished.UTC(),
		Duration:   finished.Sub(started),
		Provenance: ProvenanceDirect,
	}
	if err != nil {
		observation.Error = err.Error()
		return observation, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	observation.StatusCode = response.StatusCode
	if response.StatusCode != http.StatusNoContent {
		return observation, fmt.Errorf("receiver baseline returned HTTP %d", response.StatusCode)
	}
	return observation, nil
}

func receiverClientBaseURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("parse receiver address: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

func runColdWarm(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	return runSequential(ctx, env, "cold-warm", 0, 2, BehaviorPlan{
		Statuses: []int{http.StatusNoContent},
	})
}

func runConcurrent(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	started := time.Now()
	var allRecords []ExecutionRecord
	var observations []Observation
	var errs []error
	for _, level := range env.Config.Concurrency {
		name := fmt.Sprintf("concurrent-%d", level)
		job, target, createObservation, err := env.createJob(ctx, name, BehaviorPlan{
			Statuses: []int{http.StatusNoContent},
		})
		observations = append(observations, createObservation)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		records := make([]ExecutionRecord, env.Config.SampleCount)
		levelErrors := make([]error, env.Config.SampleCount)
		semaphore := make(chan struct{}, level)
		var wg sync.WaitGroup
		for index := 0; index < env.Config.SampleCount; index++ {
			wg.Add(1)
			go func(sampleIndex int) {
				defer wg.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()
				records[sampleIndex], levelErrors[sampleIndex] = runSingleManual(
					ctx,
					env,
					name,
					job.ID,
					target,
					sampleIndex+1,
					false,
				)
			}(index)
		}
		wg.Wait()
		allRecords = append(allRecords, records...)
		for _, levelErr := range levelErrors {
			if levelErr != nil {
				errs = append(errs, levelErr)
			}
		}
	}
	return finalizeScenario("concurrent", started, allRecords, observations, errs)
}

func runLoad(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	result := runConcurrent(ctx, env)
	result.Name = "load"
	return result
}

func runControlPlane(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	started := time.Now()
	job, _, createObservation, err := env.createJob(ctx, "control-plane", BehaviorPlan{})
	observations := []Observation{createObservation}
	if err != nil {
		return finalizeScenario("control-plane", started, nil, observations, []error{err})
	}
	var errs []error
	_, observation, err := env.API.getJob(ctx, job.ID)
	observations = append(observations, observation)
	errs = appendIfError(errs, err)
	name := job.Name + "-updated"
	_, observation, err = env.API.updateJob(ctx, job.ID, UpdateJobInput{Name: &name})
	observations = append(observations, observation)
	errs = appendIfError(errs, err)
	observation, err = env.API.pauseJob(ctx, job.ID)
	observations = append(observations, observation)
	errs = appendIfError(errs, err)
	observation, err = env.API.resumeJob(ctx, job.ID)
	observations = append(observations, observation)
	errs = appendIfError(errs, err)
	_, observation, err = env.API.listExecutions(ctx, job.ID)
	observations = append(observations, observation)
	errs = appendIfError(errs, err)
	return finalizeScenario("control-plane", started, nil, observations, errs)
}

func runSlowReceiver(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	started := time.Now()
	var records []ExecutionRecord
	var observations []Observation
	var errs []error
	delays := []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 2 * time.Second}
	nearTimeout := env.Config.Timeout - 100*time.Millisecond
	if nearTimeout > 2*time.Second && nearTimeout <= defaultReceiverLimits().MaxDelay {
		delays = append(delays, nearTimeout)
	}
	for index, delay := range delays {
		name := fmt.Sprintf("slow-receiver-%s", delay)
		job, target, observation, err := env.createJob(ctx, name, BehaviorPlan{
			Statuses: []int{http.StatusNoContent},
			Delay:    delay,
		})
		observations = append(observations, observation)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		record, err := runSingleManual(ctx, env, name, job.ID, target, index+1, false)
		records = append(records, record)
		errs = appendIfError(errs, err)
	}
	return finalizeScenario("slow-receiver", started, records, observations, errs)
}

func runRetry(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	if env.Config.RetryProfile == "fast-test" {
		return skippedScenario(
			"retry",
			"fast-test unavailable: CronLite has no test-only retry injection",
		)
	}
	started := time.Now()
	connectionTarget, err := connectionFailureTarget(env.Config.ReceiverPublicURL)
	if err != nil {
		return finalizeScenario("retry", started, nil, nil, []error{err})
	}
	cases := []struct {
		name           string
		statuses       []int
		delay          time.Duration
		targetOverride string
	}{
		{name: "permanent-500", statuses: []int{500}},
		{name: "500-eventual-success", statuses: []int{500, 204}},
		{name: "503-eventual-success", statuses: []int{503, 204}},
		{name: "429-eventual-success", statuses: []int{429, 204}},
		{name: "non-retryable-400", statuses: []int{400}},
		{name: "timeout", statuses: []int{204}, delay: env.Config.Timeout + time.Second},
		{name: "connection-failure", targetOverride: connectionTarget},
	}
	records := make([]ExecutionRecord, len(cases))
	recorded := make([]bool, len(cases))
	observations := make([]Observation, len(cases))
	caseErrors := make([]error, len(cases))
	var wg sync.WaitGroup
	for index, retryCase := range cases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			caseCtx, cancel := productionRetryCaseContext(ctx)
			defer cancel()
			var job APIJob
			var target string
			var err error
			if retryCase.targetOverride != "" {
				target = retryCase.targetOverride
				job, observations[index], err = env.createJobWithTarget(
					caseCtx,
					retryCase.name,
					target,
				)
			} else {
				job, target, observations[index], err = env.createJob(
					caseCtx,
					retryCase.name,
					BehaviorPlan{
						Statuses: retryCase.statuses,
						Delay:    retryCase.delay,
					},
				)
			}
			if err != nil {
				caseErrors[index] = err
				return
			}
			records[index], caseErrors[index] = runSingleManual(
				caseCtx,
				env,
				retryCase.name,
				job.ID,
				target,
				index+1,
				false,
			)
			recorded[index] = true
		}()
	}
	wg.Wait()
	resultRecords := make([]ExecutionRecord, 0, len(records))
	var errs []error
	for index := range cases {
		if recorded[index] {
			resultRecords = append(resultRecords, records[index])
		}
		errs = appendIfError(errs, caseErrors[index])
	}
	return finalizeScenario("retry", started, resultRecords, observations, errs)
}

func connectionFailureTarget(receiverPublicURL string) (string, error) {
	target, err := url.Parse(receiverPublicURL)
	if err != nil || target.Scheme == "" || target.Hostname() == "" {
		return "", fmt.Errorf("parse receiver public URL for connection failure target")
	}
	target.Host = net.JoinHostPort(target.Hostname(), "1")
	target.Path = "/hook"
	target.RawPath = ""
	target.RawQuery = ""
	target.Fragment = ""
	return target.String(), nil
}

func productionRetryCaseContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, productionRetryCaseTimeout)
}

func runRecurring(ctx context.Context, env *scenarioEnvironment) ScenarioResult {
	started := time.Now()
	token := env.Receiver.registerPlan(BehaviorPlan{Statuses: []int{http.StatusNoContent}})
	target := env.Config.ReceiverPublicURL + "/hook/" + token
	job, createObservation, err := env.API.createJob(ctx, CreateJobInput{
		Name:           "benchmark-recurring",
		CronExpression: "* * * * *",
		Timezone:       "UTC",
		WebhookURL:     target,
		WebhookSecret:  env.Config.WebhookSecret,
		TimeoutSeconds: 30,
		Tags: map[string]string{
			"benchmark_run_id": env.RunID,
			"scenario":         "recurring",
		},
	})
	if err != nil {
		return finalizeScenario("recurring", started, nil, []Observation{createObservation}, []error{err})
	}
	env.trackJob(job.ID)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var executions []APIExecution
	for len(executions) < env.Config.SampleCount {
		select {
		case <-ctx.Done():
			return recurringResult(env, started, job.ID, target, executions, createObservation, ctx.Err())
		case <-ticker.C:
			executions, _, err = env.API.listExecutions(ctx, job.ID)
			if err != nil {
				return recurringResult(env, started, job.ID, target, executions, createObservation, err)
			}
		}
	}
	return recurringResult(env, started, job.ID, target, executions, createObservation, nil)
}

func recurringResult(
	env *scenarioEnvironment,
	started time.Time,
	jobID string,
	target string,
	executions []APIExecution,
	observation Observation,
	runErr error,
) ScenarioResult {
	records := make([]ExecutionRecord, 0, len(executions))
	for index := range executions {
		execution := &executions[index]
		record := ExecutionRecord{
			RunID:         env.RunID,
			Scenario:      "recurring",
			CorrelationID: execution.ID,
			SampleIndex:   index + 1,
			JobID:         jobID,
			ExecutionID:   execution.ID,
			TargetURL:     target,
			APIExecution:  *execution,
			Callbacks:     env.Receiver.callbacksFor(execution.ID),
		}
		if env.Diagnostic != nil {
			diagnostic, err := env.Diagnostic.execution(context.Background(), execution.ID)
			if err == nil {
				record.Diagnostic = &diagnostic
			}
		}
		records = append(records, correlate(record))
	}
	var errs []error
	errs = appendIfError(errs, runErr)
	return finalizeScenario("recurring", started, records, []Observation{observation}, errs)
}

func appendIfError(errs []error, err error) []error {
	if err != nil {
		return append(errs, err)
	}
	return errs
}
