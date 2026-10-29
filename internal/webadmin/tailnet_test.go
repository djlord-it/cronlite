package webadmin

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/tailnet"
	"github.com/djlord-it/cronlite/internal/webhookurl"
	"github.com/google/uuid"
)

const testSuffix = "example-tailnet.ts.net"

var testSelf = netip.MustParseAddr("100.64.0.7")

type fakeTailnetNode struct {
	info    tailnet.Info
	infoErr error
}

func (*fakeTailnetNode) Suffix() string          { return testSuffix }
func (*fakeTailnetNode) SelfAddrs() []netip.Addr { return []netip.Addr{testSelf} }
func (*fakeTailnetNode) CertDomain() string      { return "cronlite." + testSuffix }
func (*fakeTailnetNode) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return nil, errors.New("not used")
}
func (*fakeTailnetNode) Dial(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("not used")
}
func (n *fakeTailnetNode) Info(context.Context) (tailnet.Info, error) { return n.info, n.infoErr }

func runningNode() *fakeTailnetNode {
	return &fakeTailnetNode{info: tailnet.Info{
		BackendState: "Running", Online: true, DNSName: "cronlite." + testSuffix,
		TailnetName: "example.com", LoginName: "tagged-devices", Tags: []string{"tag:cronlite"},
	}}
}

func mustPolicy(t *testing.T, raw string) *webhookurl.TailnetPolicy {
	t.Helper()
	p, err := webhookurl.ParseTailnetPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func newTailnetTestHandler(t *testing.T, svc *fakeAdminService, sessions *fakeAdminSessionStore, tc TailnetConfig) http.Handler {
	t.Helper()
	if svc == nil {
		svc = &fakeAdminService{hasKeys: true}
	}
	handler, err := NewHandler(Config{
		Service:            svc,
		Sessions:           sessions,
		Keys:               &fakeKeyLookup{},
		SessionTTL:         12 * time.Hour,
		SessionAbsoluteTTL: 24 * time.Hour,
		Tailnet:            tc,
		Now:                func() time.Time { return time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC) },
		Logger:             log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return handler
}

func jobRow(name, webhookURL string) domain.JobWithSchedule {
	return domain.JobWithSchedule{
		Job:      domain.Job{ID: uuid.New(), Namespace: "team", Name: name, Enabled: true, Delivery: domain.DeliveryConfig{WebhookURL: webhookURL}},
		Schedule: domain.Schedule{CronExpression: "*/5 * * * *", Timezone: "UTC"},
	}
}

// getTailnet requests path as namespace "team" and returns the body.
func getTailnet(t *testing.T, path string, svc *fakeAdminService, tc TailnetConfig, ctx context.Context) string {
	t.Helper()
	sessions := &fakeAdminSessionStore{}
	handler := newTailnetTestHandler(t, svc, sessions, tc)
	req := authenticatedRequest(http.MethodGet, path, nil, sessions)
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func assertContains(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("body missing %q", w)
		}
	}
}

func assertNotContains(t *testing.T, body string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(body, u) {
			t.Errorf("body must not contain %q", u)
		}
	}
}

