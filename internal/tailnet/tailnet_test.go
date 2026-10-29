package tailnet

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func buildResponse(t *testing.T, rcode dnsmessage.RCode, answers ...dnsmessage.Resource) []byte {
	t.Helper()
	name := dnsmessage.MustNewName("svc.example-tailnet.ts.net.")
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, RCode: rcode})
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := b.Question(dnsmessage.Question{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}); err != nil {
		t.Fatal(err)
	}
	if err := b.StartAnswers(); err != nil {
		t.Fatal(err)
	}
	for i := range answers {
		a := &answers[i]
		a.Header.Name, a.Header.Class = name, dnsmessage.ClassINET
		var err error
		switch body := a.Body.(type) {
		case *dnsmessage.AResource:
			err = b.AResource(a.Header, *body)
		case *dnsmessage.AAAAResource:
			err = b.AAAAResource(a.Header, *body)
		case *dnsmessage.TXTResource:
			err = b.TXTResource(a.Header, *body)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestParseAnswers(t *testing.T) {
	msg := buildResponse(t, dnsmessage.RCodeSuccess,
		dnsmessage.Resource{Body: &dnsmessage.AResource{A: [4]byte{100, 101, 102, 103}}},
		dnsmessage.Resource{Body: &dnsmessage.TXTResource{TXT: []string{"ignored"}}},
		dnsmessage.Resource{Body: &dnsmessage.AAAAResource{AAAA: [16]byte{0xfd, 0x7a, 0x11, 0x5c, 0xa1, 0xe0, 15: 2}}},
	)
	got, err := parseAnswers("svc.example-tailnet.ts.net", msg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"100.101.102.103", "fd7a:115c:a1e0::2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].IP.Equal(net.ParseIP(want[i])) {
			t.Errorf("answer %d = %v, want %s", i, got[i].IP, want[i])
		}
	}

	if got, err := parseAnswers("svc", buildResponse(t, dnsmessage.RCodeSuccess)); err != nil || len(got) != 0 {
		t.Errorf("empty answer: got %v, %v", got, err)
	}

	var dnsErr *net.DNSError
	_, err = parseAnswers("gone", buildResponse(t, dnsmessage.RCodeNameError))
	if !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
		t.Errorf("NXDOMAIN: err = %v, want not-found DNSError", err)
	}
	_, err = parseAnswers("svc", buildResponse(t, dnsmessage.RCodeServerFailure))
	if !errors.As(err, &dnsErr) || dnsErr.IsNotFound || !dnsErr.IsTemporary {
		t.Errorf("SERVFAIL: err = %v, want temporary DNSError", err)
	}
	if _, err := parseAnswers("svc", []byte{1, 2}); err == nil {
		t.Error("truncated message: want error")
	}
}

func TestRedirectHandler(t *testing.T) {
	h := RedirectHandler("cronlite.example-tailnet.ts.net")
	for target, want := range map[string]string{
		"http://cronlite.example-tailnet.ts.net/admin?x=1": "https://cronlite.example-tailnet.ts.net/admin?x=1",
		"http://evil.example/admin":                        "https://cronlite.example-tailnet.ts.net/admin", // Host header ignored
		"http://100.64.0.1/":                               "https://cronlite.example-tailnet.ts.net/",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, nil))
		if rec.Code != http.StatusPermanentRedirect {
			t.Errorf("%s: status = %d, want 308", target, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("%s: Location = %q, want %q", target, got, want)
		}
	}
}

func TestIdentityAdminNamespaces(t *testing.T) {
	caps := func(vals ...string) Identity {
		var id Identity
		for _, v := range vals {
			id.AdminCaps = append(id.AdminCaps, json.RawMessage(v))
		}
		return id
	}
	tests := []struct {
		name    string
		id      Identity
		want    []string
		wantErr bool
	}{
		{name: "no grant", id: Identity{}, want: nil},
		{name: "one value", id: caps(`{"namespaces":["ops","billing"]}`), want: []string{"billing", "ops"}},
		{name: "merged and deduplicated", id: caps(`{"namespaces":["ops"]}`, `{"namespaces":["billing","ops"]}`), want: []string{"billing", "ops"}},
		{name: "unknown fields ignored", id: caps(`{"namespaces":["ops"],"role":"viewer"}`), want: []string{"ops"}},
		{name: "no namespaces key", id: caps(`{}`), want: nil},
		{name: "malformed", id: caps(`["ops"]`), wantErr: true},
		{name: "wildcard", id: caps(`{"namespaces":["*"]}`), wantErr: true},
		{name: "empty name", id: caps(`{"namespaces":[""]}`), wantErr: true},
		{name: "padded name", id: caps(`{"namespaces":[" ops"]}`), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.id.AdminNamespaces()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
