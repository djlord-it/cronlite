package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"time"
)

// FaultWindow records when a fault was applied, cleared and recovered from.
type FaultWindow struct {
	Spec        FaultSpec  `json:"spec"`
	Services    []string   `json:"services"`
	AppliedAt   time.Time  `json:"applied_at"`
	ClearedAt   time.Time  `json:"cleared_at"`
	RecoveredAt *time.Time `json:"recovered_at,omitempty"`
	ApplyError  string     `json:"apply_error,omitempty"`
	ClearError  string     `json:"clear_error,omitempty"`
}

type faultController interface {
	stopService(context.Context, string) error
	startService(context.Context, string) error
	pauseService(context.Context, string) error
	unpauseService(context.Context, string) error
	leaderService(context.Context) (string, error)
	cronLiteServices() []string
}

var _ faultController = (*composeController)(nil)

type faultDriver struct {
	controller faultController
	mu         sync.Mutex
	rng        *rand.Rand
	touched    []string
}

func newFaultDriver(controller faultController, seed int64) *faultDriver {
	return &faultDriver{
		controller: controller,
		rng:        rand.New(rand.NewPCG(uint64(seed), 0)),
	}
}

// apply resolves the spec's target and injects the fault.
func (d *faultDriver) apply(ctx context.Context, spec FaultSpec) (FaultWindow, error) {
	window := FaultWindow{Spec: spec}
	services, err := d.resolve(ctx, spec)
	if err != nil {
		window.ApplyError = err.Error()
		return window, err
	}
	window.Services = services
	d.markTouched(services)
	window.AppliedAt = time.Now().UTC()
	for index, service := range services {
		if err := d.inject(ctx, spec.Type, service); err != nil {
			// Undo what was injected so a half-applied fault does not stay in
			// force until the final cleanup.
			window.Services = services[:index]
			window.ApplyError = err.Error()
			_ = d.clear(ctx, &window)
			return window, err
		}
	}
	return window, nil
}

// clear reverses a fault applied by apply.
func (d *faultDriver) clear(ctx context.Context, window *FaultWindow) error {
	var firstErr error
	for _, service := range window.Services {
		if err := d.restore(ctx, window.Spec.Type, service); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	window.ClearedAt = time.Now().UTC()
	if firstErr != nil {
		window.ClearError = firstErr.Error()
	}
	return firstErr
}

// cleanupAll starts and unpauses every service this driver ever touched. It is
// idempotent; errors such as "not paused" are recorded, not returned.
func (d *faultDriver) cleanupAll(ctx context.Context) []Observation {
	d.mu.Lock()
	services := slices.Clone(d.touched)
	d.mu.Unlock()
	var observations []Observation
	for _, service := range services {
		observation, _ := observeControl("chaos_cleanup_unpause_"+service, func() error {
			return d.controller.unpauseService(ctx, service)
		})
		observations = append(observations, observation)
		if service == "postgres" {
			continue
		}
		observation, _ = observeControl("chaos_cleanup_start_"+service, func() error {
			return d.controller.startService(ctx, service)
		})
		observations = append(observations, observation)
	}
	return observations
}

func (d *faultDriver) resolve(ctx context.Context, spec FaultSpec) ([]string, error) {
	switch spec.Target {
	case "postgres":
		return []string{"postgres"}, nil
	case "all":
		return d.controller.cronLiteServices(), nil
	}
	leader, err := d.controller.leaderService(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve %s target: %w", spec.Target, err)
	}
	if spec.Target == "leader" {
		return []string{leader}, nil
	}
	var followers []string
	for _, service := range d.controller.cronLiteServices() {
		if service != leader {
			followers = append(followers, service)
		}
	}
	if len(followers) == 0 {
		return nil, fmt.Errorf("resolve follower target: no follower available")
	}
	d.mu.Lock()
	pick := followers[d.rng.IntN(len(followers))]
	d.mu.Unlock()
	return []string{pick}, nil
}

func (d *faultDriver) inject(ctx context.Context, fault FaultType, service string) error {
	if fault == FaultProcStop {
		return d.controller.stopService(ctx, service)
	}
	return d.controller.pauseService(ctx, service)
}

func (d *faultDriver) restore(ctx context.Context, fault FaultType, service string) error {
	if fault == FaultProcStop {
		return d.controller.startService(ctx, service)
	}
	return d.controller.unpauseService(ctx, service)
}

func (d *faultDriver) markTouched(services []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, service := range services {
		if !slices.Contains(d.touched, service) {
			d.touched = append(d.touched, service)
		}
	}
}