func TestTailscalePageRequiresAuth(t *testing.T) {
	handler := newTailnetTestHandler(t, nil, &fakeAdminSessionStore{}, TailnetConfig{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/tailscale", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("status=%d location=%q, want login redirect", rec.Code, rec.Header().Get("Location"))
	}
}

func TestTailscalePageNotBuiltIn(t *testing.T) {
	body := getTailnet(t, "/admin/tailscale", nil, TailnetConfig{}, nil)
	assertContains(t, body, "Tailscale is not built into this binary", "-tags tailscale", `href="/admin/tailscale" aria-current="page"`)
	assertNotContains(t, body, "MagicDNS name", "Jobs calling tailnet hosts")
}

func TestTailscalePageBuiltButOff(t *testing.T) {
	body := getTailnet(t, "/admin/tailscale", nil, TailnetConfig{Built: true}, nil)
	assertContains(t, body, "Tailscale is built in but not enabled", "TS_STATE_DIR")
	assertNotContains(t, body, "not built into this binary", "MagicDNS name")
}

func TestTailscalePageRunningNode(t *testing.T) {
	svc := &fakeAdminService{hasKeys: true, jobs: []domain.JobWithSchedule{
		jobRow("pay-invoices", "https://invoices."+testSuffix+"/hook"),
		jobRow("read-secrets", "https://secret."+testSuffix+"/hook"),
		jobRow("public-job", "https://api.example.com/hook"),
	}}
	tc := TailnetConfig{
		Built: true, Delivery: true, Listen: true, Hostname: "cronlite", PublicAddr: ":8080",
		Policy: mustPolicy(t, "team=invoices;other=secret;*=status"),
		Node:   runningNode(),
	}
	body := getTailnet(t, "/admin/tailscale", svc, tc, nil)

	assertContains(t, body,
		"Connected", "cronlite."+testSuffix, testSuffix, "100.64.0.7", "tag:cronlite", "example.com",
		"https://cronlite."+testSuffix, "On <code>:8080</code>", "Over the public listener",
		`<span class="tag">invoices</span>`, `<span class="tag">status</span>`,
		"pay-invoices", "read-secrets", "Tailnet · allowed", "Tailnet · blocked",
	)
	// Other namespaces' allowlist entries stay hidden.
	assertNotContains(t, body, `<span class="tag">secret</span>`, "other=", "public-job", "Key expiry soon", "not running")
}

func TestTailscalePageNodeProblems(t *testing.T) {
	tests := []struct {
		name string
		node *fakeTailnetNode
		want string
	}{
		{"needs login", &fakeTailnetNode{info: tailnet.Info{BackendState: "NeedsLogin"}}, "The tailnet node is not running (state: NeedsLogin)"},
		{"status error", &fakeTailnetNode{infoErr: errors.New("boom")}, "did not report its status"},
		{"key expiring", &fakeTailnetNode{info: tailnet.Info{BackendState: "Running", KeyExpiry: time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)}}, "The node key expires 2026-07-30"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := getTailnet(t, "/admin/tailscale", nil, TailnetConfig{Built: true, Listen: true, Node: tt.node}, nil)
			assertContains(t, body, tt.want)
			assertNotContains(t, body, "boom")
		})
	}
}

func TestTailscalePageDeliveryOffAndTailnetOnly(t *testing.T) {
	ctx := tailnet.WithViaTailnet(context.Background())
	tc := TailnetConfig{Built: true, Listen: true, DisablePublicListener: true, Node: runningNode()}
	body := getTailnet(t, "/admin/tailscale", nil, tc, ctx)
	assertContains(t, body, "Over the tailnet", "Disabled <small", "TS_DISABLE_PUBLIC_LISTENER=true", "Off: tailnet hosts are not reachable")
	assertNotContains(t, body, "This namespace may call")
}

func TestTailscalePageMarksScanLimit(t *testing.T) {
	jobs := make([]domain.JobWithSchedule, tailnetJobPageSize)
	for i := range jobs {
		jobs[i] = jobRow("public", "https://api.example.com/hook")
	}
	svc := &fakeAdminService{hasKeys: true, jobs: jobs}
	body := getTailnet(t, "/admin/tailscale", svc, TailnetConfig{Built: true, Listen: true, Node: runningNode()}, nil)
	assertContains(t, body, "Only the first 2,000 jobs were checked.", "No job in this namespace calls a tailnet host.")
	if svc.listFilter.Namespace != "team" || svc.listFilter.Offset != tailnetJobScanLimit-tailnetJobPageSize {
		t.Fatalf("last list filter = %+v", svc.listFilter)
	}
}

