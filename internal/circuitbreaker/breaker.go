package circuitbreaker

import (
	"errors"
	"sync"
	"time"
)

var ErrCircuitOpen = errors.New("circuit breaker is open")

type state int

const (
	stateClosed state = iota
	stateOpen
	stateHalfOpen
)

type urlState struct {
	state               state
	consecutiveFailures int
	openedAt            time.Time
	halfOpenProbes      int
	probeEpoch          uint64
}

// DefaultHalfOpenProbes is the number of probe requests allowed in half-open
// state before the circuit must decide (close on success or re-open on failure).
const DefaultHalfOpenProbes = 3

type CircuitBreaker struct {
	mu             sync.Mutex
	states         map[string]*urlState
	threshold      int
	cooldown       time.Duration
	halfOpenProbes int
}

func New(threshold int, cooldown time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		states:         make(map[string]*urlState),
		threshold:      threshold,
		cooldown:       cooldown,
		halfOpenProbes: DefaultHalfOpenProbes,
	}
}

// WithHalfOpenProbes sets the number of probe requests allowed in half-open state.
func (cb *CircuitBreaker) WithHalfOpenProbes(n int) *CircuitBreaker {
	if n < 1 {
		n = 1
	}
	cb.halfOpenProbes = n
	return cb
}

func (cb *CircuitBreaker) Allow(url string) error {
	_, err := cb.AllowWithCancel(url)
	return err
}

// AllowWithCancel returns neutral cleanup for an abandoned half-open admission.
// Cleanup is idempotent and cannot release a permit in a newer circuit epoch.
func (cb *CircuitBreaker) AllowWithCancel(url string) (func(), error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	s, ok := cb.states[url]
	if !ok {
		return nil, nil
	}

	switch s.state {
	case stateClosed:
		return nil, nil
	case stateOpen:
		if time.Since(s.openedAt) >= cb.cooldown {
			s.state = stateHalfOpen
			s.halfOpenProbes = 1 // this call counts as the first probe
			s.probeEpoch++
			return cb.cancelProbe(url, s), nil
		}
		return nil, ErrCircuitOpen
	case stateHalfOpen:
		if s.halfOpenProbes < cb.halfOpenProbes {
			s.halfOpenProbes++
			return cb.cancelProbe(url, s), nil
		}
		return nil, ErrCircuitOpen
	default:
		return nil, nil
	}
}

func (cb *CircuitBreaker) cancelProbe(url string, admitted *urlState) func() {
	epoch := admitted.probeEpoch
	var once sync.Once
	return func() {
		once.Do(func() {
			cb.mu.Lock()
			defer cb.mu.Unlock()
			if current := cb.states[url]; current == admitted && current.state == stateHalfOpen && current.probeEpoch == epoch && current.halfOpenProbes > 0 {
				current.halfOpenProbes--
			}
		})
	}
}

func (cb *CircuitBreaker) RecordSuccess(url string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// Absence is the closed state. Release recovered destination URLs rather
	// than retaining every destination that has ever failed.
	delete(cb.states, url)
}

func (cb *CircuitBreaker) RecordFailure(url string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	s, ok := cb.states[url]
	if !ok {
		s = &urlState{}
		cb.states[url] = s
	}

	s.consecutiveFailures++
	if s.consecutiveFailures >= cb.threshold {
		s.state = stateOpen
		s.openedAt = time.Now()
	}
}
