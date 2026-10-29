package webadmin

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/service"
	"github.com/djlord-it/cronlite/internal/tailnet"
)

const testPeer = "100.64.0.9:51234"

func alice(namespaces ...string) tailnet.Identity {
	id := tailnet.Identity{LoginName: "alice@example.com", DisplayName: "Alice", NodeName: "laptop." + testSuffix}
	if namespaces != nil {
		raw, _ := json.Marshal(map[string][]string{"namespaces": namespaces})
		id.AdminCaps = []json.RawMessage{raw}
	}
	return id
}

func identityConfig(node *fakeTailnetNode) TailnetConfig {
	return TailnetConfig{Built: true, Listen: true, IdentityLogin: true, Node: node}
}

// overTailnet marks r as accepted by the tailnet listener from testPeer.
func overTailnet(r *http.Request) *http.Request {
	r = r.WithContext(tailnet.WithViaTailnet(r.Context()))
	r.RemoteAddr = testPeer
	return r
}

func getLogin(t *testing.T, tc TailnetConfig, via bool) string {
	t.Helper()
	h := newTailnetTestHandler(t, nil, &fakeAdminSessionStore{}, tc)
	r := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	if via {
		r = overTailnet(r)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/login status = %d", rec.Code)
	}
	return rec.Body.String()
}

func TestLoginPageTailnetSignIn(t *testing.T) {
	tests := []struct {
		name     string
		node     *fakeTailnetNode
		tc       func(*fakeTailnetNode) TailnetConfig
		via      bool
		want     []string
		unwanted []string
	}{
		{
			name: "one namespace", node: &fakeTailnetNode{id: alice("billing")}, via: true,
			want: []string{`action="/admin/login/tailscale"`, "Continue as alice@example.com", `name="namespace" value="billing"`, "Alice"},
		},
		{
			name: "several namespaces", node: &fakeTailnetNode{id: alice("ops", "billing")}, via: true,
			want: []string{`<select id="ts_namespace" name="namespace"><option>billing</option><option>ops</option></select>`},
		},
		{
			name: "no grant", node: &fakeTailnetNode{id: alice()}, via: true,
			want:     []string{"with no CronLite namespace", tailnet.AdminCapability},
			unwanted: []string{"/admin/login/tailscale"},
		},
		{
			name: "invalid grant", node: &fakeTailnetNode{id: alice("*")}, via: true,
			want:     []string{"the grant is invalid"},
			unwanted: []string{"/admin/login/tailscale"},
		},
		{
			name: "tagged device", node: &fakeTailnetNode{id: tailnet.Identity{Tagged: true, AdminCaps: alice("billing").AdminCaps}}, via: true,
			want:     []string{"This device is tagged"},
			unwanted: []string{"/admin/login/tailscale"},
		},
		{
			name: "whois fails", node: &fakeTailnetNode{whoisErr: errors.New("boom")}, via: true,
			want:     []string{"could not be checked"},
			unwanted: []string{"/admin/login/tailscale", "boom"},
		},
		{
			name: "public listener", node: &fakeTailnetNode{id: alice("billing")}, via: false,
			unwanted: []string{"/admin/login/tailscale", "alice@example.com"},
		},
		{
			name: "flag off", node: &fakeTailnetNode{id: alice("billing")}, via: true,
			tc:       func(n *fakeTailnetNode) TailnetConfig { tc := identityConfig(n); tc.IdentityLogin = false; return tc },
			unwanted: []string{"/admin/login/tailscale", "alice@example.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := identityConfig(tt.node)
			if tt.tc != nil {
				tc = tt.tc(tt.node)
			}
			body := getLogin(t, tc, tt.via)
			assertContains(t, body, tt.want...)
			assertNotContains(t, body, tt.unwanted...)
			assertContains(t, body, `name="api_key"`)
			if tt.via && tc.IdentityLogin && tt.node.whoisArg != testPeer {
				t.Errorf("WhoIs called with %q, want %q", tt.node.whoisArg, testPeer)
			}
			if !tt.via && tt.node.whoisArg != "" {
				t.Errorf("WhoIs called for a public request (%q)", tt.node.whoisArg)
			}
		})
	}
}

