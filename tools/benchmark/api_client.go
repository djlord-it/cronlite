package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxAPIErrorBodyBytes = 64 << 10
	maxAPIResponseBytes  = 4 << 20
)

type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("CronLite API returned HTTP %d: %s", e.StatusCode, e.Body)
}

func asAPIError(err error, target **APIError) bool {
	return errors.As(err, target)
}

type apiClient struct {
	baseURL string
	apiKey  string
	timeout time.Duration
	client  *http.Client
}

func newAPIClient(baseURL, apiKey string, timeout time.Duration) *apiClient {
	return &apiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		timeout: timeout,
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   100,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: timeout,
			},
		},
	}
}

type CreateJobInput struct {
	Name           string            `json:"name"`
	CronExpression string            `json:"cron_expression"`
	Timezone       string            `json:"timezone"`
	WebhookURL     string            `json:"webhook_url"`
	WebhookSecret  string            `json:"webhook_secret,omitempty"`
	TimeoutSeconds int               `json:"webhook_timeout_seconds"`
	Tags           map[string]string `json:"tags,omitempty"`
}

type UpdateJobInput struct {
	Name           *string `json:"name,omitempty"`
	CronExpression *string `json:"cron_expression,omitempty"`
	Timezone       *string `json:"timezone,omitempty"`
	WebhookURL     *string `json:"webhook_url,omitempty"`
	WebhookSecret  *string `json:"webhook_secret,omitempty"`
	TimeoutSeconds *int    `json:"webhook_timeout_seconds,omitempty"`
	Enabled        *bool   `json:"enabled,omitempty"`
}

