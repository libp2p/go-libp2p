package quicreuse

import (
	"net"

	"github.com/prometheus/client_golang/prometheus"
)

type Option func(*ConnManager) error

type listenUDP func(network string, laddr *net.UDPAddr) (net.PacketConn, error)

func CustomListenUDP(f listenUDP) Option {
	return func(m *ConnManager) error {
		m.customListenUDP = f
		return nil
	}
}

func DisableReuseport() Option {
	return func(m *ConnManager) error {
		m.enableReuseport = false
		return nil
	}
}

// EnableMetrics enables Prometheus metrics collection. If reg is nil,
// prometheus.DefaultRegisterer will be used as the registerer.
func EnableMetrics(reg prometheus.Registerer) Option {
	return func(m *ConnManager) error {
		m.enableMetrics = true
		if reg != nil {
			m.registerer = reg
		}
		return nil
	}
}
