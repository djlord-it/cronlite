package main

import (
	"testing"
	"time"
)

func TestCorrelateComputesDiagnosticLifecycle(t *testing.T) {
	record := fixtureExecutionRecord()
	created := fixtureTime(100)
	claimed := fixtureTime(120)
	record.Diagnostic = &DiagnosticExecution{
		ExecutionID: "exec-1",
		CreatedAt:   created,
		ClaimedAt:   &claimed,
		Attempts: []DiagnosticAttempt{{
			ID:         "attempt-1",
			Attempt:    1,
			StatusCode: 204,
			StartedAt:  fixtureTime(125),
			FinishedAt: fixtureTime(150),
		}},
	}

	got := correlate(record)
	assertMeasurement(t, got.Measurements, "queue_wait_ms", 20, ProvenanceDatabase)
	assertMeasurement(t, got.Measurements, "claim_to_dispatch_ms", 5, ProvenanceDerived)
	assertMeasurement(t, got.Measurements, "webhook_rtt_ms", 25, ProvenanceDatabase)
}

func TestCorrelateComputesManualEndToEndAndReceiverProcessing(t *testing.T) {
	record := fixtureExecutionRecord()
	got := correlate(record)
	assertMeasurement(t, got.Measurements, "end_to_end_delivery_ms", 50, ProvenanceDerived)
	assertMeasurement(t, got.Measurements, "receiver_processing_ms", 10, ProvenanceDirect)
}

func TestCorrelateReportsConcurrentDuplicate(t *testing.T) {
	record := fixtureExecutionRecord()
	record.Callbacks = append(record.Callbacks, CallbackObservation{
		ReceivedAt:             fixtureTime(55),
		ResponseStartedAt:      fixtureTime(56),
		ResponseCompletedAt:    fixtureTime(65),
		ExecutionID:            "exec-1",
		AttemptID:              "attempt-2",
		SignatureValid:         true,
		StatusCode:             204,
		BodySHA256:             "same",
		ConcurrentForExecution: 2,
	})
	got := correlate(record)
	if !hasFinding(got.Findings, SeverityCritical, "concurrent_duplicate_delivery") {
		t.Fatalf("findings: %+v", got.Findings)
	}
	if !hasFinding(got.Findings, SeverityWarning, "duplicate_execution_delivery") {
		t.Fatalf("findings: %+v", got.Findings)
	}
}

func TestCorrelateReportsInvalidSignatureAndPayloadChange(t *testing.T) {
	record := fixtureExecutionRecord()
	record.Callbacks[0].SignatureValid = false
	record.Callbacks = append(record.Callbacks, CallbackObservation{
		ReceivedAt:          fixtureTime(70),
		ResponseStartedAt:   fixtureTime(71),
		ResponseCompletedAt: fixtureTime(72),
		ExecutionID:         "exec-1",
		AttemptID:           "attempt-2",
		SignatureValid:      true,
		StatusCode:          204,
		BodySHA256:          "changed",
	})
	got := correlate(record)
	if !hasFinding(got.Findings, SeverityCritical, "invalid_webhook_signature") {
		t.Fatalf("findings: %+v", got.Findings)
	}
	if !hasFinding(got.Findings, SeverityCritical, "unexpected_payload_change") {
		t.Fatalf("findings: %+v", got.Findings)
	}
}

func TestCorrelateComputesRetryBackoffError(t *testing.T) {
	record := fixtureExecutionRecord()
	record.Diagnostic = &DiagnosticExecution{
		Attempts: []DiagnosticAttempt{
			{Attempt: 1, StartedAt: fixtureTime(0), FinishedAt: fixtureTime(100)},
			{Attempt: 2, StartedAt: fixtureTime(30_150), FinishedAt: fixtureTime(30_200)},
		},
	}
	got := correlate(record)
	assertMeasurement(t, got.Measurements, "retry_backoff_actual_ms_attempt_2", 30_050, ProvenanceDerived)
	assertMeasurement(t, got.Measurements, "retry_backoff_error_ms_attempt_2", 50, ProvenanceDerived)
}

func fixtureExecutionRecord() ExecutionRecord {
	return ExecutionRecord{
		RunID:         "run-1",
		Scenario:      "smoke",
		CorrelationID: "correlation-1",
		JobID:         "job-1",
		ExecutionID:   "exec-1",
		TriggerRequest: Observation{
			StartedAt:  fixtureTime(0),
			FinishedAt: fixtureTime(5),
			Duration:   5 * time.Millisecond,
			Provenance: ProvenanceDirect,
		},
		APIExecution: APIExecution{
			ID:          "exec-1",
			JobID:       "job-1",
			TriggerType: "manual",
			ScheduledAt: fixtureTime(1),
			FiredAt:     fixtureTime(2),
			CreatedAt:   fixtureTime(3),
		},
		Callbacks: []CallbackObservation{{
			ReceivedAt:             fixtureTime(50),
			ResponseStartedAt:      fixtureTime(51),
			ResponseCompletedAt:    fixtureTime(60),
			ExecutionID:            "exec-1",
			AttemptID:              "attempt-1",
			SignatureValid:         true,
			StatusCode:             204,
			BodySHA256:             "same",
			ConcurrentForExecution: 1,
		}},
	}
}

func fixtureTime(milliseconds int) time.Time {
	return time.Unix(0, int64(milliseconds)*int64(time.Millisecond)).UTC()
}

func assertMeasurement(
	t *testing.T,
	measurements []Measurement,
	name string,
	want float64,
	provenance Provenance,
) {
	t.Helper()
	for _, measurement := range measurements {
		if measurement.Name != name {
			continue
		}
		if measurement.ValueMS == nil || *measurement.ValueMS != want {
			t.Fatalf("%s: want %v got %+v", name, want, measurement)
		}
		if measurement.Provenance != provenance {
			t.Fatalf("%s provenance: want %s got %s", name, provenance, measurement.Provenance)
		}
		return
	}
	t.Fatalf("measurement %q not found: %+v", name, measurements)
}

func hasFinding(findings []Finding, severity Severity, code string) bool {
	for _, finding := range findings {
		if finding.Severity == severity && finding.Code == code {
			return true
		}
	}
	return false
}
