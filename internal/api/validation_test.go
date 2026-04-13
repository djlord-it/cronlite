package api

import (
	"testing"
)

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
