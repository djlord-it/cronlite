package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type OutputPaths struct {
	JSON     string `json:"json"`
	CSV      string `json:"csv"`
	Markdown string `json:"markdown"`
}

var (
	apiKeyPattern   = regexp.MustCompile(`(?i)(--api-key(?:=|\s+))("[^"]*"|'[^']*'|\S+)`)
	bearerPattern   = regexp.MustCompile(`(?i)Bearer\s+\S+`)
	postgresPattern = regexp.MustCompile(`(?i)postgres(?:ql)?://[^\s"']+`)
)

func writeOutputs(outputDir string, result RunResult) (OutputPaths, error) {
	if err := os.MkdirAll(outputDir, 0o750); err != nil {
		return OutputPaths{}, fmt.Errorf("create output directory: %w", err)
	}
	paths := OutputPaths{
		JSON:     filepath.Join(outputDir, "benchmark-results.json"),
		CSV:      filepath.Join(outputDir, "benchmark-results.csv"),
		Markdown: filepath.Join(outputDir, "benchmark-report.md"),
	}
	safe := sanitizeRunResult(result)
	if err := atomicWrite(paths.JSON, func(buffer *bytes.Buffer) error {
		return writeJSON(buffer, safe)
	}); err != nil {
		return paths, err
	}
	if err := atomicWrite(paths.CSV, func(buffer *bytes.Buffer) error {
		return writeCSV(buffer, safe)
	}); err != nil {
		return paths, err
	}
	if err := atomicWrite(paths.Markdown, func(buffer *bytes.Buffer) error {
		_, err := buffer.WriteString(renderReport(safe, paths))
		return err
	}); err != nil {
		return paths, err
	}
	return paths, nil
}

func writeJSON(out *bytes.Buffer, result RunResult) error {
	safe := sanitizeRunResult(result)
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(safe)
}

var csvHeader = []string{
	"run_id",
	"scenario",
	"correlation_id",
	"job_id",
	"execution_id",
	"attempt_id",
	"attempt",
	"status_code",
	"signature_valid",
	"duplicate",
	"warmup",
	"trigger_type",
	"final_status",
	"error_class",
	"scheduled_at",
	"fired_at",
	"execution_created_at",
	"attempt_started_at",
	"attempt_finished_at",
	"receiver_arrived_at",
	"api_trigger_latency_ms",
	"scheduler_lag_ms",
	"queue_wait_ms",
	"claim_to_dispatch_ms",
	"webhook_rtt_ms",
	"receiver_processing_ms",
	"terminal_persistence_lag_ms",
	"end_to_end_delivery_ms",
	"measurement_provenance",
}

func writeCSV(out *bytes.Buffer, result RunResult) error {
	writer := csv.NewWriter(out)
	if err := writer.Write(csvHeader); err != nil {
		return err
	}
	for _, scenario := range result.Scenarios {
		for _, execution := range scenario.Executions {
			rows := executionCSVRows(execution)
			for _, row := range rows {
				if err := writer.Write(row); err != nil {
					return err
				}
			}
		}
	}
	writer.Flush()
	return writer.Error()
}

