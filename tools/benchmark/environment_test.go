package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestComposeControllerRejectsUnownedProject(t *testing.T) {
	controller := &composeController{
		File:        "tools/benchmark/docker-compose.yml",
		Project:     "production",
		OwnedPrefix: benchmarkComposePrefix,
		Runner:      &recordingCommandRunner{},
	}
	if err := controller.validateDestructiveTarget(); !errors.Is(err, ErrUnownedComposeProject) {
		t.Fatalf("expected ownership error, got %v", err)
	}
}

func TestComposeControllerTargetsFileAndProjectWithoutShell(t *testing.T) {
	runner := &recordingCommandRunner{}
	controller := &composeController{
		File:        "tools/benchmark/docker-compose.yml",
		Project:     benchmarkComposePrefix + "run-1",
		OwnedPrefix: benchmarkComposePrefix,
		Runner:      runner,
	}
	if err := controller.stopService(context.Background(), "cronlite_1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"compose",
		"--file", "tools/benchmark/docker-compose.yml",
		"--project-name", benchmarkComposePrefix + "run-1",
		"stop", "cronlite_1",
	}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args: want=%v got=%v", want, runner.args)
	}
}

func TestComposeControllerRejectsUnknownService(t *testing.T) {
	controller := &composeController{
		File:        "tools/benchmark/docker-compose.yml",
		Project:     benchmarkComposePrefix + "run-1",
		OwnedPrefix: benchmarkComposePrefix,
		Runner:      &recordingCommandRunner{},
	}
	if err := controller.stopService(context.Background(), "production-db"); !errors.Is(err, ErrUnknownComposeService) {
		t.Fatalf("expected service error, got %v", err)
	}
}

type recordingCommandRunner struct {
	name string
	args []string
	err  error
}

func (r *recordingCommandRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	return nil, r.err
}
