package tailnet

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
)

func TestConnContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	marked := tailnetListener{ln}

	dial := func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
	}
	dial()
	dial()
	c, err := marked.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if !ViaTailnet(ConnContext(context.Background(), c)) {
		t.Error("conn from the tailnet listener: ViaTailnet = false")
	}
	if !ViaTailnet(ConnContext(context.Background(), tls.Server(c, &tls.Config{}))) {
		t.Error("TLS conn over the tailnet listener: ViaTailnet = false")
	}

	plain, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if ViaTailnet(ConnContext(context.Background(), plain)) {
		t.Error("conn from a public listener: ViaTailnet = true")
	}
	if ViaTailnet(ConnContext(context.Background(), tls.Server(plain, &tls.Config{}))) {
		t.Error("TLS conn over a public listener: ViaTailnet = true")
	}
	if ViaTailnet(context.Background()) {
		t.Error("bare context: ViaTailnet = true")
	}
}