type APIJob struct {
	ID             string            `json:"id"`
	Namespace      string            `json:"namespace"`
	Name           string            `json:"name"`
	Enabled        bool              `json:"enabled"`
	CronExpression string            `json:"cron_expression"`
	Timezone       string            `json:"timezone"`
	WebhookURL     string            `json:"webhook_url"`
	Tags           map[string]string `json:"tags,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

func (c *apiClient) health(ctx context.Context, verbose bool) (Observation, error) {
	path := "/health"
	if verbose {
		path += "?verbose=true"
	}
	observation, _, err := c.do(ctx, http.MethodGet, path, nil, http.StatusOK)
	observation.Name = "baseline_cronlite_health_rtt_ms"
	return observation, err
}

func (c *apiClient) createJob(
	ctx context.Context,
	input CreateJobInput,
) (APIJob, Observation, error) {
	var job APIJob
	observation, body, err := c.do(ctx, http.MethodPost, "/jobs", input, http.StatusCreated)
	observation.Name = "api_create_latency_ms"
	if err != nil {
		return job, observation, err
	}
	if err := json.Unmarshal(body, &job); err != nil {
		return job, observation, fmt.Errorf("decode create job response: %w", err)
	}
	return job, observation, nil
}

func (c *apiClient) getJob(ctx context.Context, jobID string) (APIJob, Observation, error) {
	var job APIJob
	observation, body, err := c.do(
		ctx,
		http.MethodGet,
		"/jobs/"+url.PathEscape(jobID),
		nil,
		http.StatusOK,
	)
	observation.Name = "api_get_job_latency_ms"
	if err != nil {
		return job, observation, err
	}
	if err := json.Unmarshal(body, &job); err != nil {
		return job, observation, fmt.Errorf("decode get job response: %w", err)
	}
	return job, observation, nil
}

func (c *apiClient) updateJob(
	ctx context.Context,
	jobID string,
	input UpdateJobInput,
) (APIJob, Observation, error) {
	var job APIJob
	observation, body, err := c.do(
		ctx,
		http.MethodPatch,
		"/jobs/"+url.PathEscape(jobID),
		input,
		http.StatusOK,
	)
	observation.Name = "api_update_latency_ms"
	if err != nil {
		return job, observation, err
	}
	if err := json.Unmarshal(body, &job); err != nil {
		return job, observation, fmt.Errorf("decode update job response: %w", err)
	}
	return job, observation, nil
}

func (c *apiClient) pauseJob(ctx context.Context, jobID string) (Observation, error) {
	observation, _, err := c.do(
		ctx,
		http.MethodPost,
		"/jobs/"+url.PathEscape(jobID)+"/pause",
		nil,
		http.StatusOK,
	)
	observation.Name = "api_pause_latency_ms"
	return observation, err
}

func (c *apiClient) resumeJob(ctx context.Context, jobID string) (Observation, error) {
	observation, _, err := c.do(
		ctx,
		http.MethodPost,
		"/jobs/"+url.PathEscape(jobID)+"/resume",
		nil,
		http.StatusOK,
	)
	observation.Name = "api_resume_latency_ms"
	return observation, err
}

func (c *apiClient) deleteJob(ctx context.Context, jobID string) (Observation, error) {
	observation, _, err := c.do(
		ctx,
		http.MethodDelete,
		"/jobs/"+url.PathEscape(jobID),
		nil,
		http.StatusNoContent,
		http.StatusNotFound,
	)
	observation.Name = "api_delete_latency_ms"
	return observation, err
}

func (c *apiClient) trigger(
	ctx context.Context,
	jobID string,
) (APIExecution, Observation, error) {
	var execution APIExecution
	observation, body, err := c.do(
		ctx,
		http.MethodPost,
		"/jobs/"+url.PathEscape(jobID)+"/trigger",
		nil,
		http.StatusCreated,
	)
	observation.Name = "api_trigger_latency_ms"
	if err != nil {
		return execution, observation, err
	}
	if err := json.Unmarshal(body, &execution); err != nil {
		return execution, observation, fmt.Errorf("decode trigger response: %w", err)
	}
	return execution, observation, nil
}

func (c *apiClient) getExecution(
	ctx context.Context,
	executionID string,
) (APIExecution, Observation, error) {
	var execution APIExecution
	observation, body, err := c.do(
		ctx,
		http.MethodGet,
		"/executions/"+url.PathEscape(executionID),
		nil,
		http.StatusOK,
	)
	observation.Name = "api_get_execution_latency_ms"
	if err != nil {
		return execution, observation, err
	}
	if err := json.Unmarshal(body, &execution); err != nil {
		return execution, observation, fmt.Errorf("decode execution response: %w", err)
	}
	return execution, observation, nil
}

func (c *apiClient) listExecutions(
	ctx context.Context,
	jobID string,
) ([]APIExecution, Observation, error) {
	var response struct {
		Executions []APIExecution `json:"executions"`
	}
	observation, body, err := c.do(
		ctx,
		http.MethodGet,
		"/jobs/"+url.PathEscape(jobID)+"/executions",
		nil,
		http.StatusOK,
	)
	observation.Name = "api_list_executions_latency_ms"
	if err != nil {
		return nil, observation, err
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, observation, fmt.Errorf("decode executions response: %w", err)
	}
	return response.Executions, observation, nil
}

func (c *apiClient) pollTerminal(
	ctx context.Context,
	executionID string,
	interval time.Duration,
) (PollBounds, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	var result PollBounds
	for {
		execution, _, err := c.getExecution(ctx, executionID)
		observedAt := time.Now().UTC()
		if err != nil {
			return result, err
		}
		result.PollCount++
		result.FinalExecution = execution
		if execution.Status == "delivered" || execution.Status == "failed" {
			result.FirstTerminalAt = &observedAt
			return result, nil
		}
		result.LastNonTerminalAt = &observedAt

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return result, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *apiClient) do(
	ctx context.Context,
	method string,
	path string,
	requestBody any,
	expectedStatus ...int,
) (Observation, []byte, error) {
	var bodyReader io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return Observation{}, nil, fmt.Errorf("encode request: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return Observation{}, nil, fmt.Errorf("create request: %w", err)
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	started := time.Now()
	startedUTC := started.UTC()
	response, err := c.client.Do(request)
	finished := time.Now()
	observation := Observation{
		Kind:       "api",
		Name:       method + " " + path,
		StartedAt:  startedUTC,
		FinishedAt: finished.UTC(),
		Duration:   finished.Sub(started),
		Provenance: ProvenanceDirect,
	}
	if err != nil {
		observation.Error = err.Error()
		observation.ErrorClass = classifyNetworkError(err)
		return observation, nil, err
	}
	defer response.Body.Close()
	observation.StatusCode = response.StatusCode

	limit := int64(maxAPIResponseBytes)
	if !containsStatus(expectedStatus, response.StatusCode) {
		limit = maxAPIErrorBodyBytes
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, limit))
	if readErr != nil {
		observation.Error = readErr.Error()
		observation.ErrorClass = "response_read_error"
		return observation, nil, fmt.Errorf("read response: %w", readErr)
	}
	if !containsStatus(expectedStatus, response.StatusCode) {
		apiErr := &APIError{StatusCode: response.StatusCode, Body: string(body)}
		observation.Error = apiErr.Error()
		observation.ErrorClass = "http_error"
		return observation, body, apiErr
	}
	return observation, body, nil
}

func containsStatus(statuses []int, status int) bool {
	for _, candidate := range statuses {
		if candidate == status {
			return true
		}
	}
	return false
}

func classifyNetworkError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "connection_error"
}
