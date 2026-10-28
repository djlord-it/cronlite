package main

import (
	"testing"
	"time"
)

var chaosT0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func at(seconds float64) time.Time {
	return chaosT0.Add(time.Duration(seconds * float64(time.Second)))
}

func ptrTime(t time.Time) *time.Time { return &t }

func stopWindow(applied, cleared float64, recovered *time.Time) FaultWindow {
	return FaultWindow{
		Spec:        FaultSpec{Type: FaultProcStop, Target: "leader", Intensity: "med", Duration: time.Duration(cleared-applied) * time.Second},
		Services:    []string{"cronlite_1"},
		AppliedAt:   at(applied),
		ClearedAt:   at(cleared),
		RecoveredAt: recovered,
	}
}

func callback(executionID string, seconds float64, status int) CallbackObservation {
	return CallbackObservation{ExecutionID: executionID, ReceivedAt: at(seconds), StatusCode: status, SignatureValid: true, ConcurrentForExecution: 1}
}

func sample(seconds float64, leaderSum int, deltas ...float64) TimelineSample {
	s := TimelineSample{At: at(seconds), LeaderSum: leaderSum, QueueDepth: 0}
	for i, service := range []string{"cronlite_1", "cronlite_2", "cronlite_3"} {
		instance := InstanceSample{Service: service, Reachable: true, Ready: true}
		if i < len(deltas) {
			instance.TriggeredDelta = deltas[i]
		}
		s.Instances = append(s.Instances, instance)
	}
	return s
}

func baseInputs() ChaosInputs {
	return ChaosInputs{
		Settings:        testChaosSettings(),
		RecoveryTimeout: 60 * time.Second,
		RunStart:        at(0),
		RunEnd:          at(600),
		BaselineEnd:     at(30),
	}
}

func hasCode(breaches []Breach, code string) *Breach {
	for i := range breaches {
		if breaches[i].Code == code {
			return &breaches[i]
		}
	}
	return nil
}

func TestAttribute(t *testing.T) {
	windows := []FaultWindow{stopWindow(100, 130, ptrTime(at(140)))}
	key := faultKey(0, windows[0])
	if got := attribute(at(120), windows, time.Minute); got != key {
		t.Fatalf("inside window = %q, want %q", got, key)
	}
	if got := attribute(at(141), windows, time.Minute); got != unattributedFault {
		t.Fatalf("after recovery = %q", got)
	}
	windows[0].RecoveredAt = nil
	if got := attribute(at(185), windows, time.Minute); got != key {
		t.Fatalf("nil recovery uses timeout, got %q", got)
	}
	if got := attribute(at(191), windows, time.Minute); got != unattributedFault {
		t.Fatalf("past timeout = %q", got)
	}
}

func TestNoLostFlagsMissingFire(t *testing.T) {
	in := baseInputs()
	in.Jobs = []RecurringJob{{ID: "j1", CronExpression: "* * * * *", CreatedAt: at(-30)}}
	// Fires at 60, 120, ..., up to RunEnd-grace. Provide all but 120.
	for minute := 1; minute <= 9; minute++ {
		if minute == 2 {
			continue
		}
		id := "e" + string(rune('0'+minute))
		in.Executions = append(in.Executions, ChaosExecutionRow{ID: id, JobID: "j1", ScheduledAt: at(float64(60 * minute)), Status: "delivered"})
		in.Callbacks = append(in.Callbacks, callback(id, float64(60*minute)+1, 204))
	}
	breaches := checkNoLost(in, 30*time.Second)
	lost := hasCode(breaches, "chaos_lost_execution")
	if lost == nil || lost.Severity != SeverityCritical || !lost.At.Equal(at(120)) {
		t.Fatalf("breaches = %+v", breaches)
	}
	if len(breaches) != 1 {
		t.Fatalf("expected exactly one breach, got %+v", breaches)
	}
}

func TestNoLostIgnoresEdges(t *testing.T) {
	in := baseInputs()
	in.RunStart = at(61)
	in.RunEnd = at(200)
	in.Jobs = []RecurringJob{{ID: "j1", CronExpression: "* * * * *", CreatedAt: at(-30)}}
	in.Executions = []ChaosExecutionRow{{ID: "e2", JobID: "j1", ScheduledAt: at(120), Status: "delivered"}}
	in.Callbacks = []CallbackObservation{callback("e2", 121, 204)}
	// 60 is before RunStart, 180 is after RunEnd-grace (200-30=170).
	if breaches := checkNoLost(in, 30*time.Second); len(breaches) != 0 {
		t.Fatalf("edge fires flagged: %+v", breaches)
	}
}

func TestNoLostAcceptsFailedStatus(t *testing.T) {
	in := baseInputs()
	in.RunEnd = at(100)
	in.Jobs = []RecurringJob{{ID: "j1", CronExpression: "* * * * *", CreatedAt: at(-30)}}
	in.Executions = []ChaosExecutionRow{{ID: "e1", JobID: "j1", ScheduledAt: at(60), Status: "failed"}}
	if breaches := checkNoLost(in, 30*time.Second); len(breaches) != 0 {
		t.Fatalf("failed execution flagged: %+v", breaches)
	}
	in.Executions[0].Status = "in_progress"
	if hasCode(checkNoLost(in, 30*time.Second), "chaos_undelivered_execution") == nil {
		t.Fatal("undelivered in_progress execution not flagged")
	}
}

