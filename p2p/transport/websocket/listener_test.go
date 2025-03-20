package websocket

import (
	"net/netip"
	"testing"
)

func TestIsProxyTrusted(t *testing.T) {
	tests := []struct {
		name           string
		trustedProxies []string
		remoteAddr     string
		want           bool
	}{
		{
			name:           "Single IP trusted",
			trustedProxies: []string{"192.168.1.1/32"},
			remoteAddr:     "192.168.1.1:1234",
			want:           true,
		},
		{
			name:           "IP not in trusted list",
			trustedProxies: []string{"192.168.1.1/32"},
			remoteAddr:     "192.168.1.2:1234",
			want:           false,
		},
		{
			name:           "CIDR range trusted",
			trustedProxies: []string{"192.168.2.0/24", "192.168.1.0/24"},
			remoteAddr:     "192.168.1.100:1234",
			want:           true,
		},
		{
			name:           "IPv6 address trusted",
			trustedProxies: []string{"2001:db8::1/128"},
			remoteAddr:     "[2001:db8::1]:1234",
			want:           true,
		},
		{
			name:           "IPv6 CIDR range trusted",
			trustedProxies: []string{"2001:db8::/32", "2001:db9::/32"},
			remoteAddr:     "[2001:db8:1:2:3:4:5:6]:1234",
			want:           true,
		},
		{
			name:           "Empty trusted proxies list",
			trustedProxies: []string{},
			remoteAddr:     "192.168.1.1:1234",
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var trustedProxies []netip.Prefix
			for _, cidr := range tt.trustedProxies {
				prefix, _ := netip.ParsePrefix(cidr)
				trustedProxies = append(trustedProxies, prefix)
			}

			got := isProxyTrusted(trustedProxies, &fakeAddr{tt.remoteAddr})
			if got != tt.want {
				t.Errorf("isProxyTrusted() = %v, want %v", got, tt.want)
			}
		})
	}
}
