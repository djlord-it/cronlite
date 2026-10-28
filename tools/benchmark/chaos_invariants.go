package main

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/robfig/cron/v3"
)

type InvariantID string

const (
	InvariantNoLost     InvariantID = "I1"
	InvariantDuplicates InvariantID = "I2"
	InvariantLateness   InvariantID = "I3"
	InvariantLeader     InvariantID = "I6"
	InvariantRecovery   InvariantID = "I7"
	InvariantStuck      InvariantID = "I8"
	InvariantSignatures InvariantID = "I10"
)

const unattributedFault = "unattributed"

type Breach struct {
	Invariant    InvariantID `json:"invariant"`
	Severity     Severity    `json:"severity"`
	Code         string      `json:"code"`
	At           time.Time   `json:"at"`
	Fault        string      `json:"fault"`
	ExecutionIDs []string    `json:"execution_ids,omitempty"`
	Message      string      `json:"message"`
	Bound        *Bound      `json:"bound,omitempty"`
}

type ManualSample struct {
	ExecutionID string    `json:"execution_id"`
	TriggeredAt time.Time `json:"triggered_at"`
}

type RecurringJob struct {
	ID             string    `json:"id"`
	CronExpression string    `json:"cron_expression"`
	CreatedAt      time.Time `json:"created_at"`
}

type ChaosInputs struct {
	Settings        ChaosSettings
	Windows         []FaultWindow
	Timeline        []TimelineSample
	Jobs            []RecurringJob
	Executions      []ChaosExecutionRow
	Manual          []ManualSample
	Callbacks       []CallbackObservation
	Stuck           []string
	RecoveryTimeout time.Duration
	RunStart        time.Time
	RunEnd          time.Time
	BaselineEnd     time.Time
}

type LatenessStat struct {
	Window string         `json:"window"`
	Kind   string         `json:"kind"`
	Count  int            `json:"count"`
	P99    *time.Duration `json:"p99_ns,omitempty"`
	Max    *time.Duration `json:"max_ns,omitempty"`
	Bound  *Bound         `json:"bound,omitempty"`
}

const (
	latenessManual    = "manual"
	latenessRecurring = "recurring"
	windowBaseline    = "baseline"
	windowSteady      = "steady"
)

// faultKey identifies one applied fault; the index separates repeated specs.
func faultKey(index int, window FaultWindow) string {
	return fmt.Sprintf("%s#%d", window.Spec, index+1)
}

func windowEnd(window FaultWindow, recoveryTimeout time.Duration) time.Time {
	switch {
	case window.RecoveredAt != nil:
		return *window.RecoveredAt
	case window.ClearedAt.IsZero():
		return time.Unix(1<<40, 0)
	default:
		return window.ClearedAt.Add(recoveryTimeout)
	}
}

// attribute returns the key of the fault whose window contains at.
func attribute(at time.Time, windows []FaultWindow, recoveryTimeout time.Duration) string {
	for index, window := range windows {
		if window.AppliedAt.IsZero() {
			continue
		}
		if !at.Before(window.AppliedAt) && !at.After(windowEnd(window, recoveryTimeout)) {
			return faultKey(index, window)
		}
	}
	return unattributedFault
}

func (in ChaosInputs) attribute(at time.Time) string {
	return attribute(at, in.Windows, in.RecoveryTimeout)
}

func isSuccess(status int) bool { return status >= 200 && status < 300 }

func firstCallbacks(callbacks []CallbackObservation) map[string]CallbackObservation {
	first := make(map[string]CallbackObservation)
	for _, callback := range callbacks {
		if existing, ok := first[callback.ExecutionID]; !ok || callback.ReceivedAt.Before(existing.ReceivedAt) {
			first[callback.ExecutionID] = callback
		}
	}
	return first
}

