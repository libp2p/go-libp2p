package mdns

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/libp2p/zeroconf/v2"

	logging "github.com/ipfs/go-log/v2"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const (
	ServiceName   = "_p2p._udp"
	mdnsDomain    = "local"
	dnsaddrPrefix = "dnsaddr="
)

var log = logging.Logger("mdns")

type Service interface {
	Start() error
	io.Closer
}

type Notifee interface {
	HandlePeerFound(peer.AddrInfo)
}

type mdnsService struct {
	host        host.Host
	serviceName string
	peerName    string

	// The context is canceled when Close() is called.
	ctx       context.Context
	ctxCancel context.CancelFunc

	resolverWG sync.WaitGroup
	server     *zeroconf.Server

	notifee Notifee
}

func NewMdnsService(host host.Host, serviceName string, notifee Notifee) *mdnsService {
	if serviceName == "" {
		serviceName = ServiceName
	}
	s := &mdnsService{
		host:        host,
		serviceName: serviceName,
		peerName:    randomString(32 + rand.Intn(32)), // generate a random string between 32 and 63 characters long
		notifee:     notifee,
	}
	s.ctx, s.ctxCancel = context.WithCancel(context.Background())
	return s
}

func (s *mdnsService) Start() error {
	if err := s.startServer(); err != nil {
		return err
	}
	s.startResolver(s.ctx)
	return nil
}

func (s *mdnsService) Close() error {
	s.ctxCancel()
	if s.server != nil {
		s.server.Shutdown()
	}
	s.resolverWG.Wait()
	return nil
}

// We don't really care about the IP addresses, but the spec (and various routers / firewalls) require us
// to send A and AAAA records.
func (s *mdnsService) getIPs(addrs []ma.Multiaddr) ([]string, error) {
	var ip4, ip6 string
	for _, addr := range addrs {
		first, remaining := ma.SplitFirst(addr)
		if first == nil {
			continue
		}
		if first.Protocol().Code == ma.P_IP6ZONE {
			first, _ = ma.SplitFirst(remaining)
			if first == nil {
				continue
			}
		}
		if ip4 == "" && first.Protocol().Code == ma.P_IP4 {
			ip4 = first.Value()
		} else if ip6 == "" && first.Protocol().Code == ma.P_IP6 {
			ip6 = first.Value()
		}
	}
	ips := make([]string, 0, 2)
	if ip4 != "" {
		ips = append(ips, ip4)
	}
	if ip6 != "" {
		ips = append(ips, ip6)
	}
	if len(ips) == 0 {
		return nil, errors.New("didn't find any IP addresses")
	}
	return ips, nil
}

func (s *mdnsService) startServer() error {
	interfaceAddrs, err := s.host.Network().InterfaceListenAddresses()
	if err != nil {
		return err
	}
	addrs, err := peer.AddrInfoToP2pAddrs(&peer.AddrInfo{
		ID:    s.host.ID(),
		Addrs: interfaceAddrs,
	})
	if err != nil {
		return err
	}
	var txts []string
	for _, addr := range addrs {
		if manet.IsThinWaist(addr) { // don't announce circuit addresses
			if manet.IsIP6LinkLocal(addr) {
				_, addr = ma.SplitFirst(addr)
			}

			txts = append(txts, dnsaddrPrefix+addr.String())
		}
	}

	ips, err := s.getIPs(addrs)
	if err != nil {
		return err
	}

	server, err := zeroconf.RegisterProxy(
		s.peerName,
		s.serviceName,
		mdnsDomain,
		4001, // we have to pass in a port number here, but libp2p only uses the TXT records
		s.peerName,
		ips,
		txts,
		nil,
	)
	if err != nil {
		return err
	}
	s.server = server
	return nil
}

func (s *mdnsService) startResolver(ctx context.Context) {
	s.resolverWG.Add(2)
	entryChan := make(chan *zeroconf.ServiceEntry, 1000)
	go func() {
		defer s.resolverWG.Done()
		for entry := range entryChan {
			// We only care about the TXT records.
			// Ignore A, AAAA and PTR.
			addrs := make([]ma.Multiaddr, 0, len(entry.Text)) // assume that all TXT records are dnsaddrs
			for _, txt := range entry.Text {
				if !strings.HasPrefix(txt, dnsaddrPrefix) {
					log.Debug("missing dnsaddr prefix")
					continue
				}
				addr, err := ma.NewMultiaddr(txt[len(dnsaddrPrefix):])
				if err != nil {
					log.Debugf("failed to parse multiaddr: %s", err)
					continue
				}
				if manet.IsIP6LinkLocal(addr) {
					ifaces, _ := net.Interfaces()
					addr = s.fixIP6LinkLocalAddress(entry.ReceivedIfIndex, entry.ReceivedSrc, addr)
					_ = ifaces
				}
				addrs = append(addrs, addr)
			}
			infos, err := peer.AddrInfosFromP2pAddrs(addrs...)
			if err != nil {
				log.Debugf("failed to get peer info: %txt", err)
				continue
			}
			for _, info := range infos {
				if info.ID == s.host.ID() {
					continue
				}
				go s.notifee.HandlePeerFound(info)
			}
		}
	}()
	go func() {
		defer s.resolverWG.Done()
		if err := zeroconf.Browse(ctx, s.serviceName, mdnsDomain, entryChan); err != nil {
			log.Debugf("zeroconf browsing failed: %s", err)
		}
	}()
}

func (s *mdnsService) fixIP6LinkLocalAddress(ifIndex int, src net.Addr, addr ma.Multiaddr) ma.Multiaddr {
	var ifName string
	udpAddr, ok := src.(*net.UDPAddr)
	if ok && len(udpAddr.Zone) > 0 {
		ifName = udpAddr.Zone
	} else if ifIndex > 0 {
		iface, err := net.InterfaceByIndex(ifIndex)
		if err == nil {
			ifName = iface.Name
		}
	}
	if len(ifName) > 0 {
		prefix, err := ma.NewMultiaddr("/ip6zone/" + ifName)
		if err == nil {
			return prefix.Encapsulate(addr)
		}
	}
	return addr
}

func randomString(l int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	s := make([]byte, 0, l)
	for i := 0; i < l; i++ {
		s = append(s, alphabet[rand.Intn(len(alphabet))])
	}
	return string(s)
}