func TestJobsShowTailnetBadges(t *testing.T) {
	allowed := jobRow("allowed-job", "https://invoices."+testSuffix+"/hook")
	blocked := jobRow("blocked-job", "https://ledger."+testSuffix+"/hook")
	public := jobRow("public-job", "https://api.example.com/hook")
	tc := TailnetConfig{Built: true, Delivery: true, Policy: mustPolicy(t, "team=invoices"), Node: runningNode()}

	for _, view := range []string{"table", "board"} {
		t.Run(view, func(t *testing.T) {
			svc := &fakeAdminService{hasKeys: true, jobs: []domain.JobWithSchedule{allowed, blocked, public}}
			body := getTailnet(t, "/admin/jobs?view="+view, svc, tc, nil)
			if got := strings.Count(body, "Tailnet · allowed"); got != 1 {
				t.Errorf("allowed badges = %d, want 1", got)
			}
			if got := strings.Count(body, "Tailnet · blocked"); got != 1 {
				t.Errorf("blocked badges = %d, want 1", got)
			}
		})
	}

	t.Run("no node", func(t *testing.T) {
		svc := &fakeAdminService{hasKeys: true, jobs: []domain.JobWithSchedule{allowed}}
		body := getTailnet(t, "/admin/jobs", svc, TailnetConfig{}, nil)
		assertNotContains(t, body, "Tailnet ·")
	})
}

func TestJobDetailExplainsBlockedTailnetHost(t *testing.T) {
	row := jobRow("blocked-job", "https://ledger."+testSuffix+"/hook")
	svc := &fakeAdminService{hasKeys: true, job: row.Job, schedule: row.Schedule}
	tc := TailnetConfig{Built: true, Delivery: true, Policy: mustPolicy(t, "team=invoices"), Node: runningNode()}
	body := getTailnet(t, "/admin/jobs/"+row.Job.ID.String(), svc, tc, nil)
	assertContains(t, body, "Tailnet · blocked", "This namespace may not call this host (WEBHOOK_TAILNET_HOSTS).", `href="/admin/tailscale"`)

	tc.Delivery, tc.Policy = false, nil
	body = getTailnet(t, "/admin/jobs/"+row.Job.ID.String(), svc, tc, nil)
	assertContains(t, body, "Tailnet delivery is off (WEBHOOK_ALLOW_TAILNET).")
}

func TestJobFormTailnetHint(t *testing.T) {
	tests := []struct {
		name      string
		tc        TailnetConfig
		want      []string
		unwanted  []string
		wantNoHit bool
	}{
		{
			name:     "allowed hosts",
			tc:       TailnetConfig{Built: true, Delivery: true, Policy: mustPolicy(t, "team=invoices;other=secret;*=status"), Node: runningNode()},
			want:     []string{`aria-describedby="webhook_help"`, "<code>invoices." + testSuffix + "</code>, <code>status." + testSuffix + "</code>"},
			unwanted: []string{"secret." + testSuffix},
		},
		{
			name: "every host",
			tc:   TailnetConfig{Built: true, Delivery: true, Policy: mustPolicy(t, "*=*"), Node: runningNode()},
			want: []string{"may call any tailnet host"},
		},
		{
			name: "no host",
			tc:   TailnetConfig{Built: true, Delivery: true, Policy: mustPolicy(t, "other=secret"), Node: runningNode()},
			want: []string{"may not call any tailnet host"},
		},
		{
			name: "delivery off",
			tc:   TailnetConfig{Built: true, Listen: true, Node: runningNode()},
			want: []string{"Tailnet delivery is off"},
		},
		{
			name:     "no node",
			tc:       TailnetConfig{Built: true},
			unwanted: []string{"webhook_help", "tailnet"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := getTailnet(t, "/admin/jobs/new", nil, tt.tc, nil)
			assertContains(t, body, tt.want...)
			assertNotContains(t, body, tt.unwanted...)
		})
	}
}
