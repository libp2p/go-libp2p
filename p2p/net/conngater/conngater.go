package conngater

import (
	"context"
	"net"
	"sync"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	"github.com/ipfs/go-datastore/query"
	logging "github.com/libp2p/go-libp2p/gologshim"
)

// BasicConnectionGater implements a connection gater that allows the application to perform
// access control on incoming and outgoing connections.
type BasicConnectionGater struct {
	sync.RWMutex

	blockedPeers   map[peer.ID]struct{}
	blockedAddrs   map[string]struct{}
	blockedSubnets map[string]*net.IPNet

	ds datastore.Datastore
}

var log = logging.Logger("net/conngater")

const (
	ns        = "/libp2p/net/conngater"
	keyPeer   = "/peer/"
	keyAddr   = "/addr/"
	keySubnet = "/subnet/"
)

// NewBasicConnectionGater creates a new connection gater.
// The ds argument is an (optional, can be nil) datastore to persist the connection gater
// filters.
func NewBasicConnectionGater(ds datastore.Datastore) (*BasicConnectionGater, error) {
	cg := &BasicConnectionGater{
		blockedPeers:   make(map[peer.ID]struct{}),
		blockedAddrs:   make(map[string]struct{}),
		blockedSubnets: make(map[string]*net.IPNet),
	}

	if ds != nil {
		cg.ds = namespace.Wrap(ds, datastore.NewKey(ns))
		err := cg.loadRules(context.Background())
		if err != nil {
			return nil, err
		}
	}

	return cg, nil
}

func (cg *BasicConnectionGater) loadRules(ctx context.Context) error {
	// load blocked peers
	res, err := cg.ds.Query(ctx, query.Query{Prefix: keyPeer})
	if err != nil {
		log.Error("error querying datastore for blocked peers", "err", err)
		return err
	}

	for r := range res.Next() {
		if r.Error != nil {
			log.Error("query result error", "err", r.Error)
			return r.Error
		}

		p := peer.ID(r.Entry.Value)
		cg.blockedPeers[p] = struct{}{}
	}

	// load blocked addrs
	res, err = cg.ds.Query(ctx, query.Query{Prefix: keyAddr})
	if err != nil {
		log.Error("error querying datastore for blocked addrs", "err", err)
		return err
	}

	for r := range res.Next() {
		if r.Error != nil {
			log.Error("query result error", "err", r.Error)
			return r.Error
		}

		ip := net.IP(r.Entry.Value)
		cg.blockedAddrs[ip.String()] = struct{}{}
	}

	// load blocked subnets
	res, err = cg.ds.Query(ctx, query.Query{Prefix: keySubnet})
	if err != nil {
		log.Error("error querying datastore for blocked subnets", "err", err)
		return err
	}

	for r := range res.Next() {
		if r.Error != nil {
			log.Error("query result error", "err", r.Error)
			return r.Error
		}

		ipnetStr := string(r.Entry.Value)
		_, ipnet, err := net.ParseCIDR(ipnetStr)
		if err != nil {
			log.Error("error parsing CIDR subnet", "err", err)
			return err
		}
		cg.blockedSubnets[ipnetStr] = ipnet
	}

	return nil
}

// BlockPeer adds a peer to the set of blocked peers.
// Note: active connections to the peer are not automatically closed.
func (cg *BasicConnectionGater) BlockPeer(p peer.ID) error {
	if cg.ds != nil {
		err := cg.ds.Put(context.Background(), datastore.NewKey(keyPeer+p.String()), []byte(p))
		if err != nil {
			log.Error("error writing blocked peer to datastore", "err", err)
			return err
		}
	}

	cg.Lock()
	defer cg.Unlock()
	cg.blockedPeers[p] = struct{}{}

	return nil
}

// UnblockPeer removes a peer from the set of blocked peers
func (cg *BasicConnectionGater) UnblockPeer(p peer.ID) error {
	if cg.ds != nil {
		err := cg.ds.Delete(context.Background(), datastore.NewKey(keyPeer+p.String()))
		if err != nil {
			log.Error("error deleting blocked peer from datastore", "err", err)
			return err
		}
	}

	cg.Lock()
	defer cg.Unlock()

	delete(cg.blockedPeers, p)

	return nil
}

// ListBlockedPeers return a list of blocked peers
func (cg *BasicConnectionGater) ListBlockedPeers() []peer.ID {
	cg.RLock()
	defer cg.RUnlock()

	result := make([]peer.ID, 0, len(cg.blockedPeers))
	for p := range cg.blockedPeers {
		result = append(result, p)
	}

	return result
}

// BlockAddr adds an IP address to the set of blocked addresses.
// Note: active connections to the IP address are not automatically closed.
func (cg *BasicConnectionGater) BlockAddr(ip net.IP) error {
	if cg.ds != nil {
		err := cg.ds.Put(context.Background(), datastore.NewKey(keyAddr+ip.String()), []byte(ip))
		if err != nil {
			log.Error("error writing blocked addr to datastore", "err", err)
			return err
		}
	}

	cg.Lock()
	defer cg.Unlock()

	cg.blockedAddrs[ip.String()] = struct{}{}

	return nil
}

