package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const maxBreachIDsShown = 5

func writeChaosSection(report *strings.Builder, chaos *ChaosResult) {
	if chaos == nil {
		return
	}
	report.WriteString("## Chaos\n\n")
	report.WriteString("| Fault | I1 | I2 dup | I3 p99 / bound | I6 | I7 recovery | I8 | I10 | Verdict |\n")
	report.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, score := range chaos.Scores {
		fmt.Fprintf(report, "| `%s` | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			markdownCell(score.Fault),
			resultMark(score.Invariants[InvariantNoLost]),
			duplicateCell(score),
			latenessCell(score),
			resultMark(score.Invariants[InvariantLeader]),
			recoveryCell(score),
			resultMark(score.Invariants[InvariantStuck]),
			resultMark(score.Invariants[InvariantSignatures]),
			score.Verdict,
		)
	}
	report.WriteString("\n### Strengths\n\n")
	strengths := 0
	for _, score := range chaos.Scores {
		if score.Verdict != verdictStrength {
			continue
		}
		strengths++
		fmt.Fprintf(report, "- `%s`: no lost executions, %d explained duplicates, recovery %s\n",
			markdownCell(score.Fault), score.Duplicates.Explained, recoveryCell(score))
	}
	if strengths == 0 {
		report.WriteString("No fault passed every invariant.\n")
	}
	report.WriteString("\n### Weak points\n\n")
	if len(chaos.Breaches) == 0 {
		report.WriteString("No invariant was broken.\n\n")
		return
	}
	for i := range chaos.Breaches {
		breach := &chaos.Breaches[i]
		fmt.Fprintf(report, "- **%s** `%s` (%s, fault `%s`, at `%s`): %s",
			strings.ToUpper(string(breach.Severity)), breach.Code, breach.Invariant,
			markdownCell(breach.Fault), formatTime(breach.At), markdownCell(breach.Message))
		if ids := breachIDs(breach.ExecutionIDs); ids != "" {
			fmt.Fprintf(report, ". Executions: %s", ids)
		}
		if breach.Bound != nil {
			fmt.Fprintf(report, ". Bound `%s`: %s", breach.Bound.Formula, breach.Bound.Plugged)
			if len(breach.Bound.Knobs) > 0 {
				fmt.Fprintf(report, ". Knobs: `%s`", strings.Join(breach.Bound.Knobs, "`, `"))
			}
		}
		report.WriteString("\n")
	}
	report.WriteString("\n")
}

func resultMark(result string) string {
	switch result {
	case invariantPass:
		return "✓"
	case invariantFail:
		return "✗"
	case "":
		return "–"
	default:
		return result
	}
}

func duplicateCell(score FaultScore) string {
	if score.Fault == runScoreName {
		return fmt.Sprintf("%d unexpl, %d conc", score.Duplicates.Unexplained, score.Duplicates.Concurrent)
	}
	return fmt.Sprintf("%d expl", score.Duplicates.Explained)
}

// worstLateness returns the counted lateness stat with the largest max.
func worstLateness(score FaultScore) *LatenessStat {
	var worst *LatenessStat
	for index := range score.Lateness {
		stat := &score.Lateness[index]
		if stat.Count == 0 || stat.Max == nil {
			continue
		}
		if worst == nil || *stat.Max > *worst.Max {
			worst = stat
		}
	}
	return worst
}

func latenessCell(score FaultScore) string {
	stat := worstLateness(score)
	if stat == nil {
		return resultMark(score.Invariants[InvariantLateness])
	}
	cell := fmt.Sprintf("%s / %s", stat.P99.Round(time.Millisecond), boundValue(stat.Bound))
	if score.Invariants[InvariantLateness] == invariantFail {
		cell += " ✗"
	}
	return cell
}

func boundValue(bound *Bound) string {
	if bound == nil {
		return "–"
	}
	return bound.Value.Round(time.Millisecond).String()
}

func recoveryCell(score FaultScore) string {
	if score.Recovery != nil {
		return score.Recovery.Round(time.Millisecond).String()
	}
	return resultMark(score.Invariants[InvariantRecovery])
}

func breachIDs(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	shown := ids
	if len(shown) > maxBreachIDsShown {
		shown = shown[:maxBreachIDsShown]
	}
	text := "`" + strings.Join(shown, "`, `") + "`"
	if len(ids) > len(shown) {
		text += fmt.Sprintf(" and %d more", len(ids)-len(shown))
	}
	return text
}

func writeChaosTimelineCSV(out *bytes.Buffer, chaos *ChaosResult) error {
	writer := csv.NewWriter(out)
	services := []string{"cronlite_1", "cronlite_2", "cronlite_3"}
	header := []string{"at", "active_fault", "leader_sum", "queue_depth", "stuck_count"}
	for _, service := range services {
		header = append(header, service+"_reachable", service+"_ready", service+"_leader", service+"_triggered_delta")
	}
	if err := writer.Write(header); err != nil {
		return err
	}
	for _, sample := range chaos.Timeline {
		row := []string{formatTime(sample.At), sample.ActiveFault, strconv.Itoa(sample.LeaderSum),
			strconv.Itoa(sample.QueueDepth), strconv.Itoa(sample.StuckCount)}
		for _, service := range services {
			instance := InstanceSample{}
			for _, candidate := range sample.Instances {
				if candidate.Service == service {
					instance = candidate
				}
			}
			row = append(row, strconv.FormatBool(instance.Reachable), strconv.FormatBool(instance.Ready),
				strconv.FormatBool(instance.Leader), strconv.FormatFloat(instance.TriggeredDelta, 'f', -1, 64))
		}
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

func writeChaosScorecardCSV(out *bytes.Buffer, chaos *ChaosResult) error {
	writer := csv.NewWriter(out)
	if err := writer.Write([]string{"fault", "invariant", "result", "p99_ms", "bound_ms", "recovery_ms", "verdict", "knobs"}); err != nil {
		return err
	}
	for _, score := range chaos.Scores {
		for _, invariant := range scoredInvariants {
			var p99, bound, recovery string
			if invariant == InvariantLateness {
				if stat := worstLateness(score); stat != nil {
					p99 = durationMS(*stat.P99)
					if stat.Bound != nil {
						bound = durationMS(stat.Bound.Value)
					}
				}
			}
			if invariant == InvariantRecovery && score.Recovery != nil {
				recovery = durationMS(*score.Recovery)
			}
			if err := writer.Write([]string{score.Fault, string(invariant), score.Invariants[invariant],
				p99, bound, recovery, score.Verdict, strings.Join(score.Knobs, ";")}); err != nil {
				return err
			}
		}
	}
	writer.Flush()
	return writer.Error()
}

func durationMS(duration time.Duration) string {
	return strconv.FormatFloat(float64(duration)/float64(time.Millisecond), 'f', 3, 64)
}
