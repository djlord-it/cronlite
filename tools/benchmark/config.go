package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var (
	ErrDisruptiveNotAllowed = errors.New("disruptive scenario requires --allow-disruptive")
	ErrNonLocalNotAllowed   = errors.New("non-local target requires --allow-non-local")
	ErrDatabaseURLRequired  = errors.New("diagnostic mode requires --database-url")
	ErrUnknownScenario      = errors.New("unknown benchmark scenario")
)

var knownScenarios = []string{
	"smoke",
	"baseline",
	"cold-warm",
	"warm-sequential",
	"concurrent",
	"control-plane",
	"recurring",
	"slow-receiver",
	"retry",
	"duplicate-race",
	"crash-recovery",
	"leader-failover",
	"database-outage",
	"load",
}

var disruptiveScenarios = []string{
	"duplicate-race",
	"crash-recovery",
	"leader-failover",
	"database-outage",
	"load",
}

type Config struct {
	BaseURL            string
	APIKey             string
	ReceiverAddr       string
	ReceiverPublicURL  string
	Scenarios          []string
	SampleCount        int
	Concurrency        []int
	Timeout            time.Duration
	PollInterval       time.Duration
	RequeueThreshold   time.Duration
	DatabaseURL        string
	Diagnostic         bool
	MetricsURL         string
	OutputDir          string
	RandomSeed         int64
	WebhookSecret      string
	RetryProfile       string
	AllowDisruptive    bool
	AllowNonLocal      bool
	FailOnCorrectness  bool
	KeepData           bool
	StartCompose       bool
	CleanupEnvironment bool
	ComposeFile        string
	ComposeProject     string
	DispatchMode       string
}

type RedactedConfig struct {
	BaseURL                 string        `json:"base_url"`
	APIKeyConfigured        bool          `json:"api_key_configured"`
	ReceiverAddr            string        `json:"receiver_addr"`
	ReceiverPublicURL       string        `json:"receiver_public_url"`
	Scenarios               []string      `json:"scenarios"`
	SampleCount             int           `json:"sample_count"`
	Concurrency             []int         `json:"concurrency"`
	Timeout                 time.Duration `json:"timeout_ns"`
	PollInterval            time.Duration `json:"poll_interval_ns"`
	RequeueThreshold        time.Duration `json:"requeue_threshold_ns"`
	DatabaseTarget          string        `json:"database_target,omitempty"`
	Diagnostic              bool          `json:"diagnostic"`
	MetricsURL              string        `json:"metrics_url,omitempty"`
	OutputDir               string        `json:"output_dir"`
	RandomSeed              int64         `json:"random_seed"`
	WebhookSecretConfigured bool          `json:"webhook_secret_configured"`
	RetryProfile            string        `json:"retry_profile"`
	AllowDisruptive         bool          `json:"allow_disruptive"`
	AllowNonLocal           bool          `json:"allow_non_local"`
	FailOnCorrectness       bool          `json:"fail_on_correctness"`
	KeepData                bool          `json:"keep_data"`
	StartCompose            bool          `json:"start_compose"`
	CleanupEnvironment      bool          `json:"cleanup_environment"`
	ComposeProject          string        `json:"compose_project,omitempty"`
	DispatchMode            string        `json:"dispatch_mode"`
}

func defaultConfig() Config {
	return Config{
		BaseURL:           "http://127.0.0.1:8080",
		ReceiverAddr:      "127.0.0.1:9090",
		ReceiverPublicURL: "http://127.0.0.1:9090",
		Scenarios:         []string{"smoke"},
		SampleCount:       10,
		Concurrency:       []int{1, 5, 10, 25, 50},
		Timeout:           45 * time.Second,
		PollInterval:      100 * time.Millisecond,
		RequeueThreshold:  19 * time.Minute,
		MetricsURL:        "http://127.0.0.1:8080/metrics",
		OutputDir:         "./benchmark-output",
		RandomSeed:        1,
		WebhookSecret:     "cronlite-benchmark-local-secret",
		RetryProfile:      "real-policy",
		ComposeFile:       filepath.FromSlash("tools/benchmark/docker-compose.yml"),
		DispatchMode:      "unknown",
	}
}

func (c Config) Validate() error {
	base, err := parseHTTPURL("base URL", c.BaseURL)
	if err != nil {
		return err
	}
	if !c.AllowNonLocal && !isLoopbackHost(base.Hostname()) {
		return ErrNonLocalNotAllowed
	}
	if _, err := parseHTTPURL("receiver public URL", c.ReceiverPublicURL); err != nil {
		return err
	}
	if c.SampleCount < 1 || c.SampleCount > 1_000_000 {
		return fmt.Errorf("sample count must be between 1 and 1000000")
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("poll interval must be positive")
	}
	if c.RequeueThreshold <= 0 {
		return fmt.Errorf("requeue threshold must be positive")
	}
	if len(c.Scenarios) == 0 {
		return fmt.Errorf("at least one scenario is required")
	}
	for _, scenario := range c.Scenarios {
		if !slices.Contains(knownScenarios, scenario) {
			return fmt.Errorf("%w: %s", ErrUnknownScenario, scenario)
		}
		if slices.Contains(disruptiveScenarios, scenario) && !c.AllowDisruptive {
			return fmt.Errorf("%w: %s", ErrDisruptiveNotAllowed, scenario)
		}
	}
	for _, level := range c.Concurrency {
		if level < 1 || level > 10_000 {
			return fmt.Errorf("concurrency must be between 1 and 10000")
		}
	}
	if c.Diagnostic && strings.TrimSpace(c.DatabaseURL) == "" {
		return ErrDatabaseURLRequired
	}
	if c.RetryProfile != "real-policy" && c.RetryProfile != "fast-test" {
		return fmt.Errorf("retry profile must be real-policy or fast-test")
	}
	if c.CleanupEnvironment && (!c.StartCompose || !c.AllowDisruptive) {
		return fmt.Errorf("environment cleanup requires --start-compose and --allow-disruptive")
	}
	return nil
}

func (c Config) Redacted() RedactedConfig {
	return RedactedConfig{
		BaseURL:                 c.BaseURL,
		APIKeyConfigured:        c.APIKey != "",
		ReceiverAddr:            c.ReceiverAddr,
		ReceiverPublicURL:       c.ReceiverPublicURL,
		Scenarios:               slices.Clone(c.Scenarios),
		SampleCount:             c.SampleCount,
		Concurrency:             slices.Clone(c.Concurrency),
		Timeout:                 c.Timeout,
		PollInterval:            c.PollInterval,
		RequeueThreshold:        c.RequeueThreshold,
		DatabaseTarget:          redactDatabaseTarget(c.DatabaseURL),
		Diagnostic:              c.Diagnostic,
		MetricsURL:              c.MetricsURL,
		OutputDir:               c.OutputDir,
		RandomSeed:              c.RandomSeed,
		WebhookSecretConfigured: c.WebhookSecret != "",
		RetryProfile:            c.RetryProfile,
		AllowDisruptive:         c.AllowDisruptive,
		AllowNonLocal:           c.AllowNonLocal,
		FailOnCorrectness:       c.FailOnCorrectness,
		KeepData:                c.KeepData,
		StartCompose:            c.StartCompose,
		CleanupEnvironment:      c.CleanupEnvironment,
		ComposeProject:          c.ComposeProject,
		DispatchMode:            c.DispatchMode,
	}
}

func parseHTTPURL(label, raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("%s must be an absolute HTTP URL", label)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("%s must use http or https", label)
	}
	return parsed, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func redactDatabaseTarget(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "configured"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	return parsed.String()
}
