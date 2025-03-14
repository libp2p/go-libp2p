package websocket

import (
	"net"
	"net/http"
	"testing"
)

func TestGetRealIP(t *testing.T) {
	tests := []struct {
		name     string
		addr     net.Addr
		header   map[string]string
		expected string
	}{
		{
			name:     "No header, simple IP",
			addr:     &net.TCPAddr{IP: net.IPv4(192, 168, 1, 1), Port: 8080},
			header:   map[string]string{},
			expected: "192.168.1.1:8080",
		},
		{
			name: "With X-Real-IP header",
			addr: &net.TCPAddr{IP: net.IPv4(192, 168, 1, 1), Port: 8080},
			header: map[string]string{
				"X-Real-IP": "203.0.113.1",
			},
			expected: "203.0.113.1:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			for key, value := range tt.header {
				header.Set(key, value)
			}

			result := GetRealIP(tt.addr, header)
			if result != tt.expected {
				t.Errorf("GetRealIP() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetRealIPFromHeader(t *testing.T) {
	tests := []struct {
		name     string
		header   map[string]string
		expected string
	}{
		{
			name: "X-Real-IP header",
			header: map[string]string{
				"X-Real-IP": "192.168.1.1",
			},
			expected: "192.168.1.1",
		},
		{
			name: "X-Forwarded-For header single IP",
			header: map[string]string{
				"X-Forwarded-For": "203.0.113.1",
			},
			expected: "203.0.113.1",
		},
		{
			name: "X-Forwarded-For header multiple IPs",
			header: map[string]string{
				"X-Forwarded-For": "203.0.113.1,192.168.1.1,10.0.0.1",
			},
			expected: "203.0.113.1",
		},
		{
			name: "CF-Connecting-IP header",
			header: map[string]string{
				"CF-Connecting-IP": "2001:db8::1",
			},
			expected: "2001:db8::1",
		},
		{
			name: "True-Client-IP header",
			header: map[string]string{
				"True-Client-IP": "192.0.2.1",
			},
			expected: "192.0.2.1",
		},
		{
			name: "Invalid IP in X-Real-IP",
			header: map[string]string{
				"X-Real-IP": "invalid-ip",
			},
			expected: "",
		},
		{
			name:     "Empty header",
			header:   map[string]string{},
			expected: "",
		},
		{
			name: "Multiple header with priority",
			header: map[string]string{
				"X-Real-IP":        "192.168.1.1",
				"X-Forwarded-For":  "203.0.113.1",
				"CF-Connecting-IP": "2001:db8::1",
			},
			expected: "192.168.1.1", // X-Real-IP should take precedence
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			for key, value := range tt.header {
				header.Set(key, value)
			}

			got := GetRealIPFromHeader(header)

			if tt.expected == "" {
				if got != nil {
					t.Errorf("GetRealIPFromHeader() = %v, want nil", got)
				}
				return
			}

			expected := net.ParseIP(tt.expected)
			if !got.Equal(expected) {
				t.Errorf("GetRealIPFromHeader() = %v, want %v", got, expected)
			}
		})
	}
}

func TestValidateIp(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "Valid IPv4",
			input:    "192.168.1.1",
			expected: true,
		},
		{
			name:     "Valid IPv6",
			input:    "2001:db8::1",
			expected: true,
		},
		{
			name:     "Empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "Invalid IP",
			input:    "invalid-ip",
			expected: false,
		},
		{
			name:     "Partial IP",
			input:    "192.168",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateIp(tt.input)
			if (got != nil) != tt.expected {
				t.Errorf("validateIp(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestIpPort(t *testing.T) {
	tests := []struct {
		name     string
		ip       string
		port     int
		expected string
	}{
		{
			name:     "IPv4 address",
			ip:       "192.168.1.1",
			port:     8080,
			expected: "192.168.1.1:8080",
		},
		{
			name:     "IPv6 address",
			ip:       "2001:db8::1",
			port:     8080,
			expected: "[2001:db8::1]:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			got := IpPort(ip, tt.port)
			if got != tt.expected {
				t.Errorf("IpPort() = %v, want %v", got, tt.expected)
			}
		})
	}
}
