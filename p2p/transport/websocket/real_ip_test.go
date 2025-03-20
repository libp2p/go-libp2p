package websocket

import (
	"github.com/stretchr/testify/require"
	"net"
	"net/http"
	"testing"
)

func TestDefaultGetRealAddr(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		header     http.Header
		want       string
	}{
		{
			name:       "basic remote addr without header",
			remoteAddr: "192.168.1.1:1234",
			header:     http.Header{},
			want:       "192.168.1.1:1234",
		},
		{
			name:       "with Forwarded header",
			remoteAddr: "10.0.0.1:1234",
			header: http.Header{
				"Forwarded": []string{"for=192.168.1.2"},
			},
			want: "192.168.1.2:1234",
		},
		{
			name:       "with Forwarded header and IPv6",
			remoteAddr: "[::1]:1234",
			header: http.Header{
				"Forwarded": []string{`for="[2001:db8:cafe::17]"`},
			},
			want: "[2001:db8:cafe::17]:1234",
		},
		{
			name:       "with X-Forwarded-For header",
			remoteAddr: "10.0.0.1:1234",
			header: http.Header{
				"X-Forwarded-For": []string{"192.168.1.2, 10.0.0.1"},
			},
			want: "192.168.1.2:1234",
		},
		{
			name:       "with multiple Forwarded values",
			remoteAddr: "10.0.0.1:1234",
			header: http.Header{
				"Forwarded": []string{"for=192.168.1.2;by=proxy1, for=192.168.1.3"},
			},
			want: "192.168.1.2:1234",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultGetRealAddr(&fakeAddr{tt.remoteAddr}, tt.header)
			require.Equal(t, tt.want, got)
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
		port     string
		expected string
	}{
		{
			name:     "IPv4 address",
			ip:       "192.168.1.1",
			port:     "8080",
			expected: "192.168.1.1:8080",
		},
		{
			name:     "IPv6 address",
			ip:       "2001:db8::1",
			port:     "8080",
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

type fakeAddr struct {
	addr string
}

func (f fakeAddr) Network() string {
	return "tcp"
}

func (f fakeAddr) String() string {
	return f.addr
}
