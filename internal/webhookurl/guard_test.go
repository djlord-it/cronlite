package webhookurl

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
)

func TestCheckIP(t *testing.T) {
	alwaysBlocked := []string{
		"0.0.0.0", "0.1.2.3", "127.0.0.1", "::1", "169.254.169.254", "fe80::1",
		"224.0.0.1", "ff02::1", "100.64.0.1", "100.127.255.254",
		"::ffff:127.0.0.1", "::ffff:169.254.169.254",
		// Translated / embedded-IPv4 forms of always-blocked addresses.
		"64:ff9b::7f00:1",    // NAT64 well-known prefix -> 127.0.0.1
		"64:ff9b::a9fe:a9fe", // NAT64 -> 169.254.169.254
		"::ffff:0:7f00:1",    // SIIT IPv4-translated -> 127.0.0.1
		"64:ff9b:1::a00:1",   // NAT64 local-use prefix (embedding not fixed)
		"::7f00:1",           // deprecated IPv4-compatible form
		"::",                 // IPv6 unspecified
		"ff05::1",            // site-local multicast
	}
	privateOnly := []string{
		"10.0.0.1", "172.16.0.1", "192.168.1.1", "fd12::1", "::ffff:10.0.0.1",
		"64:ff9b::a00:1", // NAT64 -> 10.0.0.1
	}
	allowed := []string{
		"8.8.8.8", "2606:4700::1111", "100.128.0.1",
		"::ffff:8.8.8.8",   // mapped public
		"64:ff9b::808:808", // NAT64 -> 8.8.8.8
	}

	for _, s := range alwaysBlocked {
		for _, allowPrivate := range []bool{false, true} {
			reason, ok := CheckIP(net.ParseIP(s), allowPrivate)
			if ok || reason == "" {
				t.Errorf("CheckIP(%s, allowPrivate=%v) = (%q, %v), want blocked with reason", s, allowPrivate, reason, ok)
			}
		}
	}
	for _, s := range privateOnly {
		if reason, ok := CheckIP(net.ParseIP(s), false); ok || reason == "" {
			t.Errorf("CheckIP(%s, false) = (%q, %v), want blocked", s, reason, ok)
		}
		if reason, ok := CheckIP(net.ParseIP(s), true); !ok {
			t.Errorf("CheckIP(%s, true) = (%q, %v), want allowed", s, reason, ok)
		}
	}
	for _, s := range allowed {
		for _, allowPrivate := range []bool{false, true} {
			if reason, ok := CheckIP(net.ParseIP(s), allowPrivate); !ok {
				t.Errorf("CheckIP(%s, allowPrivate=%v) = (%q, %v), want allowed", s, allowPrivate, reason, ok)
			}
		}
	}

	// Invalid input must fail closed.
	for _, ip := range []net.IP{nil, {}, {1, 2, 3}} {
		if _, ok := CheckIP(ip, true); ok {
			t.Errorf("CheckIP(%v, true) allowed an invalid IP", ip)
		}
	}
	// The 4-byte and 16-byte forms of an IPv4 address behave the same.
	if _, ok := CheckIP(net.IPv4(10, 0, 0, 1).To4(), false); ok {
		t.Error("4-byte 10.0.0.1 allowed without allowPrivate")
	}
}

func TestAllowlistMatches(t *testing.T) {
	a, err := ParseAllowlist([]string{".railway.internal", "webhook-receiver"})
	if err != nil {
		t.Fatalf("ParseAllowlist: %v", err)
	}
	for _, h := range []string{
		"api.railway.internal", "railway.internal", "API.Railway.Internal.",
		"webhook-receiver", "WEBHOOK-RECEIVER.",
	} {
		if !a.Matches(h) {
			t.Errorf("Matches(%q) = false, want true", h)
		}
	}
	for _, h := range []string{
		"evilrailway.internal", "railway.internal.evil.com",
		"x.webhook-receiver", "fd12::1",
		"", ".", "[fd12::1]", "fd12::1%eth0", "10.0.0.1",
	} {
		if a.Matches(h) {
			t.Errorf("Matches(%q) = true, want false", h)
		}
	}

	// Entries are normalised: case and trailing dot.
	b, err := ParseAllowlist([]string{"Host.Docker.Internal.", " .Corp.Example "})
	if err != nil {
		t.Fatalf("ParseAllowlist: %v", err)
	}
	for _, h := range []string{"host.docker.internal", "a.corp.example", "corp.example"} {
		if !b.Matches(h) {
			t.Errorf("normalised Matches(%q) = false, want true", h)
		}
	}
	if b.Matches("docker.internal") {
		t.Error("exact entry matched a parent domain")
	}

	// Zero value is an empty allowlist.
	var zero Allowlist
	for _, h := range []string{"railway.internal", "webhook-receiver", ""} {
		if zero.Matches(h) {
			t.Errorf("zero Allowlist matched %q", h)
		}
	}
	empty, err := ParseAllowlist(nil)
	if err != nil || empty.Matches("anything") {
		t.Errorf("ParseAllowlist(nil) = matches/err %v, want empty", err)
	}
}

