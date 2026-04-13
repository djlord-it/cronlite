package api

import (
	"fmt"
	"net"
	"net/url"
	"time"
)

func validateTimezone(tz string) error {
	_, err := time.LoadLocation(tz)
	return err
}

func validateWebhookURL(rawURL string) error {
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

	host := u.Hostname()

	// Block known metadata hostnames
	if host == "metadata.google.internal" {
		return fmt.Errorf("metadata service addresses are not allowed")
	}

	// Block localhost variants
	if host == "localhost" {
		return fmt.Errorf("localhost is not allowed")
	}

	// Block private/reserved IPs
	ip := net.ParseIP(host)
	if ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("private or reserved IP addresses are not allowed")
		}
	}

	return nil
}
