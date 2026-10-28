package webhookurl

import (
	"fmt"
	"slices"
	"strings"

	"golang.org/x/net/idna"
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
	byNamespace map[string]hostSet // keyed by exact namespace; "*" applies to all
}

// ParseTailnetPolicy parses "ns=host,host;ns=*;*=host". Entries are separated
// by ";" (a trailing one is fine) and hosts by ",". Whitespace around every
// token is ignored. Namespaces are matched exactly (CronLite namespaces are
// case-sensitive); hosts are lowercased.
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
		ns = strings.TrimSpace(ns)
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
// to the tailnet suffix. namespace is matched exactly; relHost is matched
// case-insensitively and may carry one trailing dot. The namespace's own entry and the "*" entry are unioned. A
// nil policy and an empty namespace allow nothing.
func (p *TailnetPolicy) Allows(namespace, relHost string) bool {
	if p == nil || namespace == "" {
		return false
	}
	relHost = strings.TrimSuffix(strings.ToLower(relHost), ".")
	for _, key := range []string{namespace, "*"} {
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
//
// host is judged as the HTTP transport dials it: a non-ASCII host is mapped
// through IDNA (idna.Lookup), as net/http does, so "svc.tailnet.ｔｓ.net" is the
// tailnet host "svc.tailnet.ts.net". A non-ASCII host IDNA cannot map is
// refused, since whether it is a tailnet host cannot be told.
func (p *TailnetPolicy) Check(namespace, host, suffix string) error {
	suffix = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(suffix)), ".")
	if suffix == "" {
		return nil
	}
	ascii := host
	if !isASCII(host) {
		var err error
		if ascii, err = idna.Lookup.ToASCII(host); err != nil {
			return &BlockedError{Host: host, Reason: ReasonTailnetNotAllowed}
		}
	}
	rel, isTailnet := relativeTailnetHost(ascii, suffix)
	if !isTailnet {
		return nil
	}
	if !strings.Contains(rel, ".") && p.Allows(namespace, rel) {
		return nil
	}
	return &BlockedError{Host: host, Reason: ReasonTailnetNotAllowed}
}

// relativeTailnetHost reports whether host, an ASCII hostname, is a strict
// subdomain of suffix (lowercased, without a trailing dot) and returns the
// part before the suffix. Case and one trailing dot on host are ignored.
func relativeTailnetHost(host, suffix string) (rel string, isTailnet bool) {
	if !tailnetHost(host, suffix) {
		return "", false
	}
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	return strings.TrimSuffix(h, "."+suffix), true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// Namespaces returns the namespaces the policy lists, as written, sorted.
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

// Hosts returns what namespace may reach: all is set when every tailnet host
// is allowed, otherwise hosts lists the allowed single labels, sorted. Like
// Allows, it unions the namespace's own entry and the "*" entry. A nil policy
// and an empty namespace allow nothing.
func (p *TailnetPolicy) Hosts(namespace string) (all bool, hosts []string) {
	if p == nil || namespace == "" {
		return false, nil
	}
	seen := make(map[string]struct{})
	for _, key := range []string{namespace, "*"} {
		set := p.byNamespace[key]
		if set.all {
			return true, nil
		}
		for h := range set.names {
			if _, dup := seen[h]; !dup {
				seen[h] = struct{}{}
				hosts = append(hosts, h)
			}
		}
	}
	slices.Sort(hosts)
	return false, hosts
}

// TailnetRelativeHost reports whether host is under suffix, judged as Check
// judges it (IDNA-mapped, case-insensitive, one trailing dot ignored), and
// returns the part before the suffix, e.g. "invoices". It reports false for an
// empty suffix and for a host IDNA cannot map.
func TailnetRelativeHost(host, suffix string) (rel string, ok bool) {
	suffix = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(suffix)), ".")
	if suffix == "" {
		return "", false
	}
	if !isASCII(host) {
		var err error
		if host, err = idna.Lookup.ToASCII(host); err != nil {
			return "", false
		}
	}
	return relativeTailnetHost(host, suffix)
}
