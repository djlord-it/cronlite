//go:build !tailscale

package tailnet

import (
	"context"
	"net"
	"net/netip"
)

// Supported reports whether this binary includes Tailscale support.
const Supported = false

// Node is a placeholder in builds without the tailscale tag; Start never
// returns one.
type Node struct{}

// Start always fails with ErrUnsupported in this build.
func Start(context.Context, Config) (*Node, error) { return nil, ErrUnsupported }

func (*Node) Suffix() string                                             { return "" }
func (*Node) SelfAddrs() []netip.Addr                                    { return nil }
func (*Node) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) { return nil, ErrUnsupported }
func (*Node) Dial(context.Context, string, string) (net.Conn, error)     { return nil, ErrUnsupported }
func (*Node) Running(context.Context) bool                               { return false }
func (*Node) Info(context.Context) (Info, error)                         { return Info{}, ErrUnsupported }
func (*Node) CertDomain() string                                         { return "" }
func (*Node) ListenTLS() (net.Listener, error)                           { return nil, ErrUnsupported }
func (*Node) ListenHTTP() (net.Listener, error)                          { return nil, ErrUnsupported }
func (*Node) WarmCert(context.Context) error                             { return ErrUnsupported }
func (*Node) Close() error                                               { return nil }
