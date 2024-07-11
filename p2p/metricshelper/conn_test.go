package metricshelper

import (
	"fmt"
	"testing"

	ma "github.com/multiformats/go-multiaddr"
)

func TestGetTransport(t *testing.T) {
	cases := []struct {
		addr   ma.Multiaddr
		result string
	}{
		{
			addr:   ma.StringCast("/ip4/1.1.1.1/tcp/1"),
			result: "tcp",
		},
		{
			addr:   ma.StringCast("/ip4/1.1.1.1/udp/10"),
			result: "other",
		},
		{
			addr:   nil,
			result: "other",
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			got := GetTransport(tc.addr)
			if got != tc.result {
				t.Fatalf("invalid transport for %s\ngot:%v\nwant:%v", tc.addr, got, tc.result)
			}
		})
	}
}

func TestIPVersion(t *testing.T) {
	cases := []struct {
		addr   ma.Multiaddr
		result string
	}{
		{
			addr:   ma.StringCast("/ip4/1.1.1.1/tcp/1"),
			result: "ip4",
		},
		{
			addr:   ma.StringCast("/ip4/1.1.1.1/udp/10"),
			result: "ip4",
		},
		{
			addr:   nil,
			result: "unknown",
		},
		{
			addr:   ma.StringCast("/dns/hello.world/tcp/10"),
			result: "unknown",
		},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			got := GetIPVersion(tc.addr)
			if got != tc.result {
				t.Fatalf("invalid ip version for %s\ngot:%v\nwant:%v", tc.addr, got, tc.result)
			}
		})
	}
}
