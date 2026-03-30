package api

import "time"

type CreateJobRequest struct {
	Name           string `json:"name"`
	CronExpression string `json:"cron_expression"`
	Timezone       string `json:"timezone"`
	WebhookURL     string `json:"webhook_url"`
	WebhookSecret  string `json:"webhook_secret,omitempty"`
	WebhookTimeout int    `json:"webhook_timeout_seconds,omitempty"` // default 30

	Analytics *AnalyticsRequest `json:"analytics,omitempty"`
}

// AnalyticsRequest enables per-job analytics.
// Presence of this object enables analytics; omit to disable.
type AnalyticsRequest struct {
	RetentionSeconds int `json:"retention_seconds,omitempty"` // default 86400 (24h)
}

type JobResponse struct {
	ID             string `json:"id"`
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	CronExpression string `json:"cron_expression"`
	Timezone       string `json:"timezone"`
	WebhookURL     string `json:"webhook_url"`
	CreatedAt      string `json:"created_at"`
}

type ExecutionResponse struct {
	ID          string `json:"id"`
	JobID       string `json:"job_id"`
	ScheduledAt string `json:"scheduled_at"`
	FiredAt     string `json:"fired_at"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
}

type ListJobsResponse struct {
	Jobs []JobResponse `json:"jobs"`
}

type ListExecutionsResponse struct {
	Executions []ExecutionResponse `json:"executions"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
