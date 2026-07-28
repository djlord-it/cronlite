package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestReceiverVerifiesSignatureAndRecordsAttempt(t *testing.T) {
	store := newCallbackStore()
	server := httptest.NewServer(newReceiverHandler(store, "secret", defaultReceiverLimits()))
	t.Cleanup(server.Close)

	body := callbackBody("exec-1")
	resp := sendSignedCallback(t, server.URL+"/hook/success", "secret", body, "exec-1", "attempt-1")
	resp.Body.Close()

	got := store.snapshot()
	if len(got) != 1 || !got[0].SignatureValid || got[0].AttemptID != "attempt-1" {
		t.Fatalf("unexpected callback: %+v", got)
	}
	if got[0].StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", got[0].StatusCode)
	}
}

func TestReceiverRecordsInvalidSignature(t *testing.T) {
	store := newCallbackStore()
	server := httptest.NewServer(newReceiverHandler(store, "secret", defaultReceiverLimits()))
	t.Cleanup(server.Close)

	body := callbackBody("exec-1")
	resp := sendSignedCallback(t, server.URL+"/hook/success", "wrong", body, "exec-1", "attempt-1")
	resp.Body.Close()

	got := store.snapshot()
	if len(got) != 1 || got[0].SignatureValid {
		t.Fatalf("unexpected callback: %+v", got)
	}
}

func TestReceiverFollowsStatusSequence(t *testing.T) {
	store := newCallbackStore()
	token := store.registerPlan(BehaviorPlan{Statuses: []int{500, 503, 204}})
	server := httptest.NewServer(newReceiverHandler(store, "secret", defaultReceiverLimits()))
	t.Cleanup(server.Close)

	body := callbackBody("exec-1")
	for index, want := range []int{500, 503, 204} {
		resp := sendSignedCallback(t, server.URL+"/hook/"+token, "secret", body, "exec-1", "attempt-"+string(rune('1'+index)))
		if resp.StatusCode != want {
			t.Fatalf("attempt %d: want %d got %d", index+1, want, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestReceiverRejectsOversizedBodyAndStoresBoundedObservation(t *testing.T) {
	store := newCallbackStore()
	limits := defaultReceiverLimits()
	limits.MaxBodyBytes = 32
	server := httptest.NewServer(newReceiverHandler(store, "secret", limits))
	t.Cleanup(server.Close)

	body := bytes.Repeat([]byte("x"), 64)
	resp := sendSignedCallback(t, server.URL+"/hook/success", "secret", body, "exec-1", "attempt-1")
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	got := store.snapshot()
	if len(got) != 1 || len(got[0].Body) > int(limits.MaxBodyBytes) {
		t.Fatalf("unbounded observation: %+v", got)
	}
}

func TestReceiverDetectsOverlappingExecutionCallbacks(t *testing.T) {
	store := newCallbackStore()
	token := store.registerPlan(BehaviorPlan{
		Statuses: []int{http.StatusNoContent},
		Delay:    100 * time.Millisecond,
	})
	server := httptest.NewServer(newReceiverHandler(store, "secret", defaultReceiverLimits()))
	t.Cleanup(server.Close)

	body := callbackBody("exec-1")
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, attempt := range []string{"attempt-1", "attempt-2"} {
		wg.Add(1)
		go func(attemptID string) {
			defer wg.Done()
			<-start
			resp, err := doSignedCallback(server.URL+"/hook/"+token, "secret", body, "exec-1", attemptID)
			if err != nil {
				t.Errorf("callback: %v", err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}(attempt)
	}
	close(start)
	wg.Wait()

	if !store.summary("exec-1").ConcurrentDuplicate {
		t.Fatal("expected concurrent duplicate")
	}
}

func TestReceiverBaselineIsNotRecordedAsCallback(t *testing.T) {
	store := newCallbackStore()
	server := httptest.NewServer(newReceiverHandler(store, "secret", defaultReceiverLimits()))
	t.Cleanup(server.Close)

	resp, err := http.Get(server.URL + "/baseline")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := store.snapshot(); len(got) != 0 {
		t.Fatalf("baseline recorded as callback: %+v", got)
	}
}

func callbackBody(executionID string) []byte {
	return []byte(`{"execution_id":"` + executionID + `","job_id":"job-1","scheduled_at":"2026-07-28T12:00:00Z","fired_at":"2026-07-28T12:00:01Z"}`)
}

func sendSignedCallback(t *testing.T, targetURL, secret string, body []byte, executionID, attemptID string) *http.Response {
	t.Helper()
	resp, err := doSignedCallback(targetURL, secret, body, executionID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func doSignedCallback(targetURL, secret string, body []byte, executionID, attemptID string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-CronLite-Execution-ID", executionID)
	req.Header.Set("X-CronLite-Event-ID", attemptID)
	req.Header.Set("X-CronLite-Signature", testSignature(secret, body))
	return http.DefaultClient.Do(req)
}

func testSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
