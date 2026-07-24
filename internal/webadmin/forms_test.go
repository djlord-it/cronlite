package webadmin

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestParseTags(t *testing.T) {
	tags, err := parseTags("env=prod\n team = platform\n\n")
	if err != nil {
		t.Fatalf("parseTags: %v", err)
	}
	if len(tags) != 2 || tags[0].Key != "env" || tags[0].Value != "prod" || tags[1].Key != "team" {
		t.Fatalf("unexpected tags: %#v", tags)
	}
}

func TestParseTagsRejectsMalformedAndDuplicateLines(t *testing.T) {
	for _, input := range []string{"missing-separator", "=value", "key=", "env=prod\nenv=dev"} {
		t.Run(input, func(t *testing.T) {
			if _, err := parseTags(input); err == nil {
				t.Fatalf("expected %q to fail", input)
			}
		})
	}
}

func TestParseCreateJobForm(t *testing.T) {
	form := url.Values{
		"name":            {"daily-report"},
		"cron_expression": {"0 9 * * *"},
		"timezone":        {"America/Toronto"},
		"webhook_url":     {"https://example.com/hook"},
		"webhook_secret":  {"secret"},
		"timeout_seconds": {"45"},
		"tags":            {"env=prod"},
	}
	req := httptest.NewRequest("POST", "/admin/jobs/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	input, values, err := parseCreateJobForm(req)
	if err != nil {
		t.Fatalf("parseCreateJobForm: %v", err)
	}
	if input.Name != "daily-report" || input.Timeout.Seconds() != 45 || len(input.Tags) != 1 {
		t.Fatalf("unexpected input: %#v", input)
	}
	if values.Name != "daily-report" {
		t.Fatalf("expected preserved values, got %#v", values)
	}
}

func TestParseUpdateJobFormSecretSemantics(t *testing.T) {
	t.Run("blank means unchanged", func(t *testing.T) {
		form := url.Values{
			"name":            {"job"},
			"cron_expression": {"0 * * * *"},
			"timezone":        {"UTC"},
			"webhook_url":     {"https://example.com"},
			"webhook_secret":  {""},
			"timeout_seconds": {"30"},
		}
		req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		input, _, err := parseUpdateJobForm(req)
		if err != nil {
			t.Fatalf("parseUpdateJobForm: %v", err)
		}
		if input.Secret != nil {
			t.Fatal("blank secret should remain unchanged")
		}
	})

	t.Run("clear checkbox writes empty secret", func(t *testing.T) {
		form := url.Values{
			"name":                 {"job"},
			"cron_expression":      {"0 * * * *"},
			"timezone":             {"UTC"},
			"webhook_url":          {"https://example.com"},
			"webhook_secret":       {""},
			"clear_webhook_secret": {"true"},
			"timeout_seconds":      {"30"},
		}
		req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		input, _, err := parseUpdateJobForm(req)
		if err != nil {
			t.Fatalf("parseUpdateJobForm: %v", err)
		}
		if input.Secret == nil || *input.Secret != "" {
			t.Fatalf("expected explicit empty secret, got %#v", input.Secret)
		}
	})

	t.Run("new and clear conflict", func(t *testing.T) {
		form := url.Values{
			"name":                 {"job"},
			"cron_expression":      {"0 * * * *"},
			"timezone":             {"UTC"},
			"webhook_url":          {"https://example.com"},
			"webhook_secret":       {"new"},
			"clear_webhook_secret": {"true"},
			"timeout_seconds":      {"30"},
		}
		req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		if _, _, err := parseUpdateJobForm(req); err == nil {
			t.Fatal("expected conflicting secret controls to fail")
		}
	})
}
