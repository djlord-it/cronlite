package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseDockerStatsForAllNamedServices(t *testing.T) {
	ids := map[string]string{"abcdef1234567890": "cronlite_1", "fedcba9876543210": "postgres"}
	input := strings.Join([]string{
		`{"Container":"abcdef123456","CPUPerc":"125.50%","MemUsage":"12.5MiB / 1GiB"}`,
		`{"Container":"fedcba987654","CPUPerc":"50.00%","MemUsage":"1.5GiB / 2GiB"}`,
	}, "\n")
	got, err := parseDockerStats([]byte(input), ids, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Service != "cronlite_1" || got[0].CPUPercent != 125.5 || got[0].MemoryBytes != 13107200 {
		t.Fatalf("first sample = %+v", got)
	}
	if got[1].Service != "postgres" || got[1].MemoryBytes != 1610612736 {
		t.Fatalf("second sample = %+v", got[1])
	}
}

type resourceCommandRunner struct{}

func (resourceCommandRunner) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) > 2 && args[0] == "compose" && args[len(args)-2] == "-q" {
		return []byte("id-" + args[len(args)-1]), nil
	}
	if len(args) > 0 && args[0] == "stats" {
		var rows []string
		for _, service := range []string{"postgres", "cronlite_1", "cronlite_2", "cronlite_3"} {
			rows = append(rows, fmt.Sprintf(`{"Container":"id-%s","CPUPerc":"10%%","MemUsage":"2MiB / 1GiB"}`, service))
		}
		return []byte(strings.Join(rows, "\n")), nil
	}
	return nil, fmt.Errorf("unexpected command: %v", args)
}

func TestResourceSamplerCapturesAllManagedServices(t *testing.T) {
	controller := &composeController{File: "tools/benchmark/docker-compose.yml", Project: "cronlite-benchmark-test", Runner: resourceCommandRunner{}}
	sampler, err := startResourceSampler(context.Background(), controller, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	samples, warnings := sampler.stop()
	if len(warnings) != 0 || len(samples) != 4 {
		t.Fatalf("samples=%+v warnings=%v", samples, warnings)
	}
}

type timeoutResourceRunner struct{}

func (timeoutResourceRunner) run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestResourceSamplerReportsSampleTimeout(t *testing.T) {
	sampler := &resourceSampler{
		controller: &composeController{Runner: timeoutResourceRunner{}},
		ids:        map[string]string{"id-postgres": "postgres"},
		timeout:    time.Millisecond,
	}
	sampler.sample(context.Background())
	if len(sampler.warnings) != 1 || !strings.Contains(sampler.warnings[0], "deadline exceeded") {
		t.Fatalf("sample warnings = %v", sampler.warnings)
	}
}

func TestResourceSamplerIgnoresShutdownCancellation(t *testing.T) {
	sampler := &resourceSampler{
		controller: &composeController{Runner: timeoutResourceRunner{}},
		ids:        map[string]string{"id-postgres": "postgres"},
		timeout:    time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sampler.sample(ctx)
	if len(sampler.warnings) != 0 {
		t.Fatalf("sample warnings = %v", sampler.warnings)
	}
}

func TestSummarizeResourceSamplesReportsAverageP95AndPeak(t *testing.T) {
	var samples []ResourceSample
	for _, cpu := range []float64{10, 20, 30} {
		samples = append(samples, ResourceSample{Service: "postgres", CPUPercent: cpu, MemoryBytes: uint64(cpu) * 1024})
	}
	got := summarizeResourceSamples(samples)
	if len(got) != 1 || got[0].SampleCount != 3 || got[0].AverageCPUPercent != 20 || got[0].P95CPUPercent != 30 || got[0].PeakCPUPercent != 30 || got[0].AverageMemoryBytes != 20480 || got[0].P95MemoryBytes != 30720 || got[0].PeakMemoryBytes != 30720 {
		t.Fatalf("summary = %+v", got)
	}
}
