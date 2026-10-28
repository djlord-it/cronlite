package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type fakeInstanceProbe struct {
	mu       sync.Mutex
	metrics  map[string]metricSnapshot
	down     map[string]bool
	notReady map[string]bool
}

func (p *fakeInstanceProbe) scrape(_ context.Context, service string) (metricSnapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.down[service] {
		return nil, errors.New("unreachable")
	}
	return p.metrics[service], nil
}

func (p *fakeInstanceProbe) ready(_ context.Context, service string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.down[service] && !p.notReady[service]
}

func (p *fakeInstanceProbe) set(service string, leader, triggered float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.metrics[service] = metricSnapshot{
		"cronlite_leader_is_leader":               leader,
		"cronlite_scheduler_jobs_triggered_total": triggered,
	}
}

func newFakeProbe() *fakeInstanceProbe {
	probe := &fakeInstanceProbe{metrics: map[string]metricSnapshot{}, down: map[string]bool{}, notReady: map[string]bool{}}
	probe.set("cronlite_1", 1, 0)
	probe.set("cronlite_2", 0, 0)
	probe.set("cronlite_3", 0, 0)
	return probe
}

type fakeChaosDB struct {
	rows  []ChaosExecutionRow
	depth int
	stuck []string
	err   error
}

func (f *fakeChaosDB) chaosExecutions(context.Context, []string) ([]ChaosExecutionRow, error) {
	return f.rows, f.err
}
func (f *fakeChaosDB) chaosQueueDepth(context.Context, []string) (int, error) { return f.depth, f.err }
func (f *fakeChaosDB) chaosStuck(context.Context, []string, time.Duration, time.Duration) ([]string, error) {
	return f.stuck, f.err
}

func newTestSampler(probe instanceProbe, db chaosDiagnosticReader) *timelineSampler {
	return newTimelineSampler(probe, db, func() []string { return []string{"j1"} }, func() string { return "" }, time.Second, testChaosSettings())
}

func TestSampleLeaderSumCountsReachableOnly(t *testing.T) {
	probe := newFakeProbe()
	probe.set("cronlite_2", 1, 0)
	probe.down["cronlite_2"] = true
	sample := newTestSampler(probe, &fakeChaosDB{}).sampleOnce(context.Background(), time.Now())
	if sample.LeaderSum != 1 {
		t.Fatalf("leader sum = %d", sample.LeaderSum)
	}
	if sample.Instances[1].Service != "cronlite_2" || sample.Instances[1].Reachable || sample.Instances[1].Leader {
		t.Fatalf("instance 2 = %+v", sample.Instances[1])
	}
}

func TestTriggeredDeltaHandlesReset(t *testing.T) {
	probe := newFakeProbe()
	sampler := newTestSampler(probe, &fakeChaosDB{})
	var deltas []float64
	for _, value := range []float64{10, 12, 3, 5} {
		probe.set("cronlite_1", 1, value)
		sample := sampler.sampleOnce(context.Background(), time.Now())
		deltas = append(deltas, sample.Instances[0].TriggeredDelta)
	}
	want := []float64{0, 2, 3, 2}
	for i := range want {
		if deltas[i] != want[i] {
			t.Fatalf("deltas = %v, want %v", deltas, want)
		}
	}
}

func TestHungScrapeTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()
	urls := map[string]string{"cronlite_1": server.URL, "cronlite_2": server.URL, "cronlite_3": server.URL}
	probe := newHTTPInstanceProbe(urls, urls, "key", 500*time.Millisecond)
	started := time.Now()
	sample := newTestSampler(probe, &fakeChaosDB{}).sampleOnce(context.Background(), time.Now())
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("sampleOnce took %s", elapsed)
	}
	for _, instance := range sample.Instances {
		if instance.Reachable || instance.Ready {
			t.Fatalf("hung instance reported %+v", instance)
		}
	}
}

func TestDBErrorsBecomeMinusOne(t *testing.T) {
	sample := newTestSampler(newFakeProbe(), &fakeChaosDB{err: errors.New("paused")}).sampleOnce(context.Background(), time.Now())
	if sample.QueueDepth != -1 || sample.StuckCount != -1 {
		t.Fatalf("queue=%d stuck=%d", sample.QueueDepth, sample.StuckCount)
	}
}

func TestSamplerRunCollectsSamples(t *testing.T) {
	sampler := newTimelineSampler(newFakeProbe(), &fakeChaosDB{depth: 2}, func() []string { return []string{"j1"} },
		func() string { return "proc-stop:leader:med:30s" }, 10*time.Millisecond, testChaosSettings())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	sampler.run(ctx)
	samples := sampler.snapshot()
	if len(samples) < 2 || samples[0].QueueDepth != 2 || samples[0].ActiveFault != "proc-stop:leader:med:30s" {
		t.Fatalf("samples = %+v", samples)
	}
}

func (p *fakeInstanceProbe) services() []string {
	return []string{"cronlite_1", "cronlite_2", "cronlite_3"}
}
