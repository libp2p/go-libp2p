package websocket

import (
	"github.com/stretchr/testify/require"
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
			trustedProxies: []string{"192.168.1.1"},
			remoteAddr:     "192.168.1.1:1234",
			want:           true,
		},
		{
			name:           "IP not in trusted list",
			trustedProxies: []string{"192.168.1.1"},
			remoteAddr:     "192.168.1.2:1234",
			want:           false,
		},
		{
			name:           "CIDR range trusted",
			trustedProxies: []string{"192.168.1.0/24"},
			remoteAddr:     "192.168.1.100:1234",
			want:           true,
		},
		{
			name:           "IPv6 address trusted",
			trustedProxies: []string{"2001:db8::1"},
			remoteAddr:     "[2001:db8::1]:1234",
			want:           true,
		},
		{
			name:           "IPv6 CIDR range trusted",
			trustedProxies: []string{"2001:db8::/32"},
			remoteAddr:     "[2001:db8:1:2:3:4:5:6]:1234",
			want:           true,
		},
		{
			name:           "Empty trusted proxies list",
			trustedProxies: []string{},
			remoteAddr:     "192.168.1.1:1234",
			want:           true, // Everything is trusted when list is empty
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &WebsocketTransport{}
			err := WithAllowForwardedHeader(tt.trustedProxies)(transport)
			require.NoError(t, err)
			l := &listener{
				trustedProxies: transport.trustedProxies,
			}
			got := l.isProxyTrusted(tt.remoteAddr)
			if got != tt.want {
				t.Errorf("isProxyTrusted() = %v, want %v", got, tt.want)
			}
		})
	}
}
