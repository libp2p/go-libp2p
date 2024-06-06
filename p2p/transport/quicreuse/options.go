package quicreuse

import "github.com/prometheus/client_golang/prometheus"

type Option func(*ConnManager) error

func DisableReuseport() Option {
	return func(m *ConnManager) error {
		m.enableReuseport = false
		return nil
	}
}

// EnableMetrics enables Prometheus metrics collection.
func EnableMetrics(reg prometheus.Registerer) Option {
	return func(m *ConnManager) error {
		m.enableMetrics = true
		if reg != nil {
			m.registerer = reg
		}
		return nil
	}
}
