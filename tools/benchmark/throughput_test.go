package main

import (
	"testing"
	"time"
)

func TestLoadThroughputCountsOnlyDeliveredExecutions(t *testing.T) {
	started := time.Now()
	scenario := ScenarioResult{Name: "load", StartedAt: started, FinishedAt: started.Add(time.Second), Executions: []ExecutionRecord{
		{APIExecution: APIExecution{Status: "delivered"}},
		{APIExecution: APIExecution{Status: "failed"}},
	}}
	if got := loadThroughput(&scenario); got != 1 {
		t.Fatalf("throughput = %f, want 1", got)
	}
}
