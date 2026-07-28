package main

import (
	"math"
	"reflect"
	"testing"
)

func TestSummarizeRepresentativeDistribution(t *testing.T) {
	input := []float64{1, 2, 3, 4, 100}
	got := summarize(input)
	want := Stats{
		Count:  5,
		Min:    1,
		Max:    100,
		Mean:   22,
		Median: 3,
		StdDev: math.Sqrt(1902.5),
		P50:    3,
		P90:    100,
		P95:    100,
		P99:    100,
	}
	assertStatsClose(t, want, got)
	if len(got.Warnings) == 0 {
		t.Fatal("expected unstable high-percentile warning")
	}
	if !reflect.DeepEqual(input, []float64{1, 2, 3, 4, 100}) {
		t.Fatalf("summarize mutated input: %v", input)
	}
}

func TestSummarizeEmptyIsUnavailable(t *testing.T) {
	got := summarize(nil)
	if got.Count != 0 || !got.Unavailable {
		t.Fatalf("unexpected empty summary: %+v", got)
	}
}

func TestSummarizeSingletonHasZeroStandardDeviation(t *testing.T) {
	got := summarize([]float64{42})
	if got.StdDev != 0 || got.Median != 42 || got.P99 != 42 {
		t.Fatalf("unexpected singleton summary: %+v", got)
	}
}

func TestSummarizeUsesMiddleAverageForEvenSamples(t *testing.T) {
	got := summarize([]float64{4, 1, 3, 2})
	if got.Median != 2.5 {
		t.Fatalf("median = %v", got.Median)
	}
}

func assertStatsClose(t *testing.T, want, got Stats) {
	t.Helper()
	if want.Count != got.Count {
		t.Fatalf("count: want %d got %d", want.Count, got.Count)
	}
	for name, pair := range map[string][2]float64{
		"min":    {want.Min, got.Min},
		"max":    {want.Max, got.Max},
		"mean":   {want.Mean, got.Mean},
		"median": {want.Median, got.Median},
		"stddev": {want.StdDev, got.StdDev},
		"p50":    {want.P50, got.P50},
		"p90":    {want.P90, got.P90},
		"p95":    {want.P95, got.P95},
		"p99":    {want.P99, got.P99},
	} {
		if math.Abs(pair[0]-pair[1]) > 1e-9 {
			t.Errorf("%s: want %v got %v", name, pair[0], pair[1])
		}
	}
}
