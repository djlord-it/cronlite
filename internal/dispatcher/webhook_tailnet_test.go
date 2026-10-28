package dispatcher

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/webhookurl"
)

type mapResolver map[string][]net.IPAddr

func (m mapResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if a, ok := m[host]; ok {
		return a, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// testTailnet answers MagicDNS from a map and connects every checked tailnet
// address to one loopback server standing in for the tailnet service.
type testTailnet struct {
	dns    mapResolver
	target string // loopback addr of the stand-in service
	dials  atomic.Int32
}

func (t *testTailnet) Suffix() string { return "example-tailnet.ts.net" }
func (t *testTailnet) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return t.dns.LookupIPAddr(ctx, host)
}
func (t *testTailnet) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if _, err := netip.ParseAddr(host); err != nil {
		return nil, errors.New("tailnet dial handed a hostname: " + addr)
	}
	t.dials.Add(1)
	return (&net.Dialer{}).DialContext(ctx, network, t.target)
}
func (t *testTailnet) SelfAddrs() []netip.Addr {
	return []netip.Addr{netip.MustParseAddr("100.64.0.1")}
}

// redirectServer redirects /to?u=<url> to u and answers 200 otherwise.
func redirectServer(hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if u := r.URL.Query().Get("u"); u != "" {
			http.Redirect(w, r, u, http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
}

func TestSenderTailnetRouting(t *testing.T) {
	var publicHits, tailnetHits atomic.Int32
	publicSrv := redirectServer(&publicHits)
	defer publicSrv.Close()
	tailnetSrv := redirectServer(&tailnetHits)
	defer tailnetSrv.Close()

	// The public path is the real SSRF guard with a fake resolver, except that
	// hooks.example.com (a "public" host) is connected to publicSrv.
	guarded := webhookurl.GuardedDialContext(webhookurl.Allowlist{}, mapResolver{
		"svc.example-tailnet.ts.net": {{IP: net.ParseIP("100.101.102.103")}},
		"evil-ts.net":                {{IP: net.ParseIP("100.101.102.103")}},
	}, &net.Dialer{Timeout: time.Second})
	public := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, _ := net.SplitHostPort(addr); host == "hooks.example.com" {
			return (&net.Dialer{}).DialContext(ctx, network, publicSrv.Listener.Addr().String())
		}
		return guarded(ctx, network, addr)
	}

	newTailnet := func() *testTailnet {
		return &testTailnet{
			target: tailnetSrv.Listener.Addr().String(),
			dns: mapResolver{
				"svc.example-tailnet.ts.net":     {{IP: net.ParseIP("100.101.102.103")}},
				"private.example-tailnet.ts.net": {{IP: net.ParseIP("10.0.0.1")}},
				"self.example-tailnet.ts.net":    {{IP: net.ParseIP("100.64.0.1")}},
			},
		}
	}
	via := func(base, next string) string { return base + "/to?u=" + url.QueryEscape(next) }

	const (
		pub = "http://hooks.example.com"
		svc = "http://svc.example-tailnet.ts.net"
	)
	cases := []struct {
		name        string
		url         string
		noTailnet   bool
		wantBlocked bool
		wantStatus  int
		wantPublic  int32 // requests served by publicSrv
		wantTailnet int32 // requests served by tailnetSrv
	}{
		{name: "public URL unchanged", url: pub + "/", wantStatus: 200, wantPublic: 1},
		{name: "public URL unchanged without tailnet", url: pub + "/", noTailnet: true, wantStatus: 200, wantPublic: 1},
		{name: "tailnet host delivered over tailnet", url: svc + "/", wantStatus: 200, wantTailnet: 1},
		{name: "tailnet host blocked without the flag", url: svc + "/", noTailnet: true, wantBlocked: true},
		{name: "tailnet host resolving to RFC 1918", url: "http://private.example-tailnet.ts.net/", wantBlocked: true},
		{name: "tailnet host resolving to self", url: "http://self.example-tailnet.ts.net/", wantBlocked: true},
		{name: "100.x literal blocked with tailnet on", url: "http://100.101.102.103/", wantBlocked: true},
		{name: "look-alike host blocked", url: "http://evil-ts.net/", wantBlocked: true},

		{name: "redirect public to tailnet re-routed", url: via(pub, svc+"/"), wantStatus: 200, wantPublic: 1, wantTailnet: 1},
		{name: "redirect public to tailnet blocked without flag", url: via(pub, svc+"/"), noTailnet: true, wantBlocked: true, wantPublic: 1},
		{name: "redirect tailnet to public uses public guard", url: via(svc, pub+"/"), wantStatus: 200, wantPublic: 1, wantTailnet: 1},
		{name: "redirect tailnet to RFC 1918 literal", url: via(svc, "http://10.0.0.1/"), wantBlocked: true, wantTailnet: 1},
		{name: "redirect tailnet to loopback literal", url: via(svc, "http://127.0.0.1/"), wantBlocked: true, wantTailnet: 1},
		{name: "redirect tailnet to 100.x literal", url: via(svc, "http://100.101.102.103/"), wantBlocked: true, wantTailnet: 1},
		{name: "redirect tailnet to self", url: via(svc, "http://self.example-tailnet.ts.net/"), wantBlocked: true, wantTailnet: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			publicHits.Store(0)
			tailnetHits.Store(0)
			opts := []SenderOption{WithDialContext(public)}
			if !tc.noTailnet {
				// Allow every tailnet host so this test exercises routing and
				// the dial guards, not the namespace policy.
				opts = append(opts, WithTailnet(newTailnet()), WithTailnetPolicy(mustPolicy(t, "billing=*")))
			}
			res := NewHTTPWebhookSender(opts...).Send(context.Background(), nsRequest("billing", tc.url))

			if tc.wantBlocked {
				if !errors.Is(res.Error, webhookurl.ErrDestinationBlocked) {
					t.Fatalf("err = %v, want ErrDestinationBlocked", res.Error)
				}
				if res.IsRetryable() {
					t.Error("blocked destination must not be retryable")
				}
			} else {
				if res.Error != nil {
					t.Fatalf("unexpected error: %v", res.Error)
				}
				if res.StatusCode != tc.wantStatus {
					t.Errorf("status = %d, want %d", res.StatusCode, tc.wantStatus)
				}
			}
			if got := publicHits.Load(); got != tc.wantPublic {
				t.Errorf("public server hits = %d, want %d", got, tc.wantPublic)
			}
			if got := tailnetHits.Load(); got != tc.wantTailnet {
				t.Errorf("tailnet server hits = %d, want %d", got, tc.wantTailnet)
			}
		})
	}
}

