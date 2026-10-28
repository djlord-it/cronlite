package webhookurl

import (
	"fmt"
	"slices"
	"strings"
)

// ReasonTailnetNotAllowed is the refusal reason for a tailnet host that the
// sending namespace may not reach.
const ReasonTailnetNotAllowed = "tailnet host not allowed for namespace"

// hostSet is the tailnet hosts one namespace may reach: every host when all is
// set, otherwise the single-label names in names.
type hostSet struct {
	all   bool
	names map[string]struct{}
}

// TailnetPolicy is a per-namespace allowlist of tailnet hosts. It is default
// deny: the zero value, a nil *TailnetPolicy and an empty namespace allow
// nothing.
type TailnetPolicy struct {
	byNamespace map[string]hostSet // keyed by lowercased namespace; "*" applies to all
}

// ParseTailnetPolicy parses "ns=host,host;ns=*;*=host". Entries are separated
// by ";" (a trailing one is fine) and hosts by ",". Whitespace around every
// token is ignored and names are lowercased.
//
// A host is a single label relative to the tailnet suffix, so "invoices", not
// "invoices.example-tailnet.ts.net". "*" as a host allows every tailnet host;
// "*" as a namespace applies to every namespace.
//
// Empty input, a namespace listed twice, and entries with a missing namespace
// or host, an empty host, a dotted or IP literal host, or characters that
// cannot appear in a hostname are rejected with an error naming the entry.
func ParseTailnetPolicy(raw string) (*TailnetPolicy, error) {
	p := &TailnetPolicy{byNamespace: make(map[string]hostSet)}
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		ns, hosts, found := strings.Cut(entry, "=")
		ns = strings.ToLower(strings.TrimSpace(ns))
		if !found || ns == "" {
			return nil, fmt.Errorf("invalid tailnet policy entry %q: want namespace=host[,host]", entry)
		}
		if _, dup := p.byNamespace[ns]; dup {
			return nil, fmt.Errorf("invalid tailnet policy entry %q: namespace %q listed twice", entry, ns)
		}
		set := hostSet{names: make(map[string]struct{})}
		for _, h := range strings.Split(hosts, ",") {
			h = strings.ToLower(strings.TrimSpace(h))
			if h == "*" {
				set.all = true
				continue
			}
			if err := checkPolicyHost(h); err != nil {
				return nil, fmt.Errorf("invalid tailnet policy entry %q: host %q: %w", entry, h, err)
			}
			set.names[h] = struct{}{}
		}
		p.byNamespace[ns] = set
	}
	if len(p.byNamespace) == 0 {
		return nil, fmt.Errorf("invalid tailnet policy %q: empty", raw)
	}
	return p, nil
}

func checkPolicyHost(h string) error {
	if err := checkHostnameSyntax(h); err != nil {
		return err
	}
	if strings.Contains(h, ".") {
		return fmt.Errorf("must be a single label, without the tailnet suffix")
	}
	if isIPLiteral(h) {
		return fmt.Errorf("IP literals are not allowed")
	}
	return nil
}

// Allows reports whether namespace may reach relHost, a single label relative
// to the tailnet suffix. The namespace's own entry and the "*" entry are
// unioned. A nil policy and an empty namespace allow nothing.
func (p *TailnetPolicy) Allows(namespace, relHost string) bool {
	if p == nil || namespace == "" {
		return false
	}
	for _, key := range []string{strings.ToLower(namespace), "*"} {
		set := p.byNamespace[key]
		if _, ok := set.names[relHost]; ok || set.all {
			return true
		}
	}
	return false
}

// Check returns nil when host is not under suffix (the public path decides
// those) or when namespace may reach it. Otherwise it returns a *BlockedError
// with ReasonTailnetNotAllowed. suffix may be a raw Tailnet.Suffix(); it is
// normalized here. A nil policy refuses every tailnet host.
func (p *TailnetPolicy) Check(namespace, host, suffix string) error {
	suffix = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(suffix)), ".")
	if !tailnetHost(host, suffix) {
		return nil
	}
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	rel := strings.TrimSuffix(h, "."+suffix)
	if !strings.Contains(rel, ".") && p.Allows(namespace, rel) {
		return nil
	}
	return &BlockedError{Host: host, Reason: ReasonTailnetNotAllowed}
}

// Namespaces returns the namespaces the policy lists, lowercased and sorted.
// A "*" entry is included as "*". A nil policy lists none.
func (p *TailnetPolicy) Namespaces() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.byNamespace))
	for ns := range p.byNamespace {
		out = append(out, ns)
	}
	slices.Sort(out)
	return out
}
