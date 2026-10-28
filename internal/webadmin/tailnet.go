package webadmin

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/tailnet"
	"github.com/djlord-it/cronlite/internal/webhookurl"
)

const (
	tailnetInfoTimeout = 2 * time.Second
	// tailnetJobScanLimit bounds how many jobs the Tailscale page inspects.
	tailnetJobScanLimit = 2000
	tailnetJobPageSize  = 500
	// keyExpiryWarning is how early the page warns about an expiring node key.
	keyExpiryWarning = 14 * 24 * time.Hour
)

// TailnetNode is the embedded Tailscale node as the admin UI sees it: the
// webhookurl.Tailnet that delivery dials through, plus what an operator needs
// to read about it.
type TailnetNode interface {
	webhookurl.Tailnet
	CertDomain() string
	Info(ctx context.Context) (tailnet.Info, error)
}

// TailnetConfig describes the Tailscale integration. The zero value is a
// binary built without Tailscale support.
type TailnetConfig struct {
	// Built reports whether the binary was built with -tags tailscale.
	Built bool
	// Delivery mirrors WEBHOOK_ALLOW_TAILNET.
	Delivery bool
	// Listen mirrors TS_LISTEN.
	Listen bool
	// DisablePublicListener mirrors TS_DISABLE_PUBLIC_LISTENER.
	DisablePublicListener bool
	// Hostname is TS_HOSTNAME.
	Hostname string
	// PublicAddr is HTTP_ADDR.
	PublicAddr string
	// Policy is the parsed WEBHOOK_TAILNET_HOSTS; nil when delivery is off.
	Policy *webhookurl.TailnetPolicy
	// Node is the running node; nil when neither delivery nor listening is on.
	Node TailnetNode
}

// suffix is the tailnet's MagicDNS suffix, or "" without a running node.
func (c TailnetConfig) suffix() string {
	if c.Node == nil {
		return ""
	}
	return c.Node.Suffix()
}

// tailnetTarget is how delivery treats one webhook URL.
type tailnetTarget struct {
	Tailnet bool   // the host is under this node's MagicDNS suffix
	Host    string // the host relative to the suffix, e.g. "invoices"
	Allowed bool   // the namespace may call it over the tailnet
	Reason  string // why not, when Tailnet && !Allowed
}

func (c TailnetConfig) target(namespace, rawURL string) tailnetTarget {
	u, err := url.Parse(rawURL)
	if err != nil {
		return tailnetTarget{}
	}
	rel, ok := webhookurl.TailnetRelativeHost(u.Hostname(), c.suffix())
	if !ok {
		return tailnetTarget{}
	}
	t := tailnetTarget{Tailnet: true, Host: rel}
	switch {
	case !c.Delivery:
		t.Reason = "Tailnet delivery is off (WEBHOOK_ALLOW_TAILNET)."
	case c.Policy.Check(namespace, u.Hostname(), c.suffix()) != nil:
		t.Reason = "This namespace may not call this host (WEBHOOK_TAILNET_HOSTS)."
	default:
		t.Allowed = true
	}
	return t
}

// tailnetHint is the job form's note about tailnet hosts.
type tailnetHint struct {
	Suffix   string
	Delivery bool
	AllHosts bool
	Hosts    []string
}

func (c TailnetConfig) hint(namespace string) tailnetHint {
	h := tailnetHint{Suffix: c.suffix(), Delivery: c.Delivery}
	if c.Delivery {
		h.AllHosts, h.Hosts = c.Policy.Hosts(namespace)
	}
	return h
}

type tailnetJob struct {
	Job    domain.JobWithSchedule
	Target tailnetTarget
}

// tailnetView is everything the Tailscale page shows. Only the viewer's own
// namespace's allowlist is included.
type tailnetView struct {
	TailnetConfig
	Started        bool
	Running        bool
	Info           tailnet.Info
	InfoError      bool
	Suffix         string
	Addrs          []netip.Addr
	CertDomain     string
	KeyExpiresSoon bool
	AllHosts       bool
	Hosts          []string
	ViaTailnet     bool
	Jobs           []tailnetJob
	JobsLimited    bool
}

func (h *Handler) tailnetPage(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	cfg := h.tailnet
	view := tailnetView{TailnetConfig: cfg}
	view.Policy, view.Node = nil, nil // never handed to the template
	if cfg.Delivery {
		view.AllHosts, view.Hosts = cfg.Policy.Hosts(auth.Key.Namespace.String())
	}
	if node := cfg.Node; node != nil {
		view.Started = true
		ctx, cancel := context.WithTimeout(r.Context(), tailnetInfoTimeout)
		info, err := node.Info(ctx)
		cancel()
		if err != nil {
			h.logger.Printf("webadmin: tailnet status: %v", err)
			view.InfoError = true
		}
		view.Info = info
		view.Running = err == nil && info.BackendState == "Running"
		view.Suffix, view.Addrs, view.CertDomain = node.Suffix(), node.SelfAddrs(), node.CertDomain()
		view.KeyExpiresSoon = !info.KeyExpiry.IsZero() && info.KeyExpiry.Sub(h.now()) < keyExpiryWarning
		view.ViaTailnet = servedVia(r, view.Addrs)

		jobs, limited, err := h.tailnetJobs(r.Context(), auth.Key.Namespace)
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		view.Jobs, view.JobsLimited = jobs, limited
	}
	data := h.authPage(auth, "Tailscale")
	data.Tailnet = view
	h.render(w, "tailscale", data)
}

// tailnetJobs lists the namespace's jobs whose webhook host is on the tailnet,
// inspecting at most tailnetJobScanLimit jobs.
func (h *Handler) tailnetJobs(ctx context.Context, ns domain.Namespace) ([]tailnetJob, bool, error) {
	var out []tailnetJob
	for offset := 0; offset < tailnetJobScanLimit; offset += tailnetJobPageSize {
		rows, err := h.service.ListJobsWithSchedules(ctx, domain.JobFilter{
			Namespace:  ns,
			ListParams: domain.ListParams{Limit: tailnetJobPageSize, Offset: offset},
		})
		if err != nil {
			return nil, false, err
		}
		for _, row := range rows {
			if t := h.tailnet.target(ns.String(), row.Job.Delivery.WebhookURL); t.Tailnet {
				out = append(out, tailnetJob{Job: row, Target: t})
			}
		}
		if len(rows) < tailnetJobPageSize {
			return out, false, nil
		}
	}
	return out, true, nil
}

// servedVia reports whether r arrived on one of the node's own tailnet
// addresses, i.e. through the tailnet listener.
func servedVia(r *http.Request, self []netip.Addr) bool {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || local == nil {
		return false
	}
	ap, err := netip.ParseAddrPort(local.String())
	if err != nil {
		return false
	}
	for _, s := range self {
		if s.Unmap() == ap.Addr().Unmap() {
			return true
		}
	}
	return false
}
