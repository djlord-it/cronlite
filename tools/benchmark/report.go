package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func renderReport(result RunResult, paths OutputPaths) string {
	result = sanitizeRunResult(result)
	var report strings.Builder
	report.WriteString("# CronLite Benchmark Report\n\n")
	fmt.Fprintf(&report, "- Run ID: `%s`\n", markdownCell(result.RunID))
	fmt.Fprintf(&report, "- Schema version: `%s`\n", markdownCell(result.SchemaVersion))
	fmt.Fprintf(&report, "- Started: `%s`\n", formatTime(result.StartedAt))
	fmt.Fprintf(&report, "- Finished: `%s`\n", formatTime(result.FinishedAt))
	fmt.Fprintf(&report, "- Duration: `%s`\n\n", result.FinishedAt.Sub(result.StartedAt).Round(time.Millisecond))

	writeEnvironmentSection(&report, result)
	writeCorrectnessSection(&report, result)
	writeScenarioSection(&report, result)
	writeObservationStatsSection(&report, "Baseline HTTP Latency", result, "baseline")
	writeAPIStatsSection(&report, result, []string{
		"api_create_latency_ms",
		"api_trigger_latency_ms",
		"api_get_execution_latency_ms",
		"api_list_executions_latency_ms",
	})
	writeMeasurementStatsSection(&report, "Scheduler Accuracy", result, []string{
		"scheduler_lag_ms",
		"queue_wait_ms",
		"claim_to_dispatch_ms",
	})
	writeMeasurementStatsSection(&report, "Delivery Latency", result, []string{
		"webhook_rtt_ms",
		"receiver_processing_ms",
		"terminal_persistence_lag_ms",
		"end_to_end_delivery_ms",
		"end_to_end_terminal_ms",
	})
	writeMeasurementStatsSection(&report, "Retry Timing", result, []string{
		"retry_backoff_actual_ms",
		"retry_backoff_error_ms",
	})
	writeThroughputSection(&report, result)
	writeFailureSection(&report, result)
	writeDuplicateSection(&report, result)
	writeResourceSection(&report, result)
	writeLimitationsSection(&report, result)
	writeReproductionSection(&report, result)
	writeRawPathsSection(&report, paths)
	return report.String()
}

func writeEnvironmentSection(report *strings.Builder, result RunResult) {
	report.WriteString("## Environment and Configuration\n\n")
	report.WriteString("| Field | Value |\n|---|---|\n")
	rows := [][2]string{
		{"Commit SHA", result.Environment.CommitSHA},
		{"Operating system", result.Environment.OS},
		{"Architecture", result.Environment.Architecture},
		{"CPU count", strconv.Itoa(result.Environment.CPUCount)},
		{"Memory bytes", strconv.FormatUint(result.Environment.MemoryBytes, 10)},
		{"Go version", result.Environment.GoVersion},
		{"Docker version", result.Environment.DockerVersion},
		{"PostgreSQL version", result.Environment.PostgreSQLVersion},
		{"Dispatch mode", result.Environment.DispatchMode},
		{"CronLite instances", strconv.Itoa(result.Environment.CronLiteInstances)},
		{"Random seed", strconv.FormatInt(result.RandomSeed, 10)},
		{"Retry profile", result.Config.RetryProfile},
		{"Sample count", strconv.Itoa(result.Config.SampleCount)},
		{"Diagnostic mode", strconv.FormatBool(result.Config.Diagnostic)},
	}
	for _, row := range rows {
		fmt.Fprintf(report, "| %s | %s |\n", row[0], markdownCell(row[1]))
	}
	report.WriteString("\n")
}

func writeCorrectnessSection(report *strings.Builder, result RunResult) {
	report.WriteString("## Correctness Findings\n\n")
	findings := allFindings(result)
	if len(findings) == 0 {
		report.WriteString("No correctness failures were observed in the executed scenarios.\n\n")
		return
	}
	for _, finding := range findings {
		label := strings.ToUpper(string(finding.Severity))
		fmt.Fprintf(
			report,
			"**%s:** %s (`%s`, scenario `%s`, execution `%s`)\n\n",
			label,
			markdownCell(finding.Message),
			markdownCell(finding.Code),
			markdownCell(finding.Scenario),
			markdownCell(finding.ExecutionID),
		)
	}
}

func writeScenarioSection(report *strings.Builder, result RunResult) {
	report.WriteString("## Scenario Summary\n\n")
	report.WriteString("| Scenario | Status | Executions | Callbacks | Failures | Duplicates | Duration |\n")
	report.WriteString("|---|---:|---:|---:|---:|---:|---:|\n")
	for _, scenario := range result.Scenarios {
		callbacks := 0
		duplicates := 0
		for _, execution := range scenario.Executions {
			callbacks += len(execution.Callbacks)
			if hasDuplicateFinding(execution.Findings) {
				duplicates++
			}
		}
		failures := 0
		if scenario.Status == ScenarioFailed {
			failures = 1
		}
		fmt.Fprintf(
			report,
			"| %s | %s | %d | %d | %d | %d | %s |\n",
			markdownCell(scenario.Name),
			scenario.Status,
			len(scenario.Executions),
			callbacks,
			failures,
			duplicates,
			scenario.FinishedAt.Sub(scenario.StartedAt).Round(time.Millisecond),
		)
		if scenario.Reason != "" {
			fmt.Fprintf(report, "\n%s: %s\n", markdownCell(scenario.Name), markdownCell(scenario.Reason))
		}
	}
	report.WriteString("\n")
}

