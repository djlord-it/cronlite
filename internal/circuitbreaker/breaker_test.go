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
	if err := cb.Allow(url); err != nil {
		t.Fatalf("expected nil (probe allowed), got %v", err)
	}
	if err := cb.Allow(url); err == nil {
		t.Fatal("expected ErrCircuitOpen while half-open probe in flight")
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
	if err := cb.Allow(url); err != nil {
		t.Fatalf("expected nil (probe allowed), got %v", err)
	}
	cb.RecordFailure(url)
	if err := cb.Allow(url); err == nil {
		t.Fatal("expected ErrCircuitOpen after probe failure re-open")
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
