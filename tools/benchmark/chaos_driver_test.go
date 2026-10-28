package main

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

type fakeFaultController struct {
	mu        sync.Mutex
	leader    string
	leaderErr error
	calls     []string
}

func (f *fakeFaultController) record(action, service string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, action+" "+service)
	return nil
}

func (f *fakeFaultController) stopService(_ context.Context, s string) error {
	return f.record("stop", s)
}
func (f *fakeFaultController) startService(_ context.Context, s string) error {
	return f.record("start", s)
}
func (f *fakeFaultController) pauseService(_ context.Context, s string) error {
	return f.record("pause", s)
}
func (f *fakeFaultController) unpauseService(_ context.Context, s string) error {
	return f.record("unpause", s)
}
func (f *fakeFaultController) leaderService(context.Context) (string, error) {
	return f.leader, f.leaderErr
}
func (f *fakeFaultController) cronLiteServices() []string {
	return []string{"cronlite_1", "cronlite_2", "cronlite_3"}
}

func (f *fakeFaultController) snapshotCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func TestDriverResolvesLeader(t *testing.T) {
	controller := &fakeFaultController{leader: "cronlite_2"}
	driver := newFaultDriver(controller, 1)
	window, err := driver.apply(context.Background(), FaultSpec{Type: FaultProcStop, Target: "leader", Intensity: "med", Duration: time.Second})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := driver.clear(context.Background(), &window); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := controller.snapshotCalls(); !reflect.DeepEqual(got, []string{"stop cronlite_2", "start cronlite_2"}) {
		t.Fatalf("calls = %v", got)
	}
	if !reflect.DeepEqual(window.Services, []string{"cronlite_2"}) {
		t.Fatalf("services = %v", window.Services)
	}
	if window.AppliedAt.IsZero() || window.ClearedAt.Before(window.AppliedAt) {
		t.Fatalf("timestamps applied=%s cleared=%s", window.AppliedAt, window.ClearedAt)
	}
}

func TestDriverFollowerIsSeeded(t *testing.T) {
	spec := FaultSpec{Type: FaultProcPause, Target: "follower", Intensity: "med", Duration: time.Second}
	var first []string
	for run := 0; run < 2; run++ {
		var picks []string
		driver := newFaultDriver(&fakeFaultController{leader: "cronlite_1"}, 42)
		for i := 0; i < 8; i++ {
			window, err := driver.apply(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if window.Services[0] == "cronlite_1" {
				t.Fatal("follower pick chose the leader")
			}
			picks = append(picks, window.Services[0])
		}
		if run == 0 {
			first = picks
		} else if !reflect.DeepEqual(first, picks) {
			t.Fatalf("same seed gave %v then %v", first, picks)
		}
	}
}

func TestDriverPgPause(t *testing.T) {
	controller := &fakeFaultController{}
	driver := newFaultDriver(controller, 1)
	window, err := driver.apply(context.Background(), FaultSpec{Type: FaultPGPause, Target: "postgres", Intensity: "med", Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.clear(context.Background(), &window); err != nil {
		t.Fatal(err)
	}
	if got := controller.snapshotCalls(); !reflect.DeepEqual(got, []string{"pause postgres", "unpause postgres"}) {
		t.Fatalf("calls = %v", got)
	}
}

func TestDriverNoLeaderRecordsApplyError(t *testing.T) {
	controller := &fakeFaultController{leaderErr: errors.New("expected one leader, observed 0")}
	driver := newFaultDriver(controller, 1)
	window, err := driver.apply(context.Background(), FaultSpec{Type: FaultProcStop, Target: "leader", Intensity: "med", Duration: time.Second})
	if err == nil || window.ApplyError == "" {
		t.Fatalf("expected apply error, got err=%v window=%+v", err, window)
	}
	if calls := controller.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("unexpected calls %v", calls)
	}
}

func TestCleanupAllRestoresTouchedServices(t *testing.T) {
	controller := &fakeFaultController{leader: "cronlite_1"}
	driver := newFaultDriver(controller, 1)
	if _, err := driver.apply(context.Background(), FaultSpec{Type: FaultProcPause, Target: "all", Intensity: "med", Duration: time.Second}); err != nil {
		t.Fatal(err)
	}
	driver.cleanupAll(context.Background())
	driver.cleanupAll(context.Background())
	calls := controller.snapshotCalls()
	for _, service := range []string{"cronlite_1", "cronlite_2", "cronlite_3"} {
		for _, action := range []string{"start", "unpause"} {
			if !slices.Contains(calls, action+" "+service) {
				t.Errorf("missing %s %s in %v", action, service, calls)
			}
		}
	}
}

func TestCleanupAllAfterContextCancel(t *testing.T) {
	controller := &fakeFaultController{leader: "cronlite_3"}
	driver := newFaultDriver(controller, 1)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := driver.apply(ctx, FaultSpec{Type: FaultProcStop, Target: "leader", Intensity: "med", Duration: time.Second}); err != nil {
		t.Fatal(err)
	}
	cancel()
	observations := driver.cleanupAll(context.Background())
	if !slices.Contains(controller.snapshotCalls(), "start cronlite_3") {
		t.Fatalf("cleanup did not start cronlite_3: %v", controller.snapshotCalls())
	}
	if len(observations) == 0 {
		t.Fatal("cleanup recorded no observations")
	}
}