func writeObservationStatsSection(
	report *strings.Builder,
	title string,
	result RunResult,
	kind string,
) {
	groups := make(map[string][]float64)
	for _, scenario := range result.Scenarios {
		for _, observation := range scenario.Observations {
			if observation.Kind == kind ||
				(kind == "baseline" && observation.Name == "baseline_cronlite_health_rtt_ms") {
				groups[observation.Name] = append(
					groups[observation.Name],
					float64(observation.Duration)/float64(time.Millisecond),
				)
			}
		}
	}
	writeStatsSection(report, title, groups)
}

func writeAPIStatsSection(
	report *strings.Builder,
	result RunResult,
	prefixes []string,
) {
	groups := measurementGroups(result, prefixes)
	for _, scenario := range result.Scenarios {
		for _, observation := range scenario.Observations {
			if observation.Kind == "api" && matchesMeasurement(observation.Name, prefixes) {
				groups[observation.Name] = append(
					groups[observation.Name],
					float64(observation.Duration)/float64(time.Millisecond),
				)
			}
		}
	}
	writeStatsSection(report, "API Latency", groups)
}

func writeMeasurementStatsSection(
	report *strings.Builder,
	title string,
	result RunResult,
	prefixes []string,
) {
	writeStatsSection(report, title, measurementGroups(result, prefixes))
}

func measurementGroups(result RunResult, prefixes []string) map[string][]float64 {
	groups := make(map[string][]float64)
	for _, scenario := range result.Scenarios {
		for _, execution := range scenario.Executions {
			if execution.Warmup {
				continue
			}
			for _, measurement := range execution.Measurements {
				if measurement.ValueMS == nil || !matchesMeasurement(measurement.Name, prefixes) {
					continue
				}
				groups[measurement.Name] = append(groups[measurement.Name], *measurement.ValueMS)
			}
		}
	}
	return groups
}

func writeStatsSection(report *strings.Builder, title string, groups map[string][]float64) {
	fmt.Fprintf(report, "## %s\n\n", title)
	if len(groups) == 0 {
		report.WriteString("No measurements were available.\n\n")
		return
	}
	report.WriteString("| Metric | n | Min | Max | Mean | Median | Std dev | p50 | p90 | p95 | p99 |\n")
	report.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	names := sortedKeys(groups)
	var warnings []string
	for _, name := range names {
		stats := summarize(groups[name])
		fmt.Fprintf(
			report,
			"| %s | %d | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f |\n",
			markdownCell(name),
			stats.Count,
			stats.Min,
			stats.Max,
			stats.Mean,
			stats.Median,
			stats.StdDev,
			stats.P50,
			stats.P90,
			stats.P95,
			stats.P99,
		)
		for _, warning := range stats.Warnings {
			warnings = append(warnings, name+": "+warning)
		}
	}
	if len(warnings) > 0 {
		report.WriteString("\nPercentile cautions:\n\n")
		for _, warning := range warnings {
			fmt.Fprintf(report, "- %s\n", markdownCell(warning))
		}
	}
	report.WriteString("\n")
}

func writeThroughputSection(report *strings.Builder, result RunResult) {
	report.WriteString("## Throughput\n\n")
	report.WriteString("| Scenario | Execution throughput/s | Callback throughput/s | Success rate | Retry rate |\n")
	report.WriteString("|---|---:|---:|---:|---:|\n")
	for _, scenario := range result.Scenarios {
		duration := scenario.FinishedAt.Sub(scenario.StartedAt).Seconds()
		if duration <= 0 {
			duration = 1
		}
		executions, callbacks, successes, retries := 0, 0, 0, 0
		for _, execution := range scenario.Executions {
			if execution.Warmup {
				continue
			}
			executions++
			callbacks += len(execution.Callbacks)
			if execution.PollBounds != nil &&
				execution.PollBounds.FinalExecution.Status == "delivered" {
				successes++
			}
			if len(execution.Callbacks) > 1 {
				retries++
			}
		}
		fmt.Fprintf(
			report,
			"| %s | %.3f | %.3f | %.2f%% | %.2f%% |\n",
			markdownCell(scenario.Name),
			float64(executions)/duration,
			float64(callbacks)/duration,
			percent(successes, executions),
			percent(retries, executions),
		)
	}
	report.WriteString("\n")
}

