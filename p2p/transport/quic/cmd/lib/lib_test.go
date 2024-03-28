package cmdlib

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestCmd(t *testing.T) {
	serverLocation := make(chan peer.AddrInfo)
	go RunServer("0", serverLocation)

	l := <-serverLocation
	err := RunClient(l.Addrs[0].String(), l.ID.String())
	if err != nil {
		t.Fatal(err)
	}
}