func tailnetLoginRequest(namespace string, via bool) *http.Request {
	form := url.Values{"csrf_token": {"public-csrf"}, "namespace": {namespace}}
	r := httptest.NewRequest(http.MethodPost, "/admin/login/tailscale", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: publicCSRFCookieName, Value: "public-csrf"})
	if via {
		r = overTailnet(r)
	}
	return r
}

func TestTailnetLoginStartsSession(t *testing.T) {
	svc := &fakeAdminService{hasKeys: false}
	sessions := &fakeAdminSessionStore{}
	var logs bytes.Buffer
	h, err := NewHandler(Config{
		Service: svc, Sessions: sessions, Keys: &fakeKeyLookup{},
		SessionTTL: 12 * time.Hour, SessionAbsoluteTTL: 24 * time.Hour,
		Tailnet: identityConfig(&fakeTailnetNode{id: alice("billing", "ops")}),
		Logger:  log.New(&logs, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, tailnetLoginRequest("ops", true))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/jobs" {
		t.Fatalf("status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if svc.tailnetKeyNS != "ops" || svc.tailnetKeyLogin != "alice@example.com" {
		t.Fatalf("TailnetKey(%q, %q), want (ops, alice@example.com)", svc.tailnetKeyNS, svc.tailnetKeyLogin)
	}
	if sessions.created.APIKeyID == uuid.Nil {
		t.Fatal("no session created")
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), sessionCookieName+"=") {
		t.Fatal("no session cookie set")
	}
	if !strings.Contains(logs.String(), `event=tailnet_login login="alice@example.com" namespace="ops"`) {
		t.Errorf("login not logged: %s", logs.String())
	}
}

func TestTailnetLoginRefusals(t *testing.T) {
	tests := []struct {
		name       string
		node       *fakeTailnetNode
		namespace  string
		via        bool
		noCSRF     bool
		key        domain.APIKey
		wantStatus int
	}{
		{name: "public listener", node: &fakeTailnetNode{id: alice("billing")}, namespace: "billing", wantStatus: http.StatusNotFound},
		{name: "namespace not granted", node: &fakeTailnetNode{id: alice("billing")}, namespace: "ops", via: true, wantStatus: http.StatusForbidden},
		{name: "no grant", node: &fakeTailnetNode{id: alice()}, namespace: "billing", via: true, wantStatus: http.StatusForbidden},
		{name: "tagged device", node: &fakeTailnetNode{id: tailnet.Identity{Tagged: true, AdminCaps: alice("billing").AdminCaps}}, namespace: "billing", via: true, wantStatus: http.StatusForbidden},
		{name: "whois fails", node: &fakeTailnetNode{whoisErr: errors.New("boom")}, namespace: "billing", via: true, wantStatus: http.StatusForbidden},
		{name: "missing CSRF", node: &fakeTailnetNode{id: alice("billing")}, namespace: "billing", via: true, noCSRF: true, wantStatus: http.StatusForbidden},
		{name: "disabled key", node: &fakeTailnetNode{id: alice("billing")}, namespace: "billing", via: true,
			key: domain.APIKey{ID: uuid.New(), Namespace: "billing", TailnetLogin: "alice@example.com"}, wantStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAdminService{hasKeys: true, tailnetKey: tt.key}
			sessions := &fakeAdminSessionStore{}
			h := newTailnetTestHandler(t, svc, sessions, identityConfig(tt.node))
			r := tailnetLoginRequest(tt.namespace, tt.via)
			if tt.noCSRF {
				r.Header.Del("Cookie")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if sessions.created.APIKeyID != uuid.Nil {
				t.Fatal("session created")
			}
		})
	}
}

// identitySession returns a request carrying a session whose key was made by
// identity sign-in for alice in namespace billing.
func identitySession(sessions *fakeAdminSessionStore, via bool) *http.Request {
	r := authenticatedRequest(http.MethodGet, "/admin/jobs", nil, sessions)
	sessions.key = domain.APIKey{ID: uuid.New(), Namespace: "billing", Enabled: true, TailnetLogin: "alice@example.com"}
	if via {
		r = overTailnet(r)
	}
	return r
}

func TestTailnetSessionRecheckedEachRequest(t *testing.T) {
	tests := []struct {
		name   string
		node   *fakeTailnetNode
		via    bool
		flag   bool
		wantOK bool
	}{
		{name: "still granted", node: &fakeTailnetNode{id: alice("billing")}, via: true, flag: true, wantOK: true},
		{name: "grant removed", node: &fakeTailnetNode{id: alice("ops")}, via: true, flag: true},
		{name: "other person on the device", node: &fakeTailnetNode{id: tailnet.Identity{LoginName: "bob@example.com", AdminCaps: alice("billing").AdminCaps}}, via: true, flag: true},
		{name: "device now tagged", node: &fakeTailnetNode{id: tailnet.Identity{Tagged: true, AdminCaps: alice("billing").AdminCaps}}, via: true, flag: true},
		{name: "whois fails", node: &fakeTailnetNode{whoisErr: errors.New("boom")}, via: true, flag: true},
		{name: "public listener", node: &fakeTailnetNode{id: alice("billing")}, flag: true},
		{name: "flag turned off", node: &fakeTailnetNode{id: alice("billing")}, via: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions := &fakeAdminSessionStore{}
			tc := identityConfig(tt.node)
			tc.IdentityLogin = tt.flag
			h := newTailnetTestHandler(t, &fakeAdminService{hasKeys: true}, sessions, tc)
			r := identitySession(sessions, tt.via)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if tt.wantOK {
				if rec.Code != http.StatusOK || sessions.deletedHash != "" {
					t.Fatalf("status=%d deleted=%q, want the page", rec.Code, sessions.deletedHash)
				}
				return
			}
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
				t.Fatalf("status=%d location=%q, want login redirect", rec.Code, rec.Header().Get("Location"))
			}
			if sessions.deletedHash != service.HashToken("browser-session") {
				t.Fatalf("session not deleted (deleted %q)", sessions.deletedHash)
			}
		})
	}
}

