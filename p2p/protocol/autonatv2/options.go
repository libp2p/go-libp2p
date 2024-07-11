package autonatv2

import "time"

// autoNATSettings is used to configure AutoNAT
type autoNATSettings struct {
	allowPrivateAddrs                    bool
	serverRPM                            int
	serverPerPeerRPM                     int
	serverDialDataRPM                    int
	dataRequestPolicy                    dataRequestPolicyFunc
	now                                  func() time.Time
	amplificatonAttackPreventionDialWait time.Duration
	metricsTracer                        MetricsTracer
}

func defaultSettings() *autoNATSettings {
	return &autoNATSettings{
		allowPrivateAddrs:                    false,
		serverRPM:                            60, // 1 every second
		serverPerPeerRPM:                     12, // 1 every 5 seconds
		serverDialDataRPM:                    12, // 1 every 5 seconds
		dataRequestPolicy:                    amplificationAttackPrevention,
		amplificatonAttackPreventionDialWait: 3 * time.Second,
		now:                                  time.Now,
	}
}

type AutoNATOption func(s *autoNATSettings) error

func WithServerRateLimit(rpm, perPeerRPM, dialDataRPM int) AutoNATOption {
	return func(s *autoNATSettings) error {
		s.serverRPM = rpm
		s.serverPerPeerRPM = perPeerRPM
		s.serverDialDataRPM = dialDataRPM
		return nil
	}
}

func WithMetricsTracer(m MetricsTracer) AutoNATOption {
	return func(s *autoNATSettings) error {
		s.metricsTracer = m
		return nil
	}
}

func withDataRequestPolicy(drp dataRequestPolicyFunc) AutoNATOption {
	return func(s *autoNATSettings) error {
		s.dataRequestPolicy = drp
		return nil
	}
}

func allowPrivateAddrs(s *autoNATSettings) error {
	s.allowPrivateAddrs = true
	return nil
}

func withAmplificationAttackPreventionDialWait(d time.Duration) AutoNATOption {
	return func(s *autoNATSettings) error {
		s.amplificatonAttackPreventionDialWait = d
		return nil
	}
}
