package metrics

import (
	"log"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// PrometheusSink implements Sink using Prometheus client library.
// All methods are non-blocking and fire-and-forget.
// Registration errors are logged but never propagated.
type PrometheusSink struct {
	// Scheduler metrics
	ticksTotal         prometheus.Counter
	tickErrorsTotal    prometheus.Counter
	jobsTriggeredTotal prometheus.Counter
	tickDuration       prometheus.Histogram
	tickDrift          prometheus.Histogram

	// Dispatcher metrics
	deliveryAttemptsTotal *prometheus.CounterVec
	deliveryOutcomesTotal *prometheus.CounterVec
	webhookDuration       prometheus.Histogram
	retryAttemptsTotal    *prometheus.CounterVec
	eventsInFlight        prometheus.Gauge

	// EventBus metrics
	bufferSize       prometheus.Gauge
	bufferCapacity   prometheus.Gauge
	bufferSaturation prometheus.Gauge
	emitErrorsTotal  prometheus.Counter

	// Reconciler metrics
	orphanedExecutions prometheus.Gauge
	executionLatency   prometheus.Histogram

	// Leader election metrics
	leaderIsLeader        prometheus.Gauge
	leaderAcquisitions    prometheus.Counter
	leaderLosses          *prometheus.CounterVec
}

// NewPrometheusSink creates a new Prometheus metrics sink.
// If registration fails, it logs a warning and returns a functional sink.
// Metrics that fail to register will be replaced with no-op collectors.
func NewPrometheusSink(reg prometheus.Registerer) *PrometheusSink {
	s := &PrometheusSink{}
	s.initSchedulerMetrics(reg)
	s.initDispatcherMetrics(reg)
	s.initEventBusMetrics(reg)
	s.initLeaderMetrics(reg)
	return s
}

func (s *PrometheusSink) initSchedulerMetrics(reg prometheus.Registerer) {
	s.ticksTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "cronlite_scheduler_ticks_total",
		Help: "Total number of scheduler ticks processed.",
	})
	s.tickErrorsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "cronlite_scheduler_tick_errors_total",
		Help: "Total number of scheduler tick errors.",
	})
	s.jobsTriggeredTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "cronlite_scheduler_jobs_triggered_total",
		Help: "Total number of jobs triggered (executions emitted).",
	})
	s.tickDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "cronlite_scheduler_tick_duration_seconds",
		Help:    "Duration of each scheduler tick in seconds.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10},
	})
	s.tickDrift = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "cronlite_scheduler_tick_drift_seconds",
		Help:    "Difference between actual tick time and expected interval in seconds.",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
	})

	s.register(reg, s.ticksTotal, "cronlite_scheduler_ticks_total")
	s.register(reg, s.tickErrorsTotal, "cronlite_scheduler_tick_errors_total")
	s.register(reg, s.jobsTriggeredTotal, "cronlite_scheduler_jobs_triggered_total")
	s.register(reg, s.tickDuration, "cronlite_scheduler_tick_duration_seconds")
	s.register(reg, s.tickDrift, "cronlite_scheduler_tick_drift_seconds")
}

func (s *PrometheusSink) initDispatcherMetrics(reg prometheus.Registerer) {
	s.deliveryAttemptsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cronlite_dispatcher_delivery_attempts_total",
		Help: "Total number of webhook delivery attempts.",
	}, []string{"attempt", "status_class"})

	s.deliveryOutcomesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cronlite_dispatcher_delivery_outcomes_total",
		Help: "Total number of final delivery outcomes per execution.",
	}, []string{"outcome"})

	s.webhookDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "cronlite_dispatcher_webhook_duration_seconds",
		Help:    "Webhook request latency in seconds (excludes backoff wait).",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	})

	s.retryAttemptsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cronlite_dispatcher_retry_attempts_total",
		Help: "Total number of retry attempts (excludes first attempt).",
	}, []string{"retryable"})

	s.eventsInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "cronlite_dispatcher_events_in_flight",
		Help: "Number of events currently being processed.",
	})

	s.register(reg, s.deliveryAttemptsTotal, "cronlite_dispatcher_delivery_attempts_total")
	s.register(reg, s.deliveryOutcomesTotal, "cronlite_dispatcher_delivery_outcomes_total")
	s.register(reg, s.webhookDuration, "cronlite_dispatcher_webhook_duration_seconds")
	s.register(reg, s.retryAttemptsTotal, "cronlite_dispatcher_retry_attempts_total")
	s.register(reg, s.eventsInFlight, "cronlite_dispatcher_events_in_flight")
}

