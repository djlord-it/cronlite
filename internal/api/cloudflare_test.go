package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
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

func newTestCache(cidrs ...string) *RangeCache {
	var nets []*net.IPNet
	for _, c := range cidrs {
		_, cidr, _ := net.ParseCIDR(c)
		nets = append(nets, cidr)
	}
	return &RangeCache{
		ranges:      nets,
		lastUpdated: time.Now(),
	}
}

func TestCloudflareMiddleware_ValidRequest(t *testing.T) {
	cache := newTestCache("173.245.48.0/20")

	var capturedRemoteAddr string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedRemoteAddr = r.RemoteAddr
		w.WriteHeader(http.StatusOK)
	})

	handler := CloudflareMiddleware(cache, inner)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.RemoteAddr = "173.245.48.1:12345"
	req.Header.Set("CF-Connecting-IP", "203.0.113.50")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if capturedRemoteAddr != "203.0.113.50:0" {
		t.Errorf("expected RemoteAddr=203.0.113.50:0, got %s", capturedRemoteAddr)
	}
}

func TestCloudflareMiddleware_ValidRequest_IPv6Client(t *testing.T) {
	cache := newTestCache("173.245.48.0/20")

	var capturedRemoteAddr string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedRemoteAddr = r.RemoteAddr
		w.WriteHeader(http.StatusOK)
	})

	handler := CloudflareMiddleware(cache, inner)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.RemoteAddr = "173.245.48.1:12345"
	req.Header.Set("CF-Connecting-IP", "2001:db8::1")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if capturedRemoteAddr != "[2001:db8::1]:0" {
		t.Errorf("expected RemoteAddr=[2001:db8::1]:0, got %s", capturedRemoteAddr)
	}
}

func TestCloudflareMiddleware_MissingCFHeader(t *testing.T) {
	cache := newTestCache("173.245.48.0/20")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler should not be called")
	})

	handler := CloudflareMiddleware(cache, inner)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.RemoteAddr = "173.245.48.1:12345"
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}

	var body map[string]string
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body["error"] != "forbidden" {
		t.Errorf("expected error=forbidden, got %v", body)
	}
}

func TestCloudflareMiddleware_NonCloudflareIP(t *testing.T) {
	cache := newTestCache("173.245.48.0/20")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler should not be called")
	})

	handler := CloudflareMiddleware(cache, inner)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	req.Header.Set("CF-Connecting-IP", "203.0.113.50")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}

	var body map[string]string
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body["error"] != "forbidden" {
		t.Errorf("expected error=forbidden, got %v", body)
	}
}

func TestCloudflareMiddleware_HealthExempt(t *testing.T) {
	cache := newTestCache("173.245.48.0/20")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := CloudflareMiddleware(cache, inner)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (health exempt), got %d", w.Code)
	}
}

func TestCloudflareMiddleware_HealthExactMatch(t *testing.T) {
	cache := newTestCache("173.245.48.0/20")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler should not be called for /healthz")
	})

	handler := CloudflareMiddleware(cache, inner)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for /healthz, got %d", w.Code)
	}
}

func TestExtractConnectingIP_IPv4(t *testing.T) {
	ip := extractConnectingIP("1.2.3.4:5678")
	if ip != "1.2.3.4" {
		t.Errorf("expected 1.2.3.4, got %s", ip)
	}
}

func TestExtractConnectingIP_IPv6(t *testing.T) {
	ip := extractConnectingIP("[2001:db8::1]:5678")
	if ip != "2001:db8::1" {
		t.Errorf("expected 2001:db8::1, got %s", ip)
	}
}
