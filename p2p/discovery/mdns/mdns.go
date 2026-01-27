package mdns

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/libp2p/zeroconf/v2"

	logging "github.com/libp2p/go-libp2p/gologshim"
	ma "github.com/multiformats/go-multiaddr"
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
		first, _ := ma.SplitFirst(addr)
		if first == nil {
			continue
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

// containsUnsuitableProtocol returns true if the multiaddr includes protocols
// that are not suitable for mDNS advertisement:
//   - Circuit relay (requires intermediary, not direct LAN connectivity)
//   - Browser transports: WebTransport, WebRTC, WebSocket (browsers don't use mDNS)
func containsUnsuitableProtocol(addr ma.Multiaddr) bool {
	found := false
	ma.ForEach(addr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_CIRCUIT,
			ma.P_WEBTRANSPORT,
			ma.P_WEBRTC,
			ma.P_WEBRTC_DIRECT,
			ma.P_P2P_WEBRTC_DIRECT,
			ma.P_WS,
			ma.P_WSS:
			found = true
			return false
		}
		return true
	})
	return found
}

// isSuitableForMDNS returns true for multiaddrs that should be advertised
// via mDNS.
//
// For an address to be suitable:
//  1. It must start with /ip4, /ip6, or a .local DNS name. The .local TLD is
//     reserved for mDNS (RFC 6762) and resolved via multicast, not unicast DNS.
//     Non-.local DNS names are filtered out as they require external DNS.
//  2. It must not use circuit relay or browser-only transports (WebTransport,
//     WebRTC, WebSocket) because these are not useful for direct LAN discovery.
//
// Filtering reduces mDNS packet size, helping stay within the recommended
// 1500-byte limit per RFC 6762. See: https://github.com/libp2p/go-libp2p/issues/3415
func isSuitableForMDNS(addr ma.Multiaddr) bool {
	if addr == nil {
		return false
	}

	first, _ := ma.SplitFirst(addr)
	if first == nil {
		return false
	}

	// Check the addressing scheme
	switch first.Protocol().Code {
	case ma.P_IP4, ma.P_IP6:
		// Direct IP addresses are always suitable for LAN discovery
	case ma.P_DNS, ma.P_DNS4, ma.P_DNS6, ma.P_DNSADDR:
		// DNS names are only suitable if they're in the .local TLD,
		// which is resolved via mDNS (RFC 6762), not unicast DNS.
		if !strings.HasSuffix(strings.ToLower(first.Value()), ".local") {
			return false
		}
	default:
		return false
	}

	return !containsUnsuitableProtocol(addr)
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
	// Build TXT records for addresses suitable for mDNS advertisement.
	var txts []string
	for _, addr := range addrs {
		if isSuitableForMDNS(addr) {
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
			for _, s := range entry.Text {
				if !strings.HasPrefix(s, dnsaddrPrefix) {
					log.Debug("missing dnsaddr prefix")
					continue
				}
				addr, err := ma.NewMultiaddr(s[len(dnsaddrPrefix):])
				if err != nil {
					log.Debug("failed to parse multiaddr", "err", err)
					continue
				}
				addrs = append(addrs, addr)
			}
			infos, err := peer.AddrInfosFromP2pAddrs(addrs...)
			if err != nil {
				log.Debug("failed to get peer info", "err", err)
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
			log.Debug("zeroconf browsing failed", "err", err)
		}
	}()
}

func randomString(l int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	s := make([]byte, 0, l)
	for i := 0; i < l; i++ {
		s = append(s, alphabet[rand.Intn(len(alphabet))])
	}
	return string(s)
}
