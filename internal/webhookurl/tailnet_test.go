package webhookurl

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
)

// fakeTailnet is a Tailnet backed by a fake MagicDNS resolver and a recording
// dialer.
type fakeTailnet struct {
	suffix string
	dns    *fakeResolver
	dialer *recordingDialer
	self   []netip.Addr
}

func newFakeTailnet(answers map[string][]net.IPAddr) *fakeTailnet {
	return &fakeTailnet{
		suffix: "example-tailnet.ts.net",
		dns:    &fakeResolver{addrs: answers},
		dialer: &recordingDialer{},
		self:   []netip.Addr{netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("fd7a:115c:a1e0::1")},
	}
}

func (f *fakeTailnet) Suffix() string { return f.suffix }
func (f *fakeTailnet) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f.dns.LookupIPAddr(ctx, host)
}
func (f *fakeTailnet) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return f.dialer.dial(ctx, network, addr)
}
func (f *fakeTailnet) SelfAddrs() []netip.Addr { return f.self }

func TestCheckTailnetIP(t *testing.T) {
	self := []netip.Addr{netip.MustParseAddr("100.64.0.1")}
	cases := []struct {
		ip   string
		zone string
		ok   bool
	}{
		{ip: "100.64.0.2", ok: true},
		{ip: "100.127.255.254", ok: true},
		{ip: "fd7a:115c:a1e0::2", ok: true},
		{ip: "::ffff:100.101.102.103", ok: true}, // mapped form checked as IPv4

		{ip: "100.64.0.1"},                // self
		{ip: "100.100.100.100"},           // Quad100
		{ip: "fd7a:115c:a1e0::53"},        // Quad100 IPv6
		{ip: "100.115.92.1"},              // ChromeOS VM range
		{ip: "fd7a:115c:a1e0:b1a::a00:1"}, // 4via6 to a subnet router LAN
		{ip: "fd7a:115c:a1e0::2", zone: "en0"},
		{ip: "100.63.255.255"}, // just below CGNAT
		{ip: "100.128.0.0"},    // just above CGNAT
		{ip: "10.0.0.1"},
		{ip: "127.0.0.1"},
		{ip: "169.254.169.254"},
		{ip: "8.8.8.8"},
		{ip: "fd12::1"}, // ULA outside the Tailscale prefix
		{ip: "::1"},
	}
	for _, tc := range cases {
		addr := netip.MustParseAddr(tc.ip)
		if tc.zone != "" {
			addr = addr.WithZone(tc.zone)
		}
		reason, ok := checkTailnetAddr(addr, self)
		if ok != tc.ok {
			t.Errorf("%s%%%s: ok = %v (%q), want %v", tc.ip, tc.zone, ok, reason, tc.ok)
		}
		if !ok && reason == "" {
			t.Errorf("%s: blocked without a reason", tc.ip)
		}
	}
	if _, ok := CheckTailnetIP(nil, nil); ok {
		t.Error("nil IP must be blocked")
	}
	if _, ok := CheckTailnetIP(net.ParseIP("100.64.0.2"), nil); !ok {
		t.Error("CheckTailnetIP(100.64.0.2) blocked")
	}
}

func TestTailnetHost(t *testing.T) {
	const suffix = "example-tailnet.ts.net"
	match := []string{
		"svc.example-tailnet.ts.net",
		"SVC.Example-Tailnet.TS.NET",
		"svc.example-tailnet.ts.net.",
		"a.b.example-tailnet.ts.net",
	}
	reject := []string{
		"example-tailnet.ts.net", // the bare domain is not a node
		"evil-ts.net",
		"ts.net.evil.com",
		"svc.example-tailnet.ts.net.evil.com",
		"svcexample-tailnet.ts.net",
		"svc.other-tailnet.ts.net", // another tailnet's (Funnel) host stays public
		"svc.evil-example-tailnet.ts.net",
		"ts.net",
		"100.64.0.2",
		"fd7a:115c:a1e0::2",
		"",
	}
	for _, h := range match {
		if !tailnetHost(h, suffix) {
			t.Errorf("tailnetHost(%q) = false, want true", h)
		}
	}
	for _, h := range reject {
		if tailnetHost(h, suffix) {
			t.Errorf("tailnetHost(%q) = true, want false", h)
		}
	}
	if tailnetHost("svc.example-tailnet.ts.net", "") {
		t.Error("empty suffix must match nothing")
	}
}

