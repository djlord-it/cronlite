package webhookurl

import "testing"

func TestValidate_AllowsPublicHTTPURLs(t *testing.T) {
	tests := []string{
		"http://example.com/webhook",
		"https://api.example.com/v1/hooks",
		"http://8.8.8.8:8080/callback",
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			if err := Validate(rawURL); err != nil {
				t.Fatalf("Validate(%q) returned error: %v", rawURL, err)
			}
		})
	}
}

func TestValidate_BlocksLocalAndReservedDestinations(t *testing.T) {
	tests := []string{
		"http://localhost/hook",
		"http://LOCALHOST/hook",
		"http://app.localhost/hook",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://127.0.0.1/hook",
		"http://10.0.0.1/hook",
		"http://172.16.0.1/hook",
		"http://192.168.1.1/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://0.0.0.0/hook",
		"http://[::]/hook",
		"http://[::1]/hook",
		"http://[fe80::1%25eth0]/hook",
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			if err := Validate(rawURL); err == nil {
				t.Fatalf("Validate(%q) returned nil, want error", rawURL)
			}
		})
	}
}
