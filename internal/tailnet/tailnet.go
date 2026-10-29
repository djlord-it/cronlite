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
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

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

// Info is the node's state as reported by its local Tailscale backend.
type Info struct {
	// BackendState is the ipn state, e.g. "Running" or "NeedsLogin".
	BackendState string
	// Online reports whether the node is connected to the control plane.
	Online bool
	// DNSName is the node's MagicDNS name, without the trailing dot.
	DNSName string
	// TailnetName is the tailnet's display name.
	TailnetName string
	// LoginName is the node owner's login; "tagged-devices" for tagged nodes.
	LoginName string
	// Tags are the node's ACL tags.
	Tags []string
	// KeyExpiry is when the node key expires; zero when it never does.
	KeyExpiry time.Time
	// Health lists the backend's current health warnings.
	Health []string
}

// AdminCapability is the app capability, granted in the tailnet policy file,
// that lets a person sign in to the admin UI with their Tailscale identity.
// Each value is an object {"namespaces": ["billing", ...]}.
const AdminCapability = "github.com/djlord-it/cronlite/cap/admin"

// Identity is the peer behind a tailnet connection, as reported by WhoIs.
type Identity struct {
	// LoginName is the user's login, e.g. "alice@example.com". It is empty
	// for tagged nodes, which belong to no user.
	LoginName string
	// DisplayName is the user's display name, possibly empty.
	DisplayName string
	// NodeName is the peer's MagicDNS name, without the trailing dot.
	NodeName string
	// Tagged reports whether the peer is a tagged node.
	Tagged bool
	// AdminCaps holds the raw values of AdminCapability granted to the peer.
	AdminCaps []json.RawMessage
}

// adminCapValue is one value of AdminCapability.
type adminCapValue struct {
	Namespaces []string `json:"namespaces"`
}

// AdminNamespaces returns the sorted, de-duplicated namespaces that
// AdminCapability grants. A malformed value, an empty name or the "*"
// wildcard is an error, so a mistyped grant gives no access rather than an
// unexpected one.
func (id Identity) AdminNamespaces() ([]string, error) {
	var out []string
	for _, raw := range id.AdminCaps {
		var v adminCapValue
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("tailnet: %s: malformed value %s: %w", AdminCapability, raw, err)
		}
		for _, ns := range v.Namespaces {
			switch {
			case strings.TrimSpace(ns) != ns || ns == "":
				return nil, fmt.Errorf("tailnet: %s: invalid namespace %q", AdminCapability, ns)
			case ns == "*":
				return nil, fmt.Errorf("tailnet: %s: the \"*\" wildcard is not supported; list namespaces explicitly", AdminCapability)
			}
			out = append(out, ns)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// ErrUnsupported is returned by Start in binaries built without the
// tailscale build tag.
var ErrUnsupported = errors.New("this binary was built without Tailscale support (rebuild with -tags tailscale)")

// RedirectHandler sends every request to the same path on
// https://<domain>. The target host is fixed, never taken from the request,
// so the handler cannot be used as an open redirect.
func RedirectHandler(domain string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+domain+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
}

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
