package quicreuse

import (
	"context"
	"errors"
	"net"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/quic-go/quic-go"
)

type Option func(*ConnManager) error

type listenUDP func(network string, laddr *net.UDPAddr) (net.PacketConn, error)

func OverrideListenUDP(f listenUDP) Option {
	return func(m *ConnManager) error {
		m.listenUDP = f
		return nil
	}
}

func OverrideSourceIPSelector(f func() (SourceIPSelector, error)) Option {
	return func(m *ConnManager) error {
		m.sourceIPSelectorFn = f
		return nil
	}
}

func DisableReuseport() Option {
	return func(m *ConnManager) error {
		m.enableReuseport = false
		return nil
	}
}

// ConnContext sets the context for all connections accepted by listeners. This doesn't affect the
// context for dialed connections. To reject a connection, return a non nil error.
func ConnContext(f func(ctx context.Context, clientInfo *quic.ClientInfo) (context.Context, error)) Option {
	return func(m *ConnManager) error {
		if m.connContext != nil {
			return errors.New("cannot set ConnContext more than once")
		}
		m.connContext = f
		return nil
	}
}

// VerifySourceAddress returns whether to verify the source address for incoming connection requests.
// For more details see: `quic.Transport.VerifySourceAddress`
func VerifySourceAddress(f func(addr net.Addr) bool) Option {
	return func(m *ConnManager) error {
		m.verifySourceAddress = f
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
