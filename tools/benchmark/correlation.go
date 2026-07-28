package main

import (
	"fmt"
	"sort"
	"time"
)

var productionRetryBackoff = []time.Duration{
	0,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

func correlate(record ExecutionRecord) ExecutionRecord {
	record.Measurements = nil
	record.Findings = nil

	if !record.TriggerRequest.StartedAt.IsZero() {
		record.Measurements = append(record.Measurements, durationMeasurement(
			"api_trigger_latency_ms",
			record.TriggerRequest.Duration,
			ProvenanceDirect,
		))
	}

	if !record.APIExecution.ScheduledAt.IsZero() && !record.APIExecution.CreatedAt.IsZero() {
		record.Measurements = append(record.Measurements, betweenMeasurement(
			"scheduler_lag_ms",
			record.APIExecution.ScheduledAt,
			record.APIExecution.CreatedAt,
			ProvenanceDerived,
		))
	}

	if len(record.Callbacks) > 0 {
		first := earliestCallback(record.Callbacks)
		record.Measurements = append(record.Measurements, betweenMeasurement(
			"receiver_processing_ms",
			first.ReceivedAt,
			first.ResponseCompletedAt,
			ProvenanceDirect,
		))
		start := record.APIExecution.ScheduledAt
		if record.APIExecution.TriggerType == "manual" {
			start = record.TriggerRequest.StartedAt
		}
		if !start.IsZero() {
			record.Measurements = append(record.Measurements, betweenMeasurement(
				"end_to_end_delivery_ms",
				start,
				first.ReceivedAt,
				ProvenanceDerived,
			))
		}
	}

	record.Measurements = append(record.Measurements, diagnosticMeasurements(record.Diagnostic)...)
	record.Measurements = append(record.Measurements, terminalMeasurements(record)...)
	record.Findings = append(record.Findings, callbackFindings(record)...)
	record.Findings = append(record.Findings, statusFindings(record)...)
	return record
}

func diagnosticMeasurements(diagnostic *DiagnosticExecution) []Measurement {
	if diagnostic == nil {
		return []Measurement{
			unavailableMeasurement("queue_wait_ms", "diagnostic mode is disabled or unavailable"),
			unavailableMeasurement("claim_to_dispatch_ms", "claimed_at is not public"),
			unavailableMeasurement("webhook_rtt_ms", "delivery attempts are not public"),
			unavailableMeasurement("retry_backoff_actual_ms", "delivery attempts are not public"),
		}
	}

	attempts := append([]DiagnosticAttempt(nil), diagnostic.Attempts...)
	sort.Slice(attempts, func(i, j int) bool {
		if attempts[i].Attempt == attempts[j].Attempt {
			return attempts[i].StartedAt.Before(attempts[j].StartedAt)
		}
		return attempts[i].Attempt < attempts[j].Attempt
	})

	var measurements []Measurement
	if diagnostic.ClaimedAt == nil {
		measurements = append(measurements,
			unavailableMeasurement("queue_wait_ms", "execution has no claimed_at observation"),
			unavailableMeasurement("claim_to_dispatch_ms", "execution has no claimed_at observation"),
		)
	} else if len(attempts) > 0 && diagnostic.ClaimedAt.After(attempts[0].StartedAt) {
		const reason = "claimed_at was overwritten by a later reclaim"
		measurements = append(measurements,
			unavailableMeasurement("queue_wait_ms", reason),
			unavailableMeasurement("claim_to_dispatch_ms", reason),
		)
	} else {
		measurements = append(measurements, betweenMeasurement(
			"queue_wait_ms",
			diagnostic.CreatedAt,
			*diagnostic.ClaimedAt,
			ProvenanceDatabase,
		))
		if len(diagnostic.Attempts) > 0 {
			measurements = append(measurements, betweenMeasurement(
				"claim_to_dispatch_ms",
				*diagnostic.ClaimedAt,
				diagnostic.Attempts[0].StartedAt,
				ProvenanceDerived,
			))
		}
	}

	for index, attempt := range attempts {
		name := "webhook_rtt_ms"
		if len(attempts) > 1 {
			name = fmt.Sprintf("webhook_rtt_ms_attempt_%d", attempt.Attempt)
		}
		measurements = append(measurements, betweenMeasurement(
			name,
			attempt.StartedAt,
			attempt.FinishedAt,
			ProvenanceDatabase,
		))
		if index == 0 {
			continue
		}
		actual := attempt.StartedAt.Sub(attempts[index-1].FinishedAt)
		actualName := fmt.Sprintf("retry_backoff_actual_ms_attempt_%d", attempt.Attempt)
		measurements = append(measurements, durationMeasurement(
			actualName,
			actual,
			ProvenanceDerived,
		))
		expectedIndex := attempt.Attempt - 1
		if expectedIndex >= len(productionRetryBackoff) {
			expectedIndex = len(productionRetryBackoff) - 1
		}
		if expectedIndex >= 0 {
			errorName := fmt.Sprintf("retry_backoff_error_ms_attempt_%d", attempt.Attempt)
			measurements = append(measurements, durationMeasurement(
				errorName,
				actual-productionRetryBackoff[expectedIndex],
				ProvenanceDerived,
			))
		}
	}
	return measurements
}

func terminalMeasurements(record ExecutionRecord) []Measurement {
	if record.PollBounds == nil || record.PollBounds.FirstTerminalAt == nil {
		return []Measurement{
			unavailableMeasurement(
				"terminal_persistence_lag_ms",
				"terminal update timestamp is not persisted; no polling bound was recorded",
			),
		}
	}

	var measurements []Measurement
	start := record.APIExecution.ScheduledAt
	if record.APIExecution.TriggerType == "manual" {
		start = record.TriggerRequest.StartedAt
	}
	if !start.IsZero() {
		measurement := betweenMeasurement(
			"end_to_end_terminal_ms",
			start,
			*record.PollBounds.FirstTerminalAt,
			ProvenanceDerived,
		)
		measurement.Reason = "upper bound from first terminal API poll"
		measurements = append(measurements, measurement)
	}
	if len(record.Callbacks) > 0 {
		last := latestCallback(record.Callbacks)
		measurement := betweenMeasurement(
			"terminal_persistence_lag_ms",
			last.ResponseCompletedAt,
			*record.PollBounds.FirstTerminalAt,
			ProvenanceDerived,
		)
		measurement.Reason = "upper bound from first terminal API poll"
		measurements = append(measurements, measurement)
	}
	return measurements
}

func callbackFindings(record ExecutionRecord) []Finding {
	var findings []Finding
	seenAttempts := make(map[string]bool)
	var firstHash string
	for index, callback := range record.Callbacks {
		if !callback.SignatureValid {
			findings = append(findings, finding(
				SeverityCritical,
				"invalid_webhook_signature",
				"webhook signature verification failed",
				record,
				callback.AttemptID,
			))
		}
		if callback.AttemptID != "" && seenAttempts[callback.AttemptID] {
			findings = append(findings, finding(
				SeverityCritical,
				"duplicate_attempt_id",
				"the same attempt ID produced multiple callbacks",
				record,
				callback.AttemptID,
			))
		}
		seenAttempts[callback.AttemptID] = true
		if callback.ConcurrentForExecution > 1 {
			findings = append(findings, finding(
				SeverityCritical,
				"concurrent_duplicate_delivery",
				"multiple callbacks for one execution overlapped",
				record,
				callback.AttemptID,
			))
		}
		if callback.AfterTerminal {
			findings = append(findings, finding(
				SeverityCritical,
				"callback_after_terminal",
				"callback arrived after terminal status was observed",
				record,
				callback.AttemptID,
			))
		}
		if index == 0 {
			firstHash = callback.BodySHA256
		} else if callback.BodySHA256 != firstHash {
			findings = append(findings, finding(
				SeverityCritical,
				"unexpected_payload_change",
				"callback payload changed between attempts",
				record,
				callback.AttemptID,
			))
		}
	}
	if len(record.Callbacks) > 1 {
		findings = append(findings, finding(
			SeverityWarning,
			"duplicate_execution_delivery",
			fmt.Sprintf("execution produced %d callbacks", len(record.Callbacks)),
			record,
			"",
		))
	}
	return deduplicateFindings(findings)
}

func statusFindings(record ExecutionRecord) []Finding {
	if record.PollBounds == nil ||
		record.PollBounds.FirstTerminalAt == nil ||
		record.Diagnostic == nil {
		return nil
	}
	apiStatus := record.PollBounds.FinalExecution.Status
	if apiStatus == "" || record.Diagnostic.Status == "" || apiStatus == record.Diagnostic.Status {
		return nil
	}
	return []Finding{finding(
		SeverityCritical,
		"api_database_status_mismatch",
		fmt.Sprintf("API status %q disagrees with database status %q", apiStatus, record.Diagnostic.Status),
		record,
		"",
	)}
}

func durationMeasurement(name string, duration time.Duration, provenance Provenance) Measurement {
	value := float64(duration) / float64(time.Millisecond)
	return Measurement{Name: name, ValueMS: &value, Provenance: provenance}
}

func betweenMeasurement(
	name string,
	start time.Time,
	finish time.Time,
	provenance Provenance,
) Measurement {
	return durationMeasurement(name, finish.Sub(start), provenance)
}

func unavailableMeasurement(name, reason string) Measurement {
	return Measurement{
		Name:       name,
		Provenance: ProvenanceUnavailable,
		Reason:     reason,
	}
}

func earliestCallback(callbacks []CallbackObservation) CallbackObservation {
	earliest := callbacks[0]
	for _, callback := range callbacks[1:] {
		if callback.ReceivedAt.Before(earliest.ReceivedAt) {
			earliest = callback
		}
	}
	return earliest
}

func latestCallback(callbacks []CallbackObservation) CallbackObservation {
	latest := callbacks[0]
	for _, callback := range callbacks[1:] {
		if callback.ResponseCompletedAt.After(latest.ResponseCompletedAt) {
			latest = callback
		}
	}
	return latest
}

func finding(
	severity Severity,
	code string,
	message string,
	record ExecutionRecord,
	attemptID string,
) Finding {
	return Finding{
		Severity:    severity,
		Code:        code,
		Message:     message,
		Scenario:    record.Scenario,
		ExecutionID: record.ExecutionID,
		AttemptID:   attemptID,
	}
}

func deduplicateFindings(findings []Finding) []Finding {
	seen := make(map[string]bool)
	result := make([]Finding, 0, len(findings))
	for _, item := range findings {
		key := string(item.Severity) + "\x00" + item.Code + "\x00" + item.AttemptID
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, item)
	}
	return result
}