func TestParseAllowlistRejects(t *testing.T) {
	for _, entry := range []string{
		"", ".", "10.0.0.1", "fd12::1",
		"  ", "..", ".10.0.0.1", "[fd12::1]", "fe80::1%en0",
		"*.railway.internal", "a..b", ".railway..internal", "host/path", "host:8080",
	} {
		_, err := ParseAllowlist([]string{"ok.example", entry})
		if err == nil {
			t.Errorf("ParseAllowlist(%q) succeeded, want error", entry)
			continue
		}
		if !strings.Contains(err.Error(), "\""+entry+"\"") {
			t.Errorf("ParseAllowlist(%q) error %q does not name the entry", entry, err)
		}
	}
}

type fakeResolver struct {
	addrs map[string][]net.IPAddr
	err   error
	calls []string
}

func (f *fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	f.calls = append(f.calls, host)
	if f.err != nil {
		return nil, f.err
	}
	return f.addrs[host], nil
}

func ipAddrs(ss ...string) []net.IPAddr {
	out := make([]net.IPAddr, 0, len(ss))
	for _, s := range ss {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out
}

type recordingDialer struct {
	mu      sync.Mutex
	addrs   []string
	failFor map[string]error
}

func (d *recordingDialer) dial(_ context.Context, _, addr string) (net.Conn, error) {
	d.mu.Lock()
	d.addrs = append(d.addrs, addr)
	d.mu.Unlock()
	if err := d.failFor[addr]; err != nil {
		return nil, err
	}
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, nil
}

func TestGuardedDial(t *testing.T) {
	allow, err := ParseAllowlist([]string{".railway.internal"})
	if err != nil {
		t.Fatal(err)
	}
	notFound := &net.DNSError{Err: "no such host", Name: "missing.example", IsNotFound: true}

	cases := []struct {
		name        string
		addr        string
		resolved    map[string][]net.IPAddr
		resolverErr error
		wantBlocked bool
		wantErr     error // checked with errors.Is when non-nil
		wantDials   []string
	}{
		{
			name:        "public name resolving to private IP",
			addr:        "hooks.example.com:443",
			resolved:    map[string][]net.IPAddr{"hooks.example.com": ipAddrs("10.0.0.1")},
			wantBlocked: true,
		},
		{
			name:      "allowlisted name resolving to ULA",
			addr:      "api.railway.internal:8080",
			resolved:  map[string][]net.IPAddr{"api.railway.internal": ipAddrs("fd12::1")},
			wantDials: []string{"[fd12::1]:8080"},
		},
		{
			name:        "allowlisted name resolving to metadata IP",
			addr:        "api.railway.internal:80",
			resolved:    map[string][]net.IPAddr{"api.railway.internal": ipAddrs("169.254.169.254")},
			wantBlocked: true,
		},
		{
			name:        "mixed public and private answers",
			addr:        "mixed.example.com:443",
			resolved:    map[string][]net.IPAddr{"mixed.example.com": ipAddrs("8.8.8.8", "10.0.0.1")},
			wantBlocked: true,
		},
		{
			name:        "resolver not found stays a plain lookup error",
			addr:        "missing.example:443",
			resolverErr: notFound,
			wantErr:     notFound,
		},
		{
			name:        "literal ULA is never allowlisted",
			addr:        "[fd12::1]:8080",
			wantBlocked: true,
		},
		{
			name:        "literal zoned link-local is blocked",
			addr:        "[fe80::1%en0]:8080",
			wantBlocked: true,
		},
		{
			name:      "literal public IP is dialled directly",
			addr:      "8.8.8.8:443",
			wantDials: []string{"8.8.8.8:443"},
		},
		{
			name:      "public name dials the resolved IP, not the hostname",
			addr:      "hooks.example.com:443",
			resolved:  map[string][]net.IPAddr{"hooks.example.com": ipAddrs("8.8.8.8")},
			wantDials: []string{"8.8.8.8:443"},
		},
		{
			name:      "mapped answer is dialled in its checked IPv4 form",
			addr:      "hooks.example.com:443",
			resolved:  map[string][]net.IPAddr{"hooks.example.com": ipAddrs("::ffff:8.8.8.8")},
			wantDials: []string{"8.8.8.8:443"},
		},
		{
			name: "zoned answer is blocked",
			addr: "hooks.example.com:443",
			resolved: map[string][]net.IPAddr{"hooks.example.com": {
				{IP: net.ParseIP("2606:4700::1111"), Zone: "en0"},
			}},
			wantBlocked: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeResolver{addrs: tc.resolved, err: tc.resolverErr}
			d := &recordingDialer{}
			conn, err := guardedDial(allow, r, d.dial)(context.Background(), "tcp", tc.addr)
			if conn != nil {
				defer conn.Close()
			}

			if tc.wantBlocked {
				if !errors.Is(err, ErrDestinationBlocked) {
					t.Fatalf("err = %v, want ErrDestinationBlocked", err)
				}
				var be *BlockedError
				if !errors.As(err, &be) || be.Reason == "" || be.IP == nil {
					t.Fatalf("err = %#v, want *BlockedError with IP and reason", err)
				}
				if !strings.Contains(err.Error(), "destination blocked") {
					t.Errorf("Error() = %q, want it to contain %q", err.Error(), "destination blocked")
				}
				if len(d.addrs) != 0 {
					t.Errorf("dialled %v despite block", d.addrs)
				}
				return
			}
			if errors.Is(err, ErrDestinationBlocked) {
				t.Fatalf("unexpected block: %v", err)
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if len(d.addrs) != 0 {
					t.Errorf("dialled %v after lookup error", d.addrs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(d.addrs, ",") != strings.Join(tc.wantDials, ",") {
				t.Errorf("dialled %v, want %v", d.addrs, tc.wantDials)
			}
			host, _, _ := net.SplitHostPort(tc.addr)
			for _, a := range d.addrs {
				if h, _, _ := net.SplitHostPort(a); net.ParseIP(h) == nil {
					t.Errorf("dialled non-IP address %q (original host %q)", a, host)
				}
			}
		})
	}
}

func TestGuardedDialEmptyAnswerIsNotBlocked(t *testing.T) {
	r := &fakeResolver{addrs: map[string][]net.IPAddr{}}
	d := &recordingDialer{}
	_, err := guardedDial(Allowlist{}, r, d.dial)(context.Background(), "tcp", "empty.example:443")
	if err == nil {
		t.Fatal("want error for empty resolver answer")
	}
	if errors.Is(err, ErrDestinationBlocked) {
		t.Fatalf("empty answer reported as blocked: %v", err)
	}
	if len(d.addrs) != 0 {
		t.Errorf("dialled %v with no answers", d.addrs)
	}
}

func TestGuardedDialFallsThroughToNextCheckedIP(t *testing.T) {
	r := &fakeResolver{addrs: map[string][]net.IPAddr{
		"multi.example": ipAddrs("8.8.8.8", "2606:4700::1111"),
	}}
	refused := errors.New("connection refused")
	d := &recordingDialer{failFor: map[string]error{"8.8.8.8:443": refused}}
	conn, err := guardedDial(Allowlist{}, r, d.dial)(context.Background(), "tcp", "multi.example:443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	conn.Close()
	want := "8.8.8.8:443,[2606:4700::1111]:443"
	if got := strings.Join(d.addrs, ","); got != want {
		t.Errorf("dialled %s, want %s", got, want)
	}

	// All dials fail: the dial error comes back, not a block.
	d = &recordingDialer{failFor: map[string]error{
		"8.8.8.8:443": refused, "[2606:4700::1111]:443": refused,
	}}
	_, err = guardedDial(Allowlist{}, r, d.dial)(context.Background(), "tcp", "multi.example:443")
	if !errors.Is(err, refused) || errors.Is(err, ErrDestinationBlocked) {
		t.Fatalf("err = %v, want the dial error", err)
	}
}

func TestGuardedDialBadAddr(t *testing.T) {
	d := &recordingDialer{}
	for _, addr := range []string{"no-port", ":443"} {
		if _, err := guardedDial(Allowlist{}, &fakeResolver{}, d.dial)(context.Background(), "tcp", addr); err == nil {
			t.Errorf("addr %q: want error", addr)
		}
	}
	if len(d.addrs) != 0 {
		t.Errorf("dialled %v for bad addresses", d.addrs)
	}
}

func TestGuardedDialContextNilDefaults(t *testing.T) {
	// Must not panic when constructed with nil resolver/dialer; a blocked
	// literal never reaches the network.
	_, err := GuardedDialContext(Allowlist{}, nil, nil)(context.Background(), "tcp", "127.0.0.1:1")
	if !errors.Is(err, ErrDestinationBlocked) {
		t.Fatalf("err = %v, want ErrDestinationBlocked", err)
	}
}

func TestBlockedErrorIs(t *testing.T) {
	err := error(&BlockedError{Host: "h", IP: net.ParseIP("10.0.0.1"), Reason: "private address"})
	if !errors.Is(err, ErrDestinationBlocked) {
		t.Error("BlockedError is not ErrDestinationBlocked")
	}
	if errors.Is(err, errors.New("destination blocked")) {
		t.Error("BlockedError matched an unrelated error")
	}
}