func writeFailureSection(report *strings.Builder, result RunResult) {
	report.WriteString("## Failure Analysis\n\n")
	statuses := make(map[int]int)
	errorsByClass := make(map[string]int)
	permanentFailures := 0
	signatureFailures := 0
	for _, scenario := range result.Scenarios {
		for _, execution := range scenario.Executions {
			for _, callback := range execution.Callbacks {
				statuses[callback.StatusCode]++
				if !callback.SignatureValid {
					signatureFailures++
				}
			}
			if execution.PollBounds != nil &&
				execution.PollBounds.FinalExecution.Status == "failed" {
				permanentFailures++
			}
			if execution.Diagnostic != nil {
				for _, attempt := range execution.Diagnostic.Attempts {
					errorsByClass[classifyAttemptError(attempt)]++
				}
			}
		}
	}
	fmt.Fprintf(report, "- Permanent failures: %d\n", permanentFailures)
	fmt.Fprintf(report, "- Signature-verification failures: %d\n", signatureFailures)
	fmt.Fprintf(report, "- HTTP status distribution: `%s`\n", formatIntDistribution(statuses))
	fmt.Fprintf(report, "- Error classification: `%s`\n\n", formatStringDistribution(errorsByClass))
}

func writeDuplicateSection(report *strings.Builder, result RunResult) {
	report.WriteString("## Duplicate-Delivery Findings\n\n")
	total, duplicate, concurrent := 0, 0, 0
	for _, scenario := range result.Scenarios {
		for _, execution := range scenario.Executions {
			total++
			if hasDuplicateFinding(execution.Findings) {
				duplicate++
			}
			for _, finding := range execution.Findings {
				if finding.Code == "concurrent_duplicate_delivery" {
					concurrent++
				}
			}
		}
	}
	fmt.Fprintf(report, "- Executions evaluated: %d\n", total)
	fmt.Fprintf(report, "- Executions with duplicate callback evidence: %d (%.4f%%)\n", duplicate, percent(duplicate, total))
	fmt.Fprintf(report, "- Concurrent duplicate findings: %d\n\n", concurrent)
}

func writeResourceSection(report *strings.Builder, result RunResult) {
	report.WriteString("## Resource Usage\n\n")
	if len(result.Resources) == 0 && len(result.MetricsDelta) == 0 {
		report.WriteString("Resource collection was unavailable or not enabled for this run.\n\n")
		return
	}
	for _, resource := range result.Resources {
		fmt.Fprintf(
			report,
			"- `%s`: DB connections %d, size %d bytes, queue depth %d, in progress %d\n",
			formatTime(resource.ObservedAt),
			resource.ConnectionCount,
			resource.DatabaseSize,
			resource.QueueDepth,
			resource.InProgress,
		)
	}
	for _, name := range sortedKeys(result.MetricsDelta) {
		fmt.Fprintf(report, "- `%s`: %.6f\n", markdownCell(name), result.MetricsDelta[name])
	}
	report.WriteString("\n")
}

func writeLimitationsSection(report *strings.Builder, result RunResult) {
	report.WriteString("## Limitations\n\n")
	for _, limitation := range result.Limitations {
		fmt.Fprintf(report, "- %s\n", markdownCell(limitation))
	}
	report.WriteString("\n")
}

func writeReproductionSection(report *strings.Builder, result RunResult) {
	report.WriteString("## Reproduction Instructions\n\n")
	report.WriteString("```bash\n")
	report.WriteString(redactText(result.Command))
	report.WriteString("\n```\n\n")
}

func writeRawPathsSection(report *strings.Builder, paths OutputPaths) {
	report.WriteString("## Raw File Locations\n\n")
	fmt.Fprintf(report, "- JSON: `%s`\n", markdownCell(paths.JSON))
	fmt.Fprintf(report, "- CSV: `%s`\n", markdownCell(paths.CSV))
	fmt.Fprintf(report, "- Report: `%s`\n", markdownCell(paths.Markdown))
}

func allFindings(result RunResult) []Finding {
	if len(result.Findings) > 0 {
		return deduplicateFindings(result.Findings)
	}
	var findings []Finding
	for _, scenario := range result.Scenarios {
		findings = append(findings, scenario.Findings...)
		for _, execution := range scenario.Executions {
			findings = append(findings, execution.Findings...)
		}
	}
	return deduplicateFindings(findings)
}

func matchesMeasurement(name string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if name == prefix || strings.HasPrefix(name, prefix+"_attempt_") {
			return true
		}
	}
	return false
}

func percent(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator) * 100
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func formatIntDistribution(values map[int]int) string {
	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%d=%d", key, values[key]))
	}
	return strings.Join(parts, ", ")
}

func formatStringDistribution(values map[string]int) string {
	var parts []string
	for _, key := range sortedKeys(values) {
		if key != "" {
			parts = append(parts, fmt.Sprintf("%s=%d", key, values[key]))
		}
	}
	return strings.Join(parts, ", ")
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}
