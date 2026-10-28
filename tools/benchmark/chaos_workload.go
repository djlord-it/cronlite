package main

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"
)

const chaosTriggerConcurrency = 16

// chaosWorkload keeps recurring and manual traffic flowing through faults.
type chaosWorkload struct {
	env         *scenarioEnvironment
	jobs        []RecurringJob
	manualJobID string

	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	manual []ManualSample
}

func startChaosWorkload(ctx context.Context, env *scenarioEnvironment) (*chaosWorkload, error) {
	workload := &chaosWorkload{env: env}
	for range env.Config.ChaosJobs {
		token := env.Receiver.registerPlan(BehaviorPlan{Statuses: []int{http.StatusNoContent}})
		target := env.Config.ReceiverPublicURL + "/hook/" + token
		job, _, err := env.createJobWithTarget(ctx, "chaos", target, "* * * * *")
		if err != nil {
			return workload, err
		}
		createdAt := job.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		workload.jobs = append(workload.jobs, RecurringJob{ID: job.ID, CronExpression: "* * * * *", CreatedAt: createdAt})
	}
	manualJob, _, _, err := env.createJob(ctx, "chaos", BehaviorPlan{Statuses: []int{http.StatusNoContent}})
	if err != nil {
		return workload, err
	}
	workload.manualJobID = manualJob.ID

	loopCtx, cancel := context.WithCancel(ctx)
	workload.cancel = cancel
	if rate := env.Config.ChaosTriggerRate; rate > 0 {
		workload.wg.Add(1)
		go workload.triggerLoop(loopCtx, time.Duration(float64(time.Second)/rate))
	}
	return workload, nil
}

func (w *chaosWorkload) triggerLoop(ctx context.Context, interval time.Duration) {
	defer w.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	slots := make(chan struct{}, chaosTriggerConcurrency)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		select {
		case <-ctx.Done():
			return
		case slots <- struct{}{}:
		}
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			defer func() { <-slots }()
			triggeredAt := time.Now().UTC()
			execution, _, err := w.env.API.trigger(ctx, w.manualJobID)
			if err != nil || execution.ID == "" {
				return
			}
			w.mu.Lock()
			w.manual = append(w.manual, ManualSample{ExecutionID: execution.ID, TriggeredAt: triggeredAt})
			w.mu.Unlock()
		}()
	}
}

func (w *chaosWorkload) jobIDs() []string {
	ids := make([]string, 0, len(w.jobs)+1)
	for _, job := range w.jobs {
		ids = append(ids, job.ID)
	}
	if w.manualJobID != "" {
		ids = append(ids, w.manualJobID)
	}
	return ids
}

func (w *chaosWorkload) recurringJobs() []RecurringJob {
	return slices.Clone(w.jobs)
}

func (w *chaosWorkload) manualSamples() []ManualSample {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.manual)
}

// stop ends the trigger loop, waits for in-flight triggers and returns all
// successful manual triggers.
func (w *chaosWorkload) stop() []ManualSample {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
	return w.manualSamples()
}