// checkNoLost is I1: every expected fire has a row that was delivered or
// failed terminally.
func checkNoLost(in ChaosInputs, grace time.Duration) []Breach {
	rows := make(map[string]ChaosExecutionRow)
	for _, row := range in.Executions {
		rows[fmt.Sprintf("%s@%d", row.JobID, row.ScheduledAt.UTC().Unix())] = row
	}
	delivered := make(map[string]bool)
	for _, callback := range in.Callbacks {
		if isSuccess(callback.StatusCode) {
			delivered[callback.ExecutionID] = true
		}
	}
	horizon := in.RunEnd.Add(-grace)
	var breaches []Breach
	for _, job := range in.Jobs {
		schedule, err := cron.ParseStandard(job.CronExpression)
		if err != nil {
			continue
		}
		for fire := schedule.Next(job.CreatedAt.UTC()); !fire.After(horizon); fire = schedule.Next(fire) {
			if !fire.After(in.RunStart) {
				continue
			}
			row, ok := rows[fmt.Sprintf("%s@%d", job.ID, fire.Unix())]
			switch {
			case !ok:
				breaches = append(breaches, Breach{
					Invariant: InvariantNoLost, Severity: SeverityCritical, Code: "chaos_lost_execution",
					At: fire, Fault: in.attribute(fire),
					Message: fmt.Sprintf("job %s has no execution for scheduled_at %s", job.ID, fire.Format(time.RFC3339)),
				})
			case !delivered[row.ID] && row.Status != "failed":
				breaches = append(breaches, Breach{
					Invariant: InvariantNoLost, Severity: SeverityCritical, Code: "chaos_undelivered_execution",
					At: fire, Fault: in.attribute(fire), ExecutionIDs: []string{row.ID},
					Message: fmt.Sprintf("execution %s (status %s) was never delivered", row.ID, row.Status),
				})
			}
		}
	}
	return breaches
}

// classifyDuplicates is I2: deliveries after the first 2xx are duplicates,
// explained when a fault window overlaps them.
func classifyDuplicates(in ChaosInputs) (explained, unexplained, concurrent int, breaches []Breach) {
	byExecution := make(map[string][]CallbackObservation)
	for _, callback := range in.Callbacks {
		byExecution[callback.ExecutionID] = append(byExecution[callback.ExecutionID], callback)
		if callback.ConcurrentForExecution > 1 {
			concurrent++
		}
	}
	for _, executionID := range sortedKeys(byExecution) {
		callbacks := byExecution[executionID]
		sort.SliceStable(callbacks, func(i, j int) bool { return callbacks[i].ReceivedAt.Before(callbacks[j].ReceivedAt) })
		firstSuccess := -1
		for index, callback := range callbacks {
			if firstSuccess < 0 {
				if isSuccess(callback.StatusCode) {
					firstSuccess = index
				}
				continue
			}
			if in.overlapsFault(callbacks[firstSuccess].ReceivedAt, callback.ReceivedAt) {
				explained++
				continue
			}
			unexplained++
			breaches = append(breaches, Breach{
				Invariant: InvariantDuplicates, Severity: SeverityCritical, Code: "chaos_unexplained_duplicate",
				At: callback.ReceivedAt, Fault: unattributedFault, ExecutionIDs: []string{executionID},
				Message: fmt.Sprintf("execution %s was delivered again after a 2xx with no fault active", executionID),
			})
		}
	}
	return explained, unexplained, concurrent, breaches
}

func (in ChaosInputs) overlapsFault(from, to time.Time) bool {
	for _, window := range in.Windows {
		if window.AppliedAt.IsZero() {
			continue
		}
		if !window.AppliedAt.After(to) && !windowEnd(window, in.RecoveryTimeout).Before(from) {
			return true
		}
	}
	return false
}

type latenessPoint struct {
	at       time.Time
	lateness time.Duration
	id       string
}

func (in ChaosInputs) latenessPoints() map[string][]latenessPoint {
	first := firstCallbacks(in.Callbacks)
	points := make(map[string][]latenessPoint)
	for _, manual := range in.Manual {
		if callback, ok := first[manual.ExecutionID]; ok {
			points[latenessManual] = append(points[latenessManual], latenessPoint{manual.TriggeredAt, callback.ReceivedAt.Sub(manual.TriggeredAt), manual.ExecutionID})
		}
	}
	recurring := make(map[string]bool)
	for _, job := range in.Jobs {
		recurring[job.ID] = true
	}
	for _, row := range in.Executions {
		if !recurring[row.JobID] {
			continue
		}
		if callback, ok := first[row.ID]; ok {
			points[latenessRecurring] = append(points[latenessRecurring], latenessPoint{row.ScheduledAt, callback.ReceivedAt.Sub(row.ScheduledAt), row.ID})
		}
	}
	return points
}

func (in ChaosInputs) windowOf(at time.Time) string {
	if at.Before(in.BaselineEnd) {
		return windowBaseline
	}
	if key := in.attribute(at); key != unattributedFault {
		return key
	}
	return windowSteady
}

func p99Of(points []latenessPoint) (time.Duration, time.Duration) {
	values := make([]float64, 0, len(points))
	for _, point := range points {
		values = append(values, float64(point.lateness))
	}
	slices.Sort(values)
	return time.Duration(percentile(values, 0.99)), time.Duration(values[len(values)-1])
}

