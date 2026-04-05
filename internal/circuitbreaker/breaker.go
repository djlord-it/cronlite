package circuitbreaker

import (
	"errors"
	"sync"
	"time"
)

var ErrCircuitOpen = errors.New("circuit breaker is open")

type state int

const (
	stateClosed   state = iota
	stateOpen
	stateHalfOpen
)

type urlState struct {
	state               state
	consecutiveFailures int
	openedAt            time.Time
	halfOpenProbes      int
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
	cb.mu.Lock()
	defer cb.mu.Unlock()

	s, ok := cb.states[url]
	if !ok {
		return nil
	}

	switch s.state {
	case stateClosed:
		return nil
	case stateOpen:
		if time.Since(s.openedAt) >= cb.cooldown {
			s.state = stateHalfOpen
			s.halfOpenProbes = 1 // this call counts as the first probe
			return nil
		}
		return ErrCircuitOpen
	case stateHalfOpen:
		if s.halfOpenProbes < cb.halfOpenProbes {
			s.halfOpenProbes++
			return nil
		}
		return ErrCircuitOpen
	default:
		return nil
	}
}

func (cb *CircuitBreaker) RecordSuccess(url string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	s, ok := cb.states[url]
	if !ok {
		return
	}
	s.state = stateClosed
	s.consecutiveFailures = 0
	s.halfOpenProbes = 0
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