func (s *PrometheusSink) initEventBusMetrics(reg prometheus.Registerer) {
	s.bufferSize = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "cronlite_eventbus_buffer_size",
		Help: "Current number of events in the event bus buffer.",
	})
	s.bufferCapacity = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "cronlite_eventbus_buffer_capacity",
		Help: "Total capacity of the event bus buffer.",
	})
	s.bufferSaturation = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "cronlite_eventbus_buffer_saturation",
		Help: "Buffer saturation ratio (0.0-1.0). Above 0.8 indicates risk of event loss.",
	})
	s.emitErrorsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "cronlite_eventbus_emit_errors_total",
		Help: "Total number of emit errors (buffer full).",
	})

	s.register(reg, s.bufferSize, "cronlite_eventbus_buffer_size")
	s.register(reg, s.bufferCapacity, "cronlite_eventbus_buffer_capacity")
	s.register(reg, s.bufferSaturation, "cronlite_eventbus_buffer_saturation")
	s.register(reg, s.emitErrorsTotal, "cronlite_eventbus_emit_errors_total")

	s.orphanedExecutions = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "cronlite_orphaned_executions",
		Help: "Current count of orphaned executions (status=emitted older than threshold).",
	})
	s.executionLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "cronlite_execution_latency_seconds",
		Help:    "End-to-end latency from scheduled_at to delivered (successful deliveries only).",
		Buckets: []float64{1, 5, 10, 30, 60, 120, 300, 600, 1800},
	})

	s.register(reg, s.orphanedExecutions, "cronlite_orphaned_executions")
	s.register(reg, s.executionLatency, "cronlite_execution_latency_seconds")
}

// register attempts to register a collector, logging any errors without propagating them.
func (s *PrometheusSink) register(reg prometheus.Registerer, c prometheus.Collector, name string) {
	if err := reg.Register(c); err != nil {
		log.Printf("metrics: failed to register %s: %v", name, err)
	}
}

func (s *PrometheusSink) TickStarted() {
	s.ticksTotal.Inc()
}

func (s *PrometheusSink) TickCompleted(duration time.Duration, jobsTriggered int, err error) {
	s.tickDuration.Observe(duration.Seconds())
	s.jobsTriggeredTotal.Add(float64(jobsTriggered))
	if err != nil {
		s.tickErrorsTotal.Inc()
	}
}

func (s *PrometheusSink) TickDrift(drift time.Duration) {
	d := drift.Seconds()
	if d < 0 {
		d = -d
	}
	s.tickDrift.Observe(d)
}

func (s *PrometheusSink) DeliveryAttemptCompleted(attempt int, statusClass string, duration time.Duration) {
	s.deliveryAttemptsTotal.WithLabelValues(strconv.Itoa(attempt), statusClass).Inc()
	s.webhookDuration.Observe(duration.Seconds())
}

func (s *PrometheusSink) DeliveryOutcome(outcome string) {
	s.deliveryOutcomesTotal.WithLabelValues(outcome).Inc()
}

func (s *PrometheusSink) RetryAttempt(retryable bool) {
	label := "false"
	if retryable {
		label = "true"
	}
	s.retryAttemptsTotal.WithLabelValues(label).Inc()
}

func (s *PrometheusSink) EventsInFlightIncr() {
	s.eventsInFlight.Inc()
}

func (s *PrometheusSink) EventsInFlightDecr() {
	s.eventsInFlight.Dec()
}

func (s *PrometheusSink) BufferSizeUpdate(size int) {
	s.bufferSize.Set(float64(size))
}

func (s *PrometheusSink) BufferCapacitySet(capacity int) {
	s.bufferCapacity.Set(float64(capacity))
}

func (s *PrometheusSink) BufferSaturationUpdate(saturation float64) {
	s.bufferSaturation.Set(saturation)
}

func (s *PrometheusSink) EmitError() {
	s.emitErrorsTotal.Inc()
}

func (s *PrometheusSink) OrphanedExecutionsUpdate(count int) {
	s.orphanedExecutions.Set(float64(count))
}

func (s *PrometheusSink) ExecutionLatencyObserve(latencySeconds float64) {
	s.executionLatency.Observe(latencySeconds)
}

func (s *PrometheusSink) initLeaderMetrics(reg prometheus.Registerer) {
	s.leaderIsLeader = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "cronlite_leader_is_leader",
		Help: "Whether this instance is currently the leader (1=leader, 0=follower).",
	})
	s.leaderAcquisitions = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "cronlite_leader_acquisitions_total",
		Help: "Total number of times this instance acquired leadership.",
	})
	s.leaderLosses = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cronlite_leader_losses_total",
		Help: "Total number of times this instance lost leadership.",
	}, []string{"reason"})

	s.register(reg, s.leaderIsLeader, "cronlite_leader_is_leader")
	s.register(reg, s.leaderAcquisitions, "cronlite_leader_acquisitions_total")
	s.register(reg, s.leaderLosses, "cronlite_leader_losses_total")
}

func (s *PrometheusSink) LeaderStatusChanged(isLeader bool) {
	if isLeader {
		s.leaderIsLeader.Set(1)
	} else {
		s.leaderIsLeader.Set(0)
	}
}

func (s *PrometheusSink) LeaderAcquired() {
	s.leaderAcquisitions.Inc()
}

func (s *PrometheusSink) LeaderLost(reason string) {
	s.leaderLosses.WithLabelValues(reason).Inc()
}
