package analytics

import (
	"context"
	"log"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/djlord-it/easy-cron/internal/domain"
)

// RedisSink writes execution counts to Redis.
// Best-effort: errors are logged internally and never propagated.
type RedisSink struct {
	client *redis.Client
}

// NewRedisSink creates a new Redis analytics sink.
func NewRedisSink(client *redis.Client) *RedisSink {
	return &RedisSink{client: client}
}

// Record increments the execution count for the given event's minute bucket.
// Returns immediately if analytics is not enabled.
// Errors are logged but never returned; analytics must not affect dispatch correctness.
func (s *RedisSink) Record(ctx context.Context, event domain.TriggerEvent, config domain.AnalyticsConfig) {
	if !config.Enabled {
		return
	}

	key := buildKey(event.Namespace.String(), event.JobID.String(), event.ScheduledAt)
	ttl := resolveTTL(config.RetentionSeconds)

	pipe := s.client.Pipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, ttl)

	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("analytics: redis error: %v", err)
	}
}

// buildKey constructs the Redis key for a given execution.
// Format: ns:{namespace}:j:{jobID}:exec:{YYYYMMDDHHMM}
func buildKey(namespace, jobID string, scheduledAt time.Time) string {
	bucket := scheduledAt.UTC().Format("200601021504")
	return "ns:" + namespace + ":j:" + jobID + ":exec:" + bucket
}

// resolveTTL returns the TTL duration, using the default if retentionSeconds is zero or negative.
func resolveTTL(retentionSeconds int) time.Duration {
	if retentionSeconds <= 0 {
		return time.Duration(domain.DefaultRetentionSeconds) * time.Second
	}
	return time.Duration(retentionSeconds) * time.Second
}
