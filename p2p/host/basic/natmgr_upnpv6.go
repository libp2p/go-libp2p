package basichost

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	inat "github.com/libp2p/go-libp2p/p2p/net/nat"
	"github.com/libp2p/go-libp2p/p2p/net/nat/upnpv6"

	ma "github.com/multiformats/go-multiaddr"
)

const (
	upnpv6Lease     = 2 * time.Minute
	upnpv6ResyncDur = 2 * time.Minute
)

// NewUPnPv6NATManager creates a NAT manager that opens IPv6 pinholes via UPnP.
func NewUPnPv6NATManager(net network.Network) NATManager {
	return newUPnPv6NATManager(net)
}

type upnpv6Entry struct {
	ip       string
	port     uint16
	protocol string
}

type upnpv6NATManager struct {
	net       network.Network
	mu        sync.RWMutex
	pinner    *upnpv6.Manager
	tracked   map[upnpv6Entry]uint16
	syncFlag  chan struct{}
	ctx       context.Context
	ctxCancel context.CancelFunc
	refCount  sync.WaitGroup
}

func newUPnPv6NATManager(net network.Network) *upnpv6NATManager {
	ctx, cancel := context.WithCancel(context.Background())
	mgr := &upnpv6NATManager{
		net:       net,
		tracked:   make(map[upnpv6Entry]uint16),
		syncFlag:  make(chan struct{}, 1),
		ctx:       ctx,
		ctxCancel: cancel,
	}
	mgr.refCount.Add(1)
	go mgr.background(ctx)
	return mgr
}

func (m *upnpv6NATManager) Close() error {
	m.ctxCancel()
	m.refCount.Wait()
	return nil
}

func (m *upnpv6NATManager) HasDiscoveredNAT() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pinner != nil
}

func (m *upnpv6NATManager) GetMapping(ma.Multiaddr) ma.Multiaddr {
	return nil
}

func (m *upnpv6NATManager) background(ctx context.Context) {
	defer m.refCount.Done()
	defer func() {
		m.mu.Lock()
		pinner := m.pinner
		m.pinner = nil
		m.mu.Unlock()
		if pinner != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = pinner.Close(closeCtx)
			cancel()
		}
	}()

	discoverCtx, cancel := context.WithTimeout(ctx, inat.DiscoveryTimeout)
	client, err := upnpv6.Discover(discoverCtx)
	cancel()
	if err != nil {
		log.Info("UPnP IPv6 discovery error", "err", err)
		return
	}

	m.mu.Lock()
	m.pinner = upnpv6.NewManager(client)
	m.mu.Unlock()

	m.net.Notify((*upnpv6Notifiee)(m))
	defer m.net.StopNotify((*upnpv6Notifiee)(m))

	m.doSync()
	resync := time.NewTicker(upnpv6ResyncDur)
	defer resync.Stop()
	for {
		select {
		case <-m.syncFlag:
			m.doSync()
		case <-resync.C:
			m.doSync()
		case <-ctx.Done():
			return
		}
	}
}

func (m *upnpv6NATManager) sync() {
	select {
	case m.syncFlag <- struct{}{}:
	default:
	}
}

func (m *upnpv6NATManager) doSync() {
	m.mu.RLock()
	pinner := m.pinner
	m.mu.RUnlock()
	if pinner == nil {
		return
	}

	desired := make(map[upnpv6Entry]struct{})
	for _, maddr := range m.net.ListenAddresses() {
		entry, ok := parseUPnPv6Entry(maddr)
		if ok {
			desired[entry] = struct{}{}
		}
	}

	var toClose []upnpv6Entry
	var toOpen []upnpv6Entry

	m.mu.Lock()
	for entry := range desired {
		if _, ok := m.tracked[entry]; !ok {
			toOpen = append(toOpen, entry)
		}
	}
	for entry := range m.tracked {
		if _, ok := desired[entry]; !ok {
			toClose = append(toClose, entry)
		}
	}
	m.mu.Unlock()

	for _, entry := range toClose {
		m.mu.RLock()
		id := m.tracked[entry]
		m.mu.RUnlock()
		if id != 0 {
			_ = pinner.ClosePinhole(m.ctx, id)
		}
		m.mu.Lock()
		delete(m.tracked, entry)
		m.mu.Unlock()
	}

	for _, entry := range toOpen {
		internalIP := net.ParseIP(entry.ip)
		if internalIP == nil {
			continue
		}
		id, err := pinner.OpenPinhole(m.ctx, internalIP, entry.port, entry.protocol, upnpv6Lease)
		if err != nil {
			log.Debug("UPnP IPv6 pinhole failed", "ip", entry.ip, "port", entry.port, "err", err)
			continue
		}
		m.mu.Lock()
		m.tracked[entry] = id
		m.mu.Unlock()
	}
}

func parseUPnPv6Entry(maddr ma.Multiaddr) (upnpv6Entry, bool) {
	maIP, rest := ma.SplitFirst(maddr)
	if maIP == nil || len(rest) == 0 {
		return upnpv6Entry{}, false
	}
	if maIP.Protocol().Code != ma.P_IP6 {
		return upnpv6Entry{}, false
	}
	ip := net.IP(maIP.RawValue())
	if ip.To16() == nil || ip.IsUnspecified() || !ip.IsGlobalUnicast() {
		return upnpv6Entry{}, false
	}
	proto, _ := ma.SplitFirst(rest)
	if proto == nil || proto.Protocol().Code != ma.P_TCP {
		return upnpv6Entry{}, false
	}
	port, err := strconv.ParseUint(proto.Value(), 10, 16)
	if err != nil {
		return upnpv6Entry{}, false
	}
	return upnpv6Entry{ip: ip.String(), port: uint16(port), protocol: "tcp"}, true
}

type upnpv6Notifiee upnpv6NATManager

func (nn *upnpv6Notifiee) natManager() *upnpv6NATManager              { return (*upnpv6NATManager)(nn) }
func (nn *upnpv6Notifiee) Listen(network.Network, ma.Multiaddr)       { nn.natManager().sync() }
func (nn *upnpv6Notifiee) ListenClose(network.Network, ma.Multiaddr)  { nn.natManager().sync() }
func (nn *upnpv6Notifiee) Connected(network.Network, network.Conn)    {}
func (nn *upnpv6Notifiee) Disconnected(network.Network, network.Conn) {}
