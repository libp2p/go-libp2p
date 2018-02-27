package discovery

import (
	"context"
	"io"
	"io/ioutil"
	golog "log"
	"net"
	"strings"
	"sync"
	"time"

	mdns "github.com/grandcat/zeroconf"
	logging "github.com/ipfs/go-log"
	"github.com/libp2p/go-libp2p-host"
	"github.com/libp2p/go-libp2p-peer"
	pstore "github.com/libp2p/go-libp2p-peerstore"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr-net"
)

var log = logging.Logger("mdns")

const ServiceTag = "_ipfs-discovery._udp"

type Service interface {
	io.Closer
	RegisterNotifee(Notifee)
	UnregisterNotifee(Notifee)
}

type Notifee interface {
	HandlePeerFound(pstore.PeerInfo)
}

type mdnsService struct {
	server  *mdns.Server
	service *mdns.Resolver
	host    host.Host
	tag     string

	lk       sync.Mutex
	notifees []Notifee
	interval time.Duration
}

func NewMdnsService(ctx context.Context, peerhost host.Host, interval time.Duration, serviceTag string) (Service, error) {

	// TODO: dont let mdns use logging...
	golog.SetOutput(ioutil.Discard)

	port := 42424
	myid := peerhost.ID().Pretty()

	info := []string{myid}
	if serviceTag == "" {
		serviceTag = ServiceTag
	}

	resolver, err := mdns.NewResolver(nil)
	if err != nil {
		log.Error("Failed to initialize resolver:", err)
	}

	// Create the mDNS server, defer shutdown
	server, err := mdns.Register(myid, serviceTag, "", port, info, nil)
	if err != nil {
		return nil, err
	}

	s := &mdnsService{
		server:   server,
		service:  resolver,
		host:     peerhost,
		interval: interval,
		tag:      serviceTag,
	}

	go s.pollForEntries(ctx)

	return s, nil
}

func (m *mdnsService) Close() error {
	m.server.Shutdown()
	// grandcat/zerconf swallows error, satisfy interface
	return nil
}

func (m *mdnsService) pollForEntries(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	for {
		//execute mdns query right away at method call and then with every tick
		entriesCh := make(chan *mdns.ServiceEntry, 16)
		go func(results <-chan *mdns.ServiceEntry) {
			for entry := range results {
				m.handleEntry(entry)
			}
		}(entriesCh)

		log.Debug("starting mdns query")

		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()

		if err := m.service.Browse(ctx, m.tag, "local", entriesCh); err != nil {
			log.Error("mdns lookup error: ", err)
		}
		close(entriesCh)

		log.Debug("mdns query complete")

		select {
		case <-ticker.C:
			continue
		case <-ctx.Done():
			log.Debug("mdns service halting")
			return
		}
	}
}

func (m *mdnsService) handleEntry(e *mdns.ServiceEntry) {
	// pull out the txt
	info := strings.Join(e.Text, "|")

	mpeer, err := peer.IDB58Decode(info)
	if err != nil {
		log.Warning("Error parsing peer ID from mdns entry: ", err)
		return
	}

	if mpeer == m.host.ID() {
		log.Debug("got our own mdns entry, skipping")
		return
	}

	for _, ipv4 := range e.AddrIPv4 {
		log.Debugf("Handling MDNS entry: %s:%d %s", ipv4, e.Port, info)

		maddr, err := manet.FromNetAddr(&net.TCPAddr{
			IP:   ipv4,
			Port: e.Port,
		})
		if err != nil {
			log.Warning("Error parsing multiaddr from mdns entry: ", err)
			return
		}

		pi := pstore.PeerInfo{
			ID:    mpeer,
			Addrs: []ma.Multiaddr{maddr},
		}

		m.lk.Lock()
		for _, n := range m.notifees {
			go n.HandlePeerFound(pi)
		}
		m.lk.Unlock()
	}
}

func (m *mdnsService) RegisterNotifee(n Notifee) {
	m.lk.Lock()
	m.notifees = append(m.notifees, n)
	m.lk.Unlock()
}

func (m *mdnsService) UnregisterNotifee(n Notifee) {
	m.lk.Lock()
	found := -1
	for i, notif := range m.notifees {
		if notif == n {
			found = i
			break
		}
	}
	if found != -1 {
		m.notifees = append(m.notifees[:found], m.notifees[found+1:]...)
	}
	m.lk.Unlock()
}