// baselineP99 returns the baseline p99 for a lateness kind, or false when the
// baseline has no samples of that kind.
func baselineP99(in ChaosInputs, kind string) (time.Duration, bool) {
	var baseline []latenessPoint
	for _, point := range in.latenessPoints()[kind] {
		if point.at.Before(in.BaselineEnd) {
			baseline = append(baseline, point)
		}
	}
	if len(baseline) == 0 {
		return 0, false
	}
	p99, _ := p99Of(baseline)
	return p99, true
}

// checkLateness is I3: per window and kind, max lateness stays under its bound.
func checkLateness(in ChaosInputs) ([]LatenessStat, []Breach) {
	windows := []string{windowBaseline}
	bounds := make(map[string]Bound)
	for index, window := range in.Windows {
		key := faultKey(index, window)
		windows = append(windows, key)
		bounds[key] = in.Settings.faultLatenessBound(window.Spec)
	}
	windows = append(windows, windowSteady)

	points := in.latenessPoints()
	var stats []LatenessStat
	var breaches []Breach
	for _, kind := range []string{latenessManual, latenessRecurring} {
		grouped := make(map[string][]latenessPoint)
		for _, point := range points[kind] {
			window := in.windowOf(point.at)
			grouped[window] = append(grouped[window], point)
		}
		for _, window := range windows {
			stat := LatenessStat{Window: window, Kind: kind, Count: len(grouped[window])}
			if bound, ok := bounds[window]; ok {
				stat.Bound = &bound
			} else if window == windowSteady {
				if p99, ok := baselineP99(in, kind); ok {
					bound := in.Settings.steadyLatenessBound(p99)
					stat.Bound = &bound
				}
			}
			if stat.Count > 0 {
				p99, maximum := p99Of(grouped[window])
				stat.P99, stat.Max = &p99, &maximum
				if stat.Bound != nil && maximum > stat.Bound.Value {
					breaches = append(breaches, latenessBreach(window, kind, grouped[window], *stat.Bound))
				}
			}
			stats = append(stats, stat)
		}
	}
	return stats, breaches
}

func latenessBreach(window, kind string, points []latenessPoint, bound Bound) Breach {
	fault := window
	severity := SeverityWarning
	if window == windowSteady {
		fault = unattributedFault
		severity = SeverityCritical
	}
	worst := points[0]
	var ids []string
	for _, point := range points {
		if point.lateness > bound.Value {
			ids = append(ids, point.id)
		}
		if point.lateness > worst.lateness {
			worst = point
		}
	}
	return Breach{
		Invariant: InvariantLateness, Severity: severity, Code: "chaos_lateness_exceeded",
		At: worst.at, Fault: fault, ExecutionIDs: ids, Bound: &bound,
		Message: fmt.Sprintf("%s lateness %s exceeded %s in window %s", kind, worst.lateness, bound.Value, window),
	}
}

func dualTriggered(sample TimelineSample) bool {
	count := 0
	for _, instance := range sample.Instances {
		if instance.TriggeredDelta > 0 {
			count++
		}
	}
	return count >= 2
}

// checkLeadership is I6: no split brain, and leaderless gaps stay bounded.
func checkLeadership(in ChaosInputs) []Breach {
	var breaches []Breach
	splitBrain := func(sample TimelineSample, reason string) {
		breaches = append(breaches, Breach{
			Invariant: InvariantLeader, Severity: SeverityCritical, Code: "chaos_split_brain",
			At: sample.At, Fault: in.attribute(sample.At), Message: reason,
		})
	}
	gaugeRun, dualRun := 0, 0
	reported := false
	for index, sample := range in.Timeline {
		dual := dualTriggered(sample)
		if sample.LeaderSum > 1 {
			gaugeRun++
		} else {
			gaugeRun = 0
		}
		if dual {
			dualRun++
		} else {
			dualRun = 0
		}
		if gaugeRun == 0 && dualRun == 0 {
			reported = false
		}
		if reported {
			continue
		}
		switch {
		case gaugeRun >= 2:
			splitBrain(in.Timeline[index-1], fmt.Sprintf("leader gauge sum was %d in consecutive samples", sample.LeaderSum))
			reported = true
		case dualRun >= 2:
			splitBrain(in.Timeline[index-1], "two or more instances triggered jobs in consecutive samples")
			reported = true
		case dual && sample.LeaderSum > 1:
			splitBrain(sample, "two or more instances triggered jobs while the leader gauge sum was above one")
			reported = true
		}
	}

	bound := in.Settings.leaderlessBound()
	for index := 0; index < len(in.Timeline); index++ {
		if in.Timeline[index].LeaderSum != 0 {
			continue
		}
		start := in.Timeline[index].At
		end := in.Timeline[len(in.Timeline)-1].At
		for index < len(in.Timeline) && in.Timeline[index].LeaderSum == 0 {
			index++
		}
		if index < len(in.Timeline) {
			end = in.Timeline[index].At
		}
		if gap := end.Sub(start); gap > bound.Value {
			fault := in.attribute(start)
			severity := SeverityWarning
			if fault == unattributedFault {
				severity = SeverityCritical
			}
			breaches = append(breaches, Breach{
				Invariant: InvariantLeader, Severity: severity, Code: "chaos_leaderless_gap",
				At: start, Fault: fault, Bound: &bound,
				Message: fmt.Sprintf("no leader for %s (bound %s)", gap, bound.Value),
			})
		}
	}
	return breaches
}

