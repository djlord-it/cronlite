package webadmin

import (
	"net/http/httptest"
	"net/url"
	"strconv"
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

func TestJobFormValidationPreservesSubmittedValues(t *testing.T) {
	tests := []struct {
		name string
		form url.Values
		want string
	}{
		{
			name: "required fields",
			form: url.Values{
				"name":            {"  submitted-name  "},
				"cron_expression": {""},
				"timezone":        {"UTC"},
				"webhook_url":     {"https://example.com"},
				"timeout_seconds": {"30"},
			},
			want: "required",
		},
		{
			name: "non numeric timeout",
			form: url.Values{
				"name":            {"submitted-name"},
				"cron_expression": {"0 * * * *"},
				"timezone":        {"UTC"},
				"webhook_url":     {"https://example.com"},
				"timeout_seconds": {"soon"},
			},
			want: "timeout",
		},
		{
			name: "timeout above maximum",
			form: url.Values{
				"name":            {"submitted-name"},
				"cron_expression": {"0 * * * *"},
				"timezone":        {"UTC"},
				"webhook_url":     {"https://example.com"},
				"timeout_seconds": {"61"},
			},
			want: "timeout",
		},
		{
			name: "invalid tags",
			form: url.Values{
				"name":            {"submitted-name"},
				"cron_expression": {"0 * * * *"},
				"timezone":        {"UTC"},
				"webhook_url":     {"https://example.com"},
				"timeout_seconds": {"30"},
				"tags":            {"missing-separator"},
			},
			want: "key=value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", strings.NewReader(tt.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			_, values, err := parseCreateJobForm(req)

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want text %q", err, tt.want)
			}
			if values.Name != "submitted-name" {
				t.Fatalf("submitted name was not preserved: %#v", values)
			}
		})
	}
}

func FuzzParseTags(f *testing.F) {
	for _, seed := range []string{
		"env=prod",
		"bad",
		"a=one\na=two",
		"環境=本番\nチーム=基盤",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		tags, err := parseTags(raw)
		if err != nil {
			return
		}

		seen := make(map[string]struct{}, len(tags))
		for _, tag := range tags {
			if tag.Key == "" || tag.Key != strings.TrimSpace(tag.Key) {
				t.Fatalf("parseTags(%q) returned invalid key %q", raw, tag.Key)
			}
			if tag.Value == "" || tag.Value != strings.TrimSpace(tag.Value) {
				t.Fatalf("parseTags(%q) returned invalid value %q", raw, tag.Value)
			}
			if _, duplicate := seen[tag.Key]; duplicate {
				t.Fatalf("parseTags(%q) returned duplicate key %q", raw, tag.Key)
			}
			seen[tag.Key] = struct{}{}
		}

		roundTrip, err := parseTags(tagsText(tags))
		if err != nil {
			t.Fatalf("reparse parseTags(%q) output: %v", raw, err)
		}
		if len(roundTrip) != len(tags) {
			t.Fatalf("round trip length = %d, want %d", len(roundTrip), len(tags))
		}
		for i := range tags {
			if roundTrip[i] != tags[i] {
				t.Fatalf("round trip tag %d = %#v, want %#v", i, roundTrip[i], tags[i])
			}
		}
	})
}

func FuzzPositivePage(f *testing.F) {
	for _, seed := range []string{"", "1", "-1", "999999999999999999999999999999"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got := positivePage(raw)
		parsed, err := strconv.Atoi(raw)
		if err == nil && parsed > 0 {
			if got != parsed {
				t.Fatalf("positivePage(%q) = %d, want exact parsed value %d", raw, got, parsed)
			}
			return
		}
		if got < 1 {
			t.Fatalf("positivePage(%q) = %d, want >= 1", raw, got)
		}
	})
}
