package main

import (
	"context"
	"log"
	"net"
	"strconv"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// filtersConnectionGater is an adapter that turns multiaddr.Filter into a
// connmgr.ConnectionGater.
type filtersConnectionGater ma.Filters

var _ connmgr.ConnectionGater = (*filtersConnectionGater)(nil)

func (f *filtersConnectionGater) InterceptAddrDial(_ peer.ID, addr ma.Multiaddr) (allow bool) {
	return !(*ma.Filters)(f).AddrBlocked(addr)
}

func (f *filtersConnectionGater) InterceptPeerDial(p peer.ID) (allow bool) {
	return true
}

func (f *filtersConnectionGater) InterceptAccept(connAddr network.ConnMultiaddrs) (allow bool) {
	return !(*ma.Filters)(f).AddrBlocked(connAddr.RemoteMultiaddr())
}

func (f *filtersConnectionGater) InterceptSecured(_ network.Direction, _ peer.ID, connAddr network.ConnMultiaddrs) (allow bool) {
	return !(*ma.Filters)(f).AddrBlocked(connAddr.RemoteMultiaddr())
}

func (f *filtersConnectionGater) InterceptUpgraded(_ network.Conn) (allow bool, reason control.DisconnectReason) {
	return true, 0
}

func main() {
	rcmgr := &network.NullResourceManager{}
	addrs := []string{
		"/ip4/10.0.0.0/ipcidr/8",
		"/ip4/100.64.0.0/ipcidr/10",
		"/ip4/169.254.0.0/ipcidr/16",
		"/ip4/172.16.0.0/ipcidr/12",
		"/ip4/192.0.0.0/ipcidr/24",
		"/ip4/192.0.2.0/ipcidr/24",
		"/ip4/192.168.0.0/ipcidr/16",
		"/ip4/198.18.0.0/ipcidr/15",
		"/ip4/198.51.100.0/ipcidr/24",
		"/ip4/203.0.113.0/ipcidr/24",
		"/ip4/240.0.0.0/ipcidr/4",
		"/ip6/100::/ipcidr/64",
		"/ip6/2001:2::/ipcidr/48",
		"/ip6/2001:db8::/ipcidr/32",
		"/ip6/fc00::/ipcidr/7",
		"/ip6/fe80::/ipcidr/10",
	}
	filterAddrs := ma.NewFilters()
	for _, a := range addrs {
		addr := ma.StringCast(a)
		ipa, err := manet.ToIP(addr)
		if err != nil {
			panic(err)
		}
		v, err := addr.ValueForProtocol(ma.P_IPCIDR)
		if err != nil {
			panic(err)
		}
		i, err := strconv.Atoi(v)
		if err != nil {
			panic(err)
		}
		l := net.IPv4len * 8
		if ipa.To4() == nil {
			l = net.IPv6len * 8
		}
		ipn := net.IPNet{
			IP:   ipa,
			Mask: net.CIDRMask(i, l),
		}
		filterAddrs.AddFilter(ipn, ma.ActionDeny)
	}
	gater := (*filtersConnectionGater)(filterAddrs)
	if gater.InterceptAddrDial("", ma.StringCast("/ip4/192.168.1.1/tcp/1")) {
		panic("invalid gater")
	}
	h, err := libp2p.New(
		libp2p.EnableAutoNATv2(),
		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/4001",
			"/ip6/::/tcp/4001",
			"/ip4/0.0.0.0/udp/4001/quic-v1",
			"/ip6/::/udp/4001/quic-v1/",
			"/ip4/0.0.0.0/udp/4001/quic-v1/webtransport",
			"/ip6/::/udp/4001/quic-v1/webtransport",
		),
		libp2p.ResourceManager(rcmgr),
		libp2p.ConnectionGater(gater),
	)
	if err != nil {
		log.Fatal(err)
	}

	dht, err := dht.New(context.Background(), h, dht.BootstrapPeers(dht.GetDefaultBootstrapPeerAddrInfos()...))
	if err != nil {
		log.Fatal(err)
	}

	err = dht.Bootstrap(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	select {}
}
