package circuitbreaker

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"
)

func TestCircuitBreaker_RecoveredDestinationChurn(t *testing.T) {
	count := 1000
	if os.Getenv("CRONLITE_RELIABILITY_PROFILE_DIR") != "" {
		count = 100000
	}
	cb := New(5, time.Minute)
	var baseline runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&baseline)
	profile := func(name string) {
		if dir := os.Getenv("CRONLITE_RELIABILITY_PROFILE_DIR"); dir != "" {
			f, err := os.Create(filepath.Join(dir, name+".heap"))
			if err != nil {
				t.Fatal(err)
			}
			if err := pprof.WriteHeapProfile(f); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	profile("baseline")
	for batch := 0; batch < 3; batch++ {
		for n := 0; n < count; n++ {
			url := fmt.Sprintf("https://destination-%d.example/hook", batch*count+n)
			cb.RecordFailure(url)
			cb.RecordSuccess(url)
		}
		runtime.GC()
		var current runtime.MemStats
		runtime.ReadMemStats(&current)
		t.Logf("batch=%d entries=%d heap_delta=%d objects_delta=%d goroutines=%d", batch+1, len(cb.states), int64(current.HeapAlloc)-int64(baseline.HeapAlloc), int64(current.HeapObjects)-int64(baseline.HeapObjects), runtime.NumGoroutine())
	}
	profile("final")
	runtime.KeepAlive(cb)
	if len(cb.states) != 0 {
		t.Fatalf("retained %d recovered destinations", len(cb.states))
	}
	// Forgetting recovered entries must still restart the failure threshold.
	url := "https://recovered.example/hook"
	for n := 0; n < 4; n++ {
		cb.RecordFailure(url)
	}
	if err := cb.Allow(url); err != nil {
		t.Fatal("recovered failure count did not reset")
	}
	cb.RecordFailure(url)
	if err := cb.Allow(url); err != ErrCircuitOpen {
		t.Fatalf("expected threshold to open circuit, got %v", err)
	}
}