// UnblockAddr removes an IP address from the set of blocked addresses
func (cg *BasicConnectionGater) UnblockAddr(ip net.IP) error {
	if cg.ds != nil {
		err := cg.ds.Delete(context.Background(), datastore.NewKey(keyAddr+ip.String()))
		if err != nil {
			log.Error("error deleting blocked addr from datastore", "err", err)
			return err
		}
	}

	cg.Lock()
	defer cg.Unlock()

	delete(cg.blockedAddrs, ip.String())

	return nil
}

// ListBlockedAddrs return a list of blocked IP addresses
func (cg *BasicConnectionGater) ListBlockedAddrs() []net.IP {
	cg.RLock()
	defer cg.RUnlock()

	result := make([]net.IP, 0, len(cg.blockedAddrs))
	for ipStr := range cg.blockedAddrs {
		ip := net.ParseIP(ipStr)
		result = append(result, ip)
	}

	return result
}

// BlockSubnet adds an IP subnet to the set of blocked addresses.
// Note: active connections to the IP subnet are not automatically closed.
func (cg *BasicConnectionGater) BlockSubnet(ipnet *net.IPNet) error {
	if cg.ds != nil {
		err := cg.ds.Put(context.Background(), datastore.NewKey(keySubnet+ipnet.String()), []byte(ipnet.String()))
		if err != nil {
			log.Error("error writing blocked addr to datastore", "err", err)
			return err
		}
	}

	cg.Lock()
	defer cg.Unlock()

	cg.blockedSubnets[ipnet.String()] = ipnet

	return nil
}

// UnblockSubnet removes an IP address from the set of blocked addresses
func (cg *BasicConnectionGater) UnblockSubnet(ipnet *net.IPNet) error {
	if cg.ds != nil {
		err := cg.ds.Delete(context.Background(), datastore.NewKey(keySubnet+ipnet.String()))
		if err != nil {
			log.Error("error deleting blocked subnet from datastore", "err", err)
			return err
		}
	}

	cg.Lock()
	defer cg.Unlock()

	delete(cg.blockedSubnets, ipnet.String())

	return nil
}

// ListBlockedSubnets return a list of blocked IP subnets
func (cg *BasicConnectionGater) ListBlockedSubnets() []*net.IPNet {
	cg.RLock()
	defer cg.RUnlock()

	result := make([]*net.IPNet, 0, len(cg.blockedSubnets))
	for _, ipnet := range cg.blockedSubnets {
		result = append(result, ipnet)
	}

	return result
}

// ConnectionGater interface
var _ connmgr.ConnectionGater = (*BasicConnectionGater)(nil)

func (cg *BasicConnectionGater) InterceptPeerDial(p peer.ID) (allow bool) {
	cg.RLock()
	defer cg.RUnlock()

	_, block := cg.blockedPeers[p]
	return !block
}

func (cg *BasicConnectionGater) InterceptAddrDial(_ peer.ID, a ma.Multiaddr) (allow bool) {
	// we have already filtered blocked peers in InterceptPeerDial, so we just check the IP
	cg.RLock()
	defer cg.RUnlock()

	ip, err := manet.ToIP(a)
	if err != nil {
		log.Warn("error converting multiaddr to IP addr", "err", err)
		return true
	}

	_, block := cg.blockedAddrs[ip.String()]
	if block {
		return false
	}

	for _, ipnet := range cg.blockedSubnets {
		if ipnet.Contains(ip) {
			return false
		}
	}

	return true
}

func (cg *BasicConnectionGater) InterceptAccept(cma network.ConnMultiaddrs) (allow bool) {
	cg.RLock()
	defer cg.RUnlock()

	a := cma.RemoteMultiaddr()

	ip, err := manet.ToIP(a)
	if err != nil {
		log.Warn("error converting multiaddr to IP addr", "err", err)
		return true
	}

	_, block := cg.blockedAddrs[ip.String()]
	if block {
		return false
	}

	for _, ipnet := range cg.blockedSubnets {
		if ipnet.Contains(ip) {
			return false
		}
	}

	return true
}

func (cg *BasicConnectionGater) InterceptSecured(dir network.Direction, p peer.ID, _ network.ConnMultiaddrs) (allow bool) {
	if dir == network.DirOutbound {
		// we have already filtered those in InterceptPeerDial/InterceptAddrDial
		return true
	}

	// we have already filtered addrs in InterceptAccept, so we just check the peer ID
	cg.RLock()
	defer cg.RUnlock()

	_, block := cg.blockedPeers[p]
	return !block
}

func (cg *BasicConnectionGater) InterceptUpgraded(network.Conn) (allow bool, reason control.DisconnectReason) {
	return true, 0
}
