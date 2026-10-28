package dispatcher

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/djlord-it/cronlite/internal/webhookurl"
)

type senderOptions struct {
	allowlist webhookurl.Allowlist
	dial      webhookurl.DialFunc
	tailnet   webhookurl.Tailnet
	policy    *webhookurl.TailnetPolicy
}

// SenderOption configures NewHTTPWebhookSender.
type SenderOption func(*senderOptions)

// WithAllowlist permits private-range addresses for allowlisted hostnames.
func WithAllowlist(a webhookurl.Allowlist) SenderOption {
	return func(o *senderOptions) { o.allowlist = a }
}

// WithTailnet routes hostnames under tn's MagicDNS suffix over the tailnet
// (see webhookurl.RoutedDialContext). Every other destination still goes
// through the SSRF guard. A nil tn disables tailnet delivery.
func WithTailnet(tn webhookurl.Tailnet) SenderOption {
	return func(o *senderOptions) { o.tailnet = tn }
}

// WithTailnetPolicy sets which tailnet hosts each namespace may reach. It
// applies only with WithTailnet. Without it (or with a nil p) every tailnet
// host is refused: the policy is default deny.
func WithTailnetPolicy(p *webhookurl.TailnetPolicy) SenderOption {
	return func(o *senderOptions) { o.policy = p }
}

// WithDialContext replaces the SSRF guard's dialer for non-tailnet
// destinations, so no destination check is made for them. It is for tests
// that target loopback servers only; never use it in production wiring.
func WithDialContext(fn webhookurl.DialFunc) SenderOption {
	return func(o *senderOptions) { o.dial = fn }
}

type HTTPWebhookSender struct {
	client *http.Client
	// policy and suffix gate tailnet destinations per namespace. The check is
	// on only when suffix is non-empty, that is, when tailnet delivery is on.
	policy *webhookurl.TailnetPolicy
	suffix string
}

// namespaceKey carries the sending namespace from Send to CheckRedirect.
type namespaceKey struct{}

// checkTailnet refuses a tailnet host that namespace may not reach. It is a
// no-op when tailnet delivery is off, so the public path is unchanged.
func (s *HTTPWebhookSender) checkTailnet(namespace, host string) error {
	if s.suffix == "" {
		return nil
	}
	return s.policy.Check(namespace, host, s.suffix)
}

// NewHTTPWebhookSender creates a webhook sender with a properly configured HTTP client.
// The client is configured with:
// - Connection pooling (max 100 idle connections, 10 per host)
// - Idle connection timeout (90s)
// - TLS handshake timeout (10s)
// - Response header timeout (30s) - prevents slow loris attacks
//
// Every connection, including redirect hops, goes through the SSRF guard
// (webhookurl.GuardedDialContext), or, with WithTailnet, through the tailnet
// guard for hostnames under the tailnet's suffix. The choice is made per dial,
// so each redirect hop is routed and checked on its own. Transport.Proxy is
// deliberately left nil: a proxy would resolve and dial on our behalf and
// bypass the guard.
//
// With WithTailnet, the tailnet host policy (WithTailnetPolicy) is checked per
// request, on the request URL in Send and on every redirect hop, and not per
// dial: the transport pools keep-alive connections by host and port, so a
// connection dialled for one namespace can carry another namespace's request
// without a new dial.
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
	dial = webhookurl.RoutedDialContext(dial, o.tailnet)
	transport := &http.Transport{
		DialContext:           dial,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	s := &HTTPWebhookSender{policy: o.policy}
	if o.tailnet != nil {
		s.suffix = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(o.tailnet.Suffix())), ".")
	}
	s.client = &http.Client{
		Transport: transport,
		// No client-level timeout; we use per-request context timeouts
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			ns, _ := r.Context().Value(namespaceKey{}).(string)
			return s.checkTailnet(ns, r.URL.Hostname())
		},
	}
	return s
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
	ctxTimeout = context.WithValue(ctxTimeout, namespaceKey{}, req.Namespace)

	httpReq, err := http.NewRequestWithContext(ctxTimeout, http.MethodPost, req.URL, bytes.NewReader(body))
	if err != nil {
		return WebhookResult{Error: fmt.Errorf("create request: %w", err), Duration: time.Since(start)}
	}
	// Checked here, before any pooled connection can be reused for it.
	if err := s.checkTailnet(req.Namespace, httpReq.URL.Hostname()); err != nil {
		return WebhookResult{Error: fmt.Errorf("send: %w", err), Duration: time.Since(start)}
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
