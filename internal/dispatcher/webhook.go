package dispatcher

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/djlord-it/cronlite/internal/webhookurl"
)

type senderOptions struct {
	allowlist webhookurl.Allowlist
	dial      webhookurl.DialFunc
}

// SenderOption configures NewHTTPWebhookSender.
type SenderOption func(*senderOptions)

// WithAllowlist permits private-range addresses for allowlisted hostnames.
func WithAllowlist(a webhookurl.Allowlist) SenderOption {
	return func(o *senderOptions) { o.allowlist = a }
}

// WithDialContext replaces the SSRF guard's dialer entirely, so no destination
// check is made. It is for tests that target loopback servers only; never use
// it in production wiring.
func WithDialContext(fn webhookurl.DialFunc) SenderOption {
	return func(o *senderOptions) { o.dial = fn }
}

type HTTPWebhookSender struct {
	client *http.Client
}

// NewHTTPWebhookSender creates a webhook sender with a properly configured HTTP client.
// The client is configured with:
// - Connection pooling (max 100 idle connections, 10 per host)
// - Idle connection timeout (90s)
// - TLS handshake timeout (10s)
// - Response header timeout (30s) - prevents slow loris attacks
//
// Every connection, including redirect hops, goes through the SSRF guard
// (webhookurl.GuardedDialContext). Transport.Proxy is deliberately left nil:
// a proxy would resolve and dial on our behalf and bypass the guard.
func NewHTTPWebhookSender(opts ...SenderOption) *HTTPWebhookSender {
	var o senderOptions
	for _, opt := range opts {
		opt(&o)
	}
	dial := o.dial
	if dial == nil {
		dial = webhookurl.GuardedDialContext(o.allowlist, net.DefaultResolver,
			&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second})
	}
	transport := &http.Transport{
		DialContext:           dial,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &HTTPWebhookSender{
		client: &http.Client{
			Transport: transport,
			// No client-level timeout; we use per-request context timeouts
		},
	}
}

// Send posts the webhook payload with HMAC signature.
// Headers: X-CronLite-Event-ID (attempt), X-CronLite-Execution-ID, X-CronLite-Signature
func (s *HTTPWebhookSender) Send(ctx context.Context, req WebhookRequest) WebhookResult {
	start := time.Now()

	body, err := json.Marshal(req.Payload)
	if err != nil {
		return WebhookResult{Error: fmt.Errorf("marshal: %w", err), Duration: time.Since(start)}
	}

	signature := computeSignature(req.Secret, body)

	timeout := req.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctxTimeout, http.MethodPost, req.URL, bytes.NewReader(body))
	if err != nil {
		return WebhookResult{Error: fmt.Errorf("create request: %w", err), Duration: time.Since(start)}
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-CronLite-Event-ID", req.AttemptID)
	httpReq.Header.Set("X-CronLite-Execution-ID", req.Payload.ExecutionID)
	httpReq.Header.Set("X-CronLite-Signature", signature)

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return WebhookResult{Error: fmt.Errorf("send: %w", err), Duration: time.Since(start)}
	}
	defer resp.Body.Close()

	return WebhookResult{StatusCode: resp.StatusCode, Duration: time.Since(start)}
}

func computeSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature is for customers to verify incoming webhooks.
func VerifySignature(secret string, body []byte, signature string) bool {
	expected := computeSignature(secret, body)
	return hmac.Equal([]byte(expected), []byte(signature))
}
