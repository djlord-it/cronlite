package circuitbreaker

import "testing"

func TestAbandonedProbeCleanupIsIdempotentAndEpochScoped(t *testing.T) {
	cb := New(1, 0)
	cb.RecordFailure("url")
	old, err := cb.AllowWithCancel("url")
	if err != nil {
		t.Fatal(err)
	}
	cb.RecordFailure("url") // start another half-open epoch
	var cancels []func()
	for i := 0; i < DefaultHalfOpenProbes; i++ {
		cancel, err := cb.AllowWithCancel("url")
		if err != nil {
			t.Fatal(err)
		}
		cancels = append(cancels, cancel)
	}
	old()
	old()
	if _, err := cb.AllowWithCancel("url"); err != ErrCircuitOpen {
		t.Fatal("old cleanup released a newer permit")
	}
	cancels[0]()
	cancels[0]()
	if _, err := cb.AllowWithCancel("url"); err != nil {
		t.Fatal("abandoned permit not released")
	}
	if _, err := cb.AllowWithCancel("url"); err != ErrCircuitOpen {
		t.Fatal("duplicate cleanup released two permits")
	}
}
