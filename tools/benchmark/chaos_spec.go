package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

var ErrInvalidFaultSpec = errors.New("invalid chaos fault spec")

// FaultType names an injectable fault. P0 only uses faults the Compose
// controller can already cause.
type FaultType string

const (
	FaultProcStop  FaultType = "proc-stop"
	FaultProcPause FaultType = "proc-pause"
	FaultPGPause   FaultType = "pg-pause"
)

const (
	defaultFaultIntensity = "med"
	defaultFaultDuration  = 30 * time.Second
)

var faultIntensities = []string{"low", "med", "high"}

// faultTargets lists the allowed targets per type; the first is the default.
var faultTargets = map[FaultType][]string{
	FaultProcStop:  {"leader", "follower", "all"},
	FaultProcPause: {"leader", "follower", "all"},
	FaultPGPause:   {"postgres"},
}

type FaultSpec struct {
	Type      FaultType     `json:"type"`
	Target    string        `json:"target"`
	Intensity string        `json:"intensity"`
	Duration  time.Duration `json:"duration_ns"`
}

// String returns the canonical "type:target:intensity:duration" form.
func (f FaultSpec) String() string {
	return fmt.Sprintf("%s:%s:%s:%s", f.Type, f.Target, f.Intensity, f.Duration)
}

// parseFaultSpecs parses a comma-separated list of
// type[:target][:intensity][:duration] specs. Empty fields take the type's
// defaults.
func parseFaultSpecs(raw string) ([]FaultSpec, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%w: empty list", ErrInvalidFaultSpec)
	}
	var specs []FaultSpec
	for _, item := range strings.Split(raw, ",") {
		spec, err := parseFaultSpec(strings.TrimSpace(item))
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func parseFaultSpec(raw string) (FaultSpec, error) {
	fields := strings.Split(raw, ":")
	if len(fields) > 4 {
		return FaultSpec{}, fmt.Errorf("%w: %q has more than four fields", ErrInvalidFaultSpec, raw)
	}
	for len(fields) < 4 {
		fields = append(fields, "")
	}
	spec := FaultSpec{Type: FaultType(fields[0])}
	targets, ok := faultTargets[spec.Type]
	if !ok {
		return FaultSpec{}, fmt.Errorf("%w: unknown type %q", ErrInvalidFaultSpec, fields[0])
	}
	spec.Target = fields[1]
	if spec.Target == "" {
		spec.Target = targets[0]
	}
	if !slices.Contains(targets, spec.Target) {
		return FaultSpec{}, fmt.Errorf("%w: %s does not accept target %q", ErrInvalidFaultSpec, spec.Type, spec.Target)
	}
	spec.Intensity = fields[2]
	if spec.Intensity == "" {
		spec.Intensity = defaultFaultIntensity
	}
	if !slices.Contains(faultIntensities, spec.Intensity) {
		return FaultSpec{}, fmt.Errorf("%w: unknown intensity %q", ErrInvalidFaultSpec, spec.Intensity)
	}
	spec.Duration = defaultFaultDuration
	if fields[3] != "" {
		duration, err := time.ParseDuration(fields[3])
		if err != nil || duration <= 0 {
			return FaultSpec{}, fmt.Errorf("%w: duration %q must be positive", ErrInvalidFaultSpec, fields[3])
		}
		spec.Duration = duration
	}
	return spec, nil
}

func faultSpecStrings(specs []FaultSpec) []string {
	var values []string
	for _, spec := range specs {
		values = append(values, spec.String())
	}
	return values
}
