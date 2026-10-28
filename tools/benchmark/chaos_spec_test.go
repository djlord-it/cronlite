package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseFaultSpecs(t *testing.T) {
	tests := []struct {
		raw  string
		want []FaultSpec
	}{
		{"proc-stop", []FaultSpec{{Type: "proc-stop", Target: "leader", Intensity: "med", Duration: 30 * time.Second}}},
		{"proc-pause:follower:high:10s", []FaultSpec{{Type: "proc-pause", Target: "follower", Intensity: "high", Duration: 10 * time.Second}}},
		{"pg-pause::low", []FaultSpec{{Type: "pg-pause", Target: "postgres", Intensity: "low", Duration: 30 * time.Second}}},
		{"proc-stop:leader,pg-pause", []FaultSpec{
			{Type: "proc-stop", Target: "leader", Intensity: "med", Duration: 30 * time.Second},
			{Type: "pg-pause", Target: "postgres", Intensity: "med", Duration: 30 * time.Second},
		}},
	}
	for _, test := range tests {
		got, err := parseFaultSpecs(test.raw)
		if err != nil {
			t.Fatalf("parseFaultSpecs(%q): %v", test.raw, err)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("parseFaultSpecs(%q) = %+v, want %+v", test.raw, got, test.want)
		}
	}
}

func TestParseFaultSpecsRejectsInvalid(t *testing.T) {
	for _, raw := range []string{
		"bogus",
		"proc-stop:postgres",
		"pg-pause:leader",
		"proc-stop:leader:extreme",
		"proc-stop:leader:med:-1s",
		"proc-stop:leader:med:0s",
		"proc-stop:a:b:c:d",
		"",
	} {
		if _, err := parseFaultSpecs(raw); !errors.Is(err, ErrInvalidFaultSpec) {
			t.Errorf("parseFaultSpecs(%q) error = %v, want ErrInvalidFaultSpec", raw, err)
		}
	}
}

func TestFaultSpecStringRoundTrips(t *testing.T) {
	specs, err := parseFaultSpecs("proc-pause:follower:high:10s,pg-pause")
	if err != nil {
		t.Fatal(err)
	}
	if specs[0].String() != "proc-pause:follower:high:10s" {
		t.Fatalf("String() = %q", specs[0].String())
	}
	var raw []string
	for _, spec := range specs {
		raw = append(raw, spec.String())
	}
	again, err := parseFaultSpecs(strings.Join(raw, ","))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(specs, again) {
		t.Fatalf("round trip = %+v, want %+v", again, specs)
	}
}

func validChaosConfig(t *testing.T) Config {
	t.Helper()
	cfg := defaultConfig()
	cfg.Scenarios = []string{"chaos"}
	cfg.AllowDisruptive = true
	cfg.StartCompose = true
	cfg.Diagnostic = true
	cfg.DatabaseURL = "postgres://cronlite:cronlite@127.0.0.1:15432/cronlite?sslmode=disable"
	specs, err := parseFaultSpecs("proc-stop")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ChaosFaults = specs
	return cfg
}

func TestValidateChaos(t *testing.T) {
	if err := validChaosConfig(t).Validate(); err != nil {
		t.Fatalf("valid chaos config rejected: %v", err)
	}

	cfg := validChaosConfig(t)
	cfg.AllowDisruptive = false
	if err := cfg.Validate(); !errors.Is(err, ErrDisruptiveNotAllowed) {
		t.Fatalf("expected disruptive error, got %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"compose", func(c *Config) { c.StartCompose = false }, "chaos requires --start-compose"},
		{"diagnostic", func(c *Config) { c.Diagnostic = false }, "chaos requires --diagnostic"},
		{"faults", func(c *Config) { c.ChaosFaults = nil }, "chaos requires --chaos-faults"},
		{"jobs low", func(c *Config) { c.ChaosJobs = 0 }, "chaos jobs"},
		{"jobs high", func(c *Config) { c.ChaosJobs = 501 }, "chaos jobs"},
		{"rate low", func(c *Config) { c.ChaosTriggerRate = -1 }, "chaos trigger rate"},
		{"rate high", func(c *Config) { c.ChaosTriggerRate = 51 }, "chaos trigger rate"},
		{"interval", func(c *Config) { c.ChaosSampleInterval = 99 * time.Millisecond }, "chaos sample interval"},
		{"faults without scenario", func(c *Config) { c.Scenarios = []string{"smoke"} }, "--chaos-faults requires --scenario chaos"},
	}
	for _, test := range tests {
		cfg := validChaosConfig(t)
		test.mutate(&cfg)
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want containing %q", test.name, err, test.want)
		}
	}
}

func TestScenarioAllSkipsChaosWithoutFaults(t *testing.T) {
	cfg, _, err := parseConfig([]string{"--scenario", "all", "--allow-disruptive"}, &strings.Builder{})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	for _, scenario := range cfg.Scenarios {
		if scenario == "chaos" {
			t.Fatalf("scenario all included chaos without --chaos-faults: %v", cfg.Scenarios)
		}
	}
}

func TestParseConfigReadsChaosFlags(t *testing.T) {
	cfg, _, err := parseConfig([]string{
		"--start-compose", "--diagnostic", "--allow-disruptive",
		"--scenario", "chaos",
		"--chaos-faults", "proc-pause:follower:high:10s",
		"--chaos-jobs", "5",
		"--chaos-trigger-rate", "0.5",
	}, &strings.Builder{})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.ChaosFaults) != 1 || cfg.ChaosFaults[0].Target != "follower" {
		t.Fatalf("ChaosFaults = %+v", cfg.ChaosFaults)
	}
	if cfg.ChaosJobs != 5 || cfg.ChaosTriggerRate != 0.5 {
		t.Fatalf("jobs=%d rate=%v", cfg.ChaosJobs, cfg.ChaosTriggerRate)
	}
	if got := cfg.Redacted().ChaosFaults; !reflect.DeepEqual(got, []string{"proc-pause:follower:high:10s"}) {
		t.Fatalf("redacted ChaosFaults = %v", got)
	}
}