func TestDuplicateExplainedByOverlap(t *testing.T) {
	in := baseInputs()
	in.Windows = []FaultWindow{stopWindow(100, 130, ptrTime(at(140)))}
	in.Callbacks = []CallbackObservation{callback("e1", 90, 204), callback("e1", 110, 204)}
	explained, unexplained, _, breaches := classifyDuplicates(in)
	if explained != 1 || unexplained != 0 || len(breaches) != 0 {
		t.Fatalf("explained=%d unexplained=%d breaches=%+v", explained, unexplained, breaches)
	}
}

func TestDuplicateUnexplainedIsCritical(t *testing.T) {
	in := baseInputs()
	in.Windows = []FaultWindow{stopWindow(300, 330, ptrTime(at(340)))}
	in.Callbacks = []CallbackObservation{callback("e1", 90, 503), callback("e1", 100, 204), callback("e1", 110, 204)}
	_, unexplained, _, breaches := classifyDuplicates(in)
	breach := hasCode(breaches, "chaos_unexplained_duplicate")
	if unexplained != 1 || breach == nil || breach.Severity != SeverityCritical || breach.ExecutionIDs[0] != "e1" {
		t.Fatalf("unexplained=%d breaches=%+v", unexplained, breaches)
	}
}

func TestConcurrentCounted(t *testing.T) {
	in := baseInputs()
	second := callback("e1", 91, 204)
	second.ConcurrentForExecution = 2
	in.Callbacks = []CallbackObservation{callback("e1", 90, 204), second}
	if _, _, concurrent, _ := classifyDuplicates(in); concurrent != 1 {
		t.Fatalf("concurrent = %d", concurrent)
	}
}

func latenessInputs() ChaosInputs {
	in := baseInputs()
	// Baseline manual samples with 100ms lateness.
	for i := 0; i < 5; i++ {
		id := "b" + string(rune('0'+i))
		in.Manual = append(in.Manual, ManualSample{ExecutionID: id, TriggeredAt: at(float64(i))})
		in.Callbacks = append(in.Callbacks, callback(id, float64(i)+0.1, 204))
	}
	return in
}

func TestLatenessEmptyWindowNoBreach(t *testing.T) {
	in := latenessInputs()
	in.Windows = []FaultWindow{stopWindow(100, 130, ptrTime(at(140)))}
	stats, breaches := checkLateness(in)
	if len(breaches) != 0 {
		t.Fatalf("breaches = %+v", breaches)
	}
	found := false
	for _, stat := range stats {
		if stat.Window == faultKey(0, in.Windows[0]) && stat.Kind == "manual" {
			found = true
			if stat.Count != 0 || stat.P99 != nil {
				t.Fatalf("empty window stat = %+v", stat)
			}
		}
	}
	if !found {
		t.Fatalf("no stat for the empty fault window: %+v", stats)
	}
}

func TestLatenessOverBoundWarns(t *testing.T) {
	in := latenessInputs()
	in.Windows = []FaultWindow{stopWindow(100, 130, ptrTime(at(140)))}
	// Bound for a 30s proc-stop is 34.1s; this delivery is 40s late.
	in.Manual = append(in.Manual, ManualSample{ExecutionID: "f1", TriggeredAt: at(101)})
	in.Callbacks = append(in.Callbacks, callback("f1", 141, 204))
	_, breaches := checkLateness(in)
	breach := hasCode(breaches, "chaos_lateness_exceeded")
	if breach == nil || breach.Severity != SeverityWarning || breach.Bound == nil {
		t.Fatalf("breaches = %+v", breaches)
	}
}

func TestLatenessUnattributedIsCritical(t *testing.T) {
	in := latenessInputs()
	// Steady bound = 100ms*3 + 2s = 2.3s; this one is 5s late with no fault.
	in.Manual = append(in.Manual, ManualSample{ExecutionID: "s1", TriggeredAt: at(200)})
	in.Callbacks = append(in.Callbacks, callback("s1", 205, 204))
	_, breaches := checkLateness(in)
	breach := hasCode(breaches, "chaos_lateness_exceeded")
	if breach == nil || breach.Severity != SeverityCritical || breach.Fault != unattributedFault {
		t.Fatalf("breaches = %+v", breaches)
	}
}

func TestSplitBrainTwoConsecutiveGauge(t *testing.T) {
	in := baseInputs()
	in.Timeline = []TimelineSample{sample(40, 1), sample(41, 2), sample(42, 2), sample(43, 1)}
	breach := hasCode(checkLeadership(in), "chaos_split_brain")
	if breach == nil || breach.Severity != SeverityCritical || !breach.At.Equal(at(41)) {
		t.Fatalf("breach = %+v", breach)
	}
}

