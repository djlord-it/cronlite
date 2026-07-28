package main

import (
	"math"
	"slices"
)

type Stats struct {
	Count       int      `json:"count"`
	Successes   int      `json:"successes"`
	Failures    int      `json:"failures"`
	Duplicates  int      `json:"duplicates"`
	Min         float64  `json:"min"`
	Max         float64  `json:"max"`
	Mean        float64  `json:"mean"`
	Median      float64  `json:"median"`
	StdDev      float64  `json:"standard_deviation"`
	P50         float64  `json:"p50"`
	P90         float64  `json:"p90"`
	P95         float64  `json:"p95"`
	P99         float64  `json:"p99"`
	Unavailable bool     `json:"unavailable"`
	Warnings    []string `json:"warnings,omitempty"`
}

func summarize(values []float64) Stats {
	if len(values) == 0 {
		return Stats{Unavailable: true}
	}

	sorted := slices.Clone(values)
	slices.Sort(sorted)

	var sum float64
	for _, value := range sorted {
		sum += value
	}
	mean := sum / float64(len(sorted))

	var varianceSum float64
	for _, value := range sorted {
		delta := value - mean
		varianceSum += delta * delta
	}
	var stdDev float64
	if len(sorted) > 1 {
		stdDev = math.Sqrt(varianceSum / float64(len(sorted)-1))
	}

	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
	}

	return Stats{
		Count:    len(sorted),
		Min:      sorted[0],
		Max:      sorted[len(sorted)-1],
		Mean:     mean,
		Median:   median,
		StdDev:   stdDev,
		P50:      percentile(sorted, 0.50),
		P90:      percentile(sorted, 0.90),
		P95:      percentile(sorted, 0.95),
		P99:      percentile(sorted, 0.99),
		Warnings: percentileWarnings(len(sorted)),
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func percentileWarnings(count int) []string {
	var warnings []string
	if count < 10 {
		warnings = append(warnings, "p90 is unstable with fewer than 10 samples")
	}
	if count < 20 {
		warnings = append(warnings, "p95 is unstable with fewer than 20 samples")
	}
	if count < 100 {
		warnings = append(warnings, "p99 is unstable with fewer than 100 samples")
	}
	return warnings
}