func TestRoutedDial(t *testing.T) {
	answers := map[string][]net.IPAddr{
		"svc.example-tailnet.ts.net":     ipAddrs("100.101.102.103"),
		"v6.example-tailnet.ts.net":      ipAddrs("fd7a:115c:a1e0::2"),
		"private.example-tailnet.ts.net": ipAddrs("10.0.0.1"),
		"public.example-tailnet.ts.net":  ipAddrs("8.8.8.8"),
		"loop.example-tailnet.ts.net":    ipAddrs("127.0.0.1"),
		"quad.example-tailnet.ts.net":    ipAddrs("100.100.100.100"),
		"self.example-tailnet.ts.net":    ipAddrs("100.64.0.1"),
		"mixed.example-tailnet.ts.net":   ipAddrs("100.101.102.103", "10.0.0.1"),
	}
	publicDNS := map[string][]net.IPAddr{
		"hooks.example.com":          ipAddrs("8.8.8.8"),
		"evil-ts.net":                ipAddrs("100.101.102.103"),
		"ts.net.evil.com":            ipAddrs("100.101.102.103"),
		"svc.other-tailnet.ts.net":   ipAddrs("8.8.4.4"),
		"svc.example-tailnet.ts.net": ipAddrs("100.101.102.103"), // public DNS can't see MagicDNS
	}

	cases := []struct {
		name         string
		addr         string
		wantBlocked  bool
		wantTailnet  []string // dials made over the tailnet
		wantPublic   []string // dials made by the public dialer
		wantMagicDNS bool     // MagicDNS was consulted
	}{
		{name: "public URL unchanged", addr: "hooks.example.com:443", wantPublic: []string{"8.8.8.8:443"}},
		{name: "tailnet host dials checked IP", addr: "svc.example-tailnet.ts.net:443", wantTailnet: []string{"100.101.102.103:443"}, wantMagicDNS: true},
		{name: "tailnet host over IPv6", addr: "v6.example-tailnet.ts.net:8080", wantTailnet: []string{"[fd7a:115c:a1e0::2]:8080"}, wantMagicDNS: true},
		{name: "tailnet host resolving to RFC 1918", addr: "private.example-tailnet.ts.net:443", wantBlocked: true, wantMagicDNS: true},
		{name: "tailnet host resolving to public IP", addr: "public.example-tailnet.ts.net:443", wantBlocked: true, wantMagicDNS: true},
		{name: "tailnet host resolving to loopback", addr: "loop.example-tailnet.ts.net:443", wantBlocked: true, wantMagicDNS: true},
		{name: "tailnet host resolving to Quad100", addr: "quad.example-tailnet.ts.net:53", wantBlocked: true, wantMagicDNS: true},
		{name: "tailnet host resolving to self", addr: "self.example-tailnet.ts.net:443", wantBlocked: true, wantMagicDNS: true},
		{name: "one bad answer blocks all", addr: "mixed.example-tailnet.ts.net:443", wantBlocked: true, wantMagicDNS: true},
		{name: "100.x literal still blocked", addr: "100.101.102.103:443", wantBlocked: true},
		{name: "tailnet IPv6 literal still blocked", addr: "[fd7a:115c:a1e0::2]:443", wantBlocked: true},
		{name: "look-alike evil-ts.net is public", addr: "evil-ts.net:443", wantBlocked: true},
		{name: "look-alike ts.net.evil.com is public", addr: "ts.net.evil.com:443", wantBlocked: true},
		{name: "other tailnet's host stays public", addr: "svc.other-tailnet.ts.net:443", wantPublic: []string{"8.8.4.4:443"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tn := newFakeTailnet(answers)
			pub := &recordingDialer{}
			dial := RoutedDialContext(guardedDial(Allowlist{}, &fakeResolver{addrs: publicDNS}, pub.dial), tn)

			conn, err := dial(context.Background(), "tcp", tc.addr)
			if conn != nil {
				conn.Close()
			}
			if tc.wantBlocked {
				if !errors.Is(err, ErrDestinationBlocked) {
					t.Fatalf("err = %v, want ErrDestinationBlocked", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got, want := strings.Join(tn.dialer.addrs, ","), strings.Join(tc.wantTailnet, ","); got != want {
				t.Errorf("tailnet dials = %q, want %q", got, want)
			}
			if got, want := strings.Join(pub.addrs, ","), strings.Join(tc.wantPublic, ","); got != want {
				t.Errorf("public dials = %q, want %q", got, want)
			}
			if consulted := len(tn.dns.calls) > 0; consulted != tc.wantMagicDNS {
				t.Errorf("MagicDNS consulted = %v (%v), want %v", consulted, tn.dns.calls, tc.wantMagicDNS)
			}
			if tc.wantMagicDNS && len(tn.dns.calls) != 1 {
				t.Errorf("MagicDNS lookups = %v, want exactly one", tn.dns.calls)
			}
		})
	}
}

func TestRoutedDialWithoutTailnetIsPublic(t *testing.T) {
	// The flag off means no Tailnet: .ts.net names go through the public guard,
	// which blocks the CGNAT answer exactly as before.
	pub := &recordingDialer{}
	public := guardedDial(Allowlist{}, &fakeResolver{addrs: map[string][]net.IPAddr{
		"svc.example-tailnet.ts.net": ipAddrs("100.101.102.103"),
	}}, pub.dial)

	for name, tn := range map[string]Tailnet{
		"nil":          nil,
		"empty suffix": &fakeTailnet{dns: &fakeResolver{}, dialer: &recordingDialer{}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := RoutedDialContext(public, tn)(context.Background(), "tcp", "svc.example-tailnet.ts.net:443")
			var be *BlockedError
			if !errors.As(err, &be) || !strings.Contains(be.Reason, "100.64.0.0/10") {
				t.Fatalf("err = %v, want CGNAT block from the public guard", err)
			}
			if len(pub.addrs) != 0 {
				t.Errorf("dialled %v", pub.addrs)
			}
		})
	}
}

func TestRoutedDialLookupErrorsStayPlain(t *testing.T) {
	notFound := &net.DNSError{Err: "no such host", Name: "gone.example-tailnet.ts.net", IsNotFound: true}
	tn := newFakeTailnet(nil)
	tn.dns.err = notFound
	_, err := RoutedDialContext(nil, tn)(context.Background(), "tcp", "gone.example-tailnet.ts.net:443")
	if !errors.Is(err, notFound) || errors.Is(err, ErrDestinationBlocked) {
		t.Fatalf("err = %v, want the lookup error", err)
	}

	// An empty MagicDNS answer is a not-found, not a block.
	tn = newFakeTailnet(map[string][]net.IPAddr{})
	_, err = RoutedDialContext(nil, tn)(context.Background(), "tcp", "empty.example-tailnet.ts.net:443")
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
		t.Fatalf("err = %v, want DNS not-found", err)
	}
	if len(tn.dialer.addrs) != 0 {
		t.Errorf("dialled %v", tn.dialer.addrs)
	}
}

func TestRoutedDialFallsThroughToNextTailnetIP(t *testing.T) {
	tn := newFakeTailnet(map[string][]net.IPAddr{
		"svc.example-tailnet.ts.net": ipAddrs("100.101.102.103", "fd7a:115c:a1e0::2"),
	})
	refused := errors.New("connection refused")
	tn.dialer.failFor = map[string]error{"100.101.102.103:443": refused}
	conn, err := RoutedDialContext(nil, tn)(context.Background(), "tcp", "svc.example-tailnet.ts.net:443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	conn.Close()
	if got, want := strings.Join(tn.dialer.addrs, ","), "100.101.102.103:443,[fd7a:115c:a1e0::2]:443"; got != want {
		t.Errorf("dialled %s, want %s", got, want)
	}
}
