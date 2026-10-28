package webhookurl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// This file implements the connect-time SSRF guard for webhook delivery.
//
// The guard runs inside the HTTP transport's DialContext: it resolves the
// hostname itself, checks every resolved address, and then dials one of the
// checked addresses directly, so the dialer never re-resolves the name (no
// DNS-rebinding window). Because the transport dials every redirect hop
// through the same function, redirects are covered too.
//
// Rules (see CheckIP):
//   - Always blocked: unspecified, loopback, link-local unicast, multicast
//     (all scopes), 0.0.0.0/8, 100.64.0.0/10 (CGNAT), any address carrying an
//     IPv6 zone, and the IPv4-mapped / IPv4-translated / NAT64 forms of these.
//   - Blocked unless the hostname is allowlisted: RFC 1918 private ranges and
//     IPv6 unique-local fc00::/7.
//   - Literal IPs in URLs are never allowlisted.

// ErrDestinationBlocked is matched (via errors.Is) by every *BlockedError.
var ErrDestinationBlocked = errors.New("destination blocked")

// BlockedError reports a webhook destination refused by the guard. It is a
// permanent failure: retrying will not change the outcome.
type BlockedError struct {
	Host   string // host as given in the dial address
	IP     net.IP // the address that failed the check
	Reason string // short human-readable reason, e.g. "loopback address"
}

func (e *BlockedError) Error() string {
	if e.IP == nil {
		return fmt.Sprintf("destination blocked: host %q: %s", e.Host, e.Reason)
	}
	return fmt.Sprintf("destination blocked: host %q resolved to %s: %s", e.Host, e.IP, e.Reason)
}

// Is reports whether target is ErrDestinationBlocked.
func (e *BlockedError) Is(target error) bool {
	return target == ErrDestinationBlocked
}

// Resolver resolves a hostname to IP addresses. *net.Resolver satisfies it.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// DialFunc has the signature of net.Dialer.DialContext and
// http.Transport.DialContext.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Allowlist is a set of hostname entries whose resolved addresses may be in
// private ranges. The zero value is an empty allowlist that matches nothing.
type Allowlist struct {
	suffixes []string // from ".example" entries, stored without the leading dot
	exact    []string // from "example" entries
}

// ParseAllowlist parses allowlist entries. Each entry is trimmed of
// surrounding whitespace, lowercased, and stripped of one trailing dot.
//
// An entry starting with "." (e.g. ".railway.internal") matches that domain
// and any subdomain on dot boundaries. Any other entry (e.g.
// "webhook-receiver") matches that exact hostname only.
//
// Empty entries, ".", IP literals, wildcards and entries with characters that
// cannot appear in a hostname are rejected with an error naming the entry.
func ParseAllowlist(entries []string) (Allowlist, error) {
	var a Allowlist
	for _, raw := range entries {
		e := strings.ToLower(strings.TrimSpace(raw))
		if e == "" || e == "." {
			return Allowlist{}, fmt.Errorf("invalid allowlist entry %q: empty hostname", raw)
		}
		e = strings.TrimSuffix(e, ".")
		suffix := strings.HasPrefix(e, ".")
		name := strings.TrimPrefix(e, ".")
		if isIPLiteral(name) {
			return Allowlist{}, fmt.Errorf("invalid allowlist entry %q: IP literals are not allowed, use a hostname", raw)
		}
		if err := checkHostnameSyntax(name); err != nil {
			return Allowlist{}, fmt.Errorf("invalid allowlist entry %q: %v", raw, err)
		}
		if suffix {
			a.suffixes = append(a.suffixes, name)
		} else {
			a.exact = append(a.exact, name)
		}
	}
	return a, nil
}

// Matches reports whether host is covered by the allowlist. Matching is
// case-insensitive and ignores one trailing dot. IP literals never match.
func (a Allowlist) Matches(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if h == "" || isIPLiteral(h) {
		return false
	}
	for _, e := range a.exact {
		if h == e {
			return true
		}
	}
	for _, s := range a.suffixes {
		if h == s || strings.HasSuffix(h, "."+s) {
			return true
		}
	}
	return false
}

// isIPLiteral reports whether s parses as an IP address, with or without
// brackets or an IPv6 zone.
func isIPLiteral(s string) bool {
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	_, err := netip.ParseAddr(s)
	return err == nil
}

// checkHostnameSyntax rejects names with empty labels or characters outside
// [a-z0-9-_.]. Underscore is accepted because Docker/Compose service names
// may contain it.
func checkHostnameSyntax(name string) error {
	if name == "" {
		return errors.New("empty hostname")
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			return errors.New("empty label")
		}
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return fmt.Errorf("character %q is not allowed in a hostname", c)
		}
	}
	return nil
}

