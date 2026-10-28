package webhookurl

import (
	"errors"
	"strings"
	"testing"
)

func TestParseTailnetPolicy(t *testing.T) {
	valid := []string{
		"billing=invoices,ledger;ops=*;*=status",
		"*=*",
		" billing = invoices , ledger ; ",
		"billing=invoices;",
	}
	for _, raw := range valid {
		if _, err := ParseTailnetPolicy(raw); err != nil {
			t.Errorf("ParseTailnetPolicy(%q) = %v, want nil", raw, err)
		}
	}

	invalid := []struct{ raw, mention string }{
		{"", "empty"},
		{"billing", "billing"},
		{"=invoices", "=invoices"},
		{"billing=", "billing="},
		{"billing=invoices;billing=ledger", "billing"},
		{"billing=invoices.example-tailnet.ts.net", "invoices.example-tailnet.ts.net"},
		{"billing=100.64.0.1", "100.64.0.1"},
		{"billing=bad_host!", "bad_host!"},
		{"billing=a,,b", "billing=a,,b"},
	}
	for _, tc := range invalid {
		_, err := ParseTailnetPolicy(tc.raw)
		if err == nil {
			t.Errorf("ParseTailnetPolicy(%q) = nil, want error", tc.raw)
			continue
		}
		if !strings.Contains(err.Error(), tc.mention) {
			t.Errorf("ParseTailnetPolicy(%q) error %q does not mention %q", tc.raw, err, tc.mention)
		}
	}
}

func TestTailnetPolicyCheck(t *testing.T) {
	const suffix = "example-tailnet.ts.net"
	p, err := ParseTailnetPolicy("billing=invoices;*=status")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		policy    *TailnetPolicy
		namespace string
		host      string
		suffix    string
		blocked   bool
	}{
		{"allowed host", p, "billing", "invoices.example-tailnet.ts.net", suffix, false},
		{"case and trailing dot", p, "billing", "INVOICES.example-tailnet.ts.net.", suffix, false},
		{"union with wildcard namespace", p, "billing", "status.example-tailnet.ts.net", suffix, false},
		{"wildcard namespace for others", p, "ops", "status.example-tailnet.ts.net", suffix, false},
		{"other namespace's host", p, "ops", "invoices.example-tailnet.ts.net", suffix, true},
		{"nested label", p, "billing", "a.invoices.example-tailnet.ts.net", suffix, true},
		{"empty namespace", p, "", "status.example-tailnet.ts.net", suffix, true},
		{"not a tailnet host", p, "ops", "api.github.com", suffix, false},
		{"nil policy, tailnet host", nil, "billing", "invoices.example-tailnet.ts.net", suffix, true},
		{"nil policy, public host", nil, "billing", "api.github.com", suffix, false},
		{"raw suffix is normalized", p, "billing", "invoices.example-tailnet.ts.net", "Example-Tailnet.TS.net.", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.Check(tc.namespace, tc.host, tc.suffix)
			if !tc.blocked {
				if err != nil {
					t.Fatalf("Check = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrDestinationBlocked) {
				t.Fatalf("Check = %v, want ErrDestinationBlocked", err)
			}
			if !strings.Contains(err.Error(), "tailnet host not allowed for namespace") {
				t.Errorf("error %q lacks the refusal reason", err)
			}
		})
	}
}

func TestTailnetPolicyWildcardHost(t *testing.T) {
	p, err := ParseTailnetPolicy("ops=*")
	if err != nil {
		t.Fatal(err)
	}
	const suffix = "example-tailnet.ts.net"
	if err := p.Check("ops", "x.example-tailnet.ts.net", suffix); err != nil {
		t.Errorf("ops: Check = %v, want nil", err)
	}
	if err := p.Check("billing", "x.example-tailnet.ts.net", suffix); !errors.Is(err, ErrDestinationBlocked) {
		t.Errorf("billing: Check = %v, want ErrDestinationBlocked", err)
	}
}

