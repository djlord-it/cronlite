// Package tailnet connects CronLite to a Tailscale tailnet through an embedded
// tsnet node. The tsnet code is compiled only with -tags tailscale; default
// builds get a stub whose Start returns ErrUnsupported, so the default binary
// carries no Tailscale code.
//
// A *Node implements webhookurl.Tailnet. Nothing here is global: callers
// create a Node and hand it to the dispatcher, so a later version can run one
// Node per namespace with separate credentials.
package tailnet

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/djlord-it/cronlite/internal/webhookurl"
)

var _ webhookurl.Tailnet = (*Node)(nil)

// Config configures a Node.
type Config struct {
	// AuthKey is a Tailscale auth key. It is passed to tsnet only and never
	// logged. It may be empty when StateDir already holds a logged-in node.
	AuthKey string
	// Hostname is the node's name on the tailnet.
	Hostname string
	// StateDir holds the node's identity and keys across restarts.
	StateDir string
	// Logf receives tsnet's user-facing messages (such as a login URL when
	// no auth key is set). tsnet's verbose backend logs are discarded.
	Logf func(format string, args ...any)
}

// ErrUnsupported is returned by Start in binaries built without the
// tailscale build tag.
var ErrUnsupported = errors.New("this binary was built without Tailscale support (rebuild with -tags tailscale)")

// parseAnswers extracts the A and AAAA records from a raw DNS response.
// NXDOMAIN and an empty answer are reported as *net.DNSError with IsNotFound,
// any other failure rcode as a temporary *net.DNSError, so the dispatcher
// treats them like ordinary lookup errors.
func parseAnswers(host string, msg []byte) ([]net.IPAddr, error) {
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil {
		return nil, fmt.Errorf("magicdns %q: parse response: %w", host, err)
	}
	switch h.RCode {
	case dnsmessage.RCodeSuccess:
	case dnsmessage.RCodeNameError:
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	default:
		return nil, &net.DNSError{Err: "server failure: " + h.RCode.String(), Name: host, IsTemporary: true}
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil, fmt.Errorf("magicdns %q: parse response: %w", host, err)
	}
	var out []net.IPAddr
	for {
		rh, err := p.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("magicdns %q: parse response: %w", host, err)
		}
		switch rh.Type {
		case dnsmessage.TypeA:
			r, err := p.AResource()
			if err != nil {
				return nil, fmt.Errorf("magicdns %q: parse response: %w", host, err)
			}
			out = append(out, net.IPAddr{IP: net.IP(r.A[:])})
		case dnsmessage.TypeAAAA:
			r, err := p.AAAAResource()
			if err != nil {
				return nil, fmt.Errorf("magicdns %q: parse response: %w", host, err)
			}
			out = append(out, net.IPAddr{IP: net.IP(r.AAAA[:])})
		default:
			if err := p.SkipAnswer(); err != nil {
				return nil, fmt.Errorf("magicdns %q: parse response: %w", host, err)
			}
		}
	}
	return out, nil
}