// recoveryTimes is I7: time from a fault clearing until the stack is healthy.
func recoveryTimes(in ChaosInputs, steady Bound, recoveryTimeout time.Duration) (map[string]time.Duration, []Breach) {
	baselineQueue := 0
	for _, sample := range in.Timeline {
		if sample.At.Before(in.BaselineEnd) && sample.QueueDepth > baselineQueue {
			baselineQueue = sample.QueueDepth
		}
	}
	queueLimit := baselineQueue + len(in.Jobs)
	first := firstCallbacks(in.Callbacks)

	times := make(map[string]time.Duration)
	var breaches []Breach
	for index, window := range in.Windows {
		if window.ClearedAt.IsZero() {
			continue
		}
		key := faultKey(index, window)
		deadline := window.ClearedAt.Add(recoveryTimeout)
		recovered := false
		var last time.Time
		for _, sample := range in.Timeline {
			if sample.At.Before(window.ClearedAt) {
				continue
			}
			last = sample.At
			if sample.At.After(deadline) {
				break
			}
			if healthy(sample, queueLimit) && in.manualRecovered(window.ClearedAt, sample.At, steady, first) {
				times[key] = sample.At.Sub(window.ClearedAt)
				recovered = true
				break
			}
		}
		if !recovered && !last.Before(deadline) {
			breaches = append(breaches, Breach{
				Invariant: InvariantRecovery, Severity: SeverityWarning, Code: "chaos_not_recovered",
				At: window.ClearedAt, Fault: key,
				Message: fmt.Sprintf("not recovered within %s of the fault clearing", recoveryTimeout),
			})
		}
	}
	return times, breaches
}

func healthy(sample TimelineSample, queueLimit int) bool {
	if sample.LeaderSum != 1 || sample.QueueDepth < 0 || sample.QueueDepth > queueLimit {
		return false
	}
	for _, instance := range sample.Instances {
		if !instance.Ready {
			return false
		}
	}
	return true
}

// manualRecovered requires the latest manual trigger issued after the fault
// cleared, and delivered by now, to be within the steady bound. It is skipped
// when there is no steady bound or no manual stream.
func (in ChaosInputs) manualRecovered(cleared, now time.Time, steady Bound, first map[string]CallbackObservation) bool {
	if steady.Value <= 0 || len(in.Manual) == 0 {
		return true
	}
	var latest *ManualSample
	for index := range in.Manual {
		manual := &in.Manual[index]
		if manual.TriggeredAt.Before(cleared) || manual.TriggeredAt.After(now) {
			continue
		}
		callback, ok := first[manual.ExecutionID]
		if !ok || callback.ReceivedAt.After(now) {
			continue
		}
		if latest == nil || manual.TriggeredAt.After(latest.TriggeredAt) {
			latest = manual
		}
	}
	if latest == nil {
		return false
	}
	return first[latest.ExecutionID].ReceivedAt.Sub(latest.TriggeredAt) <= steady.Value
}

// checkStuck is I8.
func checkStuck(in ChaosInputs) []Breach {
	var breaches []Breach
	for _, id := range in.Stuck {
		breaches = append(breaches, Breach{
			Invariant: InvariantStuck, Severity: SeverityCritical, Code: "chaos_stuck_execution",
			At: in.RunEnd, Fault: unattributedFault, ExecutionIDs: []string{id},
			Message: fmt.Sprintf("execution %s is still pending past its lease or the reconcile threshold", id),
		})
	}
	return breaches
}

// checkSignatures is I10.
func checkSignatures(in ChaosInputs) []Breach {
	var breaches []Breach
	for _, callback := range in.Callbacks {
		if callback.SignatureValid {
			continue
		}
		breaches = append(breaches, Breach{
			Invariant: InvariantSignatures, Severity: SeverityCritical, Code: "chaos_invalid_signature",
			At: callback.ReceivedAt, Fault: in.attribute(callback.ReceivedAt), ExecutionIDs: []string{callback.ExecutionID},
			Message: fmt.Sprintf("callback for execution %s had an invalid signature", callback.ExecutionID),
		})
	}
	return breaches
}
