package main

import "time"

const resultSchemaVersion = "1.0"

type Provenance string

const (
	ProvenanceDirect      Provenance = "directly_observed"
	ProvenanceDerived     Provenance = "derived"
	ProvenanceDatabase    Provenance = "database_observed"
	ProvenanceUnavailable Provenance = "unavailable"
)

type Measurement struct {
	Name       string     `json:"name"`
	ValueMS    *float64   `json:"value_ms,omitempty"`
	Provenance Provenance `json:"provenance"`
	Reason     string     `json:"reason,omitempty"`
}

type Observation struct {
	Kind       string        `json:"kind"`
	Name       string        `json:"name"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Duration   time.Duration `json:"duration_ns"`
	StatusCode int           `json:"status_code,omitempty"`
	ErrorClass string        `json:"error_class,omitempty"`
	Error      string        `json:"error,omitempty"`
	Provenance Provenance    `json:"provenance"`
}

type ScenarioStatus string

const (
	ScenarioPassed  ScenarioStatus = "passed"
	ScenarioFailed  ScenarioStatus = "failed"
	ScenarioSkipped ScenarioStatus = "skipped"
)

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

type Finding struct {
	Severity    Severity `json:"severity"`
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	Scenario    string   `json:"scenario,omitempty"`
	ExecutionID string   `json:"execution_id,omitempty"`
	AttemptID   string   `json:"attempt_id,omitempty"`
}

type APIExecution struct {
	ID             string     `json:"id"`
	JobID          string     `json:"job_id"`
	ScheduledAt    time.Time  `json:"scheduled_at"`
	FiredAt        time.Time  `json:"fired_at"`
	Status         string     `json:"status"`
	TriggerType    string     `json:"trigger_type"`
	CreatedAt      time.Time  `json:"created_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
}

type CallbackObservation struct {
	ReceivedAt             time.Time         `json:"received_at"`
	ResponseStartedAt      time.Time         `json:"response_started_at"`
	ResponseCompletedAt    time.Time         `json:"response_completed_at"`
	ExecutionID            string            `json:"execution_id"`
	AttemptID              string            `json:"attempt_id"`
	SignatureValid         bool              `json:"signature_valid"`
	StatusCode             int               `json:"status_code"`
	BodySHA256             string            `json:"body_sha256"`
	Body                   string            `json:"body"`
	Headers                map[string]string `json:"headers,omitempty"`
	ConcurrentForExecution int               `json:"concurrent_for_execution"`
	AfterTerminal          bool              `json:"after_terminal,omitempty"`
}

type DiagnosticAttempt struct {
	ID         string    `json:"id"`
	Attempt    int       `json:"attempt"`
	StatusCode int       `json:"status_code"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	QueryTime  time.Time `json:"query_time"`
}

type DiagnosticExecution struct {
	ExecutionID string              `json:"execution_id"`
	JobID       string              `json:"job_id"`
	ScheduledAt time.Time           `json:"scheduled_at"`
	FiredAt     time.Time           `json:"fired_at"`
	CreatedAt   time.Time           `json:"created_at"`
	ClaimedAt   *time.Time          `json:"claimed_at,omitempty"`
	Status      string              `json:"status"`
	Attempts    []DiagnosticAttempt `json:"attempts"`
	ObservedAt  time.Time           `json:"observed_at"`
}

type PollBounds struct {
	PollCount         int          `json:"poll_count"`
	LastNonTerminalAt *time.Time   `json:"last_non_terminal_at,omitempty"`
	FirstTerminalAt   *time.Time   `json:"first_terminal_at,omitempty"`
	FinalExecution    APIExecution `json:"final_execution"`
}

type ExecutionRecord struct {
	RunID          string                `json:"run_id"`
	Scenario       string                `json:"scenario"`
	CorrelationID  string                `json:"correlation_id"`
	SampleIndex    int                   `json:"sample_index"`
	JobID          string                `json:"job_id"`
	ExecutionID    string                `json:"execution_id"`
	TargetURL      string                `json:"target_url"`
	Warmup         bool                  `json:"warmup"`
	TriggerRequest Observation           `json:"trigger_request"`
	APIExecution   APIExecution          `json:"api_execution"`
	PollBounds     *PollBounds           `json:"poll_bounds,omitempty"`
	Callbacks      []CallbackObservation `json:"callbacks"`
	Diagnostic     *DiagnosticExecution  `json:"diagnostic,omitempty"`
	Measurements   []Measurement         `json:"measurements"`
	Findings       []Finding             `json:"findings"`
}

type ScenarioResult struct {
	Name         string            `json:"name"`
	Status       ScenarioStatus    `json:"status"`
	Reason       string            `json:"reason,omitempty"`
	StartedAt    time.Time         `json:"started_at"`
	FinishedAt   time.Time         `json:"finished_at"`
	Executions   []ExecutionRecord `json:"executions"`
	Observations []Observation     `json:"observations"`
	Findings     []Finding         `json:"findings"`
}

type EnvironmentInfo struct {
	OS                string            `json:"os"`
	Architecture      string            `json:"architecture"`
	CPUCount          int               `json:"cpu_count"`
	GoVersion         string            `json:"go_version"`
	CommitSHA         string            `json:"commit_sha"`
	DockerVersion     string            `json:"docker_version,omitempty"`
	PostgreSQLVersion string            `json:"postgresql_version,omitempty"`
	DispatchMode      string            `json:"dispatch_mode,omitempty"`
	CronLiteInstances int               `json:"cronlite_instances,omitempty"`
	RelevantConfig    map[string]string `json:"relevant_config,omitempty"`
}

type RunResult struct {
	SchemaVersion string           `json:"schema_version"`
	RunID         string           `json:"run_id"`
	RandomSeed    int64            `json:"random_seed"`
	Command       string           `json:"command"`
	StartedAt     time.Time        `json:"started_at"`
	FinishedAt    time.Time        `json:"finished_at"`
	Environment   EnvironmentInfo  `json:"environment"`
	Config        RedactedConfig   `json:"config"`
	Scenarios     []ScenarioResult `json:"scenarios"`
	Findings      []Finding        `json:"findings"`
	Limitations   []string         `json:"limitations"`
}