func TestAPIKeyLoginRejectsTailnetKey(t *testing.T) {
	keys := &fakeKeyLookup{key: domain.APIKey{ID: uuid.New(), Namespace: "billing", Enabled: true, TailnetLogin: "alice@example.com"}}
	m := newSessionManager(&fakeAdminSessionStore{}, keys, time.Hour, 2*time.Hour, false, time.Now)
	_, err := m.login(t.Context(), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/admin/login", nil), "ec_whatever")
	if !errors.Is(err, errUnauthenticated) {
		t.Fatalf("login err = %v, want errUnauthenticated", err)
	}
}

func TestKeysPageMarksTailnetKeys(t *testing.T) {
	svc := &fakeAdminService{hasKeys: true, apiKeys: []domain.APIKey{
		{ID: uuid.New(), Namespace: "team", Label: "tailscale: alice@example.com", Enabled: true, TailnetLogin: "alice@example.com"},
		{ID: uuid.New(), Namespace: "team", Label: "worker", Enabled: true},
	}}
	body := getTailnet(t, "/admin/keys", svc, TailnetConfig{}, nil)
	if n := strings.Count(body, `<span class="badge info plain">Tailscale</span>`); n != 1 {
		t.Fatalf("Tailscale badge shown %d times, want 1", n)
	}
}

func TestSetupPageWithoutBootstrapToken(t *testing.T) {
	const missing = "Set ADMIN_BOOTSTRAP_TOKEN and restart CronLite before continuing."
	get := func(node *fakeTailnetNode) string {
		h := newTailnetTestHandler(t, &fakeAdminService{hasKeys: false}, &fakeAdminSessionStore{}, identityConfig(node))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, overTailnet(httptest.NewRequest(http.MethodGet, "/admin/setup", nil)))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /admin/setup status = %d", rec.Code)
		}
		return rec.Body.String()
	}

	body := get(&fakeTailnetNode{id: alice("billing")})
	assertContains(t, body, "Continue as alice@example.com")
	assertNotContains(t, body, missing)

	assertContains(t, get(&fakeTailnetNode{id: alice()}), missing)
}
