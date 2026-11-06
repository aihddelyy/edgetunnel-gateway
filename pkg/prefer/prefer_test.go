package prefer

import (
	"testing"
)

func TestParsePreferAndCleanPath(t *testing.T) {
	tests := []struct {
		name          string
		rawPath       string
		defaultPrefer string
		expectedHost  string
		expectedPath  string
	}{
		{
			name:          "No prefer param (fast path)",
			rawPath:       "/vless-endpoint",
			defaultPrefer: "default.example.com",
			expectedHost:  "default.example.com",
			expectedPath:  "/vless-endpoint",
		},
		{
			name:          "Empty path",
			rawPath:       "",
			defaultPrefer: "default.example.com",
			expectedHost:  "default.example.com",
			expectedPath:  "/",
		},
		{
			name:          "Root path slash",
			rawPath:       "/",
			defaultPrefer: "default.example.com",
			expectedHost:  "default.example.com",
			expectedPath:  "/",
		},
		{
			name:          "Standard prefer param at start",
			rawPath:       "/tunnel?prefer=1.1.1.1:443",
			defaultPrefer: "default.example.com",
			expectedHost:  "1.1.1.1:443",
			expectedPath:  "/tunnel",
		},
		{
			name:          "Prefer param in middle of query",
			rawPath:       "/tunnel?foo=bar&prefer=2.2.2.2:8443&baz=qux",
			defaultPrefer: "default.example.com",
			expectedHost:  "2.2.2.2:8443",
			expectedPath:  "/tunnel?foo=bar&baz=qux",
		},
		{
			name:          "URL-encoded prefer param",
			rawPath:       "/tunnel?prefer=104.16.1.1%3A443",
			defaultPrefer: "default.example.com",
			expectedHost:  "104.16.1.1:443",
			expectedPath:  "/tunnel",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host, path := ParsePreferAndCleanPath(tc.rawPath, tc.defaultPrefer)
			if host != tc.expectedHost {
				t.Errorf("host mismatch: got %q, want %q", host, tc.expectedHost)
			}
			if path != tc.expectedPath {
				t.Errorf("path mismatch: got %q, want %q", path, tc.expectedPath)
			}
		})
	}
}

func TestParseTargetHostPort(t *testing.T) {
	tests := []struct {
		target   string
		wantHost string
		wantPort int
	}{
		{"1.1.1.1", "1.1.1.1", 443},
		{"1.1.1.1:8443", "1.1.1.1", 8443},
		{"[2606:4700::1]:8443", "2606:4700::1", 8443},
		{"[2606:4700::1]", "2606:4700::1", 443},
		{"example.com:2053", "example.com", 2053},
		{"", "127.0.0.1", 443},
	}

	for _, tt := range tests {
		h, p := ParseTargetHostPort(tt.target)
		if h != tt.wantHost || p != tt.wantPort {
			t.Errorf("ParseTargetHostPort(%q) = (%q, %d), want (%q, %d)", tt.target, h, p, tt.wantHost, tt.wantPort)
		}
	}
}