func executionCSVRows(record ExecutionRecord) [][]string {
	type attemptRow struct {
		id         string
		attempt    int
		statusCode int
		errorClass string
		startedAt  time.Time
		finishedAt time.Time
		callback   *CallbackObservation
	}
	var attempts []attemptRow
	callbackByID := make(map[string]*CallbackObservation)
	for index := range record.Callbacks {
		callback := &record.Callbacks[index]
		callbackByID[callback.AttemptID] = callback
	}
	if record.Diagnostic != nil {
		for _, attempt := range record.Diagnostic.Attempts {
			attempts = append(attempts, attemptRow{
				id:         attempt.ID,
				attempt:    attempt.Attempt,
				statusCode: attempt.StatusCode,
				errorClass: classifyAttemptError(attempt),
				startedAt:  attempt.StartedAt,
				finishedAt: attempt.FinishedAt,
				callback:   callbackByID[attempt.ID],
			})
		}
	}
	if len(attempts) == 0 {
		for index := range record.Callbacks {
			callback := &record.Callbacks[index]
			attempts = append(attempts, attemptRow{
				id:         callback.AttemptID,
				attempt:    index + 1,
				statusCode: callback.StatusCode,
				callback:   callback,
			})
		}
	}
	if len(attempts) == 0 {
		attempts = append(attempts, attemptRow{})
	}

	duplicate := hasDuplicateFinding(record.Findings)
	finalStatus := record.APIExecution.Status
	if record.PollBounds != nil && record.PollBounds.FinalExecution.Status != "" {
		finalStatus = record.PollBounds.FinalExecution.Status
	}
	measurementValues, provenance := measurementColumns(record.Measurements)
	rows := make([][]string, 0, len(attempts))
	for _, attempt := range attempts {
		signatureValid := ""
		receiverArrived := ""
		if attempt.callback != nil {
			signatureValid = strconv.FormatBool(attempt.callback.SignatureValid)
			receiverArrived = formatTime(attempt.callback.ReceivedAt)
		}
		row := []string{
			record.RunID,
			record.Scenario,
			record.CorrelationID,
			record.JobID,
			record.ExecutionID,
			attempt.id,
			formatInt(attempt.attempt),
			formatInt(attempt.statusCode),
			signatureValid,
			strconv.FormatBool(duplicate),
			strconv.FormatBool(record.Warmup),
			record.APIExecution.TriggerType,
			finalStatus,
			attempt.errorClass,
			formatTime(record.APIExecution.ScheduledAt),
			formatTime(record.APIExecution.FiredAt),
			formatTime(record.APIExecution.CreatedAt),
			formatTime(attempt.startedAt),
			formatTime(attempt.finishedAt),
			receiverArrived,
			measurementValues["api_trigger_latency_ms"],
			measurementValues["scheduler_lag_ms"],
			measurementValues["queue_wait_ms"],
			measurementValues["claim_to_dispatch_ms"],
			firstMeasurementValue(measurementValues, "webhook_rtt_ms", "webhook_rtt_ms_attempt_"+formatInt(attempt.attempt)),
			measurementValues["receiver_processing_ms"],
			measurementValues["terminal_persistence_lag_ms"],
			measurementValues["end_to_end_delivery_ms"],
			provenance,
		}
		rows = append(rows, row)
	}
	return rows
}

func measurementColumns(measurements []Measurement) (map[string]string, string) {
	values := make(map[string]string)
	provenance := make([]string, 0, len(measurements))
	for _, measurement := range measurements {
		if measurement.ValueMS != nil {
			values[measurement.Name] = strconv.FormatFloat(*measurement.ValueMS, 'f', 6, 64)
		}
		provenance = append(provenance, measurement.Name+"="+string(measurement.Provenance))
	}
	return values, strings.Join(provenance, ";")
}

func firstMeasurementValue(values map[string]string, names ...string) string {
	for _, name := range names {
		if value := values[name]; value != "" {
			return value
		}
	}
	return ""
}

func classifyAttemptError(attempt DiagnosticAttempt) string {
	if attempt.Error != "" {
		return "network_error"
	}
	switch {
	case attempt.StatusCode == 0:
		return ""
	case attempt.StatusCode == httpStatusTooManyRequests:
		return "retryable_429"
	case attempt.StatusCode >= 500:
		return "retryable_5xx"
	case attempt.StatusCode >= 400:
		return "non_retryable_4xx"
	default:
		return ""
	}
}

const httpStatusTooManyRequests = 429

func hasDuplicateFinding(findings []Finding) bool {
	for _, finding := range findings {
		if strings.Contains(finding.Code, "duplicate") {
			return true
		}
	}
	return false
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func formatInt(value int) string {
	if value == 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func atomicWrite(path string, render func(*bytes.Buffer) error) error {
	var buffer bytes.Buffer
	if err := render(&buffer); err != nil {
		return fmt.Errorf("render %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o640); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(buffer.Bytes()); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace output %s: %w", path, err)
	}
	return nil
}

func sanitizeRunResult(result RunResult) RunResult {
	result.Command = redactText(result.Command)
	for scenarioIndex := range result.Scenarios {
		scenario := &result.Scenarios[scenarioIndex]
		scenario.Reason = redactText(scenario.Reason)
		for observationIndex := range scenario.Observations {
			observation := &scenario.Observations[observationIndex]
			observation.Error = redactText(observation.Error)
		}
		for executionIndex := range scenario.Executions {
			execution := &scenario.Executions[executionIndex]
			for findingIndex := range execution.Findings {
				execution.Findings[findingIndex].Message = redactText(
					execution.Findings[findingIndex].Message,
				)
			}
			if execution.Diagnostic != nil {
				for attemptIndex := range execution.Diagnostic.Attempts {
					attempt := &execution.Diagnostic.Attempts[attemptIndex]
					attempt.Error = redactText(attempt.Error)
				}
			}
		}
		for findingIndex := range scenario.Findings {
			scenario.Findings[findingIndex].Message = redactText(
				scenario.Findings[findingIndex].Message,
			)
		}
	}
	for findingIndex := range result.Findings {
		result.Findings[findingIndex].Message = redactText(result.Findings[findingIndex].Message)
	}
	return result
}

func redactText(value string) string {
	value = apiKeyPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = bearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = postgresPattern.ReplaceAllString(value, "postgres://[REDACTED]")
	return value
}
