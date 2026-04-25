package webhookurl

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Validate rejects webhook URLs that are malformed or target local/private
// network destinations.
func Validate(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("host is required")
	}

	host := strings.ToLower(u.Hostname())
	if isForbiddenHostname(host) {
		return fmt.Errorf("forbidden host")
	}

	if ip := net.ParseIP(stripIPv6Zone(host)); ip != nil && isForbiddenIP(ip) {
		return fmt.Errorf("forbidden ip")
	}

	return nil
}

func isForbiddenHostname(host string) bool {
	return host == "localhost" ||
		strings.HasSuffix(host, ".localhost") ||
		host == "metadata.google.internal"
}

func stripIPv6Zone(host string) string {
	if i := strings.LastIndex(host, "%"); i != -1 {
		return host[:i]
	}
	return host
}

func isForbiddenIP(ip net.IP) bool {
	return ip.IsUnspecified() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast()
}
