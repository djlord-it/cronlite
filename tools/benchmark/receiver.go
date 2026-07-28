package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type ReceiverLimits struct {
	MaxBodyBytes int64
	MaxCallbacks int
	MaxDelay     time.Duration
}

func defaultReceiverLimits() ReceiverLimits {
	return ReceiverLimits{
		MaxBodyBytes: 64 << 10,
		MaxCallbacks: 1_000_000,
		MaxDelay:     15 * time.Minute,
	}
}

type BehaviorPlan struct {
	Statuses []int
	Delay    time.Duration
}

type behaviorState struct {
	plan  BehaviorPlan
	count int
}

type CallbackSummary struct {
	Count               int
	DuplicateExecution  bool
	DuplicateAttempt    bool
	ConcurrentDuplicate bool
	SignatureFailures   int
	PayloadChanged      bool
}

type callbackStore struct {
	mu           sync.Mutex
	observations []CallbackObservation
	plans        map[string]*behaviorState
	active       map[string]int
	maxActive    map[string]int
	terminal     map[string]bool
	attempts     map[string]int
	bodyHashes   map[string]string
	summaries    map[string]CallbackSummary
}

func newCallbackStore() *callbackStore {
	store := &callbackStore{
		plans:      make(map[string]*behaviorState),
		active:     make(map[string]int),
		maxActive:  make(map[string]int),
		terminal:   make(map[string]bool),
		attempts:   make(map[string]int),
		bodyHashes: make(map[string]string),
		summaries:  make(map[string]CallbackSummary),
	}
	store.plans["success"] = &behaviorState{
		plan: BehaviorPlan{Statuses: []int{http.StatusNoContent}},
	}
	return store
}

func (s *callbackStore) registerPlan(plan BehaviorPlan) string {
	if len(plan.Statuses) == 0 {
		plan.Statuses = []int{http.StatusNoContent}
	}
	token := uuid.NewString()
	s.mu.Lock()
	s.plans[token] = &behaviorState{plan: plan}
	s.mu.Unlock()
	return token
}

func (s *callbackStore) planStep(token string, limits ReceiverLimits) (int, time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.plans[token]
	if !ok {
		return http.StatusNotFound, 0, false
	}
	index := state.count
	state.count++
	if index >= len(state.plan.Statuses) {
		index = len(state.plan.Statuses) - 1
	}
	status := state.plan.Statuses[index]
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
	}
	delay := state.plan.Delay
	if delay < 0 {
		delay = 0
	}
	if delay > limits.MaxDelay {
		delay = limits.MaxDelay
	}
	return status, delay, true
}

func (s *callbackStore) begin(executionID string) (active int, afterTerminal bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[executionID]++
	active = s.active[executionID]
	if active > s.maxActive[executionID] {
		s.maxActive[executionID] = active
	}
	summary := s.summaries[executionID]
	if active > 1 {
		summary.ConcurrentDuplicate = true
	}
	s.summaries[executionID] = summary
	return active, s.terminal[executionID]
}

func (s *callbackStore) finish(observation CallbackObservation, maxCallbacks int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.active[observation.ExecutionID] > 0 {
		s.active[observation.ExecutionID]--
	}

	summary := s.summaries[observation.ExecutionID]
	summary.Count++
	if summary.Count > 1 {
		summary.DuplicateExecution = true
	}
	if observation.AttemptID != "" {
		s.attempts[observation.AttemptID]++
		if s.attempts[observation.AttemptID] > 1 {
			summary.DuplicateAttempt = true
		}
	}
	if !observation.SignatureValid {
		summary.SignatureFailures++
	}
	if previous, ok := s.bodyHashes[observation.ExecutionID]; ok && previous != observation.BodySHA256 {
		summary.PayloadChanged = true
	} else if !ok {
		s.bodyHashes[observation.ExecutionID] = observation.BodySHA256
	}
	if s.maxActive[observation.ExecutionID] > 1 {
		summary.ConcurrentDuplicate = true
	}
	s.summaries[observation.ExecutionID] = summary

	if len(s.observations) < maxCallbacks {
		s.observations = append(s.observations, observation)
	}
}

func (s *callbackStore) markTerminal(executionID string) {
	s.mu.Lock()
	s.terminal[executionID] = true
	s.mu.Unlock()
}

func (s *callbackStore) snapshot() []CallbackObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]CallbackObservation, len(s.observations))
	copy(result, s.observations)
	return result
}

func (s *callbackStore) callbacksFor(executionID string) []CallbackObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []CallbackObservation
	for index := range s.observations {
		observation := &s.observations[index]
		if observation.ExecutionID == executionID {
			result = append(result, *observation)
		}
	}
	return result
}

func (s *callbackStore) summary(executionID string) CallbackSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.summaries[executionID]
}

func (s *callbackStore) activeCount(executionID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[executionID]
}

func newReceiverHandler(store *callbackStore, secret string, limits ReceiverLimits) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /baseline", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /hook/{token}", func(w http.ResponseWriter, r *http.Request) {
		handleCallback(w, r, store, secret, limits)
	})
	return mux
}

func handleCallback(
	w http.ResponseWriter,
	r *http.Request,
	store *callbackStore,
	secret string,
	limits ReceiverLimits,
) {
	arrivedAt := time.Now().UTC()
	token := r.PathValue("token")
	status, delay, ok := store.planStep(token, limits)
	if !ok {
		http.NotFound(w, r)
		return
	}

	reader := http.MaxBytesReader(w, r.Body, limits.MaxBodyBytes)
	body, readErr := io.ReadAll(reader)
	_ = r.Body.Close()
	oversized := readErr != nil && strings.Contains(readErr.Error(), "request body too large")
	if len(body) > int(limits.MaxBodyBytes) {
		body = body[:limits.MaxBodyBytes]
	}

	executionID := r.Header.Get("X-CronLite-Execution-ID")
	attemptID := r.Header.Get("X-CronLite-Event-ID")
	active, afterTerminal := store.begin(executionID)

	if delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-r.Context().Done():
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}

	if oversized {
		status = http.StatusRequestEntityTooLarge
	}
	responseStartedAt := time.Now().UTC()
	w.WriteHeader(status)
	responseCompletedAt := time.Now().UTC()

	store.finish(CallbackObservation{
		ReceivedAt:          arrivedAt,
		ResponseStartedAt:   responseStartedAt,
		ResponseCompletedAt: responseCompletedAt,
		ExecutionID:         executionID,
		AttemptID:           attemptID,
		SignatureValid:      verifySignature(secret, body, r.Header.Get("X-CronLite-Signature")),
		StatusCode:          status,
		BodySHA256:          hashBody(body),
		Body:                string(body),
		Headers: map[string]string{
			"X-CronLite-Execution-ID": executionID,
			"X-CronLite-Event-ID":     attemptID,
		},
		ConcurrentForExecution: active,
		AfterTerminal:          afterTerminal,
	}, limits.MaxCallbacks)
}

func verifySignature(secret string, body []byte, signature string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func hashBody(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

type receiverServer struct {
	server   *http.Server
	listener net.Listener
	store    *callbackStore
}

func startReceiver(addr, secret string, limits ReceiverLimits) (*receiverServer, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	store := newCallbackStore()
	server := &http.Server{
		Handler:           newReceiverHandler(store, secret, limits),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	result := &receiverServer{server: server, listener: listener, store: store}
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = listener.Close()
		}
	}()
	return result, nil
}

func (s *receiverServer) Close(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}
