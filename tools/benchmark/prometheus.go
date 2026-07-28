package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxPrometheusBodyBytes = 8 << 20

type metricSnapshot map[string]float64

type prometheusCollector struct {
	url    string
	apiKey string
	client *http.Client
}

func newPrometheusCollector(url, apiKey string, timeout time.Duration) *prometheusCollector {
	return &prometheusCollector{
		url:    url,
		apiKey: apiKey,
		client: &http.Client{Timeout: timeout},
	}
}

func (c *prometheusCollector) snapshot(
	ctx context.Context,
) (metricSnapshot, Observation, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, Observation{}, err
	}
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	started := time.Now()
	response, err := c.client.Do(request)
	finished := time.Now()
	observation := Observation{
		Kind:       "metrics",
		Name:       "prometheus_snapshot",
		StartedAt:  started.UTC(),
		FinishedAt: finished.UTC(),
		Duration:   finished.Sub(started),
		Provenance: ProvenanceDirect,
	}
	if err != nil {
		observation.Error = err.Error()
		observation.ErrorClass = classifyNetworkError(err)
		return nil, observation, err
	}
	defer response.Body.Close()
	observation.StatusCode = response.StatusCode
	if response.StatusCode != http.StatusOK {
		return nil, observation, fmt.Errorf("metrics returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPrometheusBodyBytes))
	if err != nil {
		return nil, observation, err
	}
	return parsePrometheus(body), observation, nil
}

func parsePrometheus(body []byte) metricSnapshot {
	result := make(metricSnapshot)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		result[fields[0]] = value
	}
	return result
}

func metricDelta(before, after metricSnapshot) metricSnapshot {
	result := make(metricSnapshot)
	for key, current := range after {
		previous, ok := before[key]
		if !ok || current < previous {
			result[key] = current
			continue
		}
		result[key] = current - previous
	}
	return result
}
