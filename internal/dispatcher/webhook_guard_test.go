package dispatcher

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/webhookurl"
)

func guardTestRequest(url string) WebhookRequest {
	return WebhookRequest{
		URL:       url,
		Secret:    "s",
		Timeout:   5 * time.Second,
		AttemptID: "a-1",
		Payload:   WebhookPayload{JobID: "j", ExecutionID: "e"},
	}
}

func TestSenderBlocksLoopbackByDefault(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	res := NewHTTPWebhookSender().Send(context.Background(), guardTestRequest(srv.URL))
	if !errors.Is(res.Error, webhookurl.ErrDestinationBlocked) {
		t.Fatalf("expected ErrDestinationBlocked, got %v", res.Error)
	}
	if res.IsRetryable() {
		t.Error("blocked destination must not be retryable")
	}
	if hits.Load() != 0 {
		t.Error("server must not be contacted")
	}
}

func TestSenderBlocksRedirectToPrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://10.0.0.1/", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	guarded := webhookurl.GuardedDialContext(webhookurl.Allowlist{}, nil, &net.Dialer{Timeout: time.Second})
	var first atomic.Bool
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if first.CompareAndSwap(false, true) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		}
		return guarded(ctx, network, addr)
	}

	res := NewHTTPWebhookSender(WithDialContext(dial)).Send(context.Background(), guardTestRequest(srv.URL))
	if !errors.Is(res.Error, webhookurl.ErrDestinationBlocked) {
		t.Fatalf("expected ErrDestinationBlocked on redirect hop, got %v", res.Error)
	}
	if res.IsRetryable() {
		t.Error("blocked redirect must not be retryable")
	}
}

func TestIsRetryableTransportErrorStillTrue(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens: connection refused

	sender := NewHTTPWebhookSender(WithDialContext((&net.Dialer{}).DialContext))
	res := sender.Send(context.Background(), guardTestRequest("http://"+addr))
	if res.Error == nil {
		t.Fatal("expected connection error")
	}
	if errors.Is(res.Error, webhookurl.ErrDestinationBlocked) {
		t.Fatalf("connection refused must not be classified as blocked: %v", res.Error)
	}
	if !res.IsRetryable() {
		t.Error("connection refused must stay retryable")
	}
}

func TestIsRetryableDNSNotFoundStillTrue(t *testing.T) {
	err := &net.DNSError{Err: "no addresses found", Name: "x.example", IsNotFound: true}
	if !(WebhookResult{Error: err}).IsRetryable() {
		t.Error("DNS not-found must stay retryable")
	}
}

func TestSenderTransportHasNoProxy(t *testing.T) {
	s := NewHTTPWebhookSender()
	tr, ok := s.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", s.client.Transport)
	}
	if tr.Proxy != nil {
		t.Fatal("transport.Proxy must be nil: a proxy would bypass the SSRF guard")
	}
	if tr.DialContext == nil {
		t.Fatal("transport.DialContext must be set")
	}
}

type blockedSender struct{ calls atomic.Int32 }

func (b *blockedSender) Send(ctx context.Context, req WebhookRequest) WebhookResult {
	b.calls.Add(1)
	return WebhookResult{Error: &webhookurl.BlockedError{Host: "h", IP: net.ParseIP("127.0.0.1"), Reason: "loopback address"}}
}

func TestDispatchBlockedSkipsBreakerAndRetries(t *testing.T) {
	store := newMockStore()
	sender := &blockedSender{}
	cb := newMockCircuitBreaker()
	jobID, execID := uuid.New(), uuid.New()
	store.addJob(domain.Job{
		ID: jobID, Namespace: domain.Namespace("test-ns"), Name: "j", Enabled: true,
		Delivery: domain.DeliveryConfig{Type: "webhook", WebhookURL: "http://127.0.0.1/hook", Timeout: 30 * time.Second},
	})
	disp := New(store, sender).WithCircuitBreaker(cb)
	disp.backoff = []time.Duration{0, 0, 0, 0}

	err := disp.Dispatch(context.Background(), domain.TriggerEvent{
		JobID: jobID, ExecutionID: execID, ScheduledAt: time.Now(), FiredAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if n := sender.calls.Load(); n != 1 {
		t.Errorf("attempts = %d, want 1", n)
	}
	if store.getExecutionStatus(execID) != domain.ExecutionStatusFailed {
		t.Errorf("status = %v, want failed", store.getExecutionStatus(execID))
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if len(cb.failureCalls) != 0 {
		t.Errorf("RecordFailure calls = %d, want 0", len(cb.failureCalls))
	}
}
