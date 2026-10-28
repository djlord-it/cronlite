package main

import (
	"slices"
	"time"
)

const (
	invariantPass   = "pass"
	invariantFail   = "fail"
	invariantNA     = "n/a"
	invariantNoData = "no data"

	verdictStrength     = "strength"
	verdictWeakPoint    = "weak point"
	verdictInconclusive = "inconclusive"

	runScoreName = "run"
)

var scoredInvariants = []InvariantID{
	InvariantNoLost, InvariantDuplicates, InvariantLateness, InvariantLeader,
	InvariantRecovery, InvariantStuck, InvariantSignatures,
}

type DuplicateCounts struct {
	Explained   int `json:"explained"`
	Unexplained int `json:"unexplained"`
	Concurrent  int `json:"concurrent"`
}

type FaultScore struct {
	Fault      string                 `json:"fault"`
	Invariants map[InvariantID]string `json:"invariants"`
	Duplicates DuplicateCounts        `json:"duplicates"`
	Lateness   []LatenessStat         `json:"lateness"`
	Recovery   *time.Duration         `json:"recovery_ns,omitempty"`
	Verdict    string                 `json:"verdict"`
	Knobs      []string               `json:"knobs,omitempty"`
}

type ChaosResult struct {
	Settings   ChaosSettings    `json:"settings"`
	Windows    []FaultWindow    `json:"windows"`
	Scores     []FaultScore     `json:"scores"`
	Breaches   []Breach         `json:"breaches"`
	Timeline   []TimelineSample `json:"timeline"`
	Duplicates DuplicateCounts  `json:"duplicates"`
}

// newChaosResult scores each fault window, plus a "run" row for breaches no
// fault explains.
func newChaosResult(
	in ChaosInputs,
	breaches []Breach,
	lateness []LatenessStat,
	recovery map[string]time.Duration,
	duplicates DuplicateCounts,
) *ChaosResult {
	result := &ChaosResult{
		Settings:   in.Settings,
		Windows:    in.Windows,
		Breaches:   breaches,
		Timeline:   in.Timeline,
		Duplicates: duplicates,
	}
	explained := explainedByFault(in)
	for index, window := range in.Windows {
		key := faultKey(index, window)
		score := FaultScore{Fault: key, Invariants: make(map[InvariantID]string)}
		score.Duplicates.Explained = explained[key]
		for _, stat := range lateness {
			if stat.Window == key {
				score.Lateness = append(score.Lateness, stat)
			}
		}
		if duration, ok := recovery[key]; ok {
			score.Recovery = &duration
		}
		hasSamples := samplesBetween(in.Timeline, window.AppliedAt, windowEnd(window, in.RecoveryTimeout))
		for _, invariant := range scoredInvariants {
			result := faultInvariantResult(invariant, key, breaches, score)
			if result == invariantPass && (invariant == InvariantLeader && !hasSamples ||
				invariant == InvariantNoLost && in.ExecutionsUnavailable) {
				result = invariantNoData
			}
			score.Invariants[invariant] = result
		}
		score.Verdict = verdictFor(score, window.ApplyError != "")
		score.Knobs = knobsFor(key, breaches)
		result.Scores = append(result.Scores, score)
	}

	run := FaultScore{Fault: runScoreName, Invariants: make(map[InvariantID]string), Duplicates: duplicates}
	for _, invariant := range scoredInvariants {
		switch {
		case hasBreach(breaches, invariant, unattributedFault):
			run.Invariants[invariant] = invariantFail
		case invariant == InvariantLeader && len(in.Timeline) == 0,
			invariant == InvariantNoLost && in.ExecutionsUnavailable:
			run.Invariants[invariant] = invariantNoData
		default:
			run.Invariants[invariant] = invariantPass
		}
	}
	run.Verdict = verdictFor(run, false)
	run.Knobs = knobsFor(unattributedFault, breaches)
	result.Scores = append(result.Scores, run)
	return result
}

func faultInvariantResult(invariant InvariantID, key string, breaches []Breach, score FaultScore) string {
	if hasBreach(breaches, invariant, key) {
		return invariantFail
	}
	switch invariant {
	case InvariantStuck, InvariantDuplicates:
		// Evaluated for the whole run; see the run row.
		return invariantNA
	case InvariantLateness:
		for _, stat := range score.Lateness {
			if stat.Count > 0 {
				return invariantPass
			}
		}
		return invariantNoData
	case InvariantRecovery:
		if score.Recovery == nil {
			return invariantNoData
		}
	}
	return invariantPass
}

func samplesBetween(timeline []TimelineSample, from, to time.Time) bool {
	for _, sample := range timeline {
		if !sample.At.Before(from) && !sample.At.After(to) {
			return true
		}
	}
	return false
}

func verdictFor(score FaultScore, applyFailed bool) string {
	for _, result := range score.Invariants {
		if result == invariantFail {
			return verdictWeakPoint
		}
	}
	if applyFailed || score.Invariants[InvariantLateness] == invariantNoData ||
		score.Invariants[InvariantLeader] == invariantNoData || score.Invariants[InvariantNoLost] == invariantNoData {
		return verdictInconclusive
	}
	return verdictStrength
}

func hasBreach(breaches []Breach, invariant InvariantID, fault string) bool {
	for _, breach := range breaches {
		if breach.Invariant == invariant && breach.Fault == fault {
			return true
		}
	}
	return false
}

func knobsFor(fault string, breaches []Breach) []string {
	var knobs []string
	for _, breach := range breaches {
		if breach.Fault != fault || breach.Bound == nil {
			continue
		}
		for _, knob := range breach.Bound.Knobs {
			if !slices.Contains(knobs, knob) {
				knobs = append(knobs, knob)
			}
		}
	}
	return knobs
}

// explainedByFault counts explained duplicates per fault, keyed by the fault
// active when the duplicate arrived.
func explainedByFault(in ChaosInputs) map[string]int {
	counts := make(map[string]int)
	seenSuccess := make(map[string]bool)
	callbacks := slices.Clone(in.Callbacks)
	slices.SortStableFunc(callbacks, func(a, b CallbackObservation) int { return a.ReceivedAt.Compare(b.ReceivedAt) })
	for _, callback := range callbacks {
		if !seenSuccess[callback.ExecutionID] {
			seenSuccess[callback.ExecutionID] = isSuccess(callback.StatusCode)
			continue
		}
		if key := in.attribute(callback.ReceivedAt); key != unattributedFault {
			counts[key]++
		}
	}
	return counts
}
