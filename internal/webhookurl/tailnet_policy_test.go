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
