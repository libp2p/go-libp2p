package cmdlib

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

func TestCmd(t *testing.T) {
	serverLocation := make(chan peer.AddrInfo)
	go RunServer("0", serverLocation)

	l := <-serverLocation

	ip, rest := multiaddr.SplitFirst(l.Addrs[0])
	if ip.Protocol().Code == multiaddr.P_IP4 && ip.Value() == "0.0.0.0" {
		// Windows can't dial to 0.0.0.0 so replace with localhost
		var err error
		ip, err = multiaddr.NewComponent("ip4", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
	}

	err := RunClient(multiaddr.Join(ip, rest).String(), l.ID.String())
	if err != nil {
		t.Fatal(err)
	}
}