func TestBlockedErrorWithoutIP(t *testing.T) {
	got := (&BlockedError{Host: "h", Reason: "r"}).Error()
	if want := `destination blocked: host "h": r`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestTailnetPolicyNamespaces(t *testing.T) {
	p, err := ParseTailnetPolicy("Ops=*;billing=invoices;*=status")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Namespaces(), ",")
	if want := "*,billing,ops"; got != want {
		t.Errorf("Namespaces() = %q, want %q", got, want)
	}
	var nilPolicy *TailnetPolicy
	if n := nilPolicy.Namespaces(); len(n) != 0 {
		t.Errorf("nil policy Namespaces() = %v, want none", n)
	}
}

// The HTTP transport maps a non-ASCII host through IDNA before dialing, so
// each of these reaches payroll.example-tailnet.ts.net. Check must see the
// same host the transport dials.
func TestTailnetPolicyCheckIDNA(t *testing.T) {
	const suffix = "example-tailnet.ts.net"
	deny, err := ParseTailnetPolicy("billing=invoices")
	if err != nil {
		t.Fatal(err)
	}
	allow, err := ParseTailnetPolicy("payroll-ns=payroll")
	if err != nil {
		t.Fatal(err)
	}
	hosts := map[string]string{
		"fullwidth ts":        "payroll.example-tailnet.ｔｓ.net",
		"fullwidth full stop": "payroll.example-tailnet．ts.net",
		"fullwidth label":     "ＰＡＹＲＯＬＬ.example-tailnet.ts.net",
		"ideographic dots":    "payroll。example-tailnet。ts。net",
	}
	for name, host := range hosts {
		t.Run(name, func(t *testing.T) {
			if err := deny.Check("ops", host, suffix); !errors.Is(err, ErrDestinationBlocked) {
				t.Errorf("Check(ops, %q) = %v, want ErrDestinationBlocked", host, err)
			}
		})
	}
	for _, name := range []string{"fullwidth ts", "ideographic dots"} {
		host := hosts[name]
		t.Run(name+" allowed", func(t *testing.T) {
			if err := allow.Check("payroll-ns", host, suffix); err != nil {
				t.Errorf("Check(payroll-ns, %q) = %v, want nil", host, err)
			}
		})
	}
}

func TestTailnetPolicyCheckIDNAFailure(t *testing.T) {
	const suffix = "example-tailnet.ts.net"
	p, err := ParseTailnetPolicy("*=*")
	if err != nil {
		t.Fatal(err)
	}
	// Invalid non-ASCII input fails closed: its tailnet status is unknown.
	if err := p.Check("ops", "a‍.example.com", suffix); !errors.Is(err, ErrDestinationBlocked) {
		t.Errorf("non-ASCII conversion failure: Check = %v, want ErrDestinationBlocked", err)
	}
	if err := p.Check("ops", "a\u200d.example-tailnet.ts.net", suffix); !errors.Is(err, ErrDestinationBlocked) {
		t.Errorf("non-ASCII tailnet conversion failure: Check = %v, want ErrDestinationBlocked", err)
	}
	// An ASCII public host that IDNA rejects is left to the public path.
	if err := p.Check("ops", "under_score.example.com", suffix); err != nil {
		t.Errorf("ASCII public host: Check = %v, want nil", err)
	}
	// net/http dials an ASCII host as written, without IDNA, so an allowed
	// ASCII tailnet host that IDNA would reject keeps working.
	named, err := ParseTailnetPolicy("ops=my_svc")
	if err != nil {
		t.Fatal(err)
	}
	if err := named.Check("ops", "my_svc.example-tailnet.ts.net", suffix); err != nil {
		t.Errorf("ASCII tailnet host IDNA rejects: Check = %v, want nil", err)
	}
	if err := named.Check("billing", "my_svc.example-tailnet.ts.net", suffix); !errors.Is(err, ErrDestinationBlocked) {
		t.Errorf("ASCII tailnet host, other namespace: Check = %v, want ErrDestinationBlocked", err)
	}
}

func TestTailnetPolicyAllowsNormalizesHost(t *testing.T) {
	p, err := ParseTailnetPolicy("billing=invoices")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"INVOICES", "invoices.", "Invoices."} {
		if !p.Allows("billing", h) {
			t.Errorf("Allows(billing, %q) = false, want true", h)
		}
	}
}
