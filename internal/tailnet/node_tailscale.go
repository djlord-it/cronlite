//go:build tailscale

package tailnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/tsnet"
)

// Supported reports whether this binary includes Tailscale support.
const Supported = true

// Node is a running tsnet node. It implements webhookurl.Tailnet.
type Node struct {
	srv    *tsnet.Server
	lc     *local.Client
	suffix string
	self   []netip.Addr
}

// Start brings a tsnet node up and waits until it is connected to the tailnet
// or ctx ends. The tailnet must have MagicDNS enabled, since webhook
// destinations are matched and resolved by MagicDNS name.
func Start(ctx context.Context, cfg Config) (*Node, error) {
	srv := &tsnet.Server{
		Hostname: cfg.Hostname,
		Dir:      cfg.StateDir,
		AuthKey:  cfg.AuthKey,
		UserLogf: cfg.Logf,
		// Logf is left nil: tsnet's backend logs are verbose and discarded.
	}
	st, err := srv.Up(ctx)
	if err != nil {
		_ = srv.Close()
		return nil, fmt.Errorf("tailnet: bring node up: %w", err)
	}
	if st.CurrentTailnet == nil || st.CurrentTailnet.MagicDNSSuffix == "" {
		_ = srv.Close()
		return nil, errors.New("tailnet: node is up but reports no MagicDNS suffix")
	}
	if !st.CurrentTailnet.MagicDNSEnabled {
		_ = srv.Close()
		return nil, fmt.Errorf("tailnet: MagicDNS is disabled on tailnet %q; enable it in the Tailscale admin console", st.CurrentTailnet.Name)
	}
	lc, err := srv.LocalClient()
	if err != nil {
		_ = srv.Close()
		return nil, fmt.Errorf("tailnet: local client: %w", err)
	}
	return &Node{
		srv:    srv,
		lc:     lc,
		suffix: st.CurrentTailnet.MagicDNSSuffix,
		self:   append([]netip.Addr(nil), st.TailscaleIPs...),
	}, nil
}

// Suffix returns the tailnet's MagicDNS domain, e.g. "example-tailnet.ts.net".
func (n *Node) Suffix() string { return n.suffix }

// SelfAddrs returns this node's tailnet addresses.
func (n *Node) SelfAddrs() []netip.Addr { return n.self }

// LookupIPAddr resolves host through the node's MagicDNS resolver.
func (n *Node) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	var out []net.IPAddr
	var notFound error
	for _, qt := range []string{"A", "AAAA"} {
		msg, _, err := n.lc.QueryDNS(ctx, host, qt)
		if err != nil {
			return nil, &net.DNSError{Err: err.Error(), Name: host, IsTemporary: true}
		}
		addrs, err := parseAnswers(host, msg)
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			notFound = err
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, addrs...)
	}
	if len(out) == 0 && notFound != nil {
		return nil, notFound
	}
	return out, nil
}

// Dial connects to addr, a literal IP:port, over the tailnet.
func (n *Node) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return n.srv.Dial(ctx, network, addr)
}

// Running reports whether the node is connected to the tailnet.
func (n *Node) Running(ctx context.Context) bool {
	st, err := n.lc.StatusWithoutPeers(ctx)
	return err == nil && st.BackendState == ipn.Running.String()
}

// CertDomain returns the node's HTTPS name, e.g.
// "cronlite.example-tailnet.ts.net", or "" when the tailnet has HTTPS
// certificates disabled.
func (n *Node) CertDomain() string {
	if d := n.srv.CertDomains(); len(d) > 0 {
		return d[0]
	}
	return ""
}

// ListenTLS listens on :443 on the tailnet only, serving the node's Tailscale
// HTTPS certificate. The tailnet must have HTTPS certificates enabled.
func (n *Node) ListenTLS() (net.Listener, error) {
	return n.srv.ListenTLS("tcp", ":443")
}

// WarmCert fetches the node's HTTPS certificate into the state directory so
// the first TLS handshake does not wait for issuance, which can take tens of
// seconds. It needs public DNS and outbound HTTPS to Let's Encrypt.
func (n *Node) WarmCert(ctx context.Context) error {
	domain := n.CertDomain()
	if domain == "" {
		return errors.New("tailnet: HTTPS certificates are disabled on this tailnet")
	}
	_, _, err := n.lc.CertPair(ctx, domain)
	return err
}

// ListenHTTP listens on :80 on the tailnet only, without TLS.
func (n *Node) ListenHTTP() (net.Listener, error) {
	return n.srv.Listen("tcp", ":80")
}

// Close shuts the node down. It is safe to call more than once.
func (n *Node) Close() error {
	if err := n.srv.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}
