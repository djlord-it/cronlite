package api

import (
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/robfig/cron/v3"
)

// Maximum retention: 7 days (604800 seconds)
const maxRetentionSeconds = 7 * 24 * 60 * 60

func validateCreateJob(req CreateJobRequest) error {
	if req.Name == "" {
		return fmt.Errorf("name is required")
	}

	if req.CronExpression == "" {
		return fmt.Errorf("cron_expression is required")
	}

	if err := validateCron(req.CronExpression); err != nil {
		return fmt.Errorf("invalid cron_expression: %w", err)
	}

	if req.Timezone == "" {
		return fmt.Errorf("timezone is required")
	}
	if err := validateTimezone(req.Timezone); err != nil {
		return fmt.Errorf("invalid timezone: %w", err)
	}

	if req.WebhookTimeout != 0 && (req.WebhookTimeout < 1 || req.WebhookTimeout > 60) {
		return fmt.Errorf("webhook_timeout_seconds must be between 1 and 60")
	}

	if req.WebhookURL == "" {
		return fmt.Errorf("webhook_url is required")
	}
	if err := validateWebhookURL(req.WebhookURL); err != nil {
		return fmt.Errorf("invalid webhook_url: %w", err)
	}

	if req.Analytics != nil {
		if err := validateAnalytics(req.Analytics); err != nil {
			return fmt.Errorf("invalid analytics: %w", err)
		}
	}

	return nil
}

func validateAnalytics(a *AnalyticsRequest) error {
	if a.RetentionSeconds < 0 {
		return fmt.Errorf("retention_seconds must be non-negative")
	}
	if a.RetentionSeconds > maxRetentionSeconds {
		return fmt.Errorf("retention_seconds must not exceed %d (7 days)", maxRetentionSeconds)
	}
	return nil
}

func validateCron(expr string) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	_, err := parser.Parse(expr)
	return err
}

func validateTimezone(tz string) error {
	_, err := time.LoadLocation(tz)
	return err
}

func validateWebhookURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("host is required")
	}

	host := u.Hostname()

	// Block known metadata hostnames
	if host == "metadata.google.internal" {
		return fmt.Errorf("metadata service addresses are not allowed")
	}

	// Block localhost variants
	if host == "localhost" {
		return fmt.Errorf("localhost is not allowed")
	}

	// Block private/reserved IPs
	ip := net.ParseIP(host)
	if ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("private or reserved IP addresses are not allowed")
		}
	}

	return nil
}
