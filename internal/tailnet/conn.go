package tailnet

import (
	"context"
	"crypto/tls"
	"net"
)

// viaTailnetKey marks a request context whose connection was accepted on a
// tailnet listener.
type viaTailnetKey struct{}

// tailnetListener wraps the node's raw tailnet listener so ConnContext can
// recognise its connections. The types are unexported: nothing a client
// sends can produce them.
type tailnetListener struct{ net.Listener }

func (l tailnetListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return tailnetConn{c}, nil
}

type tailnetConn struct{ net.Conn }

// ConnContext is an http.Server ConnContext. It marks requests on
// connections accepted by the node's tailnet listeners, so handlers can
// tell them from requests on the public listener with ViaTailnet.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	if tc, ok := c.(*tls.Conn); ok {
		c = tc.NetConn()
	}
	if _, ok := c.(tailnetConn); ok {
		return WithViaTailnet(ctx)
	}
	return ctx
}

// WithViaTailnet returns ctx marked as arriving over the tailnet. It is for
// ConnContext and tests; request data never reaches it.
func WithViaTailnet(ctx context.Context) context.Context {
	return context.WithValue(ctx, viaTailnetKey{}, true)
}

// ViaTailnet reports whether ctx belongs to a request that arrived on one of
// the node's tailnet listeners. Only such a request's RemoteAddr is a tailnet
// peer that WhoIs can identify.
func ViaTailnet(ctx context.Context) bool {
	v, _ := ctx.Value(viaTailnetKey{}).(bool)
	return v
}
