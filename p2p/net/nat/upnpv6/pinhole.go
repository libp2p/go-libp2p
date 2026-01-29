package upnpv6

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway2"
)

const (
	minLeaseSeconds = 1
	maxLeaseSeconds = 86400
	defaultLease    = 2 * time.Minute
)

var ErrDeviceNotFound = errors.New("upnp wanipv6 firewall control not found")

type Client struct {
	fc *internetgateway2.WANIPv6FirewallControl1
}

func Discover(ctx context.Context) (*Client, error) {
	clients, _, err := internetgateway2.NewWANIPv6FirewallControl1ClientsCtx(ctx)
	if err != nil {
		return nil, err
	}
	if len(clients) == 0 {
		return nil, ErrDeviceNotFound
	}
	return &Client{fc: clients[0]}, nil
}

func (c *Client) OpenPinhole(ctx context.Context, internalIP net.IP, port uint16, proto string, lifetime time.Duration) (uint16, error) {
	if c == nil || c.fc == nil {
		return 0, ErrDeviceNotFound
	}
	if internalIP == nil || internalIP.To16() == nil || internalIP.To4() != nil {
		return 0, errors.New("internalIP must be IPv6")
	}
	protocol, err := protocolNumber(proto)
	if err != nil {
		return 0, err
	}
	lease := clampLease(lifetime)
	return c.fc.AddPinholeCtx(ctx, "", 0, internalIP.String(), port, protocol, uint32(lease.Seconds()))
}

func (c *Client) ClosePinhole(ctx context.Context, id uint16) error {
	if c == nil || c.fc == nil {
		return ErrDeviceNotFound
	}
	return c.fc.DeletePinholeCtx(ctx, id)
}

func (c *Client) UpdatePinhole(ctx context.Context, id uint16, lifetime time.Duration) error {
	if c == nil || c.fc == nil {
		return ErrDeviceNotFound
	}
	lease := clampLease(lifetime)
	return c.fc.UpdatePinholeCtx(ctx, id, uint32(lease.Seconds()))
}

func (c *Client) CheckPinholeWorking(ctx context.Context, id uint16) (bool, error) {
	if c == nil || c.fc == nil {
		return false, ErrDeviceNotFound
	}
	return c.fc.CheckPinholeWorkingCtx(ctx, id)
}

type Manager struct {
	client *Client
	mu     sync.Mutex
	byKey  map[string]uint16
	byID   map[uint16]*pinhole
}

type pinhole struct {
	id       uint16
	key      string
	internal net.IP
	port     uint16
	protocol string
	lease    time.Duration
	cancel   context.CancelFunc
}

func NewManager(client *Client) *Manager {
	return &Manager{
		client: client,
		byKey:  make(map[string]uint16),
		byID:   make(map[uint16]*pinhole),
	}
}

func (m *Manager) OpenPinhole(ctx context.Context, internalIP net.IP, port uint16, proto string, lifetime time.Duration) (uint16, error) {
	key := pinholeKey(internalIP, port, proto)
	m.mu.Lock()
	if id, ok := m.byKey[key]; ok {
		m.mu.Unlock()
		return id, nil
	}
	m.mu.Unlock()

	id, err := m.client.OpenPinhole(ctx, internalIP, port, proto, lifetime)
	if err != nil {
		return 0, err
	}

	ctxKeep, cancel := context.WithCancel(context.Background())
	ph := &pinhole{
		id:       id,
		key:      key,
		internal: append(net.IP(nil), internalIP...),
		port:     port,
		protocol: proto,
		lease:    lifetime,
		cancel:   cancel,
	}

	m.mu.Lock()
	m.byKey[key] = id
	m.byID[id] = ph
	m.mu.Unlock()

	go m.keepAlive(ctxKeep, ph)
	return id, nil
}

func (m *Manager) ClosePinhole(ctx context.Context, id uint16) error {
	if m == nil || m.client == nil {
		return ErrDeviceNotFound
	}
	m.mu.Lock()
	ph := m.byID[id]
	if ph != nil {
		ph.cancel()
		delete(m.byID, id)
		delete(m.byKey, ph.key)
	}
	m.mu.Unlock()
	return m.client.ClosePinhole(ctx, id)
}

func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	ids := make([]uint16, 0, len(m.byID))
	for id, ph := range m.byID {
		ph.cancel()
		ids = append(ids, id)
	}
	m.byID = make(map[uint16]*pinhole)
	m.byKey = make(map[string]uint16)
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.client.ClosePinhole(ctx, id)
	}
	return nil
}

func (m *Manager) keepAlive(ctx context.Context, ph *pinhole) {
	lease := clampLease(ph.lease)
	interval := lease / 2
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ctxRefresh, cancel := context.WithTimeout(context.Background(), interval)
			err := m.client.UpdatePinhole(ctxRefresh, ph.id, lease)
			if err == nil {
				working, checkErr := m.client.CheckPinholeWorking(ctxRefresh, ph.id)
				if checkErr == nil && working {
					cancel()
					continue
				}
			}
			cancel()
			m.reopenPinhole(ph)
		}
	}
}

func (m *Manager) reopenPinhole(ph *pinhole) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	newID, err := m.client.OpenPinhole(ctx, ph.internal, ph.port, ph.protocol, ph.lease)
	if err != nil || newID == 0 {
		return
	}

	m.mu.Lock()
	delete(m.byID, ph.id)
	ph.id = newID
	m.byID[newID] = ph
	m.byKey[ph.key] = newID
	m.mu.Unlock()
}

func clampLease(lifetime time.Duration) time.Duration {
	if lifetime <= 0 {
		lifetime = defaultLease
	}
	seconds := int(lifetime.Seconds())
	if seconds < minLeaseSeconds {
		seconds = minLeaseSeconds
	}
	if seconds > maxLeaseSeconds {
		seconds = maxLeaseSeconds
	}
	return time.Duration(seconds) * time.Second
}

func protocolNumber(proto string) (uint16, error) {
	switch strings.ToLower(proto) {
	case "tcp":
		return 6, nil
	case "udp":
		return 17, nil
	default:
		return 0, errors.New("unsupported protocol")
	}
}

func pinholeKey(ip net.IP, port uint16, proto string) string {
	return ip.String() + ":" + strings.ToLower(proto) + ":" + strconv.FormatUint(uint64(port), 10)
}
