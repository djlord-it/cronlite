package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ResourceSample struct {
	ObservedAt  time.Time `json:"observed_at"`
	Service     string    `json:"service"`
	CPUPercent  float64   `json:"cpu_percent"`
	MemoryBytes uint64    `json:"memory_bytes"`
}

type ResourceSummary struct {
	Service            string  `json:"service"`
	SampleCount        int     `json:"sample_count"`
	AverageCPUPercent  float64 `json:"average_cpu_percent"`
	P95CPUPercent      float64 `json:"p95_cpu_percent"`
	PeakCPUPercent     float64 `json:"peak_cpu_percent"`
	AverageMemoryBytes uint64  `json:"average_memory_bytes"`
	P95MemoryBytes     uint64  `json:"p95_memory_bytes"`
	PeakMemoryBytes    uint64  `json:"peak_memory_bytes"`
}

func summarizeResourceSamples(samples []ResourceSample) []ResourceSummary {
	groups := make(map[string][]ResourceSample)
	for _, sample := range samples {
		groups[sample.Service] = append(groups[sample.Service], sample)
	}
	services := sortedKeys(groups)
	result := make([]ResourceSummary, 0, len(services))
	for _, service := range services {
		group := groups[service]
		cpu := make([]float64, 0, len(group))
		memory := make([]float64, 0, len(group))
		for _, sample := range group {
			cpu = append(cpu, sample.CPUPercent)
			memory = append(memory, float64(sample.MemoryBytes))
		}
		cpuStats, memoryStats := summarize(cpu), summarize(memory)
		result = append(result, ResourceSummary{
			Service: service, SampleCount: len(group),
			AverageCPUPercent: cpuStats.Mean, P95CPUPercent: cpuStats.P95, PeakCPUPercent: cpuStats.Max,
			AverageMemoryBytes: uint64(math.Round(memoryStats.Mean)),
			P95MemoryBytes:     uint64(memoryStats.P95), PeakMemoryBytes: uint64(memoryStats.Max),
		})
	}
	return result
}

func parseDockerStats(data []byte, ids map[string]string, at time.Time) ([]ResourceSample, error) {
	var samples []ResourceSample
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		var row struct {
			Container string `json:"Container"`
			CPUPerc   string `json:"CPUPerc"`
			MemUsage  string `json:"MemUsage"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return samples, fmt.Errorf("decode docker stats: %w", err)
		}
		service := ""
		for id, name := range ids {
			if strings.HasPrefix(id, row.Container) || strings.HasPrefix(row.Container, id) {
				service = name
				break
			}
		}
		if service == "" {
			return samples, fmt.Errorf("unrecognized docker container %q", row.Container)
		}
		cpu, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(row.CPUPerc), "%"), 64)
		if err != nil || math.IsNaN(cpu) || math.IsInf(cpu, 0) || cpu < 0 {
			return samples, fmt.Errorf("invalid CPU percentage for %s: %q", service, row.CPUPerc)
		}
		memory, err := parseDockerMemory(strings.TrimSpace(strings.Split(row.MemUsage, "/")[0]))
		if err != nil {
			return samples, fmt.Errorf("invalid memory usage for %s: %w", service, err)
		}
		samples = append(samples, ResourceSample{ObservedAt: at.UTC(), Service: service, CPUPercent: cpu, MemoryBytes: memory})
	}
	if err := scanner.Err(); err != nil {
		return samples, err
	}
	if len(samples) != len(ids) {
		return samples, fmt.Errorf("docker stats returned %d of %d services", len(samples), len(ids))
	}
	slices.SortFunc(samples, func(a, b ResourceSample) int { return strings.Compare(a.Service, b.Service) })
	return samples, nil
}

func parseDockerMemory(value string) (uint64, error) {
	units := []struct {
		name string
		size float64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"B", 1},
	}
	for _, unit := range units {
		if strings.HasSuffix(value, unit.name) {
			n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(value, unit.name)), 64)
			if err != nil || n < 0 || math.IsNaN(n) || math.IsInf(n, 0) || n*unit.size > math.MaxUint64 {
				return 0, fmt.Errorf("invalid value %q", value)
			}
			return uint64(math.Round(n * unit.size)), nil
		}
	}
	return 0, fmt.Errorf("unknown unit in %q", value)
}

type resourceSampler struct {
	controller *composeController
	ids        map[string]string
	interval   time.Duration
	timeout    time.Duration
	cancel     context.CancelFunc
	done       chan struct{}
	mu         sync.Mutex
	samples    []ResourceSample
	warnings   []string
}

func startResourceSampler(ctx context.Context, controller *composeController, interval time.Duration) (*resourceSampler, error) {
	ids := make(map[string]string)
	for _, service := range append([]string{"postgres"}, controller.cronLiteServices()...) {
		output, err := controller.Runner.run(ctx, "docker", controller.composeArgs("ps", "-q", service)...)
		if err != nil {
			return nil, fmt.Errorf("find %s container: %w", service, err)
		}
		id := strings.TrimSpace(string(output))
		if id == "" || strings.Contains(id, "\n") {
			return nil, fmt.Errorf("expected one container for %s", service)
		}
		ids[id] = service
	}
	sampleCtx, cancel := context.WithCancel(ctx)
	sampler := &resourceSampler{controller: controller, ids: ids, interval: interval, timeout: 10 * time.Second, cancel: cancel, done: make(chan struct{})}
	sampler.sample(sampleCtx)
	go sampler.loop(sampleCtx)
	return sampler, nil
}

func (s *resourceSampler) loop(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sample(ctx)
		}
	}
}

func (s *resourceSampler) sample(parentCtx context.Context) {
	timeout := s.timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()
	args := []string{"stats", "--no-stream", "--format", "{{json .}}"}
	ids := sortedKeys(s.ids)
	args = append(args, ids...)
	output, err := s.controller.Runner.run(ctx, "docker", args...)
	if err != nil {
		if parentCtx.Err() != nil {
			return
		}
		s.mu.Lock()
		s.warnings = append(s.warnings, err.Error())
		s.mu.Unlock()
		return
	}
	samples, parseErr := parseDockerStats(output, s.ids, time.Now())
	s.mu.Lock()
	s.samples = append(s.samples, samples...)
	if parseErr != nil {
		s.warnings = append(s.warnings, parseErr.Error())
	}
	s.mu.Unlock()
}

func (s *resourceSampler) stop() ([]ResourceSample, []string) {
	s.cancel()
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.samples), slices.Clone(s.warnings)
}
