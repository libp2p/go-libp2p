package quicreuse

type Option func(*ConnManager) error

func DisableReuseport() Option {
	return func(m *ConnManager) error {
		m.enableReuseport = false
		return nil
	}
}

// EnableMetrics enables Prometheus metrics collection.
func EnableMetrics() Option {
	return func(m *ConnManager) error {
		m.enableMetrics = true
		return nil
	}
}
