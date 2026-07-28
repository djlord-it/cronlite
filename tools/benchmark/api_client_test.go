package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAPIClientTriggerCapturesExecutionAndTiming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer key" {
			t.Fatalf("authorization = %q", got)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/jobs/22222222-2222-2222-2222-222222222222/trigger" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, executionJSON("emitted"))
	}))
	t.Cleanup(server.Close)

	client := newAPIClient(server.URL, "key", time.Second)
	exec, observation, err := client.trigger(context.Background(), "22222222-2222-2222-2222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if exec.ID == "" || observation.Duration <= 0 || observation.StatusCode != http.StatusCreated {
		t.Fatalf("execution=%+v observation=%+v", exec, observation)
	}
}

func TestAPIClientBoundsErrorBodies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, strings.Repeat("x", maxAPIErrorBodyBytes*2))
	}))
	t.Cleanup(server.Close)

	client := newAPIClient(server.URL, "key", time.Second)
	_, _, err := client.trigger(context.Background(), "22222222-2222-2222-2222-222222222222")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("error type = %T", err)
	}
	if len(apiErr.Body) > maxAPIErrorBodyBytes {
		t.Fatalf("unbounded body length = %d", len(apiErr.Body))
	}
}

func TestPollTerminalPreservesObservationBounds(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		status := "emitted"
		if requests.Add(1) == 2 {
			status = "delivered"
		}
		_, _ = fmt.Fprint(w, executionJSON(status))
	}))
	t.Cleanup(server.Close)

	client := newAPIClient(server.URL, "key", time.Second)
	got, err := client.pollTerminal(
		context.Background(),
		"11111111-1111-1111-1111-111111111111",
		time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.PollCount != 2 || got.LastNonTerminalAt == nil || got.FirstTerminalAt == nil {
		t.Fatalf("poll result = %+v", got)
	}
	if got.LastNonTerminalAt.After(*got.FirstTerminalAt) {
		t.Fatalf("invalid bounds: %+v", got)
	}
}

func TestAPIClientCreateAndDeleteJob(t *testing.T) {
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/jobs":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"22222222-2222-2222-2222-222222222222","namespace":"default","name":"benchmark","enabled":true,"cron_expression":"0 0 * * *","timezone":"UTC","webhook_url":"http://receiver/hook","created_at":"2026-07-28T12:00:00Z","updated_at":"2026-07-28T12:00:00Z"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/jobs/22222222-2222-2222-2222-222222222222":
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client := newAPIClient(server.URL, "key", time.Second)
	job, _, err := client.createJob(context.Background(), CreateJobInput{
		Name:           "benchmark",
		CronExpression: "0 0 * * *",
		Timezone:       "UTC",
		WebhookURL:     "http://receiver/hook",
		WebhookSecret:  "secret",
		TimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.deleteJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if !deleted.Load() {
		t.Fatal("job was not deleted")
	}
}

func executionJSON(status string) string {
	return fmt.Sprintf(`{"id":"11111111-1111-1111-1111-111111111111","job_id":"22222222-2222-2222-2222-222222222222","scheduled_at":"2026-07-28T12:00:00Z","fired_at":"2026-07-28T12:00:00Z","status":%q,"trigger_type":"manual","created_at":"2026-07-28T12:00:00Z"}`, status)
}