// policySetup starts a stand-in tailnet service that answers for svc and other
// under testTailnet's suffix, and returns its hit counter and the tailnet.
func policySetup(t *testing.T, h http.Handler) (*testTailnet, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	tn := &testTailnet{
		target: srv.Listener.Addr().String(),
		dns: mapResolver{
			"svc.example-tailnet.ts.net":   {{IP: net.ParseIP("100.101.102.103")}},
			"other.example-tailnet.ts.net": {{IP: net.ParseIP("100.101.102.104")}},
		},
	}
	return tn, srv
}

func mustPolicy(t *testing.T, raw string) *webhookurl.TailnetPolicy {
	t.Helper()
	p, err := webhookurl.ParseTailnetPolicy(raw)
	if err != nil {
		t.Fatalf("ParseTailnetPolicy(%q): %v", raw, err)
	}
	return p
}

func nsRequest(namespace, url string) WebhookRequest {
	req := guardTestRequest(url)
	req.Namespace = namespace
	return req
}

func wantPolicyBlocked(t *testing.T, res WebhookResult) {
	t.Helper()
	if !errors.Is(res.Error, webhookurl.ErrDestinationBlocked) {
		t.Fatalf("err = %v, want ErrDestinationBlocked", res.Error)
	}
	if !strings.Contains(res.Error.Error(), webhookurl.ReasonTailnetNotAllowed) {
		t.Errorf("err = %v, want reason %q", res.Error, webhookurl.ReasonTailnetNotAllowed)
	}
	if res.IsRetryable() {
		t.Error("policy refusal must not be retryable")
	}
}

