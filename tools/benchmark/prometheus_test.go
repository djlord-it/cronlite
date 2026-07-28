package main

import (
	"math"
	"testing"
)

func TestPrometheusDelta(t *testing.T) {
	before := parsePrometheus([]byte(
		"cronlite_dispatcher_delivery_outcomes_total{outcome=\"success\"} 2\n",
	))
	after := parsePrometheus([]byte(
		"cronlite_dispatcher_delivery_outcomes_total{outcome=\"success\"} 5\n",
	))
	key := `cronlite_dispatcher_delivery_outcomes_total{outcome="success"}`
	if got := metricDelta(before, after)[key]; got != 3 {
		t.Fatalf("delta = %v", got)
	}
}

func TestPrometheusParserIgnoresCommentsAndNonFiniteValues(t *testing.T) {
	got := parsePrometheus([]byte(
		"# HELP metric sample\nmetric 1.5\nnan_metric NaN\ninf_metric +Inf\n",
	))
	if got["metric"] != 1.5 {
		t.Fatalf("metric = %v", got["metric"])
	}
	if _, ok := got["nan_metric"]; ok {
		t.Fatal("NaN sample retained")
	}
	if _, ok := got["inf_metric"]; ok {
		t.Fatal("infinite sample retained")
	}
	for _, value := range got {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			t.Fatalf("non-finite value retained: %v", value)
		}
	}
}

func TestPrometheusDeltaTreatsCounterResetAsCurrentValue(t *testing.T) {
	before := metricSnapshot{"counter": 10}
	after := metricSnapshot{"counter": 2}
	if got := metricDelta(before, after)["counter"]; got != 2 {
		t.Fatalf("reset delta = %v", got)
	}
}
