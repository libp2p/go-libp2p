package mdns

import (
	"context"
	"errors"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"io"
	"math/rand"
	"strings"
	"sync"

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

	mu sync.Mutex
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

	ipEvt, err := s.host.EventBus().Subscribe(&event.EvtLocalAddressesUpdated{})
	if err != nil {
		if err = s.Close(); err != nil {
			log.Errorf("failed to close mdns service: %s", err)
		}
		return err
	}
	go func() {
		defer func() {
			if err := ipEvt.Close(); err != nil {
				log.Errorf("failed to close ipEvt: %s", err)
			}
		}()

		for {
			select {
			case <-s.ctx.Done():
				return
			case evt := <-ipEvt.Out():
				if _, ok := evt.(event.EvtLocalAddressesUpdated); ok {
					s.mu.Lock()
					if s.server != nil {
						s.server.Shutdown()
						s.server = nil
					}
					if err = s.startServer(); err != nil {
						log.Errorf("failed to restart mdns server: %s", err)
					}
					s.mu.Unlock()
				}
			}
		}
	}()

	return nil
}

func (s *mdnsService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

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
					log.Debugf("failed to parse multiaddr: %s", err)
					continue
				}
				addrs = append(addrs, addr)
			}
			infos, err := peer.AddrInfosFromP2pAddrs(addrs...)
			if err != nil {
				log.Debugf("failed to get peer info: %s", err)
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

func randomString(l int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	s := make([]byte, 0, l)
	for i := 0; i < l; i++ {
		s = append(s, alphabet[rand.Intn(len(alphabet))])
	}
	return string(s)
}
