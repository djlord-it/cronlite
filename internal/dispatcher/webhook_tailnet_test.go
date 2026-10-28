package dispatcher

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
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
				opts = append(opts, WithTailnet(newTailnet()))
			}
			res := NewHTTPWebhookSender(opts...).Send(context.Background(), guardTestRequest(tc.url))

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
