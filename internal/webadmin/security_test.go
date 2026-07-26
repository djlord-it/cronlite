package webadmin

import (
	"bytes"
	"errors"
	"fmt"
	stdhtml "html"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/google/uuid"
)

var (
	htmlCommentPattern   = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlStartTagPattern  = regexp.MustCompile(`(?is)<\s*([a-z][a-z0-9:-]*)\b((?:[^>"']|"[^"]*"|'[^']*')*)>`)
	htmlAttributePattern = regexp.MustCompile(
		"(?is)(?:^|\\s)([a-z_:][a-z0-9_.:-]*)\\s*=\\s*(?:\"([^\"]*)\"|'([^']*)'|([^\\s\"'=<>`]+))",
	)
	templateActionPattern     = regexp.MustCompile(`(?s){{.*?}}`)
	knownDynamicURLPattern    = regexp.MustCompile(`(?s)^\s*{{\s*\.(?:PreviousURL|NextURL)\s*}}\s*$`)
	cssCommentOrStringPattern = regexp.MustCompile(`(?s)/\*.*?\*/|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'`)
	cssLoadPattern            = regexp.MustCompile(`(?i)(?:@import\b|\burl\s*\()`)
)

var executableHTMLElements = map[string]struct{}{
	"base":   {},
	"embed":  {},
	"iframe": {},
	"object": {},
	"script": {},
}

var urlBearingHTMLAttributes = map[string]struct{}{
	"action":     {},
	"archive":    {},
	"background": {},
	"cite":       {},
	"codebase":   {},
	"data":       {},
	"formaction": {},
	"href":       {},
	"longdesc":   {},
	"manifest":   {},
	"ping":       {},
	"poster":     {},
	"profile":    {},
	"src":        {},
	"srcset":     {},
	"usemap":     {},
	"xlink:href": {},
}