func TestSingleSampleHandoverNotBreach(t *testing.T) {
	in := baseInputs()
	in.Timeline = []TimelineSample{sample(40, 1, 1), sample(41, 1, 1, 1), sample(42, 1, 0, 1), sample(43, 2), sample(44, 1)}
	if breaches := checkLeadership(in); hasCode(breaches, "chaos_split_brain") != nil {
		t.Fatalf("handover flagged: %+v", breaches)
	}
}

func TestDualTriggerWithGaugeSumOne(t *testing.T) {
	in := baseInputs()
	in.Timeline = []TimelineSample{sample(40, 1, 1, 1), sample(41, 1, 1, 1)}
	if hasCode(checkLeadership(in), "chaos_split_brain") == nil {
		t.Fatal("two consecutive dual-trigger samples not flagged")
	}
	in.Timeline = []TimelineSample{sample(40, 2, 1, 1)}
	if hasCode(checkLeadership(in), "chaos_split_brain") == nil {
		t.Fatal("dual trigger with gauge sum 2 not flagged")
	}
}

func TestLeaderlessGapWarns(t *testing.T) {
	in := baseInputs()
	in.Windows = []FaultWindow{stopWindow(40, 70, nil)}
	in.Timeline = []TimelineSample{sample(40, 1), sample(41, 0), sample(42, 0), sample(43, 0), sample(44, 0), sample(46, 0), sample(47, 1)}
	breach := hasCode(checkLeadership(in), "chaos_leaderless_gap")
	if breach == nil || breach.Severity != SeverityWarning || breach.Fault != faultKey(0, in.Windows[0]) {
		t.Fatalf("breach = %+v", breach)
	}
	in.Timeline = []TimelineSample{sample(40, 1), sample(41, 0), sample(43, 1)}
	if hasCode(checkLeadership(in), "chaos_leaderless_gap") != nil {
		t.Fatal("2s gap under the 3.5s bound flagged")
	}
}

func TestRecoveryTime(t *testing.T) {
	in := baseInputs()
	in.Windows = []FaultWindow{stopWindow(40, 70, nil)}
	notReady := sample(71, 1)
	notReady.Instances[0].Ready = false
	in.Timeline = []TimelineSample{sample(10, 1), sample(70, 0), notReady, sample(73, 1), sample(74, 1), sample(75, 1)}
	times, breaches := recoveryTimes(in, Bound{}, time.Minute)
	// Recovered at the third consecutive healthy sample (73, 74, 75).
	if got := times[faultKey(0, in.Windows[0])]; got != 5*time.Second || len(breaches) != 0 {
		t.Fatalf("recovery = %s breaches = %+v", got, breaches)
	}
}

func TestRecoveryWaitsForManualDelivery(t *testing.T) {
	in := baseInputs()
	in.Windows = []FaultWindow{stopWindow(40, 70, nil)}
	in.Timeline = []TimelineSample{sample(71, 1), sample(72, 1), sample(73, 1), sample(74, 1), sample(75, 1)}
	in.Manual = []ManualSample{{ExecutionID: "m1", TriggeredAt: at(71)}}
	in.Callbacks = []CallbackObservation{callback("m1", 72.5, 204)}
	times, _ := recoveryTimes(in, Bound{Value: 2 * time.Second}, time.Minute)
	if got := times[faultKey(0, in.Windows[0])]; got != 5*time.Second {
		t.Fatalf("recovery = %s, want 5s (third healthy sample after the manual delivery)", got)
	}
}

func TestNotRecoveredWarns(t *testing.T) {
	in := baseInputs()
	in.Windows = []FaultWindow{stopWindow(40, 70, nil)}
	in.Timeline = []TimelineSample{sample(71, 0), sample(100, 0), sample(131, 0)}
	times, breaches := recoveryTimes(in, Bound{}, time.Minute)
	if _, ok := times[faultKey(0, in.Windows[0])]; ok {
		t.Fatal("recovery reported")
	}
	if breach := hasCode(breaches, "chaos_not_recovered"); breach == nil || breach.Severity != SeverityWarning {
		t.Fatalf("breaches = %+v", breaches)
	}
	in.Timeline = in.Timeline[:2]
	if _, breaches := recoveryTimes(in, Bound{}, time.Minute); len(breaches) != 0 {
		t.Fatalf("still within timeout but flagged: %+v", breaches)
	}
}

func TestStuckAndSignatureBreaches(t *testing.T) {
	in := baseInputs()
	in.Stuck = []string{"e7", "e8"}
	stuck := checkStuck(in)
	if len(stuck) != 2 || stuck[0].Code != "chaos_stuck_execution" || stuck[0].Severity != SeverityCritical {
		t.Fatalf("stuck = %+v", stuck)
	}
	bad := callback("e1", 10, 204)
	bad.SignatureValid = false
	in.Callbacks = []CallbackObservation{bad, callback("e2", 11, 204)}
	signatures := checkSignatures(in)
	if len(signatures) != 1 || signatures[0].Code != "chaos_invalid_signature" || signatures[0].ExecutionIDs[0] != "e1" {
		t.Fatalf("signatures = %+v", signatures)
	}
}