var (
	// Always-blocked IPv4 ranges not covered by netip's predicates.
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
	cgnat       = netip.MustParsePrefix("100.64.0.0/10")

	// IPv6 ranges that embed an IPv4 address in their low 32 bits.
	// Addresses here are checked as the embedded IPv4 address.
	ipv4Translated = netip.MustParsePrefix("::ffff:0:0:0/96") // IPv4-translated (SIIT), RFC 2765
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")    // RFC 6052

	// IPv6 ranges that are always blocked.
	nat64LocalUse  = netip.MustParsePrefix("64:ff9b:1::/48") // RFC 8215; embedding layout is operator-defined
	ipv4Compatible = netip.MustParsePrefix("::/96")          // deprecated, RFC 4291
)

// CheckIP reports whether ip is an acceptable webhook destination. When it is
// not, reason says why. allowPrivate permits RFC 1918 and IPv6 unique-local
// addresses; it never permits the always-blocked ranges. A nil or malformed
// ip is blocked.
func CheckIP(ip net.IP, allowPrivate bool) (reason string, ok bool) {
	addr, valid := netip.AddrFromSlice(ip)
	if !valid {
		return "invalid IP address", false
	}
	return checkAddr(addr, allowPrivate)
}

func checkAddr(addr netip.Addr, allowPrivate bool) (string, bool) {
	if !addr.IsValid() {
		return "invalid IP address", false
	}
	if addr.Zone() != "" {
		return "scoped (zoned) IPv6 address", false
	}
	// IPv4-mapped (::ffff:a.b.c.d) is checked as plain IPv4.
	addr = addr.Unmap()

	if addr.Is6() {
		if ipv4Translated.Contains(addr) || nat64WellKnown.Contains(addr) {
			b := addr.As16()
			return checkAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), allowPrivate)
		}
	}

	switch {
	case addr.IsUnspecified():
		return "unspecified address", false
	case addr.IsLoopback():
		return "loopback address", false
	case addr.IsLinkLocalUnicast():
		return "link-local address", false
	case addr.IsMulticast(): // includes link-local and interface-local multicast
		return "multicast address", false
	case thisNetwork.Contains(addr):
		return "this-network address (0.0.0.0/8)", false
	case cgnat.Contains(addr):
		return "carrier-grade NAT address (100.64.0.0/10)", false
	case nat64LocalUse.Contains(addr):
		return "NAT64 local-use address", false
	case ipv4Compatible.Contains(addr):
		return "IPv4-compatible IPv6 address", false
	}

	if addr.IsPrivate() && !allowPrivate {
		return "private address (allow it with WEBHOOK_ALLOWED_PRIVATE_SUFFIXES)", false
	}
	return "", true
}

// GuardedDialContext returns a DialFunc that resolves the dial address with
// r, checks every resolved IP with CheckIP (permitting private ranges only
// for hostnames matched by a), and then dials a checked IP with base. The
// hostname itself is never handed to base, so base cannot re-resolve it.
//
// A nil r uses net.DefaultResolver; a nil base uses a zero net.Dialer.
func GuardedDialContext(a Allowlist, r Resolver, base *net.Dialer) DialFunc {
	if r == nil {
		r = net.DefaultResolver
	}
	if base == nil {
		base = &net.Dialer{}
	}
	return guardedDial(a, r, base.DialContext)
}

// guardedDial is the testable core of GuardedDialContext.
func guardedDial(a Allowlist, r Resolver, dial DialFunc) DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if host == "" {
			return nil, fmt.Errorf("webhook dial %q: missing host", addr)
		}

		// IP literal: never allowlisted. netip accepts zoned IPv6 literals,
		// which checkAddr then blocks.
		if lit, err := netip.ParseAddr(host); err == nil {
			reason, ok := checkAddr(lit, false)
			if !ok {
				return nil, &BlockedError{Host: host, IP: net.IP(lit.WithZone("").AsSlice()), Reason: reason}
			}
			return dial(ctx, network, net.JoinHostPort(lit.Unmap().String(), port))
		}

		// Lookup errors are returned unwrapped so they stay retryable.
		ipAddrs, err := r.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ipAddrs) == 0 {
			return nil, &net.DNSError{Err: "no addresses found", Name: host, IsNotFound: true}
		}

		allowPrivate := a.Matches(host)
		checked := make([]netip.Addr, 0, len(ipAddrs))
		for _, ia := range ipAddrs {
			ip, valid := netip.AddrFromSlice(ia.IP)
			if valid && ia.Zone != "" {
				ip = ip.WithZone(ia.Zone)
			}
			reason, ok := "invalid IP address", false
			if valid {
				reason, ok = checkAddr(ip, allowPrivate)
			}
			if !ok {
				return nil, &BlockedError{Host: host, IP: ia.IP, Reason: reason}
			}
			// Dial exactly what was checked: unmapped, no zone.
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
			conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
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