func TestSenderTailnetPolicy(t *testing.T) {
	const svc = "http://svc.example-tailnet.ts.net/"
	cases := []struct {
		name        string
		namespace   string
		wantBlocked bool
	}{
		{name: "allowed namespace", namespace: "billing"},
		{name: "other namespace", namespace: "ops", wantBlocked: true},
		{name: "empty namespace", namespace: "", wantBlocked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			tn, _ := policySetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			s := NewHTTPWebhookSender(WithTailnet(tn), WithTailnetPolicy(mustPolicy(t, "billing=svc")))
			res := s.Send(context.Background(), nsRequest(tc.namespace, svc))
			if tc.wantBlocked {
				wantPolicyBlocked(t, res)
				if got := hits.Load(); got != 0 {
					t.Errorf("server hits = %d, want 0", got)
				}
				return
			}
			if res.Error != nil || res.StatusCode != http.StatusOK {
				t.Fatalf("res = %+v, want 200", res)
			}
			if got := hits.Load(); got != 1 {
				t.Errorf("server hits = %d, want 1", got)
			}
		})
	}
}

func TestSenderTailnetPolicyRedirect(t *testing.T) {
	var svcHits, otherHits atomic.Int32
	tn, _ := policySetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Host, "other.") {
			otherHits.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		svcHits.Add(1)
		http.Redirect(w, r, "http://other.example-tailnet.ts.net/", http.StatusTemporaryRedirect)
	}))
	s := NewHTTPWebhookSender(WithTailnet(tn), WithTailnetPolicy(mustPolicy(t, "billing=svc")))
	res := s.Send(context.Background(), nsRequest("billing", "http://svc.example-tailnet.ts.net/"))
	wantPolicyBlocked(t, res)
	if got := svcHits.Load(); got != 1 {
		t.Errorf("svc hits = %d, want 1", got)
	}
	if got := otherHits.Load(); got != 0 {
		t.Errorf("other hits = %d, want 0", got)
	}
}

func TestSenderTailnetPolicyPooledConnection(t *testing.T) {
	var hits atomic.Int32
	tn, _ := policySetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	s := NewHTTPWebhookSender(WithTailnet(tn), WithTailnetPolicy(mustPolicy(t, "billing=svc;ops=other")))
	const svc = "http://svc.example-tailnet.ts.net/"

	res := s.Send(context.Background(), nsRequest("billing", svc))
	if res.Error != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("billing res = %+v, want 200", res)
	}
	// ops may reach other but not svc; billing's idle connection to svc must
	// not carry its request.
	wantPolicyBlocked(t, s.Send(context.Background(), nsRequest("ops", svc)))
	if got := hits.Load(); got != 1 {
		t.Errorf("server hits = %d, want 1", got)
	}
	// A third billing send reuses the pooled connection, proving one was kept.
	if res := s.Send(context.Background(), nsRequest("billing", svc)); res.Error != nil {
		t.Fatalf("second billing send: %v", res.Error)
	}
	if got := tn.dials.Load(); got != 1 {
		t.Errorf("tailnet dials = %d, want 1 (connection pooled)", got)
	}
}

func TestSenderTailnetPolicyNilDenies(t *testing.T) {
	var hits atomic.Int32
	tn, _ := policySetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	s := NewHTTPWebhookSender(WithTailnet(tn))
	wantPolicyBlocked(t, s.Send(context.Background(), nsRequest("billing", "http://svc.example-tailnet.ts.net/")))
	if got := hits.Load(); got != 0 {
		t.Errorf("server hits = %d, want 0", got)
	}
}

func TestSenderTailnetPolicyPublicUnaffected(t *testing.T) {
	var publicHits atomic.Int32
	publicSrv := redirectServer(&publicHits)
	defer publicSrv.Close()
	public := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, publicSrv.Listener.Addr().String())
	}
	tn, _ := policySetup(t, http.NotFoundHandler())
	s := NewHTTPWebhookSender(WithDialContext(public), WithTailnet(tn),
		WithTailnetPolicy(mustPolicy(t, "billing=svc")))
	res := s.Send(context.Background(), nsRequest("ops", "http://hooks.example.com/"))
	if res.Error != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("res = %+v, want 200", res)
	}
	if got := publicHits.Load(); got != 1 {
		t.Errorf("public hits = %d, want 1", got)
	}
}

