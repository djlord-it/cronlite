package api

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ── Fallback IP ranges ──────────────────────────────────────────────────────
// Source: https://www.cloudflare.com/ips-v4 and https://www.cloudflare.com/ips-v6
// These are compiled into the binary so the cache is never completely empty.

var fallbackIPv4Ranges = []string{
	"173.245.48.0/20",
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"141.101.64.0/18",
	"108.162.192.0/18",
	"190.93.240.0/20",
	"188.114.96.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
	"162.158.0.0/15",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"172.64.0.0/13",
	"131.0.72.0/22",
}

var fallbackIPv6Ranges = []string{
	"2400:cb00::/32",
	"2606:4700::/32",
	"2803:f800::/32",
	"2405:b500::/32",
	"2405:8100::/32",
	"2a06:98c0::/29",
	"2c0f:f248::/32",
}

// ── Range cache ─────────────────────────────────────────────────────────────

// RangeCache holds parsed Cloudflare IP ranges with thread-safe access.
type RangeCache struct {
	mu          sync.RWMutex
	ranges      []*net.IPNet
	lastUpdated time.Time
	stale       bool
}

// NewCloudflareRangeCache creates a cache pre-loaded with hardcoded fallback
// ranges, then attempts a live fetch. The cache is never empty.
// The caller should check Stale() and decide whether to fatal.
func NewCloudflareRangeCache() *RangeCache {
	fallback := parseCIDRList(append(fallbackIPv4Ranges, fallbackIPv6Ranges...))
	c := &RangeCache{
		ranges: fallback,
		stale:  true, // stale until live fetch succeeds
	}

	live, err := fetchRanges()
	if err != nil {
		log.Printf("cloudflare: live fetch failed, using fallback ranges: %v", err)
		return c
	}

	c.ranges = live
	c.lastUpdated = time.Now().UTC()
	c.stale = false
	return c
}

// Contains reports whether ip falls within any cached Cloudflare range.
func (c *RangeCache) Contains(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, n := range c.ranges {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// HealthStatus returns the cache staleness and last successful update time.
// Both fields are read under a single lock for consistency.
func (c *RangeCache) HealthStatus() (stale bool, lastUpdated time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stale, c.lastUpdated
}

// Stale reports whether the cache is using stale or fallback-only ranges.
func (c *RangeCache) Stale() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stale
}

// ── Fetch ───────────────────────────────────────────────────────────────────

const (
	cfIPv4URL    = "https://www.cloudflare.com/ips-v4"
	cfIPv6URL    = "https://www.cloudflare.com/ips-v6"
	fetchTimeout = 10 * time.Second
)

// fetchRanges downloads and parses Cloudflare's published IP ranges.
func fetchRanges() ([]*net.IPNet, error) {
	client := &http.Client{Timeout: fetchTimeout}

	var all []*net.IPNet
	for _, url := range []string{cfIPv4URL, cfIPv6URL} {
		nets, err := fetchCIDRsFromURL(client, url)
		if err != nil {
			return nil, err
		}
		all = append(all, nets...)
	}

	if len(all) == 0 {
		return nil, fmt.Errorf("no CIDR ranges found")
	}
	return all, nil
}

// fetchCIDRsFromURL downloads and parses CIDR ranges from a single URL.
func fetchCIDRsFromURL(client *http.Client, url string) ([]*net.IPNet, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}

	var nets []*net.IPNet
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		_, cidr, err := net.ParseCIDR(line)
		if err != nil {
			return nil, fmt.Errorf("parse CIDR %q from %s: %w", line, url, err)
		}
		nets = append(nets, cidr)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	return nets, nil
}

// ── Background refresh ──────────────────────────────────────────────────────

const refreshInterval = 24 * time.Hour

// StartRefresh launches a background goroutine that refreshes the cache
// every 24 hours. It stops when ctx is cancelled.
func (c *RangeCache) StartRefresh(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				live, err := fetchRanges()
				if err != nil {
					log.Printf("cloudflare: refresh failed, ranges are stale: %v", err)
					c.mu.Lock()
					c.stale = true
					c.mu.Unlock()
					continue
				}

				c.mu.Lock()
				c.ranges = live
				c.lastUpdated = time.Now().UTC()
				c.stale = false
				c.mu.Unlock()
				log.Printf("cloudflare: refreshed %d IP ranges", len(live))
			}
		}
	}()
}

// ── Middleware ───────────────────────────────────────────────────────────────

// CloudflareMiddleware rejects requests that did not arrive via Cloudflare
// and rewrites RemoteAddr with the real client IP from CF-Connecting-IP.
func CloudflareMiddleware(cache *RangeCache, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Exempt health checks so Railway probes work without Cloudflare.
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		// 1. Require CF-Connecting-IP header.
		clientIP := r.Header.Get("CF-Connecting-IP")
		if clientIP == "" {
			writeCFForbidden(w)
			return
		}

		// 2. Validate connecting IP is a Cloudflare edge node.
		connectingIP := extractConnectingIP(r.RemoteAddr)
		if !cache.Contains(connectingIP) {
			writeCFForbidden(w)
			return
		}

		// 3. Rewrite RemoteAddr with real client IP.
		if strings.Contains(clientIP, ":") {
			r.RemoteAddr = "[" + clientIP + "]:0" // IPv6
		} else {
			r.RemoteAddr = clientIP + ":0" // IPv4
		}

		next.ServeHTTP(w, r)
	})
}

// writeCFForbidden writes a 403 JSON response. The body is intentionally
// identical for all failure modes to avoid leaking validation details.
func writeCFForbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	w.Write([]byte(`{"error":"forbidden"}` + "\n"))
}

// extractConnectingIP strips the port from a RemoteAddr string.
// Handles both IPv4 ("1.2.3.4:1234") and IPv6 ("[::1]:1234") formats.
func extractConnectingIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr // fallback: return as-is
	}
	return host
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// parseCIDRList parses a slice of CIDR strings into net.IPNet values.
// Invalid entries are skipped with a log warning.
func parseCIDRList(cidrs []string) []*net.IPNet {
	var nets []*net.IPNet
	for _, s := range cidrs {
		_, cidr, err := net.ParseCIDR(s)
		if err != nil {
			log.Printf("cloudflare: invalid fallback CIDR %q: %v", s, err)
			continue
		}
		nets = append(nets, cidr)
	}
	return nets
}
