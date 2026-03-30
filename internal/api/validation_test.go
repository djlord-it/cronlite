package api

import (
	"strings"
	"testing"
)

func TestValidateCreateJob_ValidRequest(t *testing.T) {
	req := LegacyCreateJobRequest{
		Name:           "test-job",
		CronExpression: "0 * * * *",
		Timezone:       "UTC",
		WebhookURL:     "https://example.com/webhook",
		WebhookTimeout: 30,
	}

	if err := validateCreateJob(req); err != nil {
		t.Errorf("valid request should not return error, got: %v", err)
	}
}

func TestValidateCreateJob_RequiredFields(t *testing.T) {
	base := LegacyCreateJobRequest{
		Name:           "test-job",
		CronExpression: "0 * * * *",
		Timezone:       "UTC",
		WebhookURL:     "https://example.com/webhook",
	}

	tests := []struct {
		name    string
		modify  func(r *LegacyCreateJobRequest)
		wantErr string
	}{
		{
			name:    "missing name",
			modify:  func(r *LegacyCreateJobRequest) { r.Name = "" },
			wantErr: "name is required",
		},
		{
			name:    "missing cron_expression",
			modify:  func(r *LegacyCreateJobRequest) { r.CronExpression = "" },
			wantErr: "cron_expression is required",
		},
		{
			name:    "missing timezone",
			modify:  func(r *LegacyCreateJobRequest) { r.Timezone = "" },
			wantErr: "timezone is required",
		},
		{
			name:    "missing webhook_url",
			modify:  func(r *LegacyCreateJobRequest) { r.WebhookURL = "" },
			wantErr: "webhook_url is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := base
			tt.modify(&req)
			err := validateCreateJob(req)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateCreateJob_InvalidCron(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"non-parseable", "invalid"},
		{"four fields", "* * * *"},
		{"invalid minute", "60 * * * *"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := LegacyCreateJobRequest{
				Name:           "test-job",
				CronExpression: tt.expr,
				Timezone:       "UTC",
				WebhookURL:     "https://example.com/webhook",
			}
			err := validateCreateJob(req)
			if err == nil {
				t.Errorf("expected error for cron expression %q", tt.expr)
			}
		})
	}
}

func TestValidateWebhookURL_Valid(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"http", "http://example.com/webhook"},
		{"https", "https://example.com/webhook"},
		{"with path", "https://api.service.com/v1/webhooks/123"},
		{"public ip", "http://8.8.8.8:3000/callback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateWebhookURL(tt.url); err != nil {
				t.Errorf("validateWebhookURL(%q) = %v, want nil", tt.url, err)
			}
		})
	}
}

func TestValidateWebhookURL_SSRF_Blocked(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"loopback ipv4", "http://127.0.0.1/hook"},
		{"loopback ipv4 alt", "http://127.0.0.2:8080/hook"},
		{"localhost", "http://localhost/hook"},
		{"localhost with port", "http://localhost:3000/hook"},
		{"private 10.x", "http://10.0.0.1/hook"},
		{"private 172.16.x", "http://172.16.0.1/hook"},
		{"private 192.168.x", "http://192.168.1.1/hook"},
		{"link-local", "http://169.254.169.254/latest/meta-data/"},
		{"ipv6 loopback", "http://[::1]/hook"},
		{"metadata gcp", "http://metadata.google.internal/computeMetadata/v1/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateWebhookURL(tt.url); err == nil {
				t.Errorf("validateWebhookURL(%q) should return error for SSRF", tt.url)
			}
		})
	}
}

func TestValidateWebhookURL_Invalid(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"ftp scheme", "ftp://example.com"},
		{"no host", "http://"},
		{"no scheme", "example.com/webhook"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateWebhookURL(tt.url); err == nil {
				t.Errorf("validateWebhookURL(%q) should return error", tt.url)
			}
		})
	}
}

func TestValidateTimezone_Valid(t *testing.T) {
	zones := []string{"UTC", "America/New_York", "Europe/London", "Asia/Tokyo"}
	for _, tz := range zones {
		t.Run(tz, func(t *testing.T) {
			if err := validateTimezone(tz); err != nil {
				t.Errorf("validateTimezone(%q) = %v, want nil", tz, err)
			}
		})
	}
}

func TestValidateTimezone_Invalid(t *testing.T) {
	if err := validateTimezone("Invalid/Zone"); err == nil {
		t.Error("validateTimezone(Invalid/Zone) should return error")
	}
}

func TestValidateWebhookTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout int
		wantErr bool
	}{
		{"zero (default)", 0, false},
		{"minimum", 1, false},
		{"mid-range", 30, false},
		{"maximum", 60, false},
		{"below minimum", -1, true},
		{"above maximum", 61, true},
		{"way above", 1000, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := LegacyCreateJobRequest{
				Name:           "test-job",
				CronExpression: "0 * * * *",
				Timezone:       "UTC",
				WebhookURL:     "https://example.com/webhook",
				WebhookTimeout: tt.timeout,
			}
			err := validateCreateJob(req)
			if tt.wantErr && err == nil {
				t.Errorf("expected error for timeout=%d", tt.timeout)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for timeout=%d: %v", tt.timeout, err)
			}
		})
	}
}

func TestValidateAnalytics_RetentionBounds(t *testing.T) {
	tests := []struct {
		name      string
		retention int
		wantErr   bool
	}{
		{"zero", 0, false},
		{"one day", 86400, false},
		{"seven days max", 604800, false},
		{"negative", -1, true},
		{"exceeds max", 604801, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAnalytics(&AnalyticsRequest{RetentionSeconds: tt.retention})
			if tt.wantErr && err == nil {
				t.Errorf("expected error for retention=%d", tt.retention)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for retention=%d: %v", tt.retention, err)
			}
		})
	}
}

func TestValidateAnalytics_Nil(t *testing.T) {
	req := LegacyCreateJobRequest{
		Name:           "test-job",
		CronExpression: "0 * * * *",
		Timezone:       "UTC",
		WebhookURL:     "https://example.com/webhook",
		Analytics:      nil,
	}

	if err := validateCreateJob(req); err != nil {
		t.Errorf("nil analytics should not return error, got: %v", err)
	}
}
