package basichost

import (
	"errors"

	"github.com/libp2p/go-libp2p/core/network"

	ma "github.com/multiformats/go-multiaddr"
)

// NewUPnPCombinedNATManager creates a NAT manager that combines IPv4 port mapping
// with IPv6 pinhole management.
func NewUPnPCombinedNATManager(net network.Network) NATManager {
	return &combinedNATManager{
		v4: newNATManager(net),
		v6: newUPnPv6NATManager(net),
	}
}

type combinedNATManager struct {
	v4 *natManager
	v6 *upnpv6NATManager
}

func (m *combinedNATManager) Close() error {
	return errors.Join(m.v4.Close(), m.v6.Close())
}

func (m *combinedNATManager) HasDiscoveredNAT() bool {
	return m.v4.HasDiscoveredNAT() || m.v6.HasDiscoveredNAT()
}

func (m *combinedNATManager) GetMapping(addr ma.Multiaddr) ma.Multiaddr {
	return m.v4.GetMapping(addr)
}