func TestTemplatesContainNoExecutableOrExternalAssets(t *testing.T) {
	_, err := template.New("embedded-admin").Funcs(template.FuncMap{
		"formatTime": func(time.Time) string { return "" },
		"tagsText":   tagsText,
	}).ParseFS(embeddedFiles, "templates/*.html")
	if err != nil {
		t.Fatalf("parse embedded templates: %v", err)
	}

	for _, root := range []string{"templates", "assets"} {
		err := fs.WalkDir(embeddedFiles, root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			contents, readErr := fs.ReadFile(embeddedFiles, path)
			if readErr != nil {
				return readErr
			}
			var findings []string
			switch {
			case strings.HasSuffix(path, ".html"):
				findings = inspectHTMLAsset(string(contents))
			case strings.HasSuffix(path, ".css"):
				findings = inspectCSSAsset(string(contents))
			}
			for _, finding := range findings {
				t.Errorf("%s: %s", path, finding)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("inspect embedded %s: %v", root, err)
		}
	}
}

func inspectHTMLAsset(contents string) []string {
	var findings []string
	contents = htmlCommentPattern.ReplaceAllString(contents, "")
	for _, tag := range htmlStartTagPattern.FindAllStringSubmatch(contents, -1) {
		name, attributes := strings.ToLower(tag[1]), tag[2]
		if _, forbidden := executableHTMLElements[name]; forbidden {
			findings = append(findings, "contains executable <"+name+"> element")
		}
		for _, match := range htmlAttributePattern.FindAllStringSubmatch(attributes, -1) {
			attribute := strings.ToLower(match[1])
			if strings.HasPrefix(attribute, "on") && len(attribute) > 2 {
				findings = append(findings, "contains inline event handler "+attribute)
				continue
			}
			if _, carriesURL := urlBearingHTMLAttributes[attribute]; !carriesURL {
				continue
			}
			value := firstNonEmpty(match[2], match[3], match[4])
			for _, candidate := range htmlAttributeURLs(attribute, value) {
				if unsafeAssetURL(candidate) {
					findings = append(findings, "contains unsafe "+attribute+" URL")
				}
			}
		}
	}
	return findings
}

func inspectCSSAsset(contents string) []string {
	contents = cssCommentOrStringPattern.ReplaceAllString(contents, "")
	if cssLoadPattern.MatchString(contents) {
		return []string{"contains CSS runtime asset load"}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func htmlAttributeURLs(attribute, value string) []string {
	switch attribute {
	case "srcset":
		var urls []string
		for _, candidate := range strings.Split(value, ",") {
			if fields := strings.Fields(candidate); len(fields) > 0 {
				urls = append(urls, fields[0])
			}
		}
		return urls
	case "archive", "ping":
		return strings.Fields(value)
	default:
		return []string{value}
	}
}

func unsafeAssetURL(value string) bool {
	value = strings.TrimSpace(stdhtml.UnescapeString(value))
	staticValue := strings.TrimSpace(templateActionPattern.ReplaceAllString(value, ""))
	if staticValue == "" {
		return !knownDynamicURLPattern.MatchString(value)
	}
	value = staticValue
	if strings.HasPrefix(value, "//") || strings.HasPrefix(value, `\\`) {
		return true
	}
	parsed, err := url.Parse(value)
	return err != nil || parsed.Scheme != "" || parsed.Host != ""
}

func TestHTMLAssetInspectionDistinguishesLoadingSurfacesFromInertText(t *testing.T) {
	tests := []struct {
		name       string
		html       string
		wantUnsafe bool
	}{
		{
			name: "allows inert URL-shaped placeholder text",
			html: `<input type="url" placeholder="https://example.com/hook"><p>Try https://example.com</p>`,
		},
		{
			name: "allows same-origin and fragment URLs",
			html: `<a href="/admin/jobs">Jobs</a><a href="#details">Details</a><form action="{{if .Edit}}/admin/jobs/{{.ID}}/edit{{else}}/admin/jobs/new{{end}}">`,
		},
		{
			name: "allows known pagination URL fields",
			html: `<a href="{{ .PreviousURL }}">Previous</a><a href="{{.NextURL}}">Next</a>`,
		},
		{
			name:       "rejects unknown wholly dynamic URL",
			html:       `<a href="{{.ExternalURL}}">External</a>`,
			wantUnsafe: true,
		},
		{name: "rejects script", html: `<ScRiPt src="/admin/assets/app.js"></sCrIpT>`, wantUnsafe: true},
		{name: "rejects iframe", html: `<iframe src="/admin/jobs"></iframe>`, wantUnsafe: true},
		{name: "rejects object", html: `<object data="/admin/file"></object>`, wantUnsafe: true},
		{name: "rejects embed", html: `<embed src="/admin/file">`, wantUnsafe: true},
		{name: "rejects base", html: `<base href="/admin/">`, wantUnsafe: true},
		{name: "rejects inline event handler", html: `<button ONCLICK="alert(1)">Run</button>`, wantUnsafe: true},
		{name: "rejects external src", html: `<img src="https://attacker.example/pixel">`, wantUnsafe: true},
		{name: "rejects protocol relative href", html: `<a href="//cdn.attacker.example/file">File</a>`, wantUnsafe: true},
		{name: "rejects javascript action", html: `<form action="JaVaScRiPt:alert(1)">`, wantUnsafe: true},
		{name: "rejects data formaction", html: `<button formaction="data:text/html,unsafe">`, wantUnsafe: true},
		{name: "rejects vbscript poster", html: `<video poster="vbscript:msgbox(1)">`, wantUnsafe: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := inspectHTMLAsset(tt.html)
			if tt.wantUnsafe && len(findings) == 0 {
				t.Fatalf("expected unsafe HTML finding for %q", tt.html)
			}
			if !tt.wantUnsafe && len(findings) != 0 {
				t.Fatalf("safe inert HTML produced findings: %v", findings)
			}
		})
	}
}

func TestCSSAssetInspectionRejectsRuntimeLoads(t *testing.T) {
	tests := []struct {
		name       string
		css        string
		wantUnsafe bool
	}{
		{name: "allows inert URL text", css: `.hint::after { content: "https://example.com/hook"; }`},
		{name: "allows ordinary declarations", css: `body { color: #123; font-family: sans-serif; }`},
		{name: "rejects import", css: `@IMPORT "https://attacker.example/theme.css";`, wantUnsafe: true},
		{name: "rejects external URL", css: `.x { background: url("https://attacker.example/pixel"); }`, wantUnsafe: true},
		{name: "rejects protocol relative URL", css: `.x { background: url(//cdn.attacker.example/pixel); }`, wantUnsafe: true},
		{name: "rejects data URL", css: `.x { background: url(data:image/svg+xml,unsafe); }`, wantUnsafe: true},
		{name: "rejects javascript URL", css: `.x { background: url(javascript:alert(1)); }`, wantUnsafe: true},
		{name: "rejects same-origin URL dependency", css: `.x { background: url("/admin/assets/other.css"); }`, wantUnsafe: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := inspectCSSAsset(tt.css)
			if tt.wantUnsafe && len(findings) == 0 {
				t.Fatalf("expected unsafe CSS finding for %q", tt.css)
			}
			if !tt.wantUnsafe && len(findings) != 0 {
				t.Fatalf("safe inert CSS produced findings: %v", findings)
			}
		})
	}
}

func assertRequiredSecurityHeaders(t *testing.T, header http.Header, secure bool) {
	t.Helper()
	const csp = "default-src 'self'; script-src 'none'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	required := map[string]string{
		"Content-Security-Policy":      csp,
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"X-Frame-Options":              "DENY",
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=()",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
	}
	for name, want := range required {
		if got := header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	gotHSTS, hasHSTS := header["Strict-Transport-Security"]
	if !secure && hasHSTS {
		t.Errorf("Strict-Transport-Security must be absent, got %q", gotHSTS)
	}
	if secure && header.Get("Strict-Transport-Security") != "max-age=31536000" {
		t.Errorf(
			"Strict-Transport-Security = %q, want %q",
			header.Get("Strict-Transport-Security"),
			"max-age=31536000",
		)
	}
}

func TestHandlerSecurityHeaders(t *testing.T) {
	for _, tt := range []struct {
		name         string
		cookieSecure bool
	}{
		{name: "insecure cookies omit HSTS", cookieSecure: false},
		{name: "secure cookies include HSTS", cookieSecure: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler := newTestHandlerWithOptions(
				t,
				nil,
				nil,
				nil,
				tt.cookieSecure,
				log.New(bytes.NewBuffer(nil), "", 0),
			)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/login", nil))

			assertRequiredSecurityHeaders(t, rec.Header(), tt.cookieSecure)
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestSecurityHeadersCoverAdminResponseMatrix(t *testing.T) {
	for _, tt := range []struct {
		name        string
		build       func(*testing.T) (http.Handler, *http.Request)
		wantStatus  int
		wantCaching string
	}{
		{
			name: "login 200",
			build: func(t *testing.T) (http.Handler, *http.Request) {
				return newTestHandler(t, nil, nil, nil),
					httptest.NewRequest(http.MethodGet, "/admin/login", nil)
			},
			wantStatus: http.StatusOK, wantCaching: "no-store",
		},
		{
			name: "CSS 200",
			build: func(t *testing.T) (http.Handler, *http.Request) {
				return newTestHandler(t, nil, nil, nil),
					httptest.NewRequest(http.MethodGet, "/admin/assets/admin.css", nil)
			},
			wantStatus: http.StatusOK, wantCaching: "public, max-age=86400",
		},
		{
			name: "unknown 404",
			build: func(t *testing.T) (http.Handler, *http.Request) {
				return newTestHandler(t, nil, nil, nil),
					httptest.NewRequest(http.MethodGet, "/admin/unknown", nil)
			},
			wantStatus: http.StatusNotFound, wantCaching: "no-store",
		},
		{
			name: "internal 500",
			build: func(t *testing.T) (http.Handler, *http.Request) {
				sessions := &fakeAdminSessionStore{}
				svc := &fakeAdminService{hasKeys: true, err: errors.New("repository unavailable")}
				handler := newTestHandler(t, svc, sessions, nil)
				return handler, authenticatedRequest(http.MethodGet, "/admin/jobs", nil, sessions)
			},
			wantStatus: http.StatusInternalServerError, wantCaching: "no-store",
		},
		{
			name: "redirect",
			build: func(t *testing.T) (http.Handler, *http.Request) {
				return newTestHandler(t, nil, nil, nil),
					httptest.NewRequest(http.MethodGet, "/admin", nil)
			},
			wantStatus: http.StatusTemporaryRedirect, wantCaching: "no-store",
		},
		{
			name: "logout",
			build: func(t *testing.T) (http.Handler, *http.Request) {
				sessions := &fakeAdminSessionStore{}
				handler := newTestHandler(t, nil, sessions, nil)
				req := authenticatedRequest(
					http.MethodPost,
					"/admin/logout",
					url.Values{"csrf_token": {"csrf-token"}},
					sessions,
				)
				return handler, req
			},
			wantStatus: http.StatusSeeOther, wantCaching: "no-store",
		},
		{
			name: "cross-origin 403",
			build: func(t *testing.T) (http.Handler, *http.Request) {
				sessions := &fakeAdminSessionStore{}
				handler := newTestHandler(t, nil, sessions, nil)
				req := authenticatedRequest(
					http.MethodPost,
					"/admin/jobs/"+uuid.New().String()+"/pause",
					url.Values{"csrf_token": {"csrf-token"}},
					sessions,
				)
				req.Header.Set("Origin", "https://attacker.example")
				req.Header.Set("Sec-Fetch-Site", "cross-site")
				return handler, req
			},
			wantStatus: http.StatusForbidden, wantCaching: "no-store",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler, req := tt.build(t)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			assertRequiredSecurityHeaders(t, rec.Header(), false)
			if got := rec.Header().Get("Cache-Control"); got != tt.wantCaching {
				t.Errorf("Cache-Control = %q, want %q", got, tt.wantCaching)
			}
		})
	}
}

func TestHandlerCrossOriginProtection(t *testing.T) {
	for _, tt := range []struct {
		name         string
		method       string
		path         func(uuid.UUID) string
		csrfToken    string
		headers      map[string]string
		wantStatus   int
		wantMutation bool
		wantBody     string
	}{
		{
			name:       "mismatching Origin alone rejects unsafe POST",
			method:     http.MethodPost,
			path:       func(id uuid.UUID) string { return "/admin/jobs/" + id.String() + "/pause" },
			csrfToken:  "csrf-token",
			headers:    map[string]string{"Origin": "https://attacker.example"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "cross-site fetch metadata alone rejects unsafe POST",
			method:     http.MethodPost,
			path:       func(id uuid.UUID) string { return "/admin/jobs/" + id.String() + "/pause" },
			csrfToken:  "csrf-token",
			headers:    map[string]string{"Sec-Fetch-Site": "cross-site"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "same-origin unsafe request is accepted",
			method:     http.MethodPost,
			path:       func(id uuid.UUID) string { return "/admin/jobs/" + id.String() + "/pause" },
			csrfToken:  "csrf-token",
			headers:    map[string]string{"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"},
			wantStatus: http.StatusSeeOther, wantMutation: true,
		},
		{
			name:       "safe cross-site GET is accepted",
			method:     http.MethodGet,
			path:       func(uuid.UUID) string { return "/admin/jobs" },
			headers:    map[string]string{"Origin": "https://attacker.example", "Sec-Fetch-Site": "cross-site"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing browser metadata reaches CSRF validation",
			method:     http.MethodPost,
			path:       func(id uuid.UUID) string { return "/admin/jobs/" + id.String() + "/pause" },
			csrfToken:  "wrong-token",
			wantStatus: http.StatusForbidden,
			wantBody:   "invalid CSRF token",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sessions := &fakeAdminSessionStore{}
			svc := &fakeAdminService{hasKeys: true}
			handler := newTestHandler(t, svc, sessions, nil)
			jobID := uuid.New()
			var form url.Values
			if tt.method == http.MethodPost {
				form = url.Values{"csrf_token": {tt.csrfToken}}
			}
			req := authenticatedRequest(tt.method, tt.path(jobID), form, sessions)
			for name, value := range tt.headers {
				req.Header.Set(name, value)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantMutation && svc.actionID != jobID {
				t.Fatalf("service mutated job %s, want %s", svc.actionID, jobID)
			}
			if !tt.wantMutation && svc.actionID != uuid.Nil {
				t.Fatalf("request unexpectedly mutated job %s", svc.actionID)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("response body %q does not contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestInternalServiceErrorIsSanitizedEverywhere(t *testing.T) {
	const sentinel = "ec_log-secret"
	internalErr := errors.New("repository failed with " + sentinel)
	var logs bytes.Buffer
	sessions := &fakeAdminSessionStore{}
	svc := &fakeAdminService{hasKeys: true, err: internalErr}
	handler := newTestHandlerWithOptions(
		t,
		svc,
		sessions,
		nil,
		false,
		log.New(&logs, "", 0),
	)
	req := authenticatedRequest(http.MethodGet, "/admin/jobs", nil, sessions)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(logs.String(), internalErr.Error()) {
		t.Fatalf("raw error was logged: %s", logs.String())
	}
	for surface, value := range map[string]string{
		"logs":             logs.String(),
		"response headers": fmt.Sprint(rec.Header()),
		"HTML":             rec.Body.String(),
	} {
		if strings.Contains(value, sentinel) {
			t.Fatalf("%s leaked sentinel %q", surface, sentinel)
		}
	}
	if !strings.Contains(logs.String(), "method=GET path=/admin/jobs error_class=internal") {
		t.Fatalf("sanitized log missing stable fields: %q", logs.String())
	}
	requestIDMatch := regexp.MustCompile(`request_id=([0-9a-f]{24})\b`).FindStringSubmatch(logs.String())
	if len(requestIDMatch) != 2 {
		t.Fatalf("log request ID must be 24 hex characters: %q", logs.String())
	}
	if !strings.Contains(rec.Body.String(), requestIDMatch[1]) {
		t.Fatalf("rendered error response does not contain logged request ID %q", requestIDMatch[1])
	}
}

func TestErrorClassUsesStableCategoriesForWrappedErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want string
	}{
		{name: "not found", err: fmt.Errorf("repository: %w", domain.ErrJobNotFound), want: "not_found"},
		{name: "namespace mismatch is not found", err: fmt.Errorf("repository: %w", domain.ErrNamespaceMismatch), want: "not_found"},
		{name: "validation", err: fmt.Errorf("service: %w", domain.ErrInvalidWebhookURL), want: "validation"},
		{name: "namespace required is internal", err: fmt.Errorf("service: %w", domain.ErrNamespaceRequired), want: "internal"},
		{name: "internal", err: errors.New("database unavailable"), want: "internal"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorClass(tt.err); got != tt.want {
				t.Fatalf("errorClass() = %q, want %q", got, tt.want)
			}
		})
	}
}
