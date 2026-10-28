package webhookurl

import (
	"context"
	"net"
	"net/netip"
	"strings"
)

// This file implements the tailnet delivery path. It is chosen per dial, by
// hostname, so every redirect hop is routed and checked independently:
//
//   - A hostname under the tailnet's MagicDNS suffix (e.g.
//     "svc.example-tailnet.ts.net") is resolved through MagicDNS, every answer
//     must be a Tailscale node address, and a checked address is dialled over
//     the tailnet.
//   - Everything else, including every IP literal, goes through the public
//     dialer (GuardedDialContext) unchanged, so 100.64.0.0/10 stays blocked
//     there.
//
// The package does not import tsnet; internal/tailnet provides the Tailnet
// implementation. Keeping it behind an interface lets a later version hold one
// Tailnet per namespace with separate credentials.

// Tailnet is a connected Tailscale node that webhook delivery may dial through.
type Tailnet interface {
	// Suffix is the tailnet's MagicDNS domain, e.g. "example-tailnet.ts.net".
	// Only strict subdomains of it are routed to the tailnet. An empty suffix
	// disables tailnet routing.
	Suffix() string
	// Resolver resolves names through MagicDNS.
	Resolver
	// Dial connects to a literal IP:port over the tailnet. It is only ever
	// handed addresses that passed CheckTailnetIP.
	Dial(ctx context.Context, network, addr string) (net.Conn, error)
	// SelfAddrs are this node's own tailnet addresses. They are never dialled,
	// so a webhook cannot reach CronLite's own tailnet listener.
	SelfAddrs() []netip.Addr
}

var (
	// Tailscale node address ranges (tailscale.com/net/tsaddr).
	tailnetIPv4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnetIPv6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")

	// Reserved inside those ranges: never a peer.
	chromeOSVM = netip.MustParsePrefix("100.115.92.0/23")
	quad100    = netip.MustParseAddr("100.100.100.100")
	quad100v6  = netip.MustParseAddr("fd7a:115c:a1e0::53")
	via6       = netip.MustParsePrefix("fd7a:115c:a1e0:b1a::/64") // 4via6: reaches subnet-router LANs, not nodes
)

// CheckTailnetIP reports whether ip may be dialled on the tailnet path: it
// must be a Tailscale node address (not Quad100, the ChromeOS VM range or the
// 4via6 subnet-router range), and not one of self. When it is not, reason says why.
func CheckTailnetIP(ip net.IP, self []netip.Addr) (reason string, ok bool) {
	addr, valid := netip.AddrFromSlice(ip)
	if !valid {
		return "invalid IP address", false
	}
	return checkTailnetAddr(addr, self)
}

func checkTailnetAddr(addr netip.Addr, self []netip.Addr) (string, bool) {
	if !addr.IsValid() {
		return "invalid IP address", false
	}
	if addr.Zone() != "" {
		return "scoped (zoned) IPv6 address", false
	}
	addr = addr.Unmap()
	switch {
	case addr == quad100 || addr == quad100v6:
		return "Tailscale service address", false
	case chromeOSVM.Contains(addr), via6.Contains(addr):
		return "reserved Tailscale address", false
	case !tailnetIPv4.Contains(addr) && !tailnetIPv6.Contains(addr):
		return "not a tailnet address", false
	}
	for _, s := range self {
		if s.Unmap() == addr {
			return "this node's own tailnet address", false
		}
	}
	return "", true
}

// tailnetHost reports whether host is a strict subdomain of suffix. Matching is
// case-insensitive, ignores one trailing dot, and is on label boundaries, so
// "evil-ts.net" and "ts.net.evil.com" never match "ts.net". IP literals never
// match.
func tailnetHost(host, suffix string) bool {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if suffix == "" || h == "" || isIPLiteral(h) {
		return false
	}
	return strings.HasSuffix(h, "."+suffix)
}

// RoutedDialContext returns a DialFunc that sends hostnames under tn's MagicDNS
// suffix over the tailnet and every other address through public. A nil tn,
// or one with an empty suffix, returns public itself.
func RoutedDialContext(public DialFunc, tn Tailnet) DialFunc {
	if tn == nil {
		return public
	}
	suffix := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(tn.Suffix())), ".")
	if suffix == "" {
		return public
	}
	viaTailnet := tailnetDial(tn)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, err := net.SplitHostPort(addr); err == nil && tailnetHost(host, suffix) {
			return viaTailnet(ctx, network, addr)
		}
		return public(ctx, network, addr)
	}
}

// tailnetDial resolves through MagicDNS, checks every answer with
// checkTailnetAddr, and dials a checked IP over the tailnet. As in
// guardedDial, the hostname is never handed to the dialer and a single bad
// answer blocks the whole destination.
func tailnetDial(tn Tailnet) DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}

		// Lookup errors are returned unwrapped so they stay retryable.
		ipAddrs, err := tn.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ipAddrs) == 0 {
			return nil, &net.DNSError{Err: "no addresses found", Name: host, IsNotFound: true}
		}

		self := tn.SelfAddrs()
		checked := make([]netip.Addr, 0, len(ipAddrs))
		for _, ia := range ipAddrs {
			ip, valid := netip.AddrFromSlice(ia.IP)
			if valid && ia.Zone != "" {
				ip = ip.WithZone(ia.Zone)
			}
			reason, ok := "invalid IP address", false
			if valid {
				reason, ok = checkTailnetAddr(ip, self)
			}
			if !ok {
				return nil, &BlockedError{Host: host, IP: ia.IP, Reason: reason}
			}
			checked = append(checked, ip.Unmap())
		}

		var firstErr error
		for _, ip := range checked {
			if err := ctx.Err(); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				break
			}
			conn, err := tn.Dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		return nil, firstErr
	}
}
