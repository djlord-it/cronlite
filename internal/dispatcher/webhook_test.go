package dispatcher

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPWebhookSender_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewHTTPWebhookSender()
	result := sender.Send(context.Background(), WebhookRequest{
		URL:       server.URL,
		Secret:    "test-secret",
		Timeout:   5 * time.Second,
		AttemptID: "attempt-1",
		Payload: WebhookPayload{
			JobID:       "job-1",
			ExecutionID: "exec-1",
			ScheduledAt: "2024-01-15T10:00:00Z",
			FiredAt:     "2024-01-15T10:00:30Z",
		},
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", result.StatusCode)
	}
	if result.Duration <= 0 {
		t.Error("duration should be positive")
	}
}

func TestHTTPWebhookSender_RequestHeaders(t *testing.T) {
	var gotHeaders http.Header
	var gotMethod string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewHTTPWebhookSender()
	sender.Send(context.Background(), WebhookRequest{
		URL:       server.URL,
		Secret:    "my-secret",
		Timeout:   5 * time.Second,
		AttemptID: "attempt-123",
		Payload: WebhookPayload{
			JobID:       "job-1",
			ExecutionID: "exec-456",
			ScheduledAt: "2024-01-15T10:00:00Z",
			FiredAt:     "2024-01-15T10:00:30Z",
		},
	})

	// Method
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}

	// Content-Type
	if ct := gotHeaders.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	// X-CronLite-Event-ID
	if id := gotHeaders.Get("X-CronLite-Event-ID"); id != "attempt-123" {
		t.Errorf("X-CronLite-Event-ID = %q, want attempt-123", id)
	}

	// X-CronLite-Execution-ID
	if id := gotHeaders.Get("X-CronLite-Execution-ID"); id != "exec-456" {
		t.Errorf("X-CronLite-Execution-ID = %q, want exec-456", id)
	}

	// X-CronLite-Signature should be non-empty
	if sig := gotHeaders.Get("X-CronLite-Signature"); sig == "" {
		t.Error("X-CronLite-Signature should not be empty")
	}
}

func TestHTTPWebhookSender_PayloadBody(t *testing.T) {
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewHTTPWebhookSender()
	sender.Send(context.Background(), WebhookRequest{
		URL:     server.URL,
		Secret:  "secret",
		Timeout: 5 * time.Second,
		Payload: WebhookPayload{
			JobID:       "job-abc",
			ExecutionID: "exec-def",
			ScheduledAt: "2024-01-15T10:00:00Z",
			FiredAt:     "2024-01-15T10:00:30Z",
		},
	})

	var payload WebhookPayload
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}

	if payload.JobID != "job-abc" {
		t.Errorf("JobID = %q, want job-abc", payload.JobID)
	}
	if payload.ExecutionID != "exec-def" {
		t.Errorf("ExecutionID = %q, want exec-def", payload.ExecutionID)
	}
	if payload.ScheduledAt != "2024-01-15T10:00:00Z" {
		t.Errorf("ScheduledAt = %q, want 2024-01-15T10:00:00Z", payload.ScheduledAt)
	}
}

func TestHTTPWebhookSender_SignatureCorrect(t *testing.T) {
	var gotSignature string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSignature = r.Header.Get("X-CronLite-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	secret := "my-webhook-secret"

	sender := NewHTTPWebhookSender()
	sender.Send(context.Background(), WebhookRequest{
		URL:     server.URL,
		Secret:  secret,
		Timeout: 5 * time.Second,
		Payload: WebhookPayload{
			JobID:       "job-1",
			ExecutionID: "exec-1",
			ScheduledAt: "2024-01-15T10:00:00Z",
			FiredAt:     "2024-01-15T10:00:30Z",
		},
	})

	// Verify signature manually
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(gotBody)
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if gotSignature != expectedSig {
		t.Errorf("signature mismatch:\n  got:  %s\n  want: %s", gotSignature, expectedSig)
	}
}

func TestHTTPWebhookSender_DefaultTimeout(t *testing.T) {
	// Verify that when Timeout=0, a timeout is still applied (30s default).
	// We can't easily test the exact value, but we can verify the request succeeds
	// with a fast server.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewHTTPWebhookSender()
	result := sender.Send(context.Background(), WebhookRequest{
		URL:     server.URL,
		Secret:  "secret",
		Timeout: 0, // should use default 30s
		Payload: WebhookPayload{
			JobID:       "job-1",
			ExecutionID: "exec-1",
		},
	})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.StatusCode != 200 {
		t.Errorf("expected 200, got %d", result.StatusCode)
	}
}

func TestHTTPWebhookSender_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	sender := NewHTTPWebhookSender()
	result := sender.Send(context.Background(), WebhookRequest{
		URL:     server.URL,
		Secret:  "secret",
		Timeout: 5 * time.Second,
		Payload: WebhookPayload{JobID: "j", ExecutionID: "e"},
	})

	if result.Error != nil {
		t.Errorf("server error should not set Error field, got: %v", result.Error)
	}
	if result.StatusCode != 500 {
		t.Errorf("expected status 500, got %d", result.StatusCode)
	}
}

func TestHTTPWebhookSender_ConnectionError(t *testing.T) {
	sender := NewHTTPWebhookSender()
	result := sender.Send(context.Background(), WebhookRequest{
		URL:     "http://localhost:1", // unlikely to be listening
		Secret:  "secret",
		Timeout: 1 * time.Second,
		Payload: WebhookPayload{JobID: "j", ExecutionID: "e"},
	})

	if result.Error == nil {
		t.Error("expected connection error, got nil")
	}
}

func TestVerifySignature_Valid(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"job_id":"j1","execution_id":"e1"}`)

	sig := computeSignature(secret, body)

	if !VerifySignature(secret, body, sig) {
		t.Error("VerifySignature should return true for valid signature")
	}
}

func TestVerifySignature_WrongSecret(t *testing.T) {
	body := []byte(`{"job_id":"j1"}`)
	sig := computeSignature("correct-secret", body)

	if VerifySignature("wrong-secret", body, sig) {
		t.Error("VerifySignature should return false for wrong secret")
	}
}

func TestVerifySignature_TamperedBody(t *testing.T) {
	secret := "test-secret"
	originalBody := []byte(`{"job_id":"j1"}`)
	sig := computeSignature(secret, originalBody)

	tamperedBody := []byte(`{"job_id":"j2"}`)
	if VerifySignature(secret, tamperedBody, sig) {
		t.Error("VerifySignature should return false for tampered body")
	}
}

func TestVerifySignature_EmptySecret(t *testing.T) {
	body := []byte(`{"job_id":"j1"}`)
	sig := computeSignature("", body)

	// Empty secret should still produce a valid (deterministic) signature
	if !VerifySignature("", body, sig) {
		t.Error("VerifySignature should work with empty secret")
	}
}

func TestComputeSignature_Deterministic(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"job_id":"j1","execution_id":"e1"}`)

	sig1 := computeSignature(secret, body)
	sig2 := computeSignature(secret, body)

	if sig1 != sig2 {
		t.Errorf("computeSignature should be deterministic: %s != %s", sig1, sig2)
	}

	// Verify it's a valid hex string
	if _, err := hex.DecodeString(sig1); err != nil {
		t.Errorf("signature should be valid hex: %v", err)
	}

	// SHA256 produces 32 bytes = 64 hex chars
	if len(sig1) != 64 {
		t.Errorf("signature length should be 64 hex chars, got %d", len(sig1))
	}
}
