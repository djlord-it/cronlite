package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const benchmarkComposePrefix = "cronlite-benchmark-"

var (
	ErrUnownedComposeProject = errors.New("compose project is not owned by the benchmark")
	ErrUnknownComposeService = errors.New("unknown benchmark compose service")
)

type Capabilities struct {
	DockerCompose     bool `json:"docker_compose"`
	ProcessControl    bool `json:"process_control"`
	MultipleInstances bool `json:"multiple_instances"`
	PostgreSQL        bool `json:"postgresql"`
}

type commandRunner interface {
	run(context.Context, string, ...string) ([]byte, error)
}

type osCommandRunner struct{}

func (osCommandRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, output)
	}
	return output, nil
}

type composeController struct {
	File        string
	Project     string
	OwnedPrefix string
	Runner      commandRunner
	MetricsURLs map[string]string
	APIURLs     map[string]string
}

var benchmarkComposeServices = map[string]bool{
	"postgres":   true,
	"cronlite_1": true,
	"cronlite_2": true,
	"cronlite_3": true,
}

func newComposeController(cfg Config) *composeController {
	return &composeController{
		File:        cfg.ComposeFile,
		Project:     cfg.ComposeProject,
		OwnedPrefix: benchmarkComposePrefix,
		Runner:      osCommandRunner{},
		MetricsURLs: map[string]string{
			"cronlite_1": "http://127.0.0.1:18080/metrics",
			"cronlite_2": "http://127.0.0.1:18081/metrics",
			"cronlite_3": "http://127.0.0.1:18082/metrics",
		},
		APIURLs: map[string]string{
			"cronlite_1": "http://127.0.0.1:18080",
			"cronlite_2": "http://127.0.0.1:18081",
			"cronlite_3": "http://127.0.0.1:18082",
		},
	}
}

func (c *composeController) validateDestructiveTarget() error {
	if c.Project == "" || !strings.HasPrefix(c.Project, c.OwnedPrefix) {
		return ErrUnownedComposeProject
	}
	return nil
}

func (c *composeController) validateService(service string) error {
	if !benchmarkComposeServices[service] {
		return fmt.Errorf("%w: %s", ErrUnknownComposeService, service)
	}
	return nil
}

func (c *composeController) composeArgs(action string, extra ...string) []string {
	args := []string{
		"compose",
		"--file", c.File,
		"--project-name", c.Project,
		action,
	}
	return append(args, extra...)
}

func (c *composeController) up(ctx context.Context) error {
	if err := c.validateDestructiveTarget(); err != nil {
		return err
	}
	_, err := c.Runner.run(ctx, "docker", c.composeArgs("up", "--detach", "--build")...)
	return err
}

func (c *composeController) stopService(ctx context.Context, service string) error {
	return c.serviceAction(ctx, "stop", service)
}

func (c *composeController) startService(ctx context.Context, service string) error {
	return c.serviceAction(ctx, "start", service)
}

func (c *composeController) pauseService(ctx context.Context, service string) error {
	return c.serviceAction(ctx, "pause", service)
}

func (c *composeController) unpauseService(ctx context.Context, service string) error {
	return c.serviceAction(ctx, "unpause", service)
}

func (c *composeController) serviceAction(
	ctx context.Context,
	action string,
	service string,
) error {
	if err := c.validateDestructiveTarget(); err != nil {
		return err
	}
	if err := c.validateService(service); err != nil {
		return err
	}
	_, err := c.Runner.run(ctx, "docker", c.composeArgs(action, service)...)
	return err
}

func (c *composeController) down(ctx context.Context, removeVolumes bool) error {
	if err := c.validateDestructiveTarget(); err != nil {
		return err
	}
	extra := []string{"--remove-orphans"}
	if removeVolumes {
		extra = append(extra, "--volumes")
	}
	_, err := c.Runner.run(ctx, "docker", c.composeArgs("down", extra...)...)
	return err
}

func (c *composeController) leaderService(ctx context.Context) (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	var leaders []string
	for service, metricsURL := range c.MetricsURLs {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
		if err != nil {
			continue
		}
		response, err := client.Do(request)
		if err != nil {
			continue
		}
		body := make([]byte, maxPrometheusBodyBytes)
		count, _ := response.Body.Read(body)
		_ = response.Body.Close()
		metrics := parsePrometheus(body[:count])
		if metrics["cronlite_leader_is_leader"] == 1 {
			leaders = append(leaders, service)
		}
	}
	if len(leaders) != 1 {
		return "", fmt.Errorf("expected one leader, observed %d", len(leaders))
	}
	return leaders[0], nil
}

func (c *composeController) waitForLeader(
	ctx context.Context,
	exclude string,
) (string, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		leader, err := c.leaderService(ctx)
		if err == nil && leader != exclude {
			return leader, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func detectCapabilities(controller *composeController) Capabilities {
	if controller == nil {
		return Capabilities{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := controller.Runner.run(ctx, "docker", "compose", "version")
	available := err == nil
	return Capabilities{
		DockerCompose:     available,
		ProcessControl:    available,
		MultipleInstances: available && len(controller.APIURLs) > 1,
		PostgreSQL:        available,
	}
}

func captureEnvironment(
	ctx context.Context,
	cfg Config,
	controller *composeController,
) EnvironmentInfo {
	info := EnvironmentInfo{
		OS:                runtime.GOOS,
		Architecture:      runtime.GOARCH,
		CPUCount:          runtime.NumCPU(),
		MemoryBytes:       physicalMemoryBytes(ctx),
		GoVersion:         runtime.Version(),
		DispatchMode:      cfg.DispatchMode,
		CronLiteInstances: 1,
		RelevantConfig: map[string]string{
			"retry_profile":     cfg.RetryProfile,
			"requeue_threshold": cfg.RequeueThreshold.String(),
		},
	}
	if cfg.StartCompose {
		info.CronLiteInstances = 3
	}
	runner := osCommandRunner{}
	if output, err := runner.run(ctx, "git", "rev-parse", "HEAD"); err == nil {
		info.CommitSHA = strings.TrimSpace(string(output))
	}
	if output, err := runner.run(ctx, "docker", "version", "--format", "{{.Server.Version}}"); err == nil {
		info.DockerVersion = strings.TrimSpace(string(output))
	}
	_ = controller
	return info
}

func physicalMemoryBytes(ctx context.Context) uint64 {
	if runtime.GOOS == "darwin" {
		output, err := osCommandRunner{}.run(ctx, "sysctl", "-n", "hw.memsize")
		if err == nil {
			value, _ := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
			return value
		}
	}
	if runtime.GOOS == "linux" {
		body, err := os.ReadFile("/proc/meminfo")
		if err == nil {
			fields := strings.Fields(string(body))
			if len(fields) >= 2 && fields[0] == "MemTotal:" {
				kib, _ := strconv.ParseUint(fields[1], 10, 64)
				return kib * 1024
			}
		}
	}
	return 0
}
