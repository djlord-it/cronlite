package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"
)

type InstanceSample struct {
	Service        string  `json:"service"`
	Reachable      bool    `json:"reachable"`
	Ready          bool    `json:"ready"`
	Leader         bool    `json:"leader"`
	Triggered      float64 `json:"triggered_total"`
	TriggeredDelta float64 `json:"triggered_delta"`
}

type TimelineSample struct {
	At          time.Time        `json:"at"`
	Instances   []InstanceSample `json:"instances"`
	LeaderSum   int              `json:"leader_sum"`
	QueueDepth  int              `json:"queue_depth"`
	StuckCount  int              `json:"stuck_count"`
	ActiveFault string           `json:"active_fault,omitempty"`
}

type instanceProbe interface {
	scrape(ctx context.Context, service string) (metricSnapshot, error)
	ready(ctx context.Context, service string) bool
	services() []string
}

type httpInstanceProbe struct {
	metricsURLs map[string]string
	apiURLs     map[string]string
	apiKey      string
	perCall     time.Duration
	client      *http.Client
}

func newHTTPInstanceProbe(metricsURLs, apiURLs map[string]string, apiKey string, perCall time.Duration) instanceProbe {
	return &httpInstanceProbe{
		metricsURLs: metricsURLs,
		apiURLs:     apiURLs,
		apiKey:      apiKey,
		perCall:     perCall,
		client:      &http.Client{},
	}
}

func (p *httpInstanceProbe) services() []string {
	var services []string
	for service := range p.metricsURLs {
		services = append(services, service)
	}
	slices.Sort(services)
	return services
}

func (p *httpInstanceProbe) get(ctx context.Context, url string) (*http.Response, context.CancelFunc, error) {
	callCtx, cancel := context.WithTimeout(ctx, p.perCall)
	request, err := http.NewRequestWithContext(callCtx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if p.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(request)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return response, cancel, nil
}

func (p *httpInstanceProbe) scrape(ctx context.Context, service string) (metricSnapshot, error) {
	response, cancel, err := p.get(ctx, p.metricsURLs[service])
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPrometheusBodyBytes))
	if err != nil {
		return nil, err
	}
	return parsePrometheus(body), nil
}

func (p *httpInstanceProbe) ready(ctx context.Context, service string) bool {
	response, cancel, err := p.get(ctx, p.apiURLs[service]+"/health")
	if err != nil {
		return false
	}
	defer cancel()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
	return response.StatusCode == http.StatusOK
}

type timelineSampler struct {
	probe       instanceProbe
	db          chaosDiagnosticReader
	jobIDs      func() []string
	activeFault func() string
	interval    time.Duration
	settings    ChaosSettings
	dbTimeout   time.Duration

	mu      sync.Mutex
	samples []TimelineSample
	prev    map[string]float64
}

func newTimelineSampler(
	probe instanceProbe,
	db chaosDiagnosticReader,
	jobIDs func() []string,
	activeFault func() string,
	interval time.Duration,
	settings ChaosSettings,
) *timelineSampler {
	return &timelineSampler{
		probe:       probe,
		db:          db,
		jobIDs:      jobIDs,
		activeFault: activeFault,
		interval:    interval,
		settings:    settings,
		dbTimeout:   chaosDBSampleTimeout,
		prev:        make(map[string]float64),
	}
}

// chaosDBSampleTimeout bounds each sampler query. lib/pq does not interrupt a
// read from a paused PostgreSQL when the context is cancelled, so the query
// runs in its own goroutine and is abandoned at the deadline.
const chaosDBSampleTimeout = 2 * time.Second

func withDeadline[T any](ctx context.Context, timeout time.Duration, query func(context.Context) (T, error)) (T, error) {
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	type outcome struct {
		value T
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		defer cancel()
		value, err := query(queryCtx)
		done <- outcome{value, err}
	}()
	select {
	case result := <-done:
		return result.value, result.err
	case <-queryCtx.Done():
		var zero T
		return zero, queryCtx.Err()
	}
}

// sampleOnce probes every instance concurrently and appends the sample.
func (t *timelineSampler) sampleOnce(ctx context.Context, at time.Time) TimelineSample {
	services := t.probe.services()
	instances := make([]InstanceSample, len(services))
	var wg sync.WaitGroup
	for index, service := range services {
		instances[index].Service = service
		wg.Add(2)
		go func() {
			defer wg.Done()
			if metrics, err := t.probe.scrape(ctx, service); err == nil {
				instances[index].Reachable = true
				instances[index].Leader = metrics["cronlite_leader_is_leader"] == 1
				instances[index].Triggered = metrics["cronlite_scheduler_jobs_triggered_total"]
			}
		}()
		go func() {
			defer wg.Done()
			instances[index].Ready = t.probe.ready(ctx, service)
		}()
	}
	wg.Wait()

	sample := TimelineSample{At: at.UTC(), Instances: instances, QueueDepth: -1, StuckCount: -1, ActiveFault: t.activeFault()}
	jobIDs := t.jobIDs()
	if depth, err := withDeadline(ctx, t.dbTimeout, func(queryCtx context.Context) (int, error) {
		return t.db.chaosQueueDepth(queryCtx, jobIDs)
	}); err == nil {
		sample.QueueDepth = depth
	}
	ageLimit := t.settings.ReconcileThreshold + t.settings.ReconcileInterval
	if stuck, err := withDeadline(ctx, t.dbTimeout, func(queryCtx context.Context) ([]string, error) {
		return t.db.chaosStuck(queryCtx, jobIDs, t.settings.stuckLeaseBound().Value, ageLimit)
	}); err == nil {
		sample.StuckCount = len(stuck)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for index := range sample.Instances {
		instance := &sample.Instances[index]
		if instance.Leader {
			sample.LeaderSum++
		}
		if !instance.Reachable {
			continue
		}
		previous, seen := t.prev[instance.Service]
		switch {
		case !seen:
			instance.TriggeredDelta = 0
		case instance.Triggered < previous:
			// The counter restarted with the process.
			instance.TriggeredDelta = instance.Triggered
		default:
			instance.TriggeredDelta = instance.Triggered - previous
		}
		t.prev[instance.Service] = instance.Triggered
	}
	t.samples = append(t.samples, sample)
	return sample
}

func (t *timelineSampler) run(ctx context.Context) {
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Stamp with the wall clock: a slow previous sample makes the
			// ticker's value stale.
			t.sampleOnce(ctx, time.Now())
		}
	}
}

func (t *timelineSampler) snapshot() []TimelineSample {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.samples)
}
