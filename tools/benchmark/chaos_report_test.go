package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fixtureChaosResult has one clean fault and one fault whose lateness broke
// its bound.
func fixtureChaosResult() *ChaosResult {
	in := latenessInputs()
	in.Windows = []FaultWindow{stopWindow(100, 130, ptrTime(at(132))), stopWindow(200, 230, ptrTime(at(233)))}
	in.Manual = append(in.Manual,
		ManualSample{ExecutionID: "ok1", TriggeredAt: at(101)},
		ManualSample{ExecutionID: "post1", TriggeredAt: at(130.5)},
		ManualSample{ExecutionID: "slow1", TriggeredAt: at(201)},
		ManualSample{ExecutionID: "post2", TriggeredAt: at(231)},
	)
	in.Callbacks = append(in.Callbacks,
		callback("ok1", 101.2, 204), callback("post1", 130.6, 204),
		callback("slow1", 260, 204), callback("post2", 231.1, 204))
	in.Timeline = []TimelineSample{sample(10, 1), sample(131, 1), sample(132, 1), sample(133, 1), sample(233, 1), sample(234, 1), sample(235, 1)}
	return scoreChaos(in, 30*time.Second)
}

func TestChaosScorecardVerdicts(t *testing.T) {
	chaos := fixtureChaosResult()
	if len(chaos.Scores) != 3 {
		t.Fatalf("scores = %+v", chaos.Scores)
	}
	if chaos.Scores[0].Verdict != verdictStrength {
		t.Fatalf("first fault verdict = %s (%+v)", chaos.Scores[0].Verdict, chaos.Scores[0].Invariants)
	}
	second := chaos.Scores[1]
	if second.Verdict != verdictWeakPoint || second.Invariants[InvariantLateness] != invariantFail {
		t.Fatalf("second fault = %+v", second)
	}
	if !slices.Contains(second.Knobs, "LEADER_RETRY_INTERVAL") {
		t.Fatalf("second fault knobs = %v", second.Knobs)
	}
	if chaos.Scores[2].Fault != runScoreName {
		t.Fatalf("last row = %s", chaos.Scores[2].Fault)
	}
}

func chaosRunResult() RunResult {
	result := fixtureRunResult()
	result.Chaos = fixtureChaosResult()
	return result
}

func TestChaosSectionAfterCorrectness(t *testing.T) {
	report := renderReport(chaosRunResult(), OutputPaths{})
	chaos := strings.Index(report, "## Chaos")
	correctness := strings.Index(report, "## Correctness Findings")
	scenarios := strings.Index(report, "## Scenario Summary")
	if chaos < 0 || chaos < correctness || chaos > scenarios {
		t.Fatalf("chaos=%d correctness=%d scenarios=%d", chaos, correctness, scenarios)
	}
	for _, want := range []string{"### Strengths", "### Weak points", "chaos_lateness_exceeded", "= 34.1s", "LEADER_RETRY_INTERVAL"} {
		if !strings.Contains(report, want) {
			t.Errorf("chaos section missing %q", want)
		}
	}
	if strings.Contains(renderReport(fixtureRunResult(), OutputPaths{}), "## Chaos") {
		t.Fatal("non-chaos report has a Chaos section")
	}
}

func TestChaosTimelineCSVHeader(t *testing.T) {
	var out bytes.Buffer
	if err := writeChaosTimelineCSV(&out, fixtureChaosResult()); err != nil {
		t.Fatal(err)
	}
	header := strings.SplitN(out.String(), "\n", 2)[0]
	want := "at,active_fault,leader_sum,queue_depth,stuck_count," +
		"cronlite_1_reachable,cronlite_1_ready,cronlite_1_leader,cronlite_1_triggered_delta," +
		"cronlite_2_reachable,cronlite_2_ready,cronlite_2_leader,cronlite_2_triggered_delta," +
		"cronlite_3_reachable,cronlite_3_ready,cronlite_3_leader,cronlite_3_triggered_delta"
	if header != want {
		t.Fatalf("header = %q", header)
	}
	if lines := strings.Count(out.String(), "\n"); lines != 8 {
		t.Fatalf("lines = %d, want header + 7 samples", lines)
	}
}

func TestChaosScorecardCSV(t *testing.T) {
	var out bytes.Buffer
	if err := writeChaosScorecardCSV(&out, fixtureChaosResult()); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.HasPrefix(text, "fault,invariant,result,p99_ms,bound_ms,recovery_ms,verdict,knobs\n") {
		t.Fatalf("csv = %q", text)
	}
	if !strings.Contains(text, "I3,fail,") || !strings.Contains(text, "weak point,LEADER_RETRY_INTERVAL;") {
		t.Fatalf("csv missing the failing lateness row:\n%s", text)
	}
}

func TestChaosOutputsOnlyWhenChaos(t *testing.T) {
	dir := t.TempDir()
	paths, err := writeOutputs(dir, fixtureRunResult())
	if err != nil {
		t.Fatal(err)
	}
	if paths.ChaosTimelineCSV != "" || paths.ChaosScorecardCSV != "" {
		t.Fatalf("paths = %+v", paths)
	}
	if _, err := os.Stat(filepath.Join(dir, "chaos-timeline.csv")); !os.IsNotExist(err) {
		t.Fatalf("chaos timeline written for a non-chaos run: %v", err)
	}
	raw, err := os.ReadFile(paths.JSON)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["chaos"]; ok {
		t.Fatal("non-chaos JSON has a chaos key")
	}

	chaosDir := t.TempDir()
	paths, err = writeOutputs(chaosDir, chaosRunResult())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.ChaosTimelineCSV, paths.ChaosScorecardCSV} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("chaos output %q: %v", path, err)
		}
	}
}

func TestSchemaVersion(t *testing.T) {
	if resultSchemaVersion != "1.1" {
		t.Fatalf("schema version = %s", resultSchemaVersion)
	}
}
