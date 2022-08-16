//lint:file-ignore U1000 Ignore all unused code, we're not running any tests.
package relay_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	. "github.com/libp2p/go-libp2p/p2p/protocol/internal/circuitv1-deprecated"

	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peerstore"

	ma "github.com/multiformats/go-multiaddr"
)

const TestProto = "test/relay-transport"

var msg = []byte("relay works!")

func testSetupRelay(t *testing.T) []host.Host {
	hosts := getNetHosts(t, 3)

	err := AddRelayTransport(hosts[0], swarmt.GenUpgrader(t, hosts[0].Network().(*swarm.Swarm)))
	if err != nil {
		t.Fatal(err)
	}

	err = AddRelayTransport(hosts[1], swarmt.GenUpgrader(t, hosts[1].Network().(*swarm.Swarm)), OptHop)
	if err != nil {
		t.Fatal(err)
	}

	err = AddRelayTransport(hosts[2], swarmt.GenUpgrader(t, hosts[2].Network().(*swarm.Swarm)))
	if err != nil {
		t.Fatal(err)
	}

	connect(t, hosts[0], hosts[1])
	connect(t, hosts[1], hosts[2])

	time.Sleep(100 * time.Millisecond)

	handler := func(s network.Stream) {
		_, err := s.Write(msg)
		if err != nil {
			t.Error(err)
		}
		s.Close()
	}

	hosts[2].SetStreamHandler(TestProto, handler)

	return hosts
}

func TestFullAddressTransportDial(t *testing.T) {
	t.Skip("This package is legacy code we only keep around for testing purposes.")

	hosts := testSetupRelay(t)

	var relayAddr ma.Multiaddr
	for _, addr := range hosts[1].Addrs() {
		// skip relay addrs.
		if _, err := addr.ValueForProtocol(ma.P_CIRCUIT); err != nil {
			relayAddr = addr
		}
	}

	addr, err := ma.NewMultiaddr(fmt.Sprintf("%s/p2p/%s/p2p-circuit/p2p/%s", relayAddr.String(), hosts[1].ID().Pretty(), hosts[2].ID().Pretty()))
	if err != nil {
		t.Fatal(err)
	}

	hosts[0].Peerstore().AddAddrs(hosts[2].ID(), []ma.Multiaddr{addr}, peerstore.TempAddrTTL)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := hosts[0].NewStream(ctx, hosts[2].ID(), TestProto)
	if err != nil {
		t.Fatal(err)
	}

	data, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(data, msg) {
		t.Fatal("message was incorrect:", string(data))
	}
}

func TestSpecificRelayTransportDial(t *testing.T) {
	t.Skip("This package is legacy code we only keep around for testing purposes.")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hosts := testSetupRelay(t)

	addr, err := ma.NewMultiaddr(fmt.Sprintf("/ipfs/%s/p2p-circuit/ipfs/%s", hosts[1].ID().Pretty(), hosts[2].ID().Pretty()))
	if err != nil {
		t.Fatal(err)
	}

	rctx, rcancel := context.WithTimeout(ctx, time.Second)
	defer rcancel()

	hosts[0].Peerstore().AddAddrs(hosts[2].ID(), []ma.Multiaddr{addr}, peerstore.TempAddrTTL)

	s, err := hosts[0].NewStream(rctx, hosts[2].ID(), TestProto)
	if err != nil {
		t.Fatal(err)
	}

	data, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(data, msg) {
		t.Fatal("message was incorrect:", string(data))
	}
}

func TestUnspecificRelayTransportDialFails(t *testing.T) {
	t.Skip("This package is legacy code we only keep around for testing purposes.")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hosts := testSetupRelay(t)

	addr, err := ma.NewMultiaddr(fmt.Sprintf("/p2p-circuit/ipfs/%s", hosts[2].ID().Pretty()))
	if err != nil {
		t.Fatal(err)
	}

	rctx, rcancel := context.WithTimeout(ctx, time.Second)
	defer rcancel()

	hosts[0].Peerstore().AddAddrs(hosts[2].ID(), []ma.Multiaddr{addr}, peerstore.TempAddrTTL)

	_, err = hosts[0].NewStream(rctx, hosts[2].ID(), TestProto)
	if err == nil {
		t.Fatal("dial to unspecified address should have failed")
	}

}