func TestSenderRedirectLimit(t *testing.T) {
	// /0 → /1 → … → /11 is 11 redirects on the same allowed host.
	tn, _ := policySetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if n >= 11 {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/"+strconv.Itoa(n+1), http.StatusTemporaryRedirect)
	}))
	s := NewHTTPWebhookSender(WithTailnet(tn), WithTailnetPolicy(mustPolicy(t, "billing=svc")))
	res := s.Send(context.Background(), nsRequest("billing", "http://svc.example-tailnet.ts.net/0"))
	if res.Error == nil || !strings.Contains(res.Error.Error(), "stopped after 10 redirects") {
		t.Fatalf("err = %v, want stopped after 10 redirects", res.Error)
	}
}

// The transport maps non-ASCII hosts through IDNA before dialing, so these
// URLs reach svc.example-tailnet.ts.net. The policy must judge that host.
func TestSenderTailnetPolicyIDNA(t *testing.T) {
	for _, u := range []string{
		"http://svc.example-tailnet.ｔｓ.net/",
		"http://svc。example-tailnet。ts。net/",
		"http://svc.example-tailnet%EF%BC%8Ets.net/",
	} {
		t.Run(u, func(t *testing.T) {
			var hits atomic.Int32
			tn, _ := policySetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			s := NewHTTPWebhookSender(WithTailnet(tn), WithTailnetPolicy(mustPolicy(t, "billing=svc")))
			wantPolicyBlocked(t, s.Send(context.Background(), nsRequest("ops", u)))
			if got := hits.Load(); got != 0 {
				t.Errorf("server hits = %d, want 0", got)
			}
		})
	}
}

func TestSenderTailnetPolicyCaseAndTrailingDot(t *testing.T) {
	const u = "http://SVC.Example-Tailnet.ts.net./"
	for _, tc := range []struct {
		namespace   string
		wantBlocked bool
	}{
		{namespace: "billing"},
		{namespace: "ops", wantBlocked: true},
	} {
		t.Run(tc.namespace, func(t *testing.T) {
			var hits atomic.Int32
			tn, _ := policySetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			// MagicDNS is case-insensitive; the stand-in resolver is exact.
			tn.dns["SVC.Example-Tailnet.ts.net."] = tn.dns["svc.example-tailnet.ts.net"]
			s := NewHTTPWebhookSender(WithTailnet(tn), WithTailnetPolicy(mustPolicy(t, "billing=svc")))
			res := s.Send(context.Background(), nsRequest(tc.namespace, u))
			if tc.wantBlocked {
				wantPolicyBlocked(t, res)
				if got := hits.Load(); got != 0 {
					t.Errorf("server hits = %d, want 0", got)
				}
				return
			}
			if res.Error != nil || res.StatusCode != http.StatusOK {
				t.Fatalf("res = %+v, want 200", res)
			}
			if got := hits.Load(); got != 1 {
				t.Errorf("server hits = %d, want 1", got)
			}
		})
	}
}

func TestSenderTailnetPolicyRedirectIDNA(t *testing.T) {
	var svcHits, otherHits atomic.Int32
	tn, _ := policySetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Host, "other.") {
			otherHits.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		svcHits.Add(1)
		w.Header().Set("Location", "http://other.example-tailnet.ｔｓ.net/")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	s := NewHTTPWebhookSender(WithTailnet(tn), WithTailnetPolicy(mustPolicy(t, "billing=svc")))
	res := s.Send(context.Background(), nsRequest("billing", "http://svc.example-tailnet.ts.net/"))
	wantPolicyBlocked(t, res)
	if got := svcHits.Load(); got != 1 {
		t.Errorf("svc hits = %d, want 1", got)
	}
	if got := otherHits.Load(); got != 0 {
		t.Errorf("other hits = %d, want 0", got)
	}
}
