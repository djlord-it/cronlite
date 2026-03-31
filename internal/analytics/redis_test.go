package analytics

import (
	"testing"
	"time"
)

func TestBuildKey(t *testing.T) {
	tests := []struct {
		name        string
		namespace   string
		jobID       string
		scheduledAt time.Time
		want        string
	}{
		{
			name:        "basic key",
			namespace:   "ns1",
			jobID:       "j1",
			scheduledAt: time.Date(2024, 1, 15, 9, 30, 0, 0, time.UTC),
			want:        "ns:ns1:j:j1:exec:202401150930",
		},
		{
			name:        "hyphenated jobID and year boundary",
			namespace:   "prod",
			jobID:       "abc-123",
			scheduledAt: time.Date(2024, 12, 31, 23, 59, 0, 0, time.UTC),
			want:        "ns:prod:j:abc-123:exec:202412312359",
		},
		{
			name:        "non-UTC timezone is converted to UTC",
			namespace:   "ns1",
			jobID:       "j1",
			scheduledAt: time.Date(2024, 1, 15, 12, 30, 0, 0, time.FixedZone("EST", -5*60*60)),
			want:        "ns:ns1:j:j1:exec:202401151730", // 12:30 EST = 17:30 UTC
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildKey(tt.namespace, tt.jobID, tt.scheduledAt)
			if got != tt.want {
				t.Errorf("buildKey(%q, %q, %v) = %q, want %q",
					tt.namespace, tt.jobID, tt.scheduledAt, got, tt.want)
			}
		})
	}
}

func TestResolveTTL(t *testing.T) {
	tests := []struct {
		name             string
		retentionSeconds int
		want             time.Duration
	}{
		{
			name:             "zero uses default 86400s",
			retentionSeconds: 0,
			want:             86400 * time.Second,
		},
		{
			name:             "negative uses default 86400s",
			retentionSeconds: -1,
			want:             86400 * time.Second,
		},
		{
			name:             "positive 3600",
			retentionSeconds: 3600,
			want:             3600 * time.Second,
		},
		{
			name:             "positive 86400",
			retentionSeconds: 86400,
			want:             86400 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveTTL(tt.retentionSeconds)
			if got != tt.want {
				t.Errorf("resolveTTL(%d) = %v, want %v",
					tt.retentionSeconds, got, tt.want)
			}
		})
	}
}
