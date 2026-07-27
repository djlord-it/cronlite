package webadmin

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/djlord-it/cronlite/internal/domain"
	"github.com/djlord-it/cronlite/internal/service"
)

type jobFormValues struct {
	Name           string
	CronExpression string
	Timezone       string
	WebhookURL     string
	TimeoutSeconds string
	Tags           string
}

func parseTags(raw string) ([]domain.Tag, error) {
	var tags []domain.Tag
	seen := make(map[string]struct{})
	for lineNumber, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("tag line %d must use key=value", lineNumber+1)
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate tag key %q", key)
		}
		seen[key] = struct{}{}
		tags = append(tags, domain.Tag{Key: key, Value: value})
	}
	return tags, nil
}

func parseCreateJobForm(r *http.Request) (service.CreateJobInput, jobFormValues, error) {
	values, timeout, tags, err := parseCommonJobForm(r)
	if err != nil {
		return service.CreateJobInput{}, values, err
	}
	return service.CreateJobInput{
		Name:           values.Name,
		CronExpression: values.CronExpression,
		Timezone:       values.Timezone,
		WebhookURL:     values.WebhookURL,
		Secret:         r.FormValue("webhook_secret"),
		Timeout:        timeout,
		Tags:           tags,
	}, values, nil
}

func parseUpdateJobForm(r *http.Request) (service.UpdateJobInput, jobFormValues, error) {
	values, timeout, tags, err := parseCommonJobForm(r)
	if err != nil {
		return service.UpdateJobInput{}, values, err
	}

	input := service.UpdateJobInput{
		Name:           &values.Name,
		CronExpression: &values.CronExpression,
		Timezone:       &values.Timezone,
		WebhookURL:     &values.WebhookURL,
		Timeout:        &timeout,
		Tags:           &tags,
	}

	secret := r.FormValue("webhook_secret")
	clearSecret := r.FormValue("clear_webhook_secret") == "true"
	if clearSecret && secret != "" {
		return service.UpdateJobInput{}, values, errors.New("choose either a new secret or clear secret")
	}
	if clearSecret {
		empty := ""
		input.Secret = &empty
	} else if secret != "" {
		input.Secret = &secret
	}

	return input, values, nil
}

func parseCommonJobForm(r *http.Request) (jobFormValues, time.Duration, []domain.Tag, error) {
	if err := r.ParseForm(); err != nil {
		return jobFormValues{}, 0, nil, errors.New("invalid form submission")
	}

	values := jobFormValues{
		Name:           strings.TrimSpace(r.FormValue("name")),
		CronExpression: strings.TrimSpace(r.FormValue("cron_expression")),
		Timezone:       strings.TrimSpace(r.FormValue("timezone")),
		WebhookURL:     strings.TrimSpace(r.FormValue("webhook_url")),
		TimeoutSeconds: strings.TrimSpace(r.FormValue("timeout_seconds")),
		Tags:           r.FormValue("tags"),
	}
	if values.Name == "" || values.CronExpression == "" || values.Timezone == "" || values.WebhookURL == "" {
		return values, 0, nil, errors.New("name, cron expression, timezone, and webhook URL are required")
	}

	seconds, err := strconv.Atoi(values.TimeoutSeconds)
	if err != nil || seconds < 1 || seconds > 60 {
		return values, 0, nil, errors.New("timeout must be between 1 and 60 seconds")
	}
	tags, err := parseTags(values.Tags)
	if err != nil {
		return values, 0, nil, err
	}

	return values, time.Duration(seconds) * time.Second, tags, nil
}
