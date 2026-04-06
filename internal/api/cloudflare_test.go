package api

import (
	"net"
	"testing"
	"time"
)

func TestRangeCache_Contains_IPv4Match(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("173.245.48.0/20")
	cache := &RangeCache{ranges: []*net.IPNet{cidr}}

	if !cache.Contains("173.245.48.1") {
		t.Error("expected 173.245.48.1 to be in 173.245.48.0/20")
	}
}

func TestRangeCache_Contains_IPv4NoMatch(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("173.245.48.0/20")
	cache := &RangeCache{ranges: []*net.IPNet{cidr}}

	if cache.Contains("192.168.1.1") {
		t.Error("expected 192.168.1.1 to NOT be in 173.245.48.0/20")
	}
}

func TestRangeCache_Contains_IPv6Match(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("2400:cb00::/32")
	cache := &RangeCache{ranges: []*net.IPNet{cidr}}

	if !cache.Contains("2400:cb00::1") {
		t.Error("expected 2400:cb00::1 to be in 2400:cb00::/32")
	}
}

func TestRangeCache_Contains_InvalidIP(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("173.245.48.0/20")
	cache := &RangeCache{ranges: []*net.IPNet{cidr}}

	if cache.Contains("not-an-ip") {
		t.Error("expected invalid IP to return false")
	}
}

func TestRangeCache_HealthStatus_NotStale(t *testing.T) {
	now := time.Now()
	cache := &RangeCache{stale: false, lastUpdated: now}

	stale, lastUpdated := cache.HealthStatus()
	if stale {
		t.Error("expected stale=false")
	}
	if !lastUpdated.Equal(now) {
		t.Errorf("expected lastUpdated=%v, got %v", now, lastUpdated)
	}
}

func TestRangeCache_HealthStatus_Stale(t *testing.T) {
	cache := &RangeCache{stale: true}

	stale, lastUpdated := cache.HealthStatus()
	if !stale {
		t.Error("expected stale=true")
	}
	if !lastUpdated.IsZero() {
		t.Error("expected zero lastUpdated when never fetched")
	}
}

func TestParseCIDRList_Valid(t *testing.T) {
	input := []string{"173.245.48.0/20", "103.21.244.0/22"}
	nets := parseCIDRList(input)
	if len(nets) != 2 {
		t.Fatalf("expected 2 networks, got %d", len(nets))
	}
}

func TestParseCIDRList_SkipsInvalid(t *testing.T) {
	input := []string{"173.245.48.0/20", "not-a-cidr", "103.21.244.0/22"}
	nets := parseCIDRList(input)
	if len(nets) != 2 {
		t.Fatalf("expected 2 networks (skipping invalid), got %d", len(nets))
	}
}
