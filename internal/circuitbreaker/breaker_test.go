package circuitbreaker

import (
	"testing"
	"time"
)

func TestAllow_UnknownURL_Allowed(t *testing.T) {
	cb := New(3, 5*time.Second)
	if err := cb.Allow("http://example.com/hook"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestAllow_BelowThreshold_Allowed(t *testing.T) {
	cb := New(3, 5*time.Second)
	url := "http://example.com/hook"
	cb.RecordFailure(url)
	cb.RecordFailure(url)
	if err := cb.Allow(url); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestAllow_AtThreshold_Open(t *testing.T) {
	cb := New(3, 5*time.Second)
	url := "http://example.com/hook"
	cb.RecordFailure(url)
	cb.RecordFailure(url)
	cb.RecordFailure(url)
	if err := cb.Allow(url); err == nil {
		t.Fatal("expected ErrCircuitOpen, got nil")
	}
}

func TestAllow_OpenAfterCooldown_HalfOpen(t *testing.T) {
	cb := New(3, 10*time.Millisecond)
	url := "http://example.com/hook"
	cb.RecordFailure(url)
	cb.RecordFailure(url)
	cb.RecordFailure(url)
	time.Sleep(15 * time.Millisecond)

	// DefaultHalfOpenProbes (3) requests should be allowed
	for i := 0; i < DefaultHalfOpenProbes; i++ {
		if err := cb.Allow(url); err != nil {
			t.Fatalf("probe %d: expected nil (probe allowed), got %v", i+1, err)
		}
	}
	// Next request should be blocked
	if err := cb.Allow(url); err == nil {
		t.Fatal("expected ErrCircuitOpen after all half-open probes used")
	}
}

func TestRecordSuccess_ResetsToClose(t *testing.T) {
	cb := New(3, 10*time.Millisecond)
	url := "http://example.com/hook"
	cb.RecordFailure(url)
	cb.RecordFailure(url)
	cb.RecordFailure(url)
	time.Sleep(15 * time.Millisecond)
	if err := cb.Allow(url); err != nil {
		t.Fatalf("expected nil (probe allowed), got %v", err)
	}
	cb.RecordSuccess(url)
	if err := cb.Allow(url); err != nil {
		t.Fatalf("expected nil after reset, got %v", err)
	}
}

func TestRecordFailure_HalfOpenReOpens(t *testing.T) {
	cb := New(3, 10*time.Millisecond)
	url := "http://example.com/hook"
	cb.RecordFailure(url)
	cb.RecordFailure(url)
	cb.RecordFailure(url)
	time.Sleep(15 * time.Millisecond)
	// Allow one probe
	if err := cb.Allow(url); err != nil {
		t.Fatalf("expected nil (probe allowed), got %v", err)
	}
	// Probe fails — circuit should re-open (failure count exceeds threshold)
	cb.RecordFailure(url)
	// Even remaining half-open probes should be blocked after re-open
	if err := cb.Allow(url); err == nil {
		t.Fatal("expected ErrCircuitOpen after probe failure re-open")
	}
}

func TestHalfOpen_CustomProbeCount(t *testing.T) {
	cb := New(3, 10*time.Millisecond).WithHalfOpenProbes(5)
	url := "http://example.com/hook"
	cb.RecordFailure(url)
	cb.RecordFailure(url)
	cb.RecordFailure(url)
	time.Sleep(15 * time.Millisecond)

	// 5 probes should be allowed (the first transitions to half-open + 4 more)
	for i := 0; i < 5; i++ {
		if err := cb.Allow(url); err != nil {
			t.Fatalf("probe %d: expected nil, got %v", i+1, err)
		}
	}
	if err := cb.Allow(url); err == nil {
		t.Fatal("expected ErrCircuitOpen after all 5 probes used")
	}
}

func TestHalfOpen_SuccessResetsProbeCounter(t *testing.T) {
	cb := New(3, 10*time.Millisecond)
	url := "http://example.com/hook"
	cb.RecordFailure(url)
	cb.RecordFailure(url)
	cb.RecordFailure(url)
	time.Sleep(15 * time.Millisecond)
	// Use one probe
	if err := cb.Allow(url); err != nil {
		t.Fatalf("expected nil (probe allowed), got %v", err)
	}
	// Probe succeeds — circuit closes
	cb.RecordSuccess(url)
	// Should now allow unlimited requests (closed state)
	for i := 0; i < 100; i++ {
		if err := cb.Allow(url); err != nil {
			t.Fatalf("request %d after reset: expected nil, got %v", i, err)
		}
	}
}

func TestRecordSuccess_ClosedState_NoOp(t *testing.T) {
	cb := New(3, 5*time.Second)
	url := "http://example.com/hook"
	cb.RecordSuccess(url)
	if err := cb.Allow(url); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestIndependentURLs(t *testing.T) {
	cb := New(2, 5*time.Second)
	url1 := "http://a.com/hook"
	url2 := "http://b.com/hook"
	cb.RecordFailure(url1)
	cb.RecordFailure(url1)
	if err := cb.Allow(url1); err == nil {
		t.Fatal("expected url1 open")
	}
	if err := cb.Allow(url2); err != nil {
		t.Fatalf("expected url2 allowed, got %v", err)
	}
}
